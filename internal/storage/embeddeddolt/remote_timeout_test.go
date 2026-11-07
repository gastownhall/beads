//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// errBlockedTransfer is what blockingTransferConn's network statement returns
// once its context ends (or the watchdog frees it).
var errBlockedTransfer = errors.New("engine: transfer interrupted")

// blockingTransferConn stands in for the engine connection at the network
// statement only: DOLT_PUSH/DOLT_FETCH block until their context ends, as a
// hung git transport does, unless block is false. The watchdog frees a
// statement whose context never ends so a missing deadline fails an
// assertion instead of hanging the test.
type blockingTransferConn struct {
	block   bool
	release chan struct{}

	mu         sync.Mutex
	ctxs       []context.Context
	endedByCtx []bool
}

func newBlockingTransferConn(t *testing.T, block bool) *blockingTransferConn {
	t.Helper()
	c := &blockingTransferConn{block: block, release: make(chan struct{})}
	watchdog := time.AfterFunc(5*time.Second, func() { close(c.release) })
	t.Cleanup(func() {
		if watchdog.Stop() {
			close(c.release)
		}
	})
	return c
}

func (c *blockingTransferConn) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	c.mu.Lock()
	c.ctxs = append(c.ctxs, ctx)
	c.mu.Unlock()
	if !strings.Contains(query, "DOLT_PUSH") && !strings.Contains(query, "DOLT_FETCH") {
		return nil, errors.New("blockingTransferConn: unexpected statement " + query)
	}
	var err error
	if c.block {
		select {
		case <-ctx.Done():
		case <-c.release:
		}
		err = errBlockedTransfer
	}
	c.mu.Lock()
	c.endedByCtx = append(c.endedByCtx, ctx.Err() != nil)
	c.mu.Unlock()
	return nil, err
}

func (c *blockingTransferConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("blockingTransferConn: queries are not supported")
}

func (c *blockingTransferConn) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

// only returns the one network statement's context and whether that
// context's end released it.
func (c *blockingTransferConn) only(t *testing.T) (context.Context, bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ctxs) != 1 || len(c.endedByCtx) != 1 {
		t.Fatalf("network statements issued = %d, want 1", len(c.ctxs))
	}
	return c.ctxs[0], c.endedByCtx[0]
}

// routeRemoteStatementsTo keeps the real versioncontrolops verbs (and so
// their transfer deadline handling) but sends their statements to conn
// instead of the engine, which has no reachable remote in a unit test.
func routeRemoteStatementsTo(t *testing.T, conn versioncontrolops.DBConn) {
	t.Helper()
	prevPush, prevForce, prevPull, prevPullStrategy, prevFetch := vcPush, vcForcePush, vcPull, vcPullWithStrategy, vcFetch
	t.Cleanup(func() {
		vcPush, vcForcePush, vcPull, vcPullWithStrategy, vcFetch = prevPush, prevForce, prevPull, prevPullStrategy, prevFetch
	})
	vcPush = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.Push(ctx, conn, remote, branch, user)
	}
	vcForcePush = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.ForcePush(ctx, conn, remote, branch, user)
	}
	vcPull = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.Pull(ctx, conn, remote, branch, user)
	}
	vcPullWithStrategy = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user, strategy string) error {
		return versioncontrolops.PullWithStrategy(ctx, conn, remote, branch, user, strategy)
	}
	vcFetch = func(ctx context.Context, _ versioncontrolops.DBConn, peer, user string) error {
		return versioncontrolops.Fetch(ctx, conn, peer, user)
	}
}

type remoteEntryPoint struct {
	name string
	pull bool // a successful fetch goes on to a merge
	call func(context.Context, *EmbeddedDoltStore) error
}

