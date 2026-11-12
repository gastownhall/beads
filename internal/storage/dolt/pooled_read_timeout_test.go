package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// testPoolReadDeadline is the pool read deadline the scripted-driver tests
// give their store. A "slow" scripted failure sleeps past it; a "fast" one
// returns at once.
const testPoolReadDeadline = 200 * time.Millisecond

const testSlowFailure = testPoolReadDeadline + 50*time.Millisecond

// pooledReadDriver is a database/sql driver whose every query is answered by
// a test-supplied handler, so a test can script how each attempt of a pooled
// read helper fails and count how many attempts reached the "server".
type pooledReadDriver struct {
	mu      sync.Mutex
	begins  int
	queries []string
	handle  func(query string, call int) (driver.Rows, error)
}

func (d *pooledReadDriver) Open(string) (driver.Conn, error) { return &pooledReadConn{d: d}, nil }

func (d *pooledReadDriver) Connect(context.Context) (driver.Conn, error) {
	return &pooledReadConn{d: d}, nil
}

func (d *pooledReadDriver) Driver() driver.Driver { return d }

// Queries returns the statements that reached the driver.
func (d *pooledReadDriver) Queries() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.queries...)
}

// Begins returns how many read transactions were opened.
func (d *pooledReadDriver) Begins() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.begins
}

type pooledReadConn struct{ d *pooledReadDriver }

func (c *pooledReadConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("pooledReadConn: prepare not supported")
}
func (c *pooledReadConn) Close() error { return nil }
func (c *pooledReadConn) Begin() (driver.Tx, error) {
	c.d.mu.Lock()
	c.d.begins++
	c.d.mu.Unlock()
	return pooledReadTx{}, nil
}

func (c *pooledReadConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.d.mu.Lock()
	c.d.queries = append(c.d.queries, query)
	call := len(c.d.queries)
	handle := c.d.handle
	c.d.mu.Unlock()
	return handle(query, call)
}

type pooledReadTx struct{}

func (pooledReadTx) Commit() error   { return nil }
func (pooledReadTx) Rollback() error { return nil }

// pooledReadRows is a one-column result. After its values it sleeps delay and
// returns nextErr instead of io.EOF when nextErr is set, modelling a read that
// fails mid-stream.
type pooledReadRows struct {
	values  []driver.Value
	nextErr error
	delay   time.Duration
}

func (r *pooledReadRows) Columns() []string { return []string{"v"} }
func (r *pooledReadRows) Close() error      { return nil }
func (r *pooledReadRows) Next(dest []driver.Value) error {
	if len(r.values) > 0 {
		dest[0] = r.values[0]
		r.values = r.values[1:]
		return nil
	}
	if r.nextErr != nil {
		time.Sleep(r.delay)
		return r.nextErr
	}
	return io.EOF
}

// newPooledReadStore is a server-mode store over a pooledReadDriver whose pool
// read deadline is deadline.
func newPooledReadStore(t *testing.T, deadline time.Duration, handle func(query string, call int) (driver.Rows, error)) (*DoltStore, *pooledReadDriver) {
	t.Helper()
	d := &pooledReadDriver{handle: handle}
	db := sql.OpenDB(d)
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db, serverMode: true, poolReadTimeout: deadline}, d
}

// failSlowly sleeps past the test deadline, then fails the way the MySQL
// driver reports a read deadline: a bare invalid connection.
func failSlowly() (driver.Rows, error) {
	time.Sleep(testSlowFailure)
	return nil, mysql.ErrInvalidConn
}

// searchCountsHandler answers SearchIssuesWithCounts' wisp probe as a healthy
// read, then hands the main counts query to mainQuery.
func searchCountsHandler(mainQuery func(call int) (driver.Rows, error)) func(string, int) (driver.Rows, error) {
	return func(query string, call int) (driver.Rows, error) {
		if strings.HasPrefix(query, "SELECT 1 FROM wisp_dependencies") {
			return &pooledReadRows{values: []driver.Value{int64(1)}}, nil
		}
		return mainQuery(call)
	}
}

func countQueries(queries []string, prefix string) int {
	n := 0
	for _, q := range queries {
		if strings.HasPrefix(strings.TrimSpace(q), prefix) {
			n++
		}
	}
	return n
}

