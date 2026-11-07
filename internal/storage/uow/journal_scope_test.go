package uow

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// The unit-of-work provider is bd's SECOND write plumbing, and journal coverage
// has two independent halves: the issueops seam must EMIT, and the plumbing must
// turn emission ON for the transaction the mutation runs in. The dolt store's
// half is guarded structurally (TestEveryRawTxJournalScopeIsScopedOrExempt);
// this is the same guard for the provider, whose only transaction-minting
// function is BeginTx.
//
// Missing activation is invisible in production — the code runs, the mutation
// commits, and the journal is simply empty — so it must be pinned from both
// directions: enabled emits, disabled does not.

// expectCanonicalJournalShapeProbe arms the one INFORMATION_SCHEMA.COLUMNS
// query SetEventsJournalEnabled(true) now issues to probe bd_events_journal's
// shape (issueops.ProbeJournalShape, PR A1), returning every column a
// canonical table carries so activation succeeds with the shape every test in
// this file was written against before shape probing existed.
func expectCanonicalJournalShapeProbe(mock sqlmock.Sqlmock) {
	expectJournalShapeProbe(mock,
		"seq", "ts", "op", "issue_id", "actor", "issue_json", "dep_json", "comment_json")
}

// expectJournalShapeProbe arms the shape probe to report exactly columns.
func expectJournalShapeProbe(mock sqlmock.Sqlmock, columns ...string) {
	rows := sqlmock.NewRows([]string{"COLUMN_NAME"})
	for _, column := range columns {
		rows.AddRow(column)
	}
	mock.ExpectQuery("SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS").
		WithArgs("bd_events_journal").
		WillReturnRows(rows)
}

func TestProviderImplementsEventsJournalConfigurer(t *testing.T) {
	p, mock := newMockTxProvider(t)
	expectCanonicalJournalShapeProbe(mock)
	var configurer storage.EventsJournalConfigurer = p
	configurer.SetEventsJournalEnabled(true)
	require.True(t, p.eventsJournalEnabled.Load(),
		"cmd/bd activates the proxied plumbing by type-asserting to storage.EventsJournalConfigurer")
}

// TestBeginTxScopesJournalActivationToThePinnedConn proves BeginTx binds
// activation to the connection it pinned: an issueops emit issued against that
// connection allocates a seq and inserts a row. sqlmock observes the statements,
// so the assertion is on the SQL actually reaching the session rather than on
// internal bookkeeping.
func TestBeginTxScopesJournalActivationToThePinnedConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	expectCanonicalJournalShapeProbe(mock)
	p.SetEventsJournalEnabled(true)
	require.NoError(t, p.EventsJournalActivationError(),
		"activation against the canonical shape the probe expectation below describes must succeed")

	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE bd_events_seq SET next_seq").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT next_seq FROM bd_events_seq").
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(7))
	mock.ExpectExec("INSERT INTO bd_events_journal").WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.Background()
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)

	require.NoError(t, issueops.RecordDeleteInTx(ctx, tx.Runner(), "bd-1", "test-actor"))
	require.NoError(t, mock.ExpectationsWereMet(),
		"an enabled provider must journal on the transaction BeginTx pinned")
}

// TestBeginTxLeavesJournalOffWhenDisabled is the other direction: the default
// (off) provider must issue no journal SQL at all, so an ordinary workspace
// pays nothing and no rows appear for a consumer that never opted in.
func TestBeginTxLeavesJournalOffWhenDisabled(t *testing.T) {
	p, mock := newMockTxProvider(t)

	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))

	ctx := context.Background()
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)

	// Any journal statement here would be an unexpected call and fail the mock.
	require.NoError(t, issueops.RecordDeleteInTx(ctx, tx.Runner(), "bd-1", "test-actor"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTxEndReleasesJournalScope pins the cleanup. Activation is keyed by the
// pinned connection in a process-lifetime map, so a scope that outlives its
// transaction is both a leak (one entry per unit of work) and a correctness
// hazard once the pool hands that connection to the next borrower.
func TestTxEndReleasesJournalScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, mock sqlmock.Sqlmock, tx Tx)
	}{
		{
			name: "rollback",
			end: func(t *testing.T, mock sqlmock.Sqlmock, tx Tx) {
				mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
				require.NoError(t, tx.Rollback(context.Background()))
			},
		},
		{
			name: "commit",
			end: func(t *testing.T, mock sqlmock.Sqlmock, tx Tx) {
				expectPendingChanges(mock, 1)
				mock.ExpectExec("DOLT_COMMIT").WillReturnResult(sqlmock.NewResult(0, 1))
				require.NoError(t, tx.Commit(context.Background(), "msg"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mock := newMockTxProvider(t)
			// Arm the shape probe so activation succeeds outright. This test
			// pins the release, so it must not lean on how a failed probe is
			// handled: while a failed probe left the transaction unjournaled,
			// the emit below passed with or without the release.
			expectCanonicalJournalShapeProbe(mock)
			p.SetEventsJournalEnabled(true)
			require.NoError(t, p.EventsJournalActivationError())
			mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))

			tx, err := p.BeginTx(context.Background())
			require.NoError(t, err)
			serverTx, ok := tx.(*doltServerTx)
			require.True(t, ok)
			conn := serverTx.conn

			tc.end(t, mock, tx)
			require.NoError(t, mock.ExpectationsWereMet())

			require.Nil(t, serverTx.clearJournalScope, "the scope must be released with the connection")
			require.Nil(t, serverTx.clearJournalShape, "the probed shape must be released with the connection")
			// Emitting against the released connection must be a no-op. Had the
			// activation entry survived, the emit would still consider itself
			// enabled and run SQL on a connection that is back in the pool —
			// which errors here and would be a cross-transaction write there.
			require.NoError(t, issueops.RecordDeleteInTx(context.Background(), conn, "bd-1", "test-actor"),
				"a leaked activation entry makes a released connection journal")
		})
	}
}

