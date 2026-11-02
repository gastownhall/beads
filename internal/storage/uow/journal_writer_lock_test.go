package uow

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
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
		waiters, err := sessionsInJournalWriterWait(ctx, db)
		require.NoError(t, err)
		if len(waiters) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never observed waiting for the journal-writer lock", who)
}

// sessionsInJournalWriterWait returns the ids of the sessions running a
// GET_LOCK. The provider's pool prepares its statements, so the processlist
// shows placeholders rather than the lock name; the holder's own GET_LOCK
// returns at once, and so does an uncontended delete fence, so a running one
// is a route's.
func sessionsInJournalWriterWait(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	var waiters []int64
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		var id int64
		waiting := false
		for i, column := range columns {
			switch {
			case strings.EqualFold(column, "id"):
				id, _ = strconv.ParseInt(values[i].String, 10, 64)
			case strings.EqualFold(column, "info") && strings.Contains(strings.ToLower(values[i].String), "get_lock("):
				waiting = true
			}
		}
		if waiting {
			waiters = append(waiters, id)
		}
	}
	return waiters, rows.Err()
}

// TestJournalWriterLock_DeleteFenceLocksTheFencedSession pins the guarded
// delete's lock order on the proxied provider (gastownhall/beads#6117): it
// takes its per-id delete fence, then queues for the journal-writer lock on
// that same session and runs its write there, never on a second pool session.
// A foreign session holds the journal lock and an ordinary journal writer is
// already queued behind it: the delete must be seen waiting on the session
// that holds its fence, and both must complete once the holder lets go,
// leaving both locks free.
func TestJournalWriterLock_DeleteFenceLocksTheFencedSession(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwf")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })
	name := issueops.JournalWriterLockName(p.database)

	const fencedID, writtenID = "jwf-fenced", "jwf-written"
	require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		for _, id := range []string{fencedID, writtenID} {
			if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
				Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
				ExplicitID: id,
				CreateOnly: true,
			}, "fence"); err != nil {
				return "", err
			}
		}
		return "bd: create fenced", nil
	}))
	version, err := RunTxRead(ctx, p, func(ctx context.Context, uw UnitOfWork) (int64, error) {
		issue, err := uw.IssueUseCase().GetIssue(ctx, fencedID)
		if err != nil {
			return 0, err
		}
		return issue.RowVersion, nil
	})
	require.NoError(t, err)
	var database string
	require.NoError(t, p.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database))
	fence := deleteFenceLockName(database, fencedID)
	deleter, err := NewDeleter(p)
	require.NoError(t, err)

	holder, err := p.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = holder.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
		_ = holder.Close()
	})
	var granted sql.NullInt64
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&granted))
	require.EqualValues(t, 1, granted.Int64, "the holder must take the lock")

	written := make(chan error, 1)
	go func() {
		written <- RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			return "bd: write", uw.IssueUseCase().UpdateIssue(ctx, writtenID, map[string]interface{}{"title": "written"}, "writer")
		})
	}()
	waitForUOWJournalWriterWaiter(t, ctx, p.db, written, "ordinary writer")

	deleted := make(chan error, 1)
	var result publicops.DeleteResult
	go func() {
		var err error
		result, err = deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{fencedID}, ExpectedVersion: &version})
		deleted <- err
	}()
	var fenceHolder sql.NullInt64
	var waiters []int64
	for deadline := time.Now().Add(20 * time.Second); ; {
		select {
		case err := <-deleted:
			t.Fatalf("the guarded delete returned (%v) while another session held the journal-writer lock", err)
		default:
		}
		require.NoError(t, p.db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", fence).Scan(&fenceHolder))
		waiters, err = sessionsInJournalWriterWait(ctx, p.db)
		require.NoError(t, err)
		if fenceHolder.Valid && slices.Contains(waiters, fenceHolder.Int64) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the guarded delete never waited for the journal on its fence's session: fence held by %v, GET_LOCK waiters %v", fenceHolder, waiters)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var released sql.NullInt64
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released))
	require.EqualValues(t, 1, released.Int64)
	for who, done := range map[string]<-chan error{"ordinary writer": written, "guarded delete": deleted} {
		select {
		case err := <-done:
			require.NoError(t, err, who)
		case <-time.After(20 * time.Second):
			t.Fatalf("the %s did not return once the journal lock was free", who)
		}
	}
	for _, lock := range []string{name, fence} {
		var user sql.NullInt64
		require.NoError(t, p.db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", lock).Scan(&user))
		require.Falsef(t, user.Valid, "lock %q is still held by session %d after both returned", lock, user.Int64)
	}
	require.Equal(t, 1, result.Deleted, "the guarded delete did not report its deletion")
	// Read back through a unit of work: a pool session that ran one may
	// still answer a plain query from an older snapshot.
	remaining, err := RunTxRead(ctx, p, func(ctx context.Context, uw UnitOfWork) ([]*types.Issue, error) {
		return uw.IssueUseCase().GetIssuesByIDs(ctx, []string{fencedID})
	})
	require.NoError(t, err)
	require.Empty(t, remaining, "the guarded delete did not delete the row")
	require.Equal(t, "written", readStoredIssue(t, ctx, p, writtenID).Title, "the ordinary writer's update did not land")
}

// TestJournalWriterLock_UnlockedUOWRefusesEmits pins the guard on the
// journal-free routes (gastownhall/beads#6117): RunTxEphemeral and RunTxRead
// take no journal-writer lock, so a journal emit inside one is refused before
// it allocates a seq, and the unit of work cannot commit even though the
// callback drops the emit's error.
func TestJournalWriterLock_UnlockedUOWRefusesEmits(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwu")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })

	// emit creates id, an emitting write, and keeps its error for the test
	// while handing the route nothing to fail on.
	emit := func(ctx context.Context, uw UnitOfWork, id string, emitErr *error) {
		_, *emitErr = uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
			ExplicitID: id,
			CreateOnly: true,
		}, "route")
	}
	for _, route := range []struct {
		name string
		id   string
		run  func(ctx context.Context, id string, emitErr *error) error
	}{
		{"RunTxEphemeral", "jwu-ephemeral", func(ctx context.Context, id string, emitErr *error) error {
			_, err := RunTxEphemeral(ctx, p, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
				emit(ctx, uw, id, emitErr)
				return struct{}{}, nil
			})
			return err
		}},
		{"RunTxRead", "jwu-read", func(ctx context.Context, id string, emitErr *error) error {
			_, err := RunTxRead(ctx, p, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
				emit(ctx, uw, id, emitErr)
				return struct{}{}, nil
			})
			return err
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			before := uowJournalSeqCounter(t, ctx, p.db)
			var emitErr error
			err := route.run(ctx, route.id, &emitErr)
			require.ErrorIs(t, emitErr, issueops.ErrJournalUnlockedTx, "the emit must be refused")
			if route.name == "RunTxEphemeral" {
				require.ErrorIs(t, err, issueops.ErrJournalUnlockedTx, "the unit of work must not commit")
			}
			require.Equal(t, before, uowJournalSeqCounter(t, ctx, p.db), "the refused emit allocated a seq")
			var rows int
			require.NoError(t, p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", route.id).Scan(&rows))
			require.Zerof(t, rows, "%s committed the write whose emit was refused", route.name)
		})
	}
}

// uowJournalSeqCounter reads the journal's seq counter and row count, which
// together move whenever a seq is allocated and committed.
func uowJournalSeqCounter(t *testing.T, ctx context.Context, db *sql.DB) [2]int64 {
	t.Helper()
	var got [2]int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&got[0]))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bd_events_journal").Scan(&got[1]))
	return got
}
