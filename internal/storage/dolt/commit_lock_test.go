package dolt

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// GH#8590 (bd-72i8dp): every server-mode commit plane holds
// versioncontrolops.CommitLockName(db) across its DOLT_ADD + DOLT_COMMIT, so
// a HEAD move by another session cannot land inside the commit and leave
// Dolt conflict artifacts in it.

// commitLockHolder is a second client of the store's database: a pinned
// session that takes the commit lock the way another bd process would, plus a
// pool of its own for reads, so the test never competes with the store's
// (deliberately small) pool while a write holds it.
type commitLockHolder struct {
	t    *testing.T
	db   *sql.DB
	conn *sql.Conn
	id   int64
}

func newCommitLockHolder(t *testing.T, ctx context.Context, store *DoltStore) *commitLockHolder {
	t.Helper()
	if testServerPort == 0 {
		if realDoltTestServerRequired() {
			t.Fatal("commit-lock test requires the real Dolt test server, which did not start")
		}
		t.Skip("no Dolt test server available")
	}
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: store.database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin lock holder: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h := &commitLockHolder{t: t, db: db, conn: conn}
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&h.id); err != nil {
		t.Fatalf("read lock holder connection id: %v", err)
	}
	return h
}

func (h *commitLockHolder) hold(ctx context.Context, name string) {
	h.t.Helper()
	var got sql.NullInt64
	if err := h.conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&got); err != nil {
		h.t.Fatalf("GET_LOCK(%q): %v", name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		h.t.Fatalf("GET_LOCK(%q) = %v, want 1", name, got)
	}
}

func (h *commitLockHolder) release(ctx context.Context, name string) {
	h.t.Helper()
	var got sql.NullInt64
	if err := h.conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&got); err != nil {
		h.t.Fatalf("RELEASE_LOCK(%q): %v", name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		h.t.Fatalf("RELEASE_LOCK(%q) = %v, want 1", name, got)
	}
}

func (h *commitLockHolder) logCount(ctx context.Context) int {
	h.t.Helper()
	var n int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
		h.t.Fatalf("count dolt_log: %v", err)
	}
	return n
}

func (h *commitLockHolder) logHas(ctx context.Context, message string) bool {
	h.t.Helper()
	var n int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", message).Scan(&n); err != nil {
		h.t.Fatalf("read dolt_log: %v", err)
	}
	return n > 0
}

// isFree reports IS_FREE_LOCK(name) from a session other than the store's.
func (h *commitLockHolder) isFree(ctx context.Context, name string) bool {
	h.t.Helper()
	var got sql.NullInt64
	if err := h.db.QueryRowContext(ctx, "SELECT IS_FREE_LOCK(?)", name).Scan(&got); err != nil {
		h.t.Fatalf("IS_FREE_LOCK(%q): %v", name, err)
	}
	return got.Valid && got.Int64 == 1
}

// usedBy reports IS_USED_LOCK(name): the connection id holding it, or 0.
func (h *commitLockHolder) usedBy(ctx context.Context, name string) int64 {
	h.t.Helper()
	var got sql.NullInt64
	if err := h.db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&got); err != nil {
		h.t.Fatalf("IS_USED_LOCK(%q): %v", name, err)
	}
	return got.Int64
}

// pendingTables lists the non-ignored tables dolt_status reports changed.
func (h *commitLockHolder) pendingTables(ctx context.Context) []string {
	h.t.Helper()
	rows, err := h.db.QueryContext(ctx, `SELECT s.table_name FROM dolt_status s
		WHERE NOT EXISTS (SELECT 1 FROM dolt_ignore di WHERE di.ignored = 1 AND s.table_name LIKE di.pattern)`)
	if err != nil {
		h.t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			h.t.Fatalf("scan dolt_status: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read dolt_status: %v", err)
	}
	return out
}

