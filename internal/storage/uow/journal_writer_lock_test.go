package uow

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestJournalWriterLock_UOWRoutesAdmitBeforeBegin pins the journal-writer
// admission on the proxied provider's write routes (gastownhall/beads#6117):
// RunTx, RunTxScoped and a direct NewUOW. A foreign session holds the lock;
// each route must be seen waiting in GET_LOCK and not return until the holder
// lets go, then succeed, and leave the lock free when it returns.
//
// One provider, no t.Parallel: the journal is database-global (see
// newUOWRoleFixtureProvider).
func TestJournalWriterLock_UOWRoutesAdmitBeforeBegin(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwl")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })
	name := issueops.JournalWriterLockName(p.database)

	create := func(ctx context.Context, uw UnitOfWork, id string, wisp bool) error {
		params := domain.CreateIssueParams{
			Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
			ExplicitID: id,
			CreateOnly: true,
		}
		var err error
		if wisp {
			_, err = uw.IssueUseCase().CreateWisp(ctx, params, "route")
		} else {
			_, err = uw.IssueUseCase().CreateIssue(ctx, params, "route")
		}
		return err
	}
	routes := []struct {
		name string
		run  func(context.Context) error
	}{
		{"RunTx", func(ctx context.Context) error {
			return RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "bd: route", create(ctx, uw, "jwl-runtx", false)
			})
		}},
		{"RunTxScoped", func(ctx context.Context) error {
			_, err := RunTxScoped(ctx, p, func(ctx context.Context, uw UnitOfWork) (struct{}, []string, string, error) {
				return struct{}{}, nil, "", create(ctx, uw, "jwl-scoped", true)
			})
			return err
		}},
		{"direct NewUOW", func(ctx context.Context) error {
			uw, err := p.NewUOW(ctx)
			if err != nil {
				return err
			}
			defer uw.Close(ctx)
			if err := create(ctx, uw, "jwl-direct", false); err != nil {
				return err
			}
			return uw.Commit(ctx, "bd: direct route")
		}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			holder, err := p.db.Conn(ctx)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = holder.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
				_ = holder.Close()
			})
			var granted sql.NullInt64
			require.NoError(t, holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&granted))
			require.EqualValues(t, 1, granted.Int64, "the holder must take the lock")

			done := make(chan error, 1)
			go func() { done <- route.run(ctx) }()
			waitForUOWJournalWriterWaiter(t, ctx, p.db, done, route.name)

			var released sql.NullInt64
			require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released))
			require.EqualValues(t, 1, released.Int64)
			select {
			case err := <-done:
				require.NoError(t, err, route.name)
			case <-time.After(20 * time.Second):
				t.Fatalf("%s did not return once the lock was free", route.name)
			}
			var user sql.NullInt64
			require.NoError(t, p.db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&user))
			require.Falsef(t, user.Valid, "the journal-writer lock is still held by session %d after %s returned", user.Int64, route.name)
		})
	}
}

// waitForUOWJournalWriterWaiter returns once some session is observed in a
// GET_LOCK on the journal-writer lock, and fails if done fires first: that
// route ran without waiting for admission.
func waitForUOWJournalWriterWaiter(t *testing.T, ctx context.Context, db *sql.DB, done <-chan error, who string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("%s returned (%v) while another session held the journal-writer lock; want it held at admission", who, err)
		default:
		}
		waiting, err := sessionInJournalWriterWait(ctx, db)
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never observed waiting for the journal-writer lock", who)
}

// sessionInJournalWriterWait reports whether any session is running a
// GET_LOCK. The provider's pool prepares its statements, so the processlist
// shows placeholders rather than the lock name; the holder's own GET_LOCK
// returns at once, so a running one is the route's.
func sessionInJournalWriterWait(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return false, err
		}
		for i, column := range columns {
			if strings.EqualFold(column, "info") && strings.Contains(strings.ToLower(values[i].String), "get_lock(") {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}
