package uow

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
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

// TestJournalWriterLock_AdvisoryDeferWakeSkipsWhenContended pins the ready
// path's advisory defer-wake sweep on the proxied provider under a contended
// journal (gastownhall/beads#6117): it makes one non-waiting attempt at the
// journal-writer lock and, finding it held, skips at once, with no warning and
// no queued wait. The next sweep, uncontended, wakes the defer.
func TestJournalWriterLock_AdvisoryDeferWakeSkipsWhenContended(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwd")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })
	name := issueops.JournalWriterLockName(p.database)

	const id = "jwd-deferred"
	require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
			ExplicitID: id,
			CreateOnly: true,
		}, "seed"); err != nil {
			return "", err
		}
		return "bd: create deferred", uw.IssueUseCase().UpdateIssue(ctx, id, map[string]interface{}{
			"status":      "deferred",
			"defer_until": time.Now().UTC().Add(-time.Hour),
		}, "seed")
	}))

	holder, err := p.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = holder.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
		_ = holder.Close()
	})
	var granted sql.NullInt64
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&granted))
	require.EqualValues(t, 1, granted.Int64, "the holder must take the lock")

	start := time.Now()
	warned := captureStderr(t, func() { WakeExpiredDefersAdvisory(ctx, p) })
	require.Less(t, time.Since(start), 5*time.Second, "the contended sweep queued; want one non-waiting attempt")
	require.Empty(t, warned, "the contended sweep must skip silently")
	require.Equal(t, types.StatusDeferred, readStoredIssue(t, ctx, p, id).Status, "the contended sweep woke the defer")

	var released sql.NullInt64
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released))
	require.EqualValues(t, 1, released.Int64)
	require.Empty(t, captureStderr(t, func() { WakeExpiredDefersAdvisory(ctx, p) }), "the uncontended sweep warned")
	woken := readStoredIssue(t, ctx, p, id)
	require.Equal(t, types.StatusOpen, woken.Status, "the uncontended sweep did not wake the defer")
	require.Nil(t, woken.DeferUntil, "the uncontended sweep left defer_until set")
	require.Equal(t, issueops.DeferWakeActor, uowLatestUpdateActor(t, ctx, p.db, id), "the wake was not journaled under the defer-wake actor")
	var user sql.NullInt64
	require.NoError(t, p.db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&user))
	require.False(t, user.Valid, "the journal-writer lock is still held after the sweeps")
}

// TestJournalWriterLock_ExplicitDeferWakeQueuesWhenContended pins the other
// side of the advisory marker (gastownhall/beads#6117): only
// WakeExpiredDefersAdvisory skips on contention. An explicit WakeExpiredDefers
// keeps normal admission, so with the lock held it must be seen waiting in
// GET_LOCK, not return until the holder lets go, and then wake the defer.
func TestJournalWriterLock_ExplicitDeferWakeQueuesWhenContended(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwe")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })
	name := issueops.JournalWriterLockName(p.database)

	const id = "jwe-deferred"
	require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
			ExplicitID: id,
			CreateOnly: true,
		}, "seed"); err != nil {
			return "", err
		}
		return "bd: create deferred", uw.IssueUseCase().UpdateIssue(ctx, id, map[string]interface{}{
			"status":      "deferred",
			"defer_until": time.Now().UTC().Add(-time.Hour),
		}, "seed")
	}))

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
	var woke atomic.Int32
	go func() {
		n, err := WakeExpiredDefers(ctx, p)
		woke.Store(int32(n))
		done <- err
	}()
	waitForUOWJournalWriterWaiter(t, ctx, p.db, done, "WakeExpiredDefers")
	require.Equal(t, types.StatusDeferred, readStoredIssue(t, ctx, p, id).Status, "the explicit wake ran while the holder held the lock")

	var released sql.NullInt64
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released))
	require.EqualValues(t, 1, released.Int64)
	select {
	case err := <-done:
		require.NoError(t, err, "the explicit wake must be admitted once the holder lets go, not refused busy")
	case <-time.After(20 * time.Second):
		t.Fatal("the explicit wake did not return once the holder released the lock")
	}
	require.EqualValues(t, 1, woke.Load(), "the explicit wake reported the wrong count")
	woken := readStoredIssue(t, ctx, p, id)
	require.Equal(t, types.StatusOpen, woken.Status, "the explicit wake did not wake the defer")
	require.Nil(t, woken.DeferUntil, "the explicit wake left defer_until set")
	require.Equal(t, issueops.DeferWakeActor, uowLatestUpdateActor(t, ctx, p.db, id), "the wake was not journaled under the defer-wake actor")
}

// uowLatestUpdateActor returns the actor of the newest update journal row for
// id.
func uowLatestUpdateActor(t *testing.T, ctx context.Context, db *sql.DB, id string) string {
	t.Helper()
	var actor string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT actor FROM bd_events_journal WHERE op = 'update' AND issue_id = ? ORDER BY seq DESC LIMIT 1", id).Scan(&actor))
	return actor
}