// commitLockTestIssue creates a committed issue to write to.
func commitLockTestIssue(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: id, Title: "commit lock " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// assertCommitWaitsForLock runs write while h holds the store's commit lock
// and requires that it neither returns nor lands a Dolt commit until the lock
// is released, then that it returns nil and lands a commit within 1s of the
// release. It returns how long the write took after the release.
func assertCommitWaitsForLock(t *testing.T, ctx context.Context, store *DoltStore, h *commitLockHolder, write func(ctx context.Context) error) time.Duration {
	t.Helper()
	name := versioncontrolops.CommitLockName(store.database)
	h.hold(ctx, name)
	before := h.logCount(ctx)

	done := make(chan error, 1)
	go func() { done <- write(ctx) }()

	select {
	case err := <-done:
		h.release(ctx, name)
		t.Fatalf("write returned (err=%v) while another session held %q: its Dolt commit is not serialized (GH#8590)", err, name)
	case <-time.After(500 * time.Millisecond):
	}
	if after := h.logCount(ctx); after != before {
		h.release(ctx, name)
		t.Fatalf("dolt_log grew from %d to %d while another session held %q: the Dolt commit landed unlocked (GH#8590)", before, after, name)
	}

	released := time.Now()
	h.release(ctx, name)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("write did not return within 5s of the lock release")
	}
	took := time.Since(released)
	if after := h.logCount(ctx); after <= before {
		t.Fatalf("dolt_log = %d after the write, want > %d: no Dolt commit landed", after, before)
	}
	if took > time.Second {
		t.Errorf("write returned %s after the lock release, want within 1s", took)
	}
	return took
}

// TestCommitLockSerializesRunInTransaction covers AC-1: RunInTransaction's
// Dolt commit waits for a commit lock another session holds, and lands with
// the transaction's message once it is released.
func TestCommitLockSerializesRunInTransaction(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	commitLockTestIssue(t, ctx, store, "test-cl1")
	h := newCommitLockHolder(t, ctx, store)

	const msg = "commit-lock test"
	assertCommitWaitsForLock(t, ctx, store, h, func(ctx context.Context) error {
		return store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
			return tx.UpdateIssue(ctx, "test-cl1", map[string]interface{}{"title": "locked title"}, "tester")
		})
	})
	if !h.logHas(ctx, msg) {
		t.Fatalf("dolt_log has no commit %q after the lock release", msg)
	}
}

// TestCommitLockSerializesStorePlanes covers AC-2: each other DoltStore
// commit plane waits for the commit lock too.
func TestCommitLockSerializesStorePlanes(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	for _, id := range []string{"test-cp1", "test-cp2", "test-cp3", "test-cp4", "test-cp5"} {
		commitLockTestIssue(t, ctx, store, id)
	}
	if err := store.CloseIssue(ctx, "test-cp4", "done", "tester", ""); err != nil {
		t.Fatalf("close test-cp4: %v", err)
	}
	h := newCommitLockHolder(t, ctx, store)

	cases := []struct {
		name  string
		setup func(ctx context.Context)
		write func(ctx context.Context) error
	}{
		{name: "funnel AddLabel", write: func(ctx context.Context) error {
			return store.AddLabel(ctx, "test-cp1", "locked", "tester")
		}},
		{name: "in-tx UpdateIssue", write: func(ctx context.Context) error {
			return store.UpdateIssue(ctx, "test-cp2", map[string]interface{}{"title": "locked update"}, "tester")
		}},
		{name: "in-tx ClaimIssue", write: func(ctx context.Context) error {
			return store.ClaimIssue(ctx, "test-cp3", "claimer")
		}},
		{name: "sweeper", write: func(ctx context.Context) error {
			sweeper, err := store.Sweeper()
			if err != nil {
				return err
			}
			res, err := sweeper.Sweep(ctx, issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: "test-cp4"})
			if err != nil {
				return err
			}
			if res.Swept == 0 {
				return errors.New("sweep swept nothing; the case needs a closed bead to sweep")
			}
			return nil
		}},
		{name: "CommitAll", setup: func(ctx context.Context) {
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET notes = 'out of band' WHERE id = 'test-cp1'"); err != nil {
				t.Fatalf("dirty the working set: %v", err)
			}
		}, write: func(ctx context.Context) error {
			committed, err := store.CommitAll(ctx, "commit-lock commit all")
			if err == nil && !committed {
				return errors.New("CommitAll committed nothing")
			}
			return err
		}},
		{name: "deleter", write: func(ctx context.Context) error {
			deleter, err := store.Deleter()
			if err != nil {
				return err
			}
			_, err = deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{"test-cp5"}, Actor: "tester"})
			return err
		}},
		{name: "commitWorkingSet (Commit)", setup: func(ctx context.Context) {
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET notes = 'out of band commit' WHERE id = 'test-cp2'"); err != nil {
				t.Fatalf("dirty the working set: %v", err)
			}
		}, write: func(ctx context.Context) error {
			return store.Commit(ctx, "commit-lock commit working set")
		}},
		{name: "CommitWithConfig", setup: func(ctx context.Context) {
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET notes = 'out of band with config' WHERE id = 'test-cp3'"); err != nil {
				t.Fatalf("dirty the working set: %v", err)
			}
		}, write: func(ctx context.Context) error {
			return store.CommitWithConfig(ctx, "commit-lock commit with config")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(ctx)
			}
			assertCommitWaitsForLock(t, ctx, store, h, tc.write)
		})
	}
}

