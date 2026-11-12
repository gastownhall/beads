package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// poolReadPolicyStep is one scripted response to a query. Exactly one of the
// fields is meaningful: value answers a query with a single int64 row, err
// fails the query itself, and rowsErr succeeds the query but fails the first
// rows.Next (so the caller sees it through Rows.Err).
type poolReadPolicyStep struct {
	delay   time.Duration
	value   int64
	err     error
	rowsErr error
}

// poolReadPolicyDriver is a scripted database/sql driver for the pooled read
// timeout policy: every query consumes the next script step (the last step
// repeats when the script runs dry), and Begin counts attempts. Modeled on
// claimCommitBoundaryDriver.
type poolReadPolicyDriver struct {
	mu      sync.Mutex
	script  []poolReadPolicyStep
	queries int
	begins  int
}

func (d *poolReadPolicyDriver) next() poolReadPolicyStep {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries++
	if len(d.script) == 0 {
		return poolReadPolicyStep{value: 1}
	}
	step := d.script[0]
	if len(d.script) > 1 {
		d.script = d.script[1:]
	}
	return step
}

func (d *poolReadPolicyDriver) Open(string) (driver.Conn, error) {
	return &poolReadPolicyConn{driver: d}, nil
}

func (d *poolReadPolicyDriver) Connect(context.Context) (driver.Conn, error) {
	return &poolReadPolicyConn{driver: d}, nil
}

func (d *poolReadPolicyDriver) Driver() driver.Driver { return d }

func (d *poolReadPolicyDriver) counts() (queries, begins int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries, d.begins
}

type poolReadPolicyConn struct {
	driver *poolReadPolicyDriver
}

func (c *poolReadPolicyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("pool read policy driver does not prepare statements")
}

func (c *poolReadPolicyConn) Close() error { return nil }

func (c *poolReadPolicyConn) Begin() (driver.Tx, error) {
	c.driver.mu.Lock()
	c.driver.begins++
	c.driver.mu.Unlock()
	return poolReadPolicyTx{}, nil
}

func (c *poolReadPolicyConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	step := c.driver.next()
	if step.delay > 0 {
		select {
		case <-time.After(step.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch {
	case step.rowsErr != nil:
		return &poolReadPolicyRows{err: step.rowsErr}, nil
	case step.err != nil:
		return nil, step.err
	default:
		return &poolReadPolicyRows{value: step.value}, nil
	}
}

type poolReadPolicyTx struct{}

func (poolReadPolicyTx) Commit() error   { return nil }
func (poolReadPolicyTx) Rollback() error { return nil }

type poolReadPolicyRows struct {
	err      error
	value    int64
	consumed bool
}

func (r *poolReadPolicyRows) Columns() []string { return []string{"v"} }
func (r *poolReadPolicyRows) Close() error      { return nil }
func (r *poolReadPolicyRows) Next(dest []driver.Value) error {
	if r.err != nil && !r.consumed {
		r.consumed = true
		return r.err
	}
	if r.consumed {
		return io.EOF
	}
	r.consumed = true
	if len(dest) > 0 {
		dest[0] = r.value
	}
	return nil
}

// policyTestDeadline is a short pool deadline; policyDelayedQuery sleeps past
// it so a scripted attempt runs at least the deadline.
const (
	policyTestDeadline = 50 * time.Millisecond
	policyDelayedQuery = 60 * time.Millisecond
)

// newPoolReadPolicyStore opens a bare store over the scripted driver with the
// given effective pool read deadline. It skips the real open path (ping,
// schema, migrations) the same way the other scripted-driver stores do; the
// helpers under test only need db, the breaker and the captured deadline.
func newPoolReadPolicyStore(script []poolReadPolicyStep, deadline time.Duration) (*DoltStore, *poolReadPolicyDriver) {
	drv := &poolReadPolicyDriver{script: script}
	return &DoltStore{db: sql.OpenDB(drv), poolReadDeadline: deadline}, drv
}

// scanOneRow scans a single int64 through the tx/row so a scripted step is
// consumed inside the read attempt.
func scanOneRow(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) error {
	var v int64
	return q.QueryRowContext(ctx, "SELECT 1").Scan(&v)
}

// netTimeoutError is a net.Error that reports Timeout.
type netTimeoutError struct{}

func (netTimeoutError) Error() string   { return "i/o timeout" }
func (netTimeoutError) Timeout() bool   { return true }
func (netTimeoutError) Temporary() bool { return false }

func netReadTimeout() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: netTimeoutError{}}
}

func netDialTimeout() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: netTimeoutError{}}
}

