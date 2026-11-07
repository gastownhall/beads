package dolt

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// This file covers the wisps.is_blocked restore that ignored migration 0032's
// pre-migration repair (restoreWispIsBlocked) performs on a clone whose
// ignored cursor already sits past ignored/0015, where the ignored/7 and
// ignored/15 repairs can never fire again (gastownhall/beads#4468, #5257).
// Every test drives it through the real server-mode open path -- New, not a
// direct MigrateUp call -- on its own database, because the defect it heals
// is an open-path failure and the fix must be proven on the same path.

// wispHealEnv owns one dedicated database on the test server plus the
// disposable .beads directory stores open under.
type wispHealEnv struct {
	t      *testing.T
	dbName string
	dir    string
}

func newWispHealEnv(t *testing.T) *wispHealEnv {
	t.Helper()
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	// No DROP DATABASE on cleanup: rapid CREATE/DROP cycles crash the Dolt
	// server; orphan databases are cleaned up when the container terminates.
	dbName := uniqueTestDBName(t)
	return &wispHealEnv{t: t, dbName: dbName, dir: filepath.Join(t.TempDir(), ".beads")}
}

// open opens a store against the env's database under its own timeout budget,
// the way every reopen in these tests must (see openStoreWithOwnBudget).
func (e *wispHealEnv) open() (*DoltStore, error) {
	e.t.Helper()
	return openStoreWithOwnBudget(e.t, &Config{
		Path:            filepath.Join(e.dir, "dolt"),
		BeadsDir:        e.dir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        e.dbName,
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
}

// scalarInt reads one integer over the store's pool.
func (e *wispHealEnv) scalarInt(store *DoltStore, query string, args ...any) int {
	e.t.Helper()
	ctx, cancel := testContext(e.t)
	defer cancel()
	var n int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	return n
}

// scalarStrings reads one string column into a slice.
func (e *wispHealEnv) scalarStrings(store *DoltStore, query string, args ...any) []string {
	e.t.Helper()
	ctx, cancel := testContext(e.t)
	defer cancel()
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatalf("scan %.80q: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("%.80q: %v", query, err)
	}
	return out
}

// driftWispIsBlockedForHeal removes the shape ignored/0006 added -- index
// first, then the column it is keyed on -- the way the drifted clones in the
// wild present.
func driftWispIsBlockedForHeal(t *testing.T, store *DoltStore) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := store.db.ExecContext(ctx, "DROP INDEX idx_wisps_is_blocked ON wisps"); err != nil {
		t.Fatalf("drop idx_wisps_is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "ALTER TABLE wisps DROP COLUMN is_blocked"); err != nil {
		t.Fatalf("drop wisps.is_blocked: %v", err)
	}
}

// rewindIgnoredCursor deletes every ignored cursor row at version or above,
// re-pending that marker and its repair for the next open.
func rewindIgnoredCursor(t *testing.T, store *DoltStore, version int) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM ignored_schema_migrations WHERE version >= ?", version); err != nil {
		t.Fatalf("rewind ignored cursor to %d: %v", version, err)
	}
}

