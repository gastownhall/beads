package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// failureDriver provides deterministic pre-commit and commit-phase connection
// failures for the L6.4 retry-boundary tests.
type failureDriver struct {
	begins     atomic.Int32
	failBegin  atomic.Int32
	failCommit bool
}

var testConnectionLoss = errors.New("invalid connection")

func (d *failureDriver) Open(string) (driver.Conn, error) { return &failureConn{driver: d}, nil }
func (d *failureDriver) Connect(context.Context) (driver.Conn, error) {
	return &failureConn{driver: d}, nil
}
func (d *failureDriver) Driver() driver.Driver { return d }

type failureConn struct{ driver *failureDriver }

func (c *failureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("failure driver does not prepare statements")
}
func (c *failureConn) Close() error { return nil }
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
	if t.driver.failCommit {
		return testConnectionLoss
	}
	return nil
}
func (t *failureTx) Rollback() error { return nil }

var _ driver.Connector = (*failureDriver)(nil)

func newFailureStore(d *failureDriver) *DoltStore {
	return &DoltStore{db: sql.OpenDB(d)}
}

// TestIndeterminateCommitIsSurfacedNotRetried pins L6.4: a connection loss
// during COMMIT is surfaced as indeterminate and the write is attempted once.
func TestIndeterminateCommitIsSurfacedNotRetried(t *testing.T) {
	driver := &failureDriver{failCommit: true}
	store := newFailureStore(driver)
	defer func() { _ = store.db.Close() }()

	err := store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil })
	if err == nil {
		t.Fatal("lost commit returned nil; indeterminacy must be surfaced")
	}
	if !errors.Is(err, errCommitPhase) {
		t.Errorf("err = %v, want errCommitPhase", err)
	}
	if !strings.Contains(err.Error(), "indeterminate") {
		t.Errorf("err = %q, want indeterminate outcome", err)
	}
	if got := driver.begins.Load(); got != 1 {
		t.Errorf("write attempts after lost commit = %d, want 1", got)
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