// TestCommitLockReleasedOnEveryReturnPath covers AC-4: whichever way a commit
// path returns, the lock is free from another session as soon as it does.
func TestCommitLockReleasedOnEveryReturnPath(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	commitLockTestIssue(t, ctx, store, "test-rp1")
	h := newCommitLockHolder(t, ctx, store)
	name := versioncontrolops.CommitLockName(store.database)
	// Keep released sessions in the pool. With no idle connections
	// (setupConcurrentTestStore), database/sql closes each session as the call
	// returns and the server frees every lock it held, so a path that never
	// released the lock would still read as free.
	store.db.SetMaxIdleConns(2)

	cases := []struct {
		name    string
		run     func(ctx context.Context) error
		wantErr bool
		// check, when set, vets the returned error further.
		check func(t *testing.T, err error)
	}{
		{name: "success", run: func(ctx context.Context) error {
			return store.RunInTransaction(ctx, "rp success", func(tx storage.Transaction) error {
				return tx.UpdateIssue(ctx, "test-rp1", map[string]interface{}{"title": "rp success"}, "tester")
			})
		}},
		{name: "nothing staged", run: func(ctx context.Context) error {
			// Something unrelated is dirty, so the commit proceeds past any
			// pending check, takes the lock, and stages nothing.
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET notes = 'unrelated dirt' WHERE id = 'test-rp1'"); err != nil {
				return err
			}
			return store.doltAddAndCommit(ctx, []string{"comments"}, "rp nothing staged")
		}},
		{name: "failed DOLT_ADD", wantErr: true, run: func(ctx context.Context) error {
			return store.doltAddAndCommit(ctx, []string{"no_such_table_rp"}, "rp failed add")
		}},
		{name: "failed DOLT_COMMIT", wantErr: true, run: func(ctx context.Context) error {
			// An author Dolt cannot parse fails DOLT_COMMIT after the lock
			// is taken and the tables are staged.
			saved := store.committerName
			store.committerName = "Jane Doe (CI)"
			defer func() { store.committerName = saved }()
			return store.RunInTransaction(ctx, "rp failed commit", func(tx storage.Transaction) error {
				return tx.UpdateIssue(ctx, "test-rp1", map[string]interface{}{"title": "rp failed commit"}, "tester")
			})
		}},
		{name: "funnel staged-screen refusal", wantErr: true, run: func(ctx context.Context) error {
			// The working-set screen passes. A key another session writes
			// right after it is staged by DOLT_ADD, so the staged screen
			// refuses under the lock and unstageRefused runs before the
			// release. Setup leaves issue_prefix uncommitted, which the
			// working-set screen would refuse first, so commit it.
			if err := store.CommitWithConfig(ctx, "rp: settle setup config"); err != nil {
				return err
			}
			if err := store.SetConfig(ctx, "kv.memory.rp-staged", "memory"); err != nil {
				return err
			}
			var raceErr error
			saved := afterStageCommitScreen
			afterStageCommitScreen = func() {
				_, raceErr = h.db.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES ('rp_staged_internal', 'x')")
			}
			defer func() { afterStageCommitScreen = saved }()
			err := store.CommitConfigUserKVOnly(ctx, "rp staged screen refusal")
			return errors.Join(raceErr, err)
		}, check: func(t *testing.T, err error) {
			var refused *storage.ConfigCommitRefusedError
			if !errors.As(err, &refused) || !slices.Equal(refused.Keys, []string{"rp_staged_internal"}) {
				t.Fatalf("CommitConfigUserKVOnly = %v, want the staged screen's refusal of rp_staged_internal", err)
			}
		}},
		{name: "funnel working-set screen refusal", wantErr: true, run: func(ctx context.Context) error {
			// Refused before the lock is taken: nothing to release.
			if _, err := store.db.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES ('rp.internal', 'x')"); err != nil {
				return err
			}
			return store.CommitConfigUserKVOnly(ctx, "rp screen refusal")
		}},
		{name: "context canceled after in-tx grant", wantErr: true, run: func(ctx context.Context) error {
			ctx, cancelWrite := context.WithCancel(ctx)
			defer cancelWrite()
			saved := afterInTxCommitLockGrant
			afterInTxCommitLockGrant = cancelWrite
			defer func() { afterInTxCommitLockGrant = saved }()
			return store.UpdateIssue(ctx, "test-rp1", map[string]interface{}{"title": "rp canceled"}, "tester")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("run err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
			if !h.isFree(ctx, name) {
				t.Fatalf("IS_FREE_LOCK(%q) = 0 right after the call returned (err=%v): the commit lock leaked", name, err)
			}
		})
	}
}