// requireWispIsBlockedShape asserts the column is back with ignored/0006's
// definition and the index is back over (is_blocked, status).
func (e *wispHealEnv) requireWispIsBlockedShape(store *DoltStore) {
	e.t.Helper()
	shape := e.scalarStrings(store, `SELECT CONCAT(COLUMN_TYPE, '|', IS_NULLABLE, '|', COALESCE(COLUMN_DEFAULT, '<null>'))
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)
	if len(shape) != 1 || shape[0] != "tinyint(1)|NO|0" {
		e.t.Fatalf("wisps.is_blocked shape = %v, want [tinyint(1)|NO|0]", shape)
	}
	indexCols := e.scalarStrings(store, `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'
		ORDER BY SEQ_IN_INDEX`)
	if len(indexCols) != 2 || indexCols[0] != "is_blocked" || indexCols[1] != "status" {
		e.t.Fatalf("idx_wisps_is_blocked columns = %v, want [is_blocked status]", indexCols)
	}
}

// TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt covers
// AC-1: a clone at the top of the ignored series whose wisps table lost
// is_blocked heals the column and the index on the next open, before the
// ignored cursor records 0032, and the healed shape matches ignored/0006's
// definition exactly.
func TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt(t *testing.T) {
	env := newWispHealEnv(t)

	store, err := env.open()
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	if n := env.scalarInt(store, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
		store.Close()
		t.Fatalf("fresh open recorded ignored 0032 %d times, want exactly 1", n)
	}
	driftWispIsBlockedForHeal(t, store)
	rewindIgnoredCursor(t, store, 32)
	if err := store.Close(); err != nil {
		t.Fatalf("close drifted store: %v", err)
	}

	healed, err := env.open()
	if err != nil {
		t.Fatalf("New on the drifted clone: %v", err)
	}
	defer func() { _ = healed.Close() }()
	env.requireWispIsBlockedShape(healed)
	if n := env.scalarInt(healed, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
		t.Fatalf("healed open recorded ignored 0032 %d times, want exactly 1", n)
	}
}

// seedWispBlockedGraph writes the dependency shape whose flags only a live
// recompute can restore: durable open blocker A; wisp B blocked by A; wisp C,
// a child of B, blocked through its parent; and independent wisp D. The
// create/dep-add paths maintain the flags until the drift wipes the column.
func seedWispBlockedGraph(t *testing.T, store *DoltStore) {
	t.Helper()
	// The batch context reads issue_prefix from the config table even when
	// every ID is explicit (SkipPrefixValidation skips matching, not the
	// read); the fresh database has no row until bd init writes one.
	if err := setConfigWithOwnBudget(t, store, "issue_prefix", "wh"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	ctx, cancel := testContext(t)
	defer cancel()
	newIssue := func(id string, ephemeral bool) *types.Issue {
		return &types.Issue{ID: id, Title: "wisp heal " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	}
	for _, is := range []*types.Issue{newIssue("wh-a", false), newIssue("wh-b", true), newIssue("wh-c", true), newIssue("wh-d", true)} {
		if err := store.CreateIssue(ctx, is, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", is.ID, err)
		}
	}
	deps := []types.Dependency{
		{IssueID: "wh-b", DependsOnID: "wh-a", Type: types.DepBlocks},
		{IssueID: "wh-c", DependsOnID: "wh-b", Type: types.DepParentChild},
	}
	for i := range deps {
		if err := store.AddDependency(ctx, &deps[i], "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", deps[i].IssueID, deps[i].DependsOnID, err)
		}
	}
}

// wispFlags reads every seeded wisp's flag as id->blocked.
func (e *wispHealEnv) wispFlags(store *DoltStore) map[string]bool {
	e.t.Helper()
	rows := map[string]bool{}
	ctx, cancel := testContext(e.t)
	defer cancel()
	r, err := store.db.QueryContext(ctx, "SELECT id, is_blocked FROM wisps")
	if err != nil {
		e.t.Fatalf("read wisp flags: %v", err)
	}
	defer r.Close()
	for r.Next() {
		var id string
		var blocked bool
		if err := r.Scan(&id, &blocked); err != nil {
			e.t.Fatalf("scan wisp flag: %v", err)
		}
		rows[id] = blocked
	}
	if err := r.Err(); err != nil {
		e.t.Fatalf("read wisp flags: %v", err)
	}
	return rows
}

// TestDoltNew_WispIsBlockedRecomputeAtIgnoredCursor_RealDolt covers AC-2: the
// restore must recompute every wisp's is_blocked from the live dependency
// graph, not merely return the column with its DEFAULT 0 -- a shape-only
// restore would surface blocked wisps as ready, because restored-to-zero
// flags with own ready-affecting edges are not read around. B (blocked by an
// open issue) and C (blocked through its parent) come back flagged; A and D
// stay clear; ready work includes exactly the unblocked ones.
func TestDoltNew_WispIsBlockedRecomputeAtIgnoredCursor_RealDolt(t *testing.T) {
	env := newWispHealEnv(t)

	store, err := env.open()
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	seedWispBlockedGraph(t, store)
	for id, want := range map[string]bool{"wh-b": true, "wh-c": true, "wh-d": false} {
		if got := env.wispFlags(store)[id]; got != want {
			store.Close()
			t.Fatalf("pre-drift flag for %s = %v, want %v (the live paths maintain it)", id, got, want)
		}
	}
	driftWispIsBlockedForHeal(t, store)
	rewindIgnoredCursor(t, store, 32)
	if err := store.Close(); err != nil {
		t.Fatalf("close drifted store: %v", err)
	}

	healed, err := env.open()
	if err != nil {
		t.Fatalf("New on the drifted clone: %v", err)
	}
	defer func() { _ = healed.Close() }()
	env.requireWispIsBlockedShape(healed)
	flags := env.wispFlags(healed)
	for id, want := range map[string]bool{"wh-b": true, "wh-c": true, "wh-d": false} {
		if got := flags[id]; got != want {
			t.Fatalf("healed flag for %s = %v, want %v: the restore must recompute, not default to 0", id, got, want)
		}
	}
	var issueBlocked bool
	ctx, cancel := testContext(t)
	defer cancel()
	if err := healed.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'wh-a'").Scan(&issueBlocked); err != nil {
		t.Fatalf("read durable blocker flag: %v", err)
	}
	if issueBlocked {
		t.Fatal("the durable blocker wh-a was flagged; the restore must not touch the durable plane")
	}

	ready, err := healed.GetReadyWork(ctx, types.WorkFilter{IncludeEphemeral: true})
	if err != nil {
		t.Fatalf("GetReadyWork on the healed clone: %v", err)
	}
	readyIDs := map[string]bool{}
	for _, is := range ready {
		readyIDs[is.ID] = true
	}
	for id, want := range map[string]bool{"wh-a": true, "wh-d": true, "wh-b": false, "wh-c": false} {
		if readyIDs[id] != want {
			t.Fatalf("ready work holds %s=%v, want %v (ready set: %v)", id, readyIDs[id], want, readyIDs)
		}
	}
}

// TestDoltNew_EphemeralCreateAfterWispHeal_RealDolt covers AC-3's server-mode
// half: after the heal the wisps plane is not a museum piece -- an ephemeral
// create on the healed clone succeeds and lands with its maintained flag.
// Before the fix this is the reported symptom itself: every ephemeral create
// on a drifted clone failed on the missing column.
func TestDoltNew_EphemeralCreateAfterWispHeal_RealDolt(t *testing.T) {
	env := newWispHealEnv(t)

	store, err := env.open()
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	if err := setConfigWithOwnBudget(t, store, "issue_prefix", "wh"); err != nil {
		store.Close()
		t.Fatalf("set issue_prefix: %v", err)
	}
	driftWispIsBlockedForHeal(t, store)
	rewindIgnoredCursor(t, store, 32)
	if err := store.Close(); err != nil {
		t.Fatalf("close drifted store: %v", err)
	}

	healed, err := env.open()
	if err != nil {
		t.Fatalf("New on the drifted clone: %v", err)
	}
	defer func() { _ = healed.Close() }()
	env.requireWispIsBlockedShape(healed)
	ctx, cancel := testContext(t)
	defer cancel()
	is := &types.Issue{ID: "wh-e", Title: "created after heal", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := healed.CreateIssue(ctx, is, "tester"); err != nil {
		t.Fatalf("CreateIssue ephemeral on the healed clone: %v (the reported symptom: every ephemeral create failed on the missing column)", err)
	}
	if got, ok := env.wispFlags(healed)["wh-e"]; !ok || got {
		t.Fatalf("created wisp wh-e flag = %v (present=%v), want present and false", got, ok)
	}
}

// TestDoltNew_WispHealHealthyCloneRecordsOnce_RealDolt covers AC-4: a healthy
// clone -- column and index intact, flags live -- records ignored 0032
// exactly once on its first open, and every converged reopen after that pays
// only the three probes: no re-record, no repair writes, no flag flips.
func TestDoltNew_WispHealHealthyCloneRecordsOnce_RealDolt(t *testing.T) {
	env := newWispHealEnv(t)

	first, err := env.open()
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	seedWispBlockedGraph(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	// The first open on a fresh database is also its first converged state;
	// a clone that adopts the marker by pull starts converged instead, which
	// is the same cursor shape this reopen observes.
	for i := 0; i < 3; i++ {
		again, err := env.open()
		if err != nil {
			t.Fatalf("converged reopen %d: %v", i, err)
		}
		if n := env.scalarInt(again, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
			again.Close()
			t.Fatalf("converged reopen %d recorded ignored 0032 %d times, want exactly 1", i, n)
		}
		env.requireWispIsBlockedShape(again)
		for id, want := range map[string]bool{"wh-b": true, "wh-c": true, "wh-d": false} {
			if got := env.wispFlags(again)[id]; got != want {
				again.Close()
				t.Fatalf("flag for %s after converged reopen %d = %v, want %v (a converged clone must not recompute)", id, i, got, want)
			}
		}
		if err := again.Close(); err != nil {
			t.Fatalf("close converged reopen %d: %v", i, err)
		}
	}
}