// requireSuspectedReadTimeout asserts err is an unconfirmed ReadTimeoutError
// for testPoolReadDeadline whose message carries op, the "possible" wording
// and both override names.
func requireSuspectedReadTimeout(t *testing.T, err error, op string) {
	t.Helper()
	var rte *storage.ReadTimeoutError
	if !errors.As(err, &rte) {
		t.Fatalf("error = %v (%T), want *storage.ReadTimeoutError", err, err)
	}
	if rte.Confirmed {
		t.Errorf("Confirmed = true for an inferred timeout: %v", err)
	}
	if rte.Timeout != testPoolReadDeadline {
		t.Errorf("Timeout = %s, want the pool deadline %s", rte.Timeout, testPoolReadDeadline)
	}
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Errorf("error %v does not unwrap to mysql.ErrInvalidConn", err)
	}
	msg := err.Error()
	for _, want := range []string{
		op,
		"possible pool read timeout (configured 200ms)",
		"read not retried",
		storage.PoolReadTimeoutEnv,
		storage.PoolReadTimeoutConfig,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "timed out (") {
		t.Errorf("unconfirmed error claims a confirmed timeout: %q", msg)
	}
}

// TestPooledReadTimeoutPolicy pins which pooled read failures the managed
// retry may replay (GH#5258). A read that failed with invalid connection after
// running the pool's read deadline is not re-sent: the server may still be
// running it, and each replay parks another session there.
func TestPooledReadTimeoutPolicy(t *testing.T) {
	ctx := context.Background()

	// AC-1: each covered helper runs a slow invalid connection once.
	t.Run("withReadTx search counts slow invalid connection runs once", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, searchCountsHandler(func(int) (driver.Rows, error) {
			return failSlowly()
		}))
		_, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
		requireSuspectedReadTimeout(t, err, "search count issues: invalid connection")
		if got := d.Begins(); got != 1 {
			t.Errorf("read transaction opened %d times, want 1 (no replay)", got)
		}
		// The healthy wisp probe ran before the reset: still only suspected.
		if got := countQueries(d.Queries(), "SELECT 1 FROM wisp_dependencies"); got != 1 {
			t.Errorf("wisp probe ran %d times, want 1", got)
		}
	})

	t.Run("withReadTx rows error after a slow stream runs once", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, searchCountsHandler(func(int) (driver.Rows, error) {
			return &pooledReadRows{nextErr: mysql.ErrInvalidConn, delay: testSlowFailure}, nil
		}))
		_, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
		requireSuspectedReadTimeout(t, err, "search count issues: rows: invalid connection")
		if got := d.Begins(); got != 1 {
			t.Errorf("read transaction opened %d times, want 1 (no replay)", got)
		}
	})

	t.Run("queryContext slow invalid connection runs once", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, func(string, int) (driver.Rows, error) {
			return failSlowly()
		})
		rows, err := s.queryContext(ctx, "SELECT id FROM issues")
		if rows != nil {
			_ = rows.Close()
		}
		requireSuspectedReadTimeout(t, err, "query: invalid connection")
		if got := len(d.Queries()); got != 1 {
			t.Errorf("query sent %d times, want 1 (no replay)", got)
		}
	})

	t.Run("queryRowContext slow invalid connection runs once", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, func(string, int) (driver.Rows, error) {
			return failSlowly()
		})
		var v int64
		err := s.queryRowContext(ctx, func(row *sql.Row) error { return row.Scan(&v) }, "SELECT COUNT(*) FROM issues")
		requireSuspectedReadTimeout(t, err, "query row: invalid connection")
		if got := len(d.Queries()); got != 1 {
			t.Errorf("query sent %d times, want 1 (no replay)", got)
		}
	})

	// queryContext classifies only the error that obtains Rows: a failure the
	// caller meets later, iterating, is outside the helper and stays raw.
	t.Run("queryContext does not classify later iteration errors", func(t *testing.T) {
		s, _ := newPooledReadStore(t, testPoolReadDeadline, func(string, int) (driver.Rows, error) {
			return &pooledReadRows{nextErr: mysql.ErrInvalidConn, delay: testSlowFailure}, nil
		})
		rows, err := s.queryContext(ctx, "SELECT id FROM issues")
		if err != nil {
			t.Fatalf("queryContext: %v", err)
		}
		for rows.Next() {
		}
		iterErr := rows.Err()
		_ = rows.Close()
		var rte *storage.ReadTimeoutError
		if !errors.Is(iterErr, mysql.ErrInvalidConn) || errors.As(iterErr, &rte) {
			t.Errorf("iteration error = %v, want the raw invalid connection", iterErr)
		}
	})

	t.Run("no pool deadline keeps the retry", func(t *testing.T) {
		s, d := newPooledReadStore(t, 0, func(_ string, call int) (driver.Rows, error) {
			if call == 1 {
				return failSlowly()
			}
			return &pooledReadRows{values: []driver.Value{int64(1)}}, nil
		})
		var v int64
		if err := s.queryRowContext(ctx, func(row *sql.Row) error { return row.Scan(&v) }, "SELECT 1"); err != nil {
			t.Errorf("queryRowContext with no pool deadline: %v", err)
		}
		if got := len(d.Queries()); got != 2 {
			t.Errorf("sent %d queries, want 2: a store without a pool deadline keeps its retry", got)
		}
	})
}