// remoteEntryPoints lists every embedded verb that issues a network
// statement; PushRemote appears twice for its two statements.
var remoteEntryPoints = []remoteEntryPoint{
	{"Push", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.Push(ctx) }},
	{"ForcePush", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.ForcePush(ctx) }},
	{"PushRemote", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushRemote(ctx, "team", false) }},
	{"PushRemote force", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushRemote(ctx, "team", true) }},
	{"PushTo", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushTo(ctx, "team") }},
	{"Fetch", false, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.Fetch(ctx, "team") }},
	{"Pull", true, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.Pull(ctx) }},
	{"PullWithStrategy", true, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PullWithStrategy(ctx, "ours") }},
	{"PullRemote", true, func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PullRemote(ctx, "team") }},
	{"PullRemoteWithStrategy", true, func(ctx context.Context, s *EmbeddedDoltStore) error {
		return s.PullRemoteWithStrategy(ctx, "team", "theirs")
	}},
	{"PullFrom", true, func(ctx context.Context, s *EmbeddedDoltStore) error {
		_, err := s.PullFrom(ctx, "team")
		return err
	}},
}

// TestEmbeddedRemoteTimeoutEntryPoints pins AC-7 of GH#7989 at the store:
// every embedded remote verb opts its network statement into
// dolt.remote-timeout, so a blocked transfer stops when the configured
// budget elapses; an unset value applies the five-minute default and an
// earlier caller deadline still wins.
func TestEmbeddedRemoteTimeoutEntryPoints(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	store := newPeerAuthTestStore(t)

	t.Run("configured timeout stops a blocked transfer", func(t *testing.T) {
		const timeout = 200 * time.Millisecond
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", timeout.String())
		for _, ep := range remoteEntryPoints {
			t.Run(ep.name, func(t *testing.T) {
				conn := newBlockingTransferConn(t, true)
				routeRemoteStatementsTo(t, conn)

				err := ep.call(t.Context(), store)
				if _, ended := conn.only(t); !ended {
					t.Fatalf("network statement was freed by the watchdog: %s did not apply the configured timeout (err %v)", ep.name, err)
				}
				var tte *versioncontrolops.TransferTimeoutError
				if !errors.As(err, &tte) || tte.Timeout != timeout {
					t.Fatalf("err = %v, want a TransferTimeoutError for %s", err, timeout)
				}
				if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errBlockedTransfer) {
					t.Errorf("err %v lost DeadlineExceeded or the engine error", err)
				}
			})
		}
	})

	t.Run("unset timeout uses the five-minute default", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "")
		for _, ep := range remoteEntryPoints {
			if ep.pull {
				continue // the fake answers no merge statements
			}
			t.Run(ep.name, func(t *testing.T) {
				conn := newBlockingTransferConn(t, false)
				routeRemoteStatementsTo(t, conn)

				start := time.Now()
				if err := ep.call(t.Context(), store); err != nil {
					t.Fatalf("%s: %v", ep.name, err)
				}
				ctx, _ := conn.only(t)
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("network statement ran without a deadline")
				}
				if got := deadline.Sub(start); got < 5*time.Minute-time.Minute || got > 5*time.Minute+time.Minute {
					t.Errorf("statement budget = %s, want about 5m", got)
				}
			})
		}
	})

	t.Run("earlier caller deadline wins", func(t *testing.T) {
		t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "1h")
		for _, ep := range remoteEntryPoints {
			t.Run(ep.name, func(t *testing.T) {
				conn := newBlockingTransferConn(t, true)
				routeRemoteStatementsTo(t, conn)

				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				callerDeadline, _ := ctx.Deadline()
				err := ep.call(ctx, store)
				stmtCtx, _ := conn.only(t)
				if got, ok := stmtCtx.Deadline(); !ok || !got.Equal(callerDeadline) {
					t.Errorf("statement deadline = %v (set %v), want the caller's %v", got, ok, callerDeadline)
				}
				var tte *versioncontrolops.TransferTimeoutError
				if err == nil || errors.As(err, &tte) {
					t.Errorf("err = %v, want the caller's deadline, not the transfer setting", err)
				}
			})
		}
	})
}
