package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestJournalEmit_UnlockedTxFails pins the unlocked scope: every journal emit
// on a transaction scoped with ScopeUnlockedJournalTransaction returns
// ErrJournalUnlockedTx and issues no SQL (sqlmock fails any statement it was
// not told to expect), even when the context form says the journal is on.
func TestJournalEmit_UnlockedTxFails(t *testing.T) {
	emits := map[string]func(context.Context, DBTX) error{
		"RecordEventInTx": func(ctx context.Context, tx DBTX) error {
			return RecordEventInTx(ctx, tx, EventUpdate, "bd-u-1", "actor")
		},
		"RecordDeleteInTx": func(ctx context.Context, tx DBTX) error {
			return RecordDeleteInTx(ctx, tx, "bd-u-1", "actor")
		},
		"RecordDepEventInTx": func(ctx context.Context, tx DBTX) error {
			return RecordDepEventInTx(ctx, tx, EventDepAdd, "bd-u-1", "blocks", "bd-u-2", "", "actor")
		},
		"RecordCommentEventInTx": func(ctx context.Context, tx DBTX) error {
			return RecordCommentEventInTx(ctx, tx, "bd-u-1", &EventComment{Source: CommentSourceStructured, Author: "actor"})
		},
		"journalableDeletesInTx": func(ctx context.Context, tx DBTX) error {
			_, err := journalableDeletesInTx(ctx, tx, "issues", []string{"bd-u-1"})
			return err
		},
		"captureBlockedJournalSnapshot": func(ctx context.Context, tx DBTX) error {
			_, err := captureBlockedJournalSnapshot(ctx, tx, []string{"bd-u-1"}, nil)
			return err
		},
	}
	for name, emit := range emits {
		for _, ctxName := range []string{"plain", "context journal on"} {
			t.Run(name+"/"+ctxName, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				defer db.Close()
				mock.ExpectBegin()
				mock.ExpectRollback()
				tx, err := db.Begin()
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				clear := ScopeUnlockedJournalTransaction(tx)
				ctx := context.Background()
				if ctxName != "plain" {
					ctx = WithEventsJournal(ctx, true)
				}
				err = emit(ctx, tx)
				clear()
				_ = tx.Rollback()
				if !errors.Is(err, ErrJournalUnlockedTx) {
					t.Fatalf("%s on an unlocked tx = %v, want ErrJournalUnlockedTx", name, err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unlocked emit SQL: %v", err)
				}
			})
		}
	}
}

// TestJournalEmit_UnscopedTxStaysSilent pins that only the unlocked scope
// fails: an unscoped tx, and one whose unlocked scope was cleared, still skip
// the emit without error or SQL.
func TestJournalEmit_UnscopedTxStaysSilent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	ctx := context.Background()
	if err := RecordEventInTx(ctx, tx, EventUpdate, "bd-u-1", "actor"); err != nil {
		t.Fatalf("emit on an unscoped tx = %v, want a silent skip", err)
	}
	ScopeUnlockedJournalTransaction(tx)()
	if err := RecordEventInTx(ctx, tx, EventUpdate, "bd-u-1", "actor"); err != nil {
		t.Fatalf("emit after the unlocked scope was cleared = %v, want a silent skip", err)
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unscoped emit SQL: %v", err)
	}
}
