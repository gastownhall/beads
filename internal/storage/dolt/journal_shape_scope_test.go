package dolt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// These are the DoltStore counterparts of the provider's
// TestBeginTxWritesTheProbedJournalShape: on a table that never gained actor
// or comment_json (gas-city-inc's lineage), a journaled write names only the
// columns SetEventsJournalEnabled's probe found. The store binds its cached
// shape to a transaction in two places — runDoltTransactionRecording, behind
// the whole doltTransaction mutator surface, and scopeEventsJournalTransaction,
// behind every raw-transaction writer — and TestEveryRawTxJournalScopeIsScopedOrExempt
// checks only the activation half of either. Dropping a shape binding falls
// back to the canonical INSERT, which is the Error 1054 that rolled back every
// journaled write on that lineage.

// degradedJournalInsert is the INSERT a table missing actor and comment_json gets.
var degradedJournalInsert = regexp.QuoteMeta(
	"INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json) VALUES")

// newDegradedJournalStore returns a store whose journal was activated against
// a table missing only the optional actor and comment_json columns.
func newDegradedJournalStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	columns := sqlmock.NewRows([]string{"COLUMN_NAME"})
	for _, column := range []string{"seq", "ts", "op", "issue_id", "issue_json", "dep_json"} {
		columns.AddRow(column)
	}
	mock.ExpectQuery("SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS").
		WithArgs("bd_events_journal").
		WillReturnRows(columns)

	store := &DoltStore{db: db}
	store.SetEventsJournalEnabled(true)
	require.NoError(t, store.EventsJournalActivationError(),
		"a table missing only OPTIONAL columns is degraded, not unsupported")
	return store, mock
}

// expectDegradedJournalWrite arms one journal row: the seq allocation, then
// the INSERT naming only the columns the probe found.
func expectDegradedJournalWrite(mock sqlmock.Sqlmock) {
	mock.ExpectExec("UPDATE bd_events_seq SET next_seq").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT next_seq FROM bd_events_seq").
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(7))
	mock.ExpectExec(degradedJournalInsert).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestDoltTransactionWritesTheProbedJournalShape(t *testing.T) {
	store, mock := newDegradedJournalStore(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT active_branch()")).
		WillReturnRows(sqlmock.NewRows([]string{"active_branch()"}).AddRow("main"))
	mock.ExpectBegin()
	expectDegradedJournalWrite(mock)
	mock.ExpectRollback()

	// The callback always returns, and stops before the commit: what is pinned
	// here is the INSERT, not the Dolt commit sequence after it. It must not
	// fail the test itself — FailNow inside the callback would skip the
	// rollback, and the deferred close of the pinned connection would then
	// wait on the open transaction forever.
	errStop := errors.New("stop before commit")
	var writeErr error
	ctx := context.Background()
	_, err := store.runDoltTransactionRecording(ctx, "test", func(tx storage.Transaction) error {
		dtx, ok := tx.(*doltTransaction)
		if !ok {
			return fmt.Errorf("callback got a %T, want *doltTransaction", tx)
		}
		writeErr = issueops.RecordDeleteInTx(ctx, dtx.regularTx, "bd-1", "test-actor")
		return errStop
	})
	require.ErrorIs(t, err, errStop)
	require.NoError(t, writeErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScopedRawTxWritesTheProbedJournalShape(t *testing.T) {
	store, mock := newDegradedJournalStore(t)
	mock.ExpectBegin()
	expectDegradedJournalWrite(mock)
	mock.ExpectRollback()

	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	clearJournalScope := store.scopeEventsJournalTransaction(tx)
	writeErr := issueops.RecordDeleteInTx(ctx, tx, "bd-1", "test-actor")
	clearJournalScope()
	_ = tx.Rollback()

	require.NoError(t, writeErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
