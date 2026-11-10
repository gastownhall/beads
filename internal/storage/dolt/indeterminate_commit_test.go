package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// failureDriver provides deterministic pre-commit and commit-phase connection
// failures for the L6.4 retry-boundary tests.
type failureDriver struct {
	begins     atomic.Int32
	prepares   atomic.Int32
	execs      atomic.Int32
	failBegin  atomic.Int32
	commitMu   sync.Mutex
	commitErrs []error
	commitErr  error
	commitFunc func() error
}

var testConnectionLoss = errors.New("invalid connection")

func (d *failureDriver) Open(string) (driver.Conn, error) { return &failureConn{driver: d}, nil }
func (d *failureDriver) Connect(context.Context) (driver.Conn, error) {
	return &failureConn{driver: d}, nil
}
func (d *failureDriver) Driver() driver.Driver { return d }

type failureConn struct{ driver *failureDriver }

func (c *failureConn) Prepare(string) (driver.Stmt, error) {
	c.driver.prepares.Add(1)
	return nil, errors.New("failure driver does not prepare statements")
}
func (c *failureConn) Close() error { return nil }
func (c *failureConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.driver.execs.Add(1)
	return driver.RowsAffected(1), nil
}
func (c *failureConn) Begin() (driver.Tx, error) {
	c.driver.begins.Add(1)
	if c.driver.failBegin.Load() > 0 {
		c.driver.failBegin.Add(-1)
		return nil, testConnectionLoss
	}
	return &failureTx{driver: c.driver}, nil
}

type failureTx struct{ driver *failureDriver }

func (t *failureTx) Commit() error {
	if t.driver.commitFunc != nil {
		return t.driver.commitFunc()
	}
	return t.driver.nextCommitError()
}
func (t *failureTx) Rollback() error { return nil }

func (d *failureDriver) nextCommitError() error {
	d.commitMu.Lock()
	defer d.commitMu.Unlock()
	if len(d.commitErrs) == 0 {
		return d.commitErr
	}
	err := d.commitErrs[0]
	d.commitErrs = d.commitErrs[1:]
	return err
}

var _ driver.Connector = (*failureDriver)(nil)
var _ driver.ExecerContext = (*failureConn)(nil)

func newFailureStore(d *failureDriver) *DoltStore {
	return &DoltStore{db: sql.OpenDB(d)}
}

// TestIndeterminateCommitIsSurfacedNotRetried pins L6.4: a connection loss
// during COMMIT is surfaced as indeterminate and the write is attempted once.
func TestIndeterminateCommitIsSurfacedNotRetried(t *testing.T) {
	driver := &failureDriver{commitErrs: []error{testConnectionLoss}}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	err := store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil })
	if err == nil {
		t.Fatal("lost commit returned nil; indeterminacy must be surfaced")
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Errorf("err = %v, want ErrCommitIndeterminate", err)
	}
	if !strings.Contains(err.Error(), "indeterminate") {
		t.Errorf("err = %q, want indeterminate outcome", err)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Errorf("write attempts after lost commit = %d, want 1", got)
	}
}

func TestWithRetryTxIndeterminateCommitFailuresTripCircuitWithoutReplay(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	driver := &failureDriver{commitErr: testConnectionLoss}
	store := newFailureStore(driver)
	store.breaker = breaker
	defer func() { _ = store.db.Close() }()

	ctx := t.Context()
	var callbacks atomic.Int32
	for attempt := 1; attempt <= circuitFailureThreshold; attempt++ {
		err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
			callbacks.Add(1)
			_, err := tx.ExecContext(ctx, "UPDATE issues SET title = ?", "changed")
			return err
		})
		if !errors.Is(err, ErrCommitIndeterminate) {
			t.Fatalf("withRetryTx() attempt %d error = %v, want ErrCommitIndeterminate", attempt, err)
		}
		if got := driver.begins.Load(); got != int32(attempt) {
			t.Fatalf("transaction begins after attempt %d = %d, want %d", attempt, got, attempt)
		}
		if got := callbacks.Load(); got != int32(attempt) {
			t.Fatalf("callback calls after attempt %d = %d, want %d", attempt, got, attempt)
		}
		if got := driver.execs.Load(); got != int32(attempt) {
			t.Fatalf("SQL mutations after attempt %d = %d, want %d", attempt, got, attempt)
		}

		wantState := circuitClosed
		if attempt == circuitFailureThreshold {
			wantState = circuitOpen
		}
		if got := breaker.State(); got != wantState {
			t.Fatalf("circuit state after attempt %d = %q, want %q", attempt, got, wantState)
		}
	}

	beginCalls := driver.begins.Load()
	callbackCalls := callbacks.Load()
	mutationCalls := driver.execs.Load()
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		callbacks.Add(1)
		_, execErr := tx.ExecContext(ctx, "UPDATE issues SET title = ?", "replayed")
		return execErr
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("withRetryTx() after circuit opened error = %v, want ErrCircuitOpen", err)
	}
	if got := driver.begins.Load(); got != beginCalls {
		t.Fatalf("transaction begins after circuit opened = %d, want unchanged %d", got, beginCalls)
	}
	if got := callbacks.Load(); got != callbackCalls {
		t.Fatalf("callback calls after circuit opened = %d, want unchanged %d", got, callbackCalls)
	}
	if got := driver.execs.Load(); got != mutationCalls {
		t.Fatalf("SQL mutations after circuit opened = %d, want unchanged %d", got, mutationCalls)
	}
}