func netWriteTimeout() error {
	return &net.OpError{Op: "write", Net: "tcp", Err: netTimeoutError{}}
}

func netConnReset() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
}

// readTimeoutFrom unwraps the typed error or fails the test.
func readTimeoutFrom(t *testing.T, err error) *storage.ReadTimeoutError {
	t.Helper()
	var typed *storage.ReadTimeoutError
	if !errors.As(err, &typed) {
		t.Fatalf("error is not a storage.ReadTimeoutError: %v", err)
	}
	return typed
}

// assertReadTimeoutMessage pins the typed error's single-line contract: the
// operation label, the cause text, the qualifier, the configured deadline, the
// no-retry statement, and both override knobs.
func assertReadTimeoutMessage(t *testing.T, err error, op string, confirmed bool, deadline time.Duration, cause string) {
	t.Helper()
	msg := err.Error()
	if want := op + ": "; !strings.HasPrefix(msg, want) {
		t.Errorf("message %q does not start with the %q operation label", msg, op)
	}
	qualifier := "possible pool read timeout"
	if confirmed {
		qualifier = "pool read timed out"
	}
	for _, want := range []string{
		cause + ": ",
		qualifier,
		fmt.Sprintf("(configured %s)", deadline),
		"read not retried",
		"BEADS_DOLT_POOL_READ_TIMEOUT",
		"dolt.pool-read-timeout",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	if strings.Count(msg, "\n") != 0 {
		t.Errorf("message must be a single stderr line, got %q", msg)
	}
}

// TestPooledReadTimeoutPolicy drives the three covered pooled read helpers —
// withReadTx, queryContext, queryRowContext — over a scripted driver and pins
// the classification policy (GH#5258): a read that runs at least the pool
// deadline and fails with the identity the driver leaves behind is reported
// as a typed storage.ReadTimeoutError and never resubmitted, while every
// other failure keeps its existing retry semantics.
func TestPooledReadTimeoutPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("slow invalid connection is typed, suspected, and runs once", func(t *testing.T) {
		t.Run("withReadTx", func(t *testing.T) {
			store, drv := newPoolReadPolicyStore(
				[]poolReadPolicyStep{{delay: policyDelayedQuery, err: mysql.ErrInvalidConn}},
				policyTestDeadline)
			err := store.withReadTx(ctx, func(tx *sql.Tx) error {
				return scanOneRow(ctx, tx)
			})
			typed := readTimeoutFrom(t, err)
			if typed.Confirmed {
				t.Errorf("Confirmed = true; an ErrInvalidConn lost the network cause and must stay suspected")
			}
			if typed.Timeout != policyTestDeadline {
				t.Errorf("Timeout = %s, want the effective pool deadline %s", typed.Timeout, policyTestDeadline)
			}
			assertReadTimeoutMessage(t, err, "read transaction", false, policyTestDeadline, "invalid connection")
			if queries, begins := drv.counts(); queries != 1 || begins != 1 {
				t.Errorf("the failed read ran %d queries in %d attempts; a classified pool read timeout must run once", queries, begins)
			}
		})

		t.Run("queryContext", func(t *testing.T) {
			store, drv := newPoolReadPolicyStore(
				[]poolReadPolicyStep{{delay: policyDelayedQuery, err: mysql.ErrInvalidConn}},
				policyTestDeadline)
			rows, err := store.queryContext(ctx, "SELECT 1")
			if rows != nil {
				t.Errorf("queryContext returned rows beside its error")
			}
			assertReadTimeoutMessage(t, err, "query", false, policyTestDeadline, "invalid connection")
			if queries, _ := drv.counts(); queries != 1 {
				t.Errorf("the failed query ran %d times; a classified pool read timeout must run once", queries)
			}
		})

		t.Run("queryRowContext", func(t *testing.T) {
			store, drv := newPoolReadPolicyStore(
				[]poolReadPolicyStep{{delay: policyDelayedQuery, err: mysql.ErrInvalidConn}},
				policyTestDeadline)
			err := store.queryRowContext(ctx, func(row *sql.Row) error {
				var v int64
				return row.Scan(&v)
			}, "SELECT 1")
			assertReadTimeoutMessage(t, err, "query row", false, policyTestDeadline, "invalid connection")
			if queries, _ := drv.counts(); queries != 1 {
				t.Errorf("the failed query ran %d times; a classified pool read timeout must run once", queries)
			}
		})
	})

	t.Run("retained network read timeout is confirmed even without elapsed time", func(t *testing.T) {
		store, _ := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{err: netReadTimeout()}},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			return scanOneRow(ctx, tx)
		})
		typed := readTimeoutFrom(t, err)
		if !typed.Confirmed {
			t.Errorf("Confirmed = false; a retained network read timeout proves the deadline fired: %v", err)
		}
		assertReadTimeoutMessage(t, err, "read transaction", true, policyTestDeadline, "read tcp: i/o timeout")
	})

	t.Run("dial and write timeouts are never classified", func(t *testing.T) {
		for name, dialOrWrite := range map[string]error{
			"dial timeout":  netDialTimeout(),
			"write timeout": netWriteTimeout(),
		} {
			t.Run(name, func(t *testing.T) {
				store, drv := newPoolReadPolicyStore(
					[]poolReadPolicyStep{
						{delay: policyDelayedQuery, err: dialOrWrite},
						{value: 1},
					},
					policyTestDeadline)
				err := store.withReadTx(ctx, func(tx *sql.Tx) error {
					return scanOneRow(ctx, tx)
				})
				if err != nil {
					t.Fatalf("existing retry recovery should have succeeded: %v", err)
				}
				var typed *storage.ReadTimeoutError
				if errors.As(err, &typed) {
					t.Errorf("a %s must not be classified as a pool read timeout", name)
				}
				if _, begins := drv.counts(); begins != 2 {
					t.Errorf("attempts = %d; a non-read timeout keeps its existing retry", begins)
				}
			})
		}
	})

	t.Run("decoded SQL errors with timeout-like wording keep their behavior", func(t *testing.T) {
		serverErr := &mysql.MySQLError{Number: 1105, Message: "query timed out waiting for the server"}
		store, drv := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{delay: policyDelayedQuery, err: serverErr}},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			return scanOneRow(ctx, tx)
		})
		if !errors.Is(err, serverErr) {
			t.Fatalf("the decoded MySQL error must be returned unchanged, got: %v", err)
		}
		var typed *storage.ReadTimeoutError
		if errors.As(err, &typed) {
			t.Errorf("a decoded server response must not be diagnosed as a pool read timeout")
		}
		if queries, begins := drv.counts(); queries != 1 || begins != 1 {
			t.Errorf("a non-retryable decoded 1105 ran %d queries in %d attempts; want one", queries, begins)
		}
	})

	t.Run("cancellation is preserved, never diagnosed", func(t *testing.T) {
		store, _ := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{delay: policyDelayedQuery, err: mysql.ErrInvalidConn}},
			policyTestDeadline)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := store.withReadTx(canceled, func(tx *sql.Tx) error {
			return scanOneRow(canceled, tx)
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation must be preserved verbatim, got: %v", err)
		}
		var typed *storage.ReadTimeoutError
		if errors.As(err, &typed) {
			t.Errorf("a caller cancellation must not be diagnosed as a pool read timeout")
		}
	})

	t.Run("no pool deadline (embedded mode) keeps existing retries", func(t *testing.T) {
		store, drv := newPoolReadPolicyStore(
			[]poolReadPolicyStep{
				{delay: policyDelayedQuery, err: mysql.ErrInvalidConn},
				{value: 1},
			},
			0)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			return scanOneRow(ctx, tx)
		})
		if err != nil {
			t.Fatalf("existing retry recovery should have succeeded without a deadline: %v", err)
		}
		if _, begins := drv.counts(); begins != 2 {
			t.Errorf("attempts = %d; without a captured deadline a slow invalid connection keeps its existing retry", begins)
		}
	})

	t.Run("multiple healthy subreads then a reset stay suspected", func(t *testing.T) {
		store, drv := newPoolReadPolicyStore(
			[]poolReadPolicyStep{
				{value: 1},
				{delay: policyDelayedQuery, err: netConnReset()},
			},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			if err := scanOneRow(ctx, tx); err != nil {
				return err
			}
			return scanOneRow(ctx, tx)
		})
		typed := readTimeoutFrom(t, err)
		if typed.Confirmed {
			t.Errorf("Confirmed = true; a reset is ambiguous (earlier healthy reads consumed the time) and must stay suspected: %v", err)
		}
		assertReadTimeoutMessage(t, err, "read transaction", false, policyTestDeadline, "read tcp: connection reset by peer")
		if queries, begins := drv.counts(); queries != 2 || begins != 1 {
			t.Errorf("the attempt ran %d queries in %d attempts; a slow ambiguous failure must stop without replay", queries, begins)
		}
	})

	t.Run("slow successful read stays successful", func(t *testing.T) {
		store, drv := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{delay: policyDelayedQuery, value: 1}},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			return scanOneRow(ctx, tx)
		})
		if err != nil {
			t.Fatalf("a slow read that succeeds is a success, got: %v", err)
		}
		if queries, begins := drv.counts(); queries != 1 || begins != 1 {
			t.Errorf("the successful slow read ran %d queries in %d attempts; want one", queries, begins)
		}
	})

	t.Run("Rows.Err failures inside withReadTx are classified", func(t *testing.T) {
		store, drv := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{delay: policyDelayedQuery, rowsErr: netConnReset()}},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT 1")
			if err != nil {
				return err
			}
			for rows.Next() {
			}
			return rows.Err()
		})
		readTimeoutFrom(t, err)
		assertReadTimeoutMessage(t, err, "read transaction", false, policyTestDeadline, "read tcp: connection reset by peer")
		if queries, begins := drv.counts(); queries != 1 || begins != 1 {
			t.Errorf("the attempt ran %d queries in %d attempts; a Rows.Err pool read timeout must run once", queries, begins)
		}
	})

	t.Run("queryContext does not classify later consumer iteration", func(t *testing.T) {
		store, _ := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{rowsErr: netReadTimeout()}},
			policyTestDeadline)
		rows, err := store.queryContext(ctx, "SELECT 1")
		if err != nil {
			t.Fatalf("queryContext: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
		}
		if rows.Err() == nil {
			t.Fatal("the scripted rows were expected to fail")
		}
		var typed *storage.ReadTimeoutError
		if errors.As(rows.Err(), &typed) {
			t.Errorf("errors from consumer iteration after queryContext returned are outside the classification: %v", rows.Err())
		}
	})

	t.Run("SearchIssuesWithCounts operation context survives the typed wrap", func(t *testing.T) {
		store, _ := newPoolReadPolicyStore(
			[]poolReadPolicyStep{{delay: policyDelayedQuery, err: mysql.ErrInvalidConn}},
			policyTestDeadline)
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			// The shape issueops returns for a wisp probe that died on the
			// pooled connection: operation context first, driver error last.
			return fmt.Errorf("search issues with counts: wisp probe: %w", scanOneRowErr(ctx, tx))
		})
		readTimeoutFrom(t, err)
		msg := err.Error()
		want := "read transaction: search issues with counts: wisp probe: invalid connection: possible pool read timeout"
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not carry the caller's operation context %q", msg, want)
		}
	})
}

