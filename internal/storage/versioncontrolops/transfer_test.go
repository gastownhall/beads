package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// errEngineTransfer stands in for the error the engine returns from a
// network statement that stopped because its context ended.
var errEngineTransfer = errors.New("engine: transfer interrupted")

// transferConn fakes the network statements (DOLT_PUSH, DOLT_FETCH). In
// blocking mode a statement waits until its context ends, as a hung git
// transport does, and then returns errEngineTransfer. release is the test's
// watchdog: closing it frees a statement whose context never ends, so a
// missing deadline fails an assertion instead of hanging the test.
type transferConn struct {
	block   bool
	result  error
	release chan struct{}

	mu   sync.Mutex
	ctxs []context.Context
	// endedByCtx records, per statement, whether its context had ended when
	// the statement returned.
	endedByCtx []bool
}

func newTransferConn(t *testing.T, block bool) *transferConn {
	t.Helper()
	c := &transferConn{block: block, release: make(chan struct{})}
	watchdog := time.AfterFunc(3*time.Second, func() { close(c.release) })
	t.Cleanup(func() {
		if watchdog.Stop() {
			close(c.release)
		}
	})
	return c
}

func (c *transferConn) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	c.mu.Lock()
	c.ctxs = append(c.ctxs, ctx)
	c.mu.Unlock()
	if !strings.Contains(query, "DOLT_PUSH") && !strings.Contains(query, "DOLT_FETCH") {
		return nil, errors.New("transferConn: unexpected statement " + query)
	}
	err := c.result
	if c.block {
		select {
		case <-ctx.Done():
		case <-c.release:
		}
		err = errEngineTransfer
	}
	c.mu.Lock()
	c.endedByCtx = append(c.endedByCtx, ctx.Err() != nil)
	c.mu.Unlock()
	return nil, err
}

func (c *transferConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("transferConn: queries are not supported")
}

func (c *transferConn) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

func (c *transferConn) statementCtx(t *testing.T) context.Context {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ctxs) != 1 {
		t.Fatalf("statements issued = %d, want 1", len(c.ctxs))
	}
	return c.ctxs[0]
}

// transferSite is one network statement site in remotes.go.
type transferSite struct {
	name string
	op   string // the operation the timeout error names
	call func(ctx context.Context, db DBConn) error
}

func transferSites() []transferSite {
	var sites []transferSite
	for _, user := range []string{"", "remote-user"} {
		suffix := ""
		if user != "" {
			suffix = " with user"
		}
		sites = append(sites,
			transferSite{"Push" + suffix, "dolt push", func(ctx context.Context, db DBConn) error {
				return Push(ctx, db, "origin", "main", user)
			}},
			transferSite{"ForcePush" + suffix, "dolt push", func(ctx context.Context, db DBConn) error {
				return ForcePush(ctx, db, "origin", "main", user)
			}},
			transferSite{"Fetch" + suffix, "dolt fetch", func(ctx context.Context, db DBConn) error {
				return Fetch(ctx, db, "peer", user)
			}},
			transferSite{"Pull fetch" + suffix, "dolt fetch", func(ctx context.Context, db DBConn) error {
				return PullWithStrategy(ctx, db, "origin", "main", user, "")
			}},
		)
	}
	return sites
}

