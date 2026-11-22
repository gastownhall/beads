package dolt

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
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