// scanOneRowErr runs scanOneRow and returns its error (nil becomes
// errors.New("unreachable") so the wrapper above always wraps a non-nil).
func scanOneRowErr(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) error {
	if err := scanOneRow(ctx, q); err != nil {
		return err
	}
	return errors.New("scan unexpectedly succeeded")
}

// TestClassifyPooledReadFailureThreshold pins the pure classifier's elapsed
// threshold deterministically (no sleeps): ErrInvalidConn counts only once
// the attempt has run at least the deadline, a retained network read timeout
// confirms without needing elapsed time, and a zero deadline classifies
// nothing.
func TestClassifyPooledReadFailureThreshold(t *testing.T) {
	const deadline = 10 * time.Second

	t.Run("fast invalid connection is not classified", func(t *testing.T) {
		if got := classifyPooledReadFailure(time.Millisecond, mysql.ErrInvalidConn, deadline); got != nil {
			t.Errorf("fast invalid connection classified as %v; a fast failure is the ordinary stale-pool case", got)
		}
	})

	t.Run("invalid connection at the deadline is suspected", func(t *testing.T) {
		got := classifyPooledReadFailure(deadline, mysql.ErrInvalidConn, deadline)
		typed := readTimeoutFrom(t, got)
		if typed.Confirmed {
			t.Error("Confirmed = true; an identity-lost failure must stay suspected")
		}
	})

	t.Run("invalid connection past the deadline is suspected", func(t *testing.T) {
		got := classifyPooledReadFailure(2*deadline, mysql.ErrInvalidConn, deadline)
		readTimeoutFrom(t, got)
	})

	t.Run("identity lost through a %v-style wrap still matches the driver text", func(t *testing.T) {
		got := classifyPooledReadFailure(deadline, fmt.Errorf("begin read tx: %s", mysql.ErrInvalidConn), deadline)
		readTimeoutFrom(t, got)
	})

	t.Run("retained read timeout confirms regardless of elapsed", func(t *testing.T) {
		got := classifyPooledReadFailure(time.Microsecond, netReadTimeout(), deadline)
		typed := readTimeoutFrom(t, got)
		if !typed.Confirmed {
			t.Error("Confirmed = false; a retained network read timeout proves the deadline fired")
		}
	})

	t.Run("zero deadline classifies nothing", func(t *testing.T) {
		if got := classifyPooledReadFailure(time.Hour, mysql.ErrInvalidConn, 0); got != nil {
			t.Errorf("no-deadline store classified %v; embedded mode keeps existing behavior", got)
		}
	})

	t.Run("permanent and decoded errors pass through before any text match", func(t *testing.T) {
		if got := classifyPooledReadFailure(time.Hour, backoff.Permanent(mysql.ErrInvalidConn), deadline); got != nil {
			t.Errorf("already-permanent error classified as %v; it is terminal for its own reason", got)
		}
		if got := classifyPooledReadFailure(time.Hour, fmt.Errorf("probe: %w", &mysql.MySQLError{Number: 1105, Message: "invalid connection"}), deadline); got != nil {
			t.Errorf("decoded MySQL error classified as %v; a server response is never a pool deadline", got)
		}
	})
}