// TestRemoteTransferDeadline pins AC-1 of GH#7989: a network statement that
// blocks until its context ends is stopped by the opted-in transfer deadline,
// and the error keeps context.DeadlineExceeded and the engine's own error
// while naming the operation, the budget and the setting. Without the
// deadline the statement runs on an unbounded context and only the watchdog
// frees it.
func TestRemoteTransferDeadline(t *testing.T) {
	const timeout = 50 * time.Millisecond
	t.Setenv("DOLT_REMOTE_PASSWORD", "s3cret-password")

	t.Run("blocked statement times out", func(t *testing.T) {
		for _, site := range transferSites() {
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, true)
				err := site.call(WithTransferTimeout(context.Background(), timeout), db)

				if !db.endedByCtx[0] {
					t.Fatalf("statement was released by the watchdog, not its context: no transfer deadline (err %v)", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("errors.Is(err, DeadlineExceeded) = false: %v", err)
				}
				if !errors.Is(err, errEngineTransfer) {
					t.Errorf("errors.Is(err, engine error) = false: %v", err)
				}
				var tte *TransferTimeoutError
				if !errors.As(err, &tte) || tte.Timeout != timeout || tte.Op != site.op {
					t.Errorf("errors.As TransferTimeoutError = %+v, want op %q timeout %s", tte, site.op, timeout)
				}
				msg := err.Error()
				for _, want := range []string{site.op, timeout.String(), "dolt.remote-timeout", "BD_DOLT_REMOTE_TIMEOUT"} {
					if !strings.Contains(msg, want) {
						t.Errorf("error %q does not mention %q", msg, want)
					}
				}
				if strings.Contains(msg, "s3cret-password") {
					t.Errorf("error leaks the remote password: %q", msg)
				}
			})
		}
	})

	t.Run("fast success", func(t *testing.T) {
		for _, site := range transferSites() {
			if strings.HasPrefix(site.name, "Pull") {
				continue // a successful fetch goes on to merge; see TestPullTransferDeadlineScope
			}
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, false)
				if err := site.call(WithTransferTimeout(context.Background(), time.Hour), db); err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if _, ok := db.statementCtx(t).Deadline(); !ok {
					t.Error("statement context has no transfer deadline")
				}
			})
		}
	})

	t.Run("error before deadline", func(t *testing.T) {
		for _, site := range transferSites() {
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, false)
				db.result = errors.New("remote rejected")
				err := site.call(WithTransferTimeout(context.Background(), time.Hour), db)
				if !errors.Is(err, db.result) {
					t.Fatalf("err = %v, want the statement's own error", err)
				}
				var tte *TransferTimeoutError
				if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &tte) {
					t.Errorf("an early statement error is reported as a timeout: %v", err)
				}
			})
		}
	})

	t.Run("caller already cancelled", func(t *testing.T) {
		for _, site := range transferSites() {
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, true)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				err := site.call(WithTransferTimeout(ctx, time.Hour), db)
				assertNotBlamedOnTransferSetting(t, err)
				if !errors.Is(db.statementCtx(t).Err(), context.Canceled) {
					t.Errorf("statement context err = %v, want the caller's Canceled", db.statementCtx(t).Err())
				}
			})
		}
	})

	t.Run("earlier caller deadline wins", func(t *testing.T) {
		for _, site := range transferSites() {
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, true)
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				callerDeadline, _ := ctx.Deadline()
				err := site.call(WithTransferTimeout(ctx, time.Hour), db)
				assertNotBlamedOnTransferSetting(t, err)
				got, ok := db.statementCtx(t).Deadline()
				if !ok || !got.Equal(callerDeadline) {
					t.Errorf("statement deadline = %v (set %v), want the caller's %v", got, ok, callerDeadline)
				}
			})
		}
	})

	t.Run("no opt-in leaves the statement unbounded", func(t *testing.T) {
		for _, site := range transferSites() {
			if strings.HasPrefix(site.name, "Pull") {
				continue
			}
			t.Run(site.name, func(t *testing.T) {
				db := newTransferConn(t, false)
				if err := site.call(context.Background(), db); err != nil {
					t.Fatalf("err = %v", err)
				}
				if d, ok := db.statementCtx(t).Deadline(); ok {
					t.Errorf("statement got deadline %v without opting in", d)
				}
			})
		}
	})
}

func assertNotBlamedOnTransferSetting(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want the statement's error")
	}
	var tte *TransferTimeoutError
	if errors.As(err, &tte) || strings.Contains(err.Error(), "dolt.remote-timeout") {
		t.Errorf("caller cancellation blamed on the transfer setting: %v", err)
	}
}
