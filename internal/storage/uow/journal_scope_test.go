package uow

import (
	"context"
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

func TestProviderImplementsEventsJournalConfigurer(t *testing.T) {
	p, _ := newMockTxProvider(t)
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
	p.SetEventsJournalEnabled(true)

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
			p.SetEventsJournalEnabled(true)
			mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))

			tx, err := p.BeginTx(context.Background())
			require.NoError(t, err)
			serverTx, ok := tx.(*doltServerTx)
			require.True(t, ok)
			conn := serverTx.conn

			tc.end(t, mock, tx)
			require.NoError(t, mock.ExpectationsWereMet())

			require.Nil(t, serverTx.clearJournalScope, "the scope must be released with the connection")
			// Emitting against the released connection must be a no-op. Had the
			// activation entry survived, the emit would still consider itself
			// enabled and run SQL on a connection that is back in the pool —
			// which errors here and would be a cross-transaction write there.
			require.NoError(t, issueops.RecordDeleteInTx(context.Background(), conn, "bd-1", "test-actor"),
				"a leaked activation entry makes a released connection journal")
		})
	}
}