// TestEffectivePoolReadDeadline pins the capture's DSN consistency: whatever
// the buildServerDSN ladder resolved (default, fallback, or explicit override)
// is exactly what the store captures as its pool read deadline.
func TestEffectivePoolReadDeadline(t *testing.T) {
	t.Run("built-in default", func(t *testing.T) {
		cfg := &Config{ServerMode: true, Database: "cap", Path: t.TempDir()}
		if got := effectivePoolReadDeadline(buildServerDSN(cfg, cfg.Database)); got != defaultPoolReadTimeout {
			t.Errorf("effectivePoolReadDeadline = %s, want the built-in default %s", got, defaultPoolReadTimeout)
		}
	})

	t.Run("fallback ladder", func(t *testing.T) {
		cfg := &Config{ServerMode: true, Database: "cap", Path: t.TempDir(), PoolReadTimeoutFallback: 45 * time.Second}
		if got := effectivePoolReadDeadline(buildServerDSN(cfg, cfg.Database)); got != 45*time.Second {
			t.Errorf("effectivePoolReadDeadline = %s, want the fallback 45s", got)
		}
	})

	t.Run("explicit override beats the fallback", func(t *testing.T) {
		cfg := &Config{
			ServerMode:              true,
			Database:                "cap",
			Path:                    t.TempDir(),
			PoolReadTimeoutFallback: 45 * time.Second,
			PoolReadTimeout:         90 * time.Second,
		}
		if got := effectivePoolReadDeadline(buildServerDSN(cfg, cfg.Database)); got != 90*time.Second {
			t.Errorf("effectivePoolReadDeadline = %s, want the explicit override 90s", got)
		}
	})

	t.Run("unparsable DSN and no-deadline DSN are zero", func(t *testing.T) {
		if got := effectivePoolReadDeadline("root@tcp(:bad:dsn"); got != 0 {
			t.Errorf("unparsable DSN captured %s, want 0 (classification off)", got)
		}
		if got := effectivePoolReadDeadline("root@tcp(127.0.0.1:3306)/db"); got != 0 {
			t.Errorf("DSN without a read timeout captured %s, want 0 (classification off)", got)
		}
	})
}
