package dolt

// Commit-lock tests (GH#8590, dolthub/dolt#12258): every server-mode commit
// plane holds the per-database named lock (versioncontrolops.CommitLockName)
// across its DOLT_ADD/DOLT_COMMIT pair, takes it after the pending fast path
// so a no-op write pays no lock round trips, and releases it on every return
// path. Each test holds the lock from a second session and asserts the commit
// does not land until the holder releases it — the shape that, before the
// lock existed, produced Dolt commits carrying merge conflict artifacts under
// concurrent writers.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// commitLockProbe is an independent session on the store's server, used to
// hold the commit lock, read dolt_log, and probe the lock's state while the
// store commits. The session is PINNED (a single *sql.Conn for the probe's
// lifetime and all its statements): named locks are session-scoped, so a
// pooled get could hold on one session while a pooled release lands on
// another and reports nonsense.
type commitLockProbe struct {
	conn *sql.Conn
	ctx  context.Context
	// held tracks which named locks this session currently holds so a
	// test-flow release followed by the cleanup release is idempotent:
	// RELEASE_LOCK of a lock this session no longer holds returns 0 and
	// would otherwise be misread as a lock-safety failure.
	held map[string]bool
}

func newCommitLockProbe(t *testing.T, store *DoltStore) *commitLockProbe {
	t.Helper()
	db, err := sql.Open("mysql", store.connStr)
	if err != nil {
		t.Fatalf("open probe session: %v", err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("pin probe session: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); _ = db.Close() })
	return &commitLockProbe{conn: conn, ctx: ctx, held: map[string]bool{}}
}

// hold takes the named lock on the probe's pinned session.
func (p *commitLockProbe) hold(t *testing.T, name string) {
	t.Helper()
	var acquired sql.NullInt64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&acquired); err != nil {
		t.Fatalf("GET_LOCK(%q): %v", name, err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		t.Fatalf("GET_LOCK(%q) = %v, want 1: another session holds the lock", name, acquired)
	}
	p.held[name] = true
	t.Cleanup(func() { p.releaseIfHeld(t, name) })
}

// release hands the named lock back and forgets it; the matching cleanup then
// releases nothing. A double release here is a test-authoring error worth
// failing on, not a probe bug to tolerate silently.
func (p *commitLockProbe) release(t *testing.T, name string) {
	t.Helper()
	if !p.held[name] {
		t.Fatalf("release(%q) without hold: the test released the lock twice", name)
	}
	p.held[name] = false
	var released sql.NullInt64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released); err != nil {
		t.Fatalf("RELEASE_LOCK(%q): %v", name, err)
	}
	if !released.Valid || released.Int64 != 1 {
		t.Fatalf("RELEASE_LOCK(%q) = %v, want 1: the lock is no longer ours to release", name, released)
	}
}

// releaseIfHeld is the cleanup-side release: a no-op for a flow that already
// released explicitly, a verified hand-back for one it did not.
func (p *commitLockProbe) releaseIfHeld(t *testing.T, name string) {
	t.Helper()
	if !p.held[name] {
		return
	}
	p.held[name] = false
	var released sql.NullInt64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released); err != nil {
		t.Fatalf("RELEASE_LOCK(%q): %v", name, err)
	}
	if !released.Valid || released.Int64 != 1 {
		t.Fatalf("cleanup RELEASE_LOCK(%q) = %v, want 1: the lock leaked to another holder", name, released)
	}
}

// lockIsFree reports IS_FREE_LOCK from the probe's independent session; it is
// the AC-4 observable: a commit path that returned must have left the lock
// free no matter which return path it took.
func (p *commitLockProbe) lockIsFree(t *testing.T, name string) bool {
	t.Helper()
	var free sql.NullInt64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT IS_FREE_LOCK(?)", name).Scan(&free); err != nil {
		t.Fatalf("IS_FREE_LOCK(%q): %v", name, err)
	}
	return free.Valid && free.Int64 == 1
}

func (p *commitLockProbe) requireLockFree(t *testing.T, name, why string) {
	t.Helper()
	if !p.lockIsFree(t, name) {
		t.Fatalf("%s: IS_FREE_LOCK(%q) is 0 after the call returned — the commit path leaked its lock", why, name)
	}
}

// connectionID is the probe session's server-side connection id.
func (p *commitLockProbe) connectionID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatalf("CONNECTION_ID(): %v", err)
	}
	return id
}

// lockUser reports IS_USED_LOCK(name): the connection id of the holder, or 0.
func (p *commitLockProbe) lockUser(t *testing.T, name string) int64 {
	t.Helper()
	var used sql.NullInt64
	if err := p.conn.QueryRowContext(p.ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&used); err != nil {
		t.Fatalf("IS_USED_LOCK(%q): %v", name, err)
	}
	if !used.Valid {
		return 0
	}
	return used.Int64
}

func (p *commitLockProbe) logCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := p.conn.QueryRowContext(p.ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	return n
}