// TestClassifyPooledReadFailure pins the classifier's decision table with
// elapsed time supplied directly, so the threshold cases are deterministic.
func TestClassifyPooledReadFailure(t *testing.T) {
	const deadline = 10 * time.Second
	readTimeout := &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name      string
		ctx       context.Context
		err       error
		elapsed   time.Duration
		deadline  time.Duration
		classify  bool
		confirmed bool
	}{
		{"slow invalid connection", nil, mysql.ErrInvalidConn, deadline, deadline, true, false},
		{"slow legacy invalid connection text", nil, errors.New("begin read tx: invalid connection"), deadline + time.Second, deadline, true, false},
		{"fast invalid connection", nil, mysql.ErrInvalidConn, deadline - time.Millisecond, deadline, false, false},
		{"retained read timeout", nil, readTimeout, deadline, deadline, true, true},
		{"dial timeout", nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}, deadline, deadline, false, false},
		{"write timeout", nil, &net.OpError{Op: "write", Net: "tcp", Err: timeoutErr{}}, deadline, deadline, false, false},
		{"decoded server error with timeout wording", nil, &mysql.MySQLError{Number: 1105, Message: "read tcp: i/o timeout: invalid connection"}, deadline, deadline, false, false},
		{"other connection text", nil, errors.New("connection reset by peer"), deadline, deadline, false, false},
		{"invalid connection mid-text", nil, errors.New("invalid connection pool settings ignored"), deadline, deadline, false, false},
		{"canceled context", canceled, mysql.ErrInvalidConn, deadline, deadline, false, false},
		{"context deadline error", nil, context.DeadlineExceeded, deadline, deadline, false, false},
		{"no pool deadline", nil, mysql.ErrInvalidConn, time.Hour, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			got := classifyPooledReadFailure(ctx, tc.err, tc.elapsed, tc.deadline, "")
			var rte *storage.ReadTimeoutError
			if !errors.As(got, &rte) {
				if tc.classify {
					t.Fatalf("classifyPooledReadFailure(%v) = %v, want a ReadTimeoutError", tc.err, got)
				}
				if got != tc.err {
					t.Errorf("unclassified error changed: %v -> %v", tc.err, got)
				}
				return
			}
			if !tc.classify {
				t.Fatalf("classifyPooledReadFailure(%v) = %v, want the error unchanged", tc.err, got)
			}
			if rte.Confirmed != tc.confirmed {
				t.Errorf("Confirmed = %v, want %v", rte.Confirmed, tc.confirmed)
			}
			wantWord := "possible pool read timeout"
			if tc.confirmed {
				wantWord = "pool read timed out"
			}
			if !strings.Contains(got.Error(), wantWord) {
				t.Errorf("message %q lacks %q", got.Error(), wantWord)
			}
		})
	}
}

// TestPoolReadTimeoutFromDSN pins that the captured deadline is the one
// buildServerDSN gives the pool: default, command fallback, explicit setting.
func TestPoolReadTimeoutFromDSN(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want time.Duration
	}{
		{"default", Config{ServerHost: "127.0.0.1", ServerPort: 3307}, defaultPoolReadTimeout},
		{"fallback", Config{ServerHost: "127.0.0.1", ServerPort: 3307, PoolReadTimeoutFallback: time.Minute}, time.Minute},
		{"override beats fallback", Config{ServerHost: "127.0.0.1", ServerPort: 3307, PoolReadTimeoutFallback: time.Minute, PoolReadTimeout: 3 * time.Second}, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if got := poolReadTimeoutFromDSN(buildServerDSN(&cfg, "beads")); got != tc.want {
				t.Errorf("poolReadTimeoutFromDSN = %s, want %s", got, tc.want)
			}
		})
	}
	if got := poolReadTimeoutFromDSN(""); got != 0 {
		t.Errorf("empty DSN deadline = %s, want 0", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