// TestCommitLockSkippedWhenNothingPending covers AC-5: a write that leaves
// nothing pending returns without waiting for (or touching) the lock.
func TestCommitLockSkippedWhenNothingPending(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	setMeta := func(ctx context.Context) error {
		return store.RunInTransaction(ctx, "commit-lock no-op", func(tx storage.Transaction) error {
			return tx.SetMetadata(ctx, "commit_lock_noop", "v")
		})
	}
	if err := setMeta(ctx); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	h := newCommitLockHolder(t, ctx, store)
	// Settle whatever store setup left uncommitted, so the only change the
	// write below could make is its own (a same-value metadata upsert).
	if pending := h.pendingTables(ctx); len(pending) > 0 {
		t.Logf("settling setup leftovers in the working set: %v", pending)
		if _, err := store.CommitAll(ctx, "commit-lock settle setup"); err != nil {
			t.Fatalf("settle working set: %v", err)
		}
	}
	if pending := h.pendingTables(ctx); len(pending) > 0 {
		t.Fatalf("working set still pending after settling: %v", pending)
	}
	name := versioncontrolops.CommitLockName(store.database)
	h.hold(ctx, name)
	defer h.release(ctx, name)

	start := time.Now()
	if err := setMeta(ctx); err != nil {
		t.Fatalf("idempotent write: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("idempotent write took %s with the lock held elsewhere, want well under the %s wait: it waited for the lock", took, versioncontrolops.DefaultCommitLockWait)
	}
	if got := h.usedBy(ctx, name); got != h.id {
		t.Fatalf("IS_USED_LOCK = %d, want the holder %d", got, h.id)
	}
	if n := store.commitLockTimeouts.Load(); n != 0 {
		t.Fatalf("commit_lock_timeouts = %d, want 0: the no-op write issued GET_LOCK", n)
	}
}

// TestCommitLockScopedPerDatabase covers AC-6: a lock held for another
// database on the same server does not delay this store's commit.
func TestCommitLockScopedPerDatabase(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	commitLockTestIssue(t, ctx, store, "test-db1")
	h := newCommitLockHolder(t, ctx, store)
	other := versioncontrolops.CommitLockName("other_db")
	h.hold(ctx, other)
	defer h.release(ctx, other)

	const msg = "commit-lock other db"
	start := time.Now()
	if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, "test-db1", map[string]interface{}{"title": "other db"}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("commit took %s while only %q was held, want no wait", took, other)
	}
	if !h.logHas(ctx, msg) {
		t.Fatalf("dolt_log has no commit %q", msg)
	}
}

// TestCommitLockTimeoutCommitsUnlocked covers AC-7: past the store's wait the
// commit lands unlocked, the timeout is counted, and the holder keeps its lock.
func TestCommitLockTimeoutCommitsUnlocked(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	store.commitLockWait = time.Second
	ctx, cancel := testContext(t)
	defer cancel()
	commitLockTestIssue(t, ctx, store, "test-to1")
	h := newCommitLockHolder(t, ctx, store)
	name := versioncontrolops.CommitLockName(store.database)
	h.hold(ctx, name)
	defer h.release(ctx, name)

	const msg = "commit-lock timeout"
	start := time.Now()
	if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, "test-to1", map[string]interface{}{"title": "timed out"}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction past the commit-lock wait = %v, want nil (fail open)", err)
	}
	took := time.Since(start)
	if took < 900*time.Millisecond || took > 4*time.Second {
		t.Errorf("RunInTransaction took %s, want about the 1s commit-lock wait", took)
	}
	if !h.logHas(ctx, msg) {
		t.Fatalf("dolt_log has no commit %q: the unlocked commit did not land", msg)
	}
	if n := store.commitLockTimeouts.Load(); n != 1 {
		t.Fatalf("commit_lock_timeouts = %d, want 1", n)
	}
	if got := h.usedBy(ctx, name); got != h.id {
		t.Fatalf("IS_USED_LOCK = %d, want the holder's CONNECTION_ID %d", got, h.id)
	}
}