func TestWithRetryTxPreCallbackConnectionFailureTripsCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &failureDriver{}
	driver.failBegin.Store(1)
	store := newFailureStore(driver)
	store.breaker = breaker
	defer func() { _ = store.db.Close() }()

	var callbacks atomic.Int32
	err := store.withRetryTx(t.Context(), func(*sql.Tx) error {
		callbacks.Add(1)
		return nil
	})
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("withRetryTx() error = %v, want connection cause %v", err, testConnectionLoss)
	}
	if got := breaker.State(); got != circuitOpen {
		t.Fatalf("circuit state = %q, want %q", got, circuitOpen)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Fatalf("transaction begins = %d, want 1", got)
	}
	if got := callbacks.Load(); got != 0 {
		t.Fatalf("callback calls = %d, want 0 after pre-callback circuit trip", got)
	}
}

func TestWithRetryTxSuccessResetsCircuitFailures(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &failureDriver{}
	store := newFailureStore(driver)
	store.breaker = breaker
	defer func() { _ = store.db.Close() }()

	if err := store.withRetryTx(t.Context(), func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("withRetryTx() error = %v, want nil", err)
	}
	breaker.RecordFailure()
	if got := breaker.State(); got != circuitClosed {
		t.Fatalf("circuit state after success and one failure = %q, want %q", got, circuitClosed)
	}
}

func TestDoltAutocommitRollbackCommitIsRetried(t *testing.T) {
	rollback := &mysql.MySQLError{
		Number:  1105,
		Message: "Merge conflict detected, @autocommit transaction rolled back",
	}
	driver := &failureDriver{commitErrs: []error{rollback}}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	var callbacks atomic.Int32
	if err := store.withRetryTx(context.Background(), func(*sql.Tx) error {
		callbacks.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("withRetryTx() error = %v, want nil", err)
	}
	if got := driver.begins.Load(); got != 2 {
		t.Errorf("Begin calls = %d, want 2", got)
	}
	if got := callbacks.Load(); got != 2 {
		t.Errorf("callback invocations = %d, want 2", got)
	}
}

func TestTyped1105CommitErrorIsDefiniteAndNotRetried(t *testing.T) {
	cause := &mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"}
	driver := &failureDriver{commitErrs: []error{cause}}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	var callbacks atomic.Int32
	err := store.withRetryTx(context.Background(), func(*sql.Tx) error {
		callbacks.Add(1)
		return nil
	})
	if !errors.Is(err, cause) {
		t.Fatalf("withRetryTx() error = %v, want %v", err, cause)
	}
	if errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("withRetryTx() error = %v must not be marked commit-indeterminate", err)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Errorf("Begin calls = %d, want 1", got)
	}
	if got := callbacks.Load(); got != 1 {
		t.Errorf("callback invocations = %d, want 1", got)
	}
}

func TestWithWriteTxPacketSyncCommitIsIndeterminate(t *testing.T) {
	driver := &failureDriver{commitErr: mysql.ErrPktSync}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	err := store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil })
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("withRetryTx() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, mysql.ErrPktSync) {
		t.Fatalf("withRetryTx() error = %v, want cause %v", err, mysql.ErrPktSync)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Fatalf("write attempts = %d, want 1", got)
	}
}

// TestPreCommitConnectionLossIsRetriedSilently is the safe-retry contrast: a
// connection failure before a transaction begins cannot have committed data.
func TestPreCommitConnectionLossIsRetriedSilently(t *testing.T) {
	driver := &failureDriver{}
	driver.failBegin.Store(2)
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	if err := store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("pre-commit connection loss surfaced: %v", err)
	}
	if got := driver.begins.Load(); got != 3 {
		t.Errorf("Begin calls = %d, want 3", got)
	}
}

func TestExecContextCommitResponseLossIsIndeterminateAndMutationRunsOnce(t *testing.T) {
	var commitCalls atomic.Int32
	driver := &failureDriver{}
	driver.commitFunc = func() error {
		if commitCalls.Add(1) == 1 {
			return testConnectionLoss
		}
		return nil
	}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	_, err := store.execContext(context.Background(), "UPDATE issues SET title = ?", "changed")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("execContext() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("execContext() error = %v, want cause %v", err, testConnectionLoss)
	}
	if got := driver.execs.Load(); got != 1 {
		t.Fatalf("SQL mutation calls = %d, want 1", got)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Fatalf("transaction begins = %d, want 1", got)
	}
	if got := commitCalls.Load(); got != 1 {
		t.Fatalf("commit calls = %d, want 1", got)
	}
}