func (p *commitLockProbe) logHasMessage(t *testing.T, msg string) bool {
	t.Helper()
	var n int
	if err := p.conn.QueryRowContext(p.ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", msg).Scan(&n); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	return n > 0
}

// dirtyTables lists dolt_status rows (table names) on the probe session — the
// pending-change observable the no-op and failure-path debugging needs.
func (p *commitLockProbe) dirtyTables(t *testing.T) []string {
	t.Helper()
	rows, err := p.conn.QueryContext(p.ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		tables = append(tables, s)
	}
	return tables
}

// commitLockStore returns a store on its own database plus the probe and the
// store database's lock name. The store's standing working-set dirt (setup's
// own issue_prefix config write, which no automatic commit stages) is swept
// into one baseline commit first, so tests that assert on pending state — or
// on dolt_log counts — start from a clean working set.
func commitLockStore(t *testing.T) (*DoltStore, *commitLockProbe, string) {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	if _, err := store.CommitAll(context.Background(), "bd: commit-lock test baseline"); err != nil {
		cleanup()
		t.Fatalf("sweep baseline working-set dirt: %v", err)
	}
	probe := newCommitLockProbe(t, store)
	return store, probe, versioncontrolops.CommitLockName(store.database)
}

// lockTestIssue seeds one standard, durably-committed issue and returns its id.
func lockTestIssue(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	id := "bd-lock-1"
	issue := &types.Issue{ID: id, Title: "commit lock probe", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return id
}

// assertCommitBlockedWhile tests one commit plane: with the probe holding the
// commit lock, the plane's op must neither return its Dolt commit nor add a
// dolt_log entry for heldAt least 500ms; once released, it must finish and
// land its commit.
func assertCommitBlockedWhileHeld(t *testing.T, store *DoltStore, probe *commitLockProbe, lockName string,
	op func(ctx context.Context) error, heldAtLeast time.Duration) {
	t.Helper()
	ctx := context.Background()
	before := probe.logCount(t)
	probe.hold(t, lockName)

	done := make(chan error, 1)
	go func() { done <- op(ctx) }()

	// While the lock is held the write's rows may already be durable in the
	// working set, but its DOLT_COMMIT must not have happened: no new dolt_log
	// entry, and the op has not returned.
	deadline := time.Now().Add(heldAtLeast)
	for time.Now().Before(deadline) {
		if after := probe.logCount(t); after != before {
			t.Fatalf("dolt_log grew from %d to %d while the commit lock was held: the commit was not serialized", before, after)
		}
		select {
		case err := <-done:
			t.Fatalf("commit returned %v while the lock was still held", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("commit returned %v while the lock was still held", err)
	default:
	}

	probe.release(t, lockName)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("commit after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("commit did not return within 10s of the lock being released")
	}
	if !probe.logHasMessage(t, "bd-lock-commit") && probe.logCount(t) == before {
		t.Fatalf("no new dolt_log entry after the lock was released (before=%d after=%d)", before, probe.logCount(t))
	}
	probe.requireLockFree(t, lockName, "commit plane")
}

// TestCommitLockSerializesRunInTransaction is AC-1: with a second session
// holding CommitLockName(db), RunInTransaction's staged commit must not land
// until the holder releases the lock.
func TestCommitLockSerializesRunInTransaction(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	store, probe, lockName := commitLockStore(t)
	ctx := context.Background()
	lockTestIssue(t, ctx, store)

	backlog := probe.logCount(t)
	probe.hold(t, lockName)

	type result struct {
		err error
	}
	done := make(chan result, 1)
	const msg = "commit-lock test"
	go func() {
		err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
			return tx.SetMetadata(ctx, "commit_lock_test", msg)
		})
		done <- result{err: err}
	}()

	// 500ms in: the Dolt commit cannot have landed and the call cannot have
	// returned — at the base commit it lands immediately, which is the
	// defect: nothing excluded another session's HEAD move from the window.
	checkFor := 500 * time.Millisecond
	deadline := time.Now().Add(checkFor)
	for time.Now().Before(deadline) {
		if n := probe.logCount(t); n != backlog {
			t.Fatalf("dolt_log grew from %d to %d while the commit lock was held", backlog, n)
		}
		select {
		case r := <-done:
			t.Fatalf("RunInTransaction returned %v while the commit lock was still held", r.err)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Release; the blocked GET_LOCK wakes, the commit lands under the
	// transaction's message.
	wake := time.Now()
	probe.release(t, lockName)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunInTransaction after lock release: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunInTransaction did not return within 5s of the lock being released")
	}
	if time.Since(wake) > testTimeout/10 {
		t.Logf("note: commit took %v after lock release", time.Since(wake))
	}
	if !probe.logHasMessage(t, msg) {
		t.Fatalf("dolt_log has no %q commit entry after the lock was released", msg)
	}
	probe.requireLockFree(t, lockName, "RunInTransaction")
}
