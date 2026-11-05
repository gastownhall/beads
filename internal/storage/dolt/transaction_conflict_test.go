package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// transactionFailureCases are failures a public transaction can surface after
// its callback ran, and whether each proves the server rolled the whole
// transaction back after losing a write conflict, so that a caller may replay
// it.
func transactionFailureCases() []struct {
	name     string
	err      error
	conflict bool
} {
	serialization := &mysql.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: "serialization failure: this transaction conflicts with a committed transaction from another client, try restarting transaction"}
	return []struct {
		name     string
		err      error
		conflict bool
	}{
		{name: "1213 serialization failure at the regular commit", err: wrapSQLCommitError("sql commit (regular)", serialization), conflict: true},
		{name: "1205 lock wait timeout", err: fmt.Errorf("callback write: %w", &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded; try restarting transaction"}), conflict: true},
		// Dolt's autocommit rollback reports merge conflicts already in the
		// working set; a replay meets them again, so it is not a conflict a
		// replay can win.
		{name: "Dolt autocommit rollback", err: &mysql.MySQLError{Number: 1105, Message: "Merge conflict detected, @autocommit transaction rolled back"}, conflict: false},
		{name: "other 1105", err: &mysql.MySQLError{Number: 1105, Message: "command denied to user 'op'@'%'"}, conflict: false},
		{name: "duplicate key", err: &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, conflict: false},
		// A serialization failure on the Dolt commit that follows a
		// successful SQL commit does not roll the transaction back: its rows
		// landed.
		{name: "1213 after the SQL commit landed", err: fmt.Errorf("stage and commit after regular SQL commit: %w: %w", serialization, ErrCommitIndeterminate), conflict: false},
		{name: "lost commit response", err: wrapSQLCommitError("sql commit (regular)", mysql.ErrInvalidConn), conflict: false},
		// Classification is by the decoded server error, never by text.
		{name: "untyped text naming 1213", err: errors.New("Error 1213 (40001): serialization failure"), conflict: false},
	}
}

func TestRunInTransactionMarksOnlyRollbackGuaranteedConflicts(t *testing.T) {
	for _, tc := range transactionFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := &DoltStore{}
			callbacks := 0
			err := store.runInTransaction(context.Background(), "test: conflict classification", func(storage.Transaction) error {
				callbacks++
				return nil
			}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
				if err := fn(nil); err != nil {
					return err
				}
				return tc.err
			})
			assertTransactionConflictClassification(t, err, tc.err, tc.conflict)
			if callbacks != 1 {
				t.Fatalf("callback calls = %d, want 1: RunInTransaction itself never replays a callback", callbacks)
			}
		})
	}
}

func TestRunInIssueLifecycleTransactionMarksOnlyRollbackGuaranteedConflicts(t *testing.T) {
	for _, tc := range transactionFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := &DoltStore{}
			callbacks := 0
			err := store.runInIssueLifecycleTransaction(context.Background(), "test: conflict classification", func(storage.IssueLifecycleTransaction) error {
				callbacks++
				return nil
			}, func(_ context.Context, fn func(*sql.Tx) error) error {
				if err := fn(nil); err != nil {
					return err
				}
				return tc.err
			})
			assertTransactionConflictClassification(t, err, tc.err, tc.conflict)
			if callbacks != 1 {
				t.Fatalf("callback calls = %d, want 1", callbacks)
			}
		})
	}
}

func assertTransactionConflictClassification(t *testing.T, err, cause error, conflict bool) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the original failure %v in the chain", err, cause)
	}
	if got := errors.Is(err, storage.ErrTransactionConflict); got != conflict {
		t.Fatalf("errors.Is(err, ErrTransactionConflict) = %v, want %v (err = %v)", got, conflict, err)
	}
	if err.Error() != cause.Error() {
		t.Fatalf("message = %q, want the unchanged %q", err.Error(), cause.Error())
	}
}

// A callback's own failure that wraps a rollback-guaranteed conflict (a write
// statement inside the transaction that lost) is a conflict too: the callback
// error rolls the transaction back.
func TestRunInTransactionMarksConflictRaisedInsideCallback(t *testing.T) {
	store := &DoltStore{}
	inner := &mysql.MySQLError{Number: 1213, Message: "serialization failure"}
	err := store.runInTransaction(context.Background(), "test: conflict in callback", func(storage.Transaction) error {
		return fmt.Errorf("adding dependency: %w", inner)
	}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
		return fn(nil)
	})
	if !errors.Is(err, storage.ErrTransactionConflict) || !errors.Is(err, inner) {
		t.Fatalf("err = %v, want a marked conflict wrapping the statement's 1213", err)
	}
}