// TestJournalWriterLock_StoreAndUOWWritersQueueOnOneLock pins the
// cross-provider half of gastownhall/beads#6117: a server store's journaled
// writer and a proxied unit of work on the same database queue on one lock.
// The holder is admitted the way the store's funnel admits (AcquireJournalWriter
// for the database on its pinned session, then BEGIN and a journal-scoped
// emit) and pauses inside its transaction. A RunTx on the provider must be
// seen waiting in GET_LOCK and not return until the holder commits; then both
// writes are journaled at contiguous seqs, the holder's first.
func TestJournalWriterLock_StoreAndUOWWritersQueueOnOneLock(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwx")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(true)
	t.Cleanup(func() { p.SetEventsJournalEnabled(false) })

	const holderID, waiterID = "jwx-holder", "jwx-waiter"
	require.NoError(t, RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		for _, id := range []string{holderID, waiterID} {
			if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
				Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
				ExplicitID: id,
				CreateOnly: true,
			}, "seed"); err != nil {
				return "", err
			}
		}
		return "bd: seed cross-provider writers", nil
	}))
	var head int64
	require.NoError(t, p.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM bd_events_journal").Scan(&head))

	conn, err := p.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	lock, err := issueops.AcquireJournalWriter(ctx, conn, issueops.JournalWriterAdmission{
		Database: p.database,
		AdmitBy:  time.Now().Add(issueops.JournalWriterAdmissionBudget),
	})
	require.NoError(t, err, "the store-shaped holder must be admitted")
	t.Cleanup(lock.Release)
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	clearScope := issueops.ScopeEventsJournalTransaction(tx, true)
	t.Cleanup(clearScope)
	_, err = issueops.UpdateIssueInTx(ctx, tx, holderID, map[string]interface{}{"title": "written by the store-shaped holder"}, "holder")
	require.NoError(t, err)

	var waiterRuns atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			waiterRuns.Add(1)
			return "bd: cross-provider waiter", uw.IssueUseCase().UpdateIssue(ctx, waiterID, map[string]interface{}{"title": "written by the unit of work"}, "waiter")
		})
	}()
	waitForUOWJournalWriterWaiter(t, ctx, p.db, done, "RunTx")
	require.Zero(t, waiterRuns.Load(), "the unit of work ran its body while the store-shaped holder was inside its transaction")

	require.NoError(t, tx.Commit())
	lock.Release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the unit of work did not return once the holder committed")
	}
	require.EqualValues(t, 1, waiterRuns.Load(), "the unit of work replayed its body")

	rows, err := p.db.QueryContext(ctx, "SELECT seq, issue_id FROM bd_events_journal WHERE seq > ? ORDER BY seq", head)
	require.NoError(t, err)
	defer rows.Close()
	var sawHolder, sawWaiter bool
	for want := head + 1; rows.Next(); want++ {
		var seq int64
		var id string
		require.NoError(t, rows.Scan(&seq, &id))
		require.Equalf(t, want, seq, "journal seq %d breaks the contiguous run after %d", seq, head)
		switch id {
		case holderID:
			require.Falsef(t, sawWaiter, "seq %d journals the holder after the unit of work; want commit order", seq)
			sawHolder = true
		case waiterID:
			sawWaiter = true
		}
	}
	require.NoError(t, rows.Err())
	require.True(t, sawHolder && sawWaiter, "want journal rows for both writers (holder=%v unit of work=%v)", sawHolder, sawWaiter)
}

// TestJournalWriterLock_DisabledJournalUOWTakesNoLock pins the journal-off
// lane on the proxied provider (gastownhall/beads#6117): with the journal off
// no unit of work takes the journal-writer lock, so an emitting write
// completes promptly while another session holds it, and its emit stays the
// usual no-op.
func TestJournalWriterLock_DisabledJournalUOWTakesNoLock(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "jwo")
	p, ok := provider.(*doltSQLProvider)
	require.Truef(t, ok, "provider is %T, want *doltSQLProvider", provider)
	p.SetEventsJournalEnabled(false)
	name := issueops.JournalWriterLockName(p.database)

	probe, err := p.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = probe.ExecContext(context.Background(), "DO RELEASE_LOCK(?)", name)
		_ = probe.Close()
	})
	var granted sql.NullInt64
	require.NoError(t, probe.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&granted))
	require.EqualValues(t, 1, granted.Int64, "the probe must take the lock")

	before := uowJournalSeqCounter(t, ctx, p.db)
	// Well inside the admission budget: a unit of work that queued would run out.
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const id = "jwo-write"
	_, err = RunTxResultWithin(wctx, p, 15*time.Second, func(ctx context.Context, uw UnitOfWork) (struct{}, string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue:      &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen},
			ExplicitID: id,
			CreateOnly: true,
		}, "route")
		return struct{}{}, "bd: journal-off write", err
	})
	require.NoError(t, err, "a journal-off unit of work must complete while another session holds the journal-writer lock")
	require.Equal(t, types.StatusOpen, readStoredIssue(t, ctx, p, id).Status)
	require.Equal(t, before, uowJournalSeqCounter(t, ctx, p.db), "a journal-off emit allocated a seq")
}