// expectJournalSeqAllocation arms the counter round trip an emit makes before
// its INSERT.
func expectJournalSeqAllocation(mock sqlmock.Sqlmock) {
	mock.ExpectExec("UPDATE bd_events_seq SET next_seq").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT next_seq FROM bd_events_seq").
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(7))
}

// canonicalJournalInsert is the INSERT a table carrying every optional column
// gets, and the one a store with no probed shape falls back to.
var canonicalJournalInsert = regexp.QuoteMeta(
	"INSERT INTO bd_events_journal (seq, ts, op, issue_id, actor, issue_json, dep_json, comment_json) VALUES")

// TestBeginTxWritesTheProbedJournalShape is the proxied-server counterpart of
// the embedded TestAdaptiveInsert_GciShape: on a table that never gained actor
// or comment_json (gas-city-inc's lineage), a unit of work's INSERT names only
// the columns the probe found. Naming either missing column is the 1054 error
// that rolled back every journaled write on that lineage.
func TestBeginTxWritesTheProbedJournalShape(t *testing.T) {
	p, mock := newMockTxProvider(t)
	expectJournalShapeProbe(mock, "seq", "ts", "op", "issue_id", "issue_json", "dep_json")
	p.SetEventsJournalEnabled(true)
	require.NoError(t, p.EventsJournalActivationError(),
		"a table missing only OPTIONAL columns is degraded, not unsupported")

	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalSeqAllocation(mock)
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json) VALUES")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.Background()
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)

	require.NoError(t, issueops.RecordDeleteInTx(ctx, tx.Runner(), "bd-1", "test-actor"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUnsupportedJournalShapeStillJournals pins both halves of a shape
// verdict. A table missing a REQUIRED column is the activation error a
// bead-writing open is refused with. A provider enabled anyway keeps
// journaling: the canonical INSERT fails and takes the mutation's transaction
// with it, rather than the mutation committing with no journal row.
func TestUnsupportedJournalShapeStillJournals(t *testing.T) {
	p, mock := newMockTxProvider(t)
	expectJournalShapeProbe(mock, "seq", "ts", "issue_id", "actor", "issue_json", "dep_json", "comment_json")
	p.SetEventsJournalEnabled(true)
	require.ErrorIs(t, p.EventsJournalActivationError(), issueops.ErrJournalShapeUnsupported)

	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalSeqAllocation(mock)
	mock.ExpectExec(canonicalJournalInsert).
		WillReturnError(errors.New("Error 1054 (42S22): Unknown column 'op' in 'field list'"))

	ctx := context.Background()
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)

	require.Error(t, issueops.RecordDeleteInTx(ctx, tx.Runner(), "bd-1", "test-actor"),
		"a mutation must not commit on a journaling provider without its journal row")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFailedShapeProbeDoesNotRefuseActivation is the other arm: a probe that
// never reached the table says nothing about its shape, so it must not refuse
// an open that would otherwise succeed. Activation reports no error and the
// journal stays on, writing the canonical column list.
func TestFailedShapeProbeDoesNotRefuseActivation(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectQuery("SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS").
		WithArgs("bd_events_journal").
		WillReturnError(errors.New("dial tcp 127.0.0.1:3307: connect: connection refused"))
	p.SetEventsJournalEnabled(true)
	require.NoError(t, p.EventsJournalActivationError(),
		"only a *JournalShapeError is a verdict about the table")

	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectJournalSeqAllocation(mock)
	mock.ExpectExec(canonicalJournalInsert).WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.Background()
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)

	require.NoError(t, issueops.RecordDeleteInTx(ctx, tx.Runner(), "bd-1", "test-actor"))
	require.NoError(t, mock.ExpectationsWereMet(),
		"a failed probe must leave the journal on; turning it off lets writes commit unrecorded")
}
