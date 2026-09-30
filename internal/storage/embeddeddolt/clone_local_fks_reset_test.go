//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// bd-7bpkd / ga-28co77: CALL DOLT_RESET('--hard') silently drops every FK on
// every clone-local (dolt_ignored) table. These tests pin that every
// production hard reset re-links what it drops (the ones it reached — flatten
// and compact — end to end, the shared helper directly), leaves an FK that was
// already severed alone while naming it, and, before re-adding a dropped FK,
// purges that FK's orphans (in these tests, the ones the reset itself
// created).

// withPinnedConn runs fn on a raw SQL session on te's main branch, pinned to
// one connection so a reset and the statements after it share a session. The
// embedded engine holds a directory lock while open, so nothing else may open
// te (the store, te.exec, ...) until fn returns.
func (te *testEnv) withPinnedConn(t *testing.T, ctx context.Context, fn func(conn *sql.Conn)) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	fn(conn)
}

// liveCloneLocalFKs lists the FOREIGN KEY constraints currently on
// clone-local tables, as "table.constraint".
func (te *testEnv) liveCloneLocalFKs(t *testing.T, ctx context.Context) map[string]bool {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, `
		SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME
		FROM information_schema.TABLE_CONSTRAINTS tc
		WHERE tc.TABLE_SCHEMA = DATABASE()
		  AND tc.CONSTRAINT_TYPE = 'FOREIGN KEY'
		  AND (tc.TABLE_NAME = 'events' OR tc.TABLE_NAME = 'leases' OR tc.TABLE_NAME = 'local_metadata'
		       OR tc.TABLE_NAME = 'repo_mtimes' OR tc.TABLE_NAME = 'wisps' OR tc.TABLE_NAME LIKE 'wisp\_%')`)
	if err != nil {
		t.Fatalf("enumerate clone-local FKs: %v", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var table, constraint string
		if err := rows.Scan(&table, &constraint); err != nil {
			t.Fatalf("scan constraint row: %v", err)
		}
		live[table+"."+constraint] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("constraint rows: %v", err)
	}
	return live
}

// assertCloneLocalFKs asserts the live clone-local FKs are exactly
// schema.CloneLocalFKs minus missing. Before any reset this doubles as the
// drift guard: a migration adding a clone-local FK without a spec entry would
// never be re-linked.
func (te *testEnv) assertCloneLocalFKs(t *testing.T, ctx context.Context, stage string, missing ...string) {
	t.Helper()
	live := te.liveCloneLocalFKs(t, ctx)
	skip := map[string]bool{}
	for _, m := range missing {
		skip[m] = true
	}
	spec := map[string]bool{}
	for _, fk := range schema.CloneLocalFKs {
		key := fk.String()
		spec[key] = true
		switch {
		case skip[key] && live[key]:
			t.Errorf("%s: clone-local FK %s is present, want it still missing", stage, key)
		case !skip[key] && !live[key]:
			t.Errorf("%s: clone-local FK %s is missing", stage, key)
		}
	}
	for key := range live {
		if !spec[key] {
			t.Errorf("%s: clone-local FK %s exists but is missing from schema.CloneLocalFKs", stage, key)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

func (te *testEnv) count(t *testing.T, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	te.queryScalar(t, ctx, query, args, &n)
	return n
}

// seedCloneLocalRows creates a tracked issue and a wisp carrying a label (a
// wisp_labels row), committing around them so the store has history to
// squash. It returns the wisp's id.
func seedCloneLocalRows(t *testing.T, ctx context.Context, te *testEnv) string {
	t.Helper()
	anchor := &types.Issue{Title: "anchor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, anchor, "tester"); err != nil {
		t.Fatalf("create anchor issue: %v", err)
	}
	if err := te.store.Commit(ctx, "test: anchor"); err != nil {
		t.Fatalf("commit anchor: %v", err)
	}
	wisp := &types.Issue{Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	te.exec(t, ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'cascade-probe')", wisp.ID)
	second := &types.Issue{Title: "second", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, second, "tester"); err != nil {
		t.Fatalf("create second issue: %v", err)
	}
	if err := te.store.Commit(ctx, "test: second"); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	return wisp.ID
}

// assertWispCascade deletes the wisp with raw SQL and asserts its wisp_labels
// rows went with it — only the FK's ON DELETE CASCADE removes them.
func assertWispCascade(t *testing.T, ctx context.Context, te *testEnv, wispID string) {
	t.Helper()
	if n := te.count(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wispID); n == 0 {
		t.Fatalf("wisp %s has no wisp_labels rows to cascade", wispID)
	}
	te.exec(t, ctx, "DELETE FROM wisps WHERE id = ?", wispID)
	if n := te.count(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wispID); n != 0 {
		t.Fatalf("after deleting wisp %s: %d wisp_labels row(s) remain, want 0 (fk_wisp_labels_issue not cascading)", wispID, n)
	}
}

func TestFlattenPreservesCloneLocalFKs(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "clfkflat")
	wispID := seedCloneLocalRows(t, ctx, te)
	te.assertCloneLocalFKs(t, ctx, "before flatten")

	if err := te.store.Flatten(ctx); err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	te.assertCloneLocalFKs(t, ctx, "after flatten")
	assertWispCascade(t, ctx, te, wispID)
}

func TestCompactPreservesCloneLocalFKs(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "clfkcomp")
	wispID := seedCloneLocalRows(t, ctx, te)
	te.assertCloneLocalFKs(t, ctx, "before compact")

	log, err := te.store.Log(ctx, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) < 3 {
		t.Fatalf("need at least 3 commits to compact, have %d", len(log))
	}
	// Newest first: keep the newest commit, squash everything older.
	initial, boundary := log[len(log)-1].Hash, log[1].Hash
	if err := te.store.Compact(ctx, initial, boundary, len(log)-1, []string{log[0].Hash}); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	te.assertCloneLocalFKs(t, ctx, "after compact")
	assertWispCascade(t, ctx, te, wispID)
}

// An FK already severed before a production reset is reported, not touched:
// no orphan purge, no re-add. Every other FK is still preserved.
func TestFlattenReportsPreSeveredCloneLocalFKWithoutTouchingIt(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "clfksev")
	seedCloneLocalRows(t, ctx, te)
	te.assertCloneLocalFKs(t, ctx, "before sever")

	te.exec(t, ctx, "ALTER TABLE wisp_labels DROP FOREIGN KEY fk_wisp_labels_issue")
	te.exec(t, ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES ('clfksev-no-such-wisp', 'orphan')")

	var flattenErr error
	stderr := captureStderr(t, func() { flattenErr = te.store.Flatten(ctx) })
	if flattenErr != nil {
		t.Fatalf("Flatten: %v", flattenErr)
	}

	te.assertCloneLocalFKs(t, ctx, "after flatten", "wisp_labels.fk_wisp_labels_issue")
	if n := te.count(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'clfksev-no-such-wisp'"); n != 1 {
		t.Fatalf("orphan wisp_labels rows after flatten = %d, want 1 (a pre-severed FK's orphans must not be purged)", n)
	}
	if !strings.Contains(stderr, "wisp_labels.fk_wisp_labels_issue") || !strings.Contains(stderr, "bd doctor --fix") {
		t.Fatalf("flatten output does not name the pre-severed FK with its remedy; stderr = %q", stderr)
	}
}

// The helper-level contract of the same case: Result.AlreadySevered names it.
func TestResetHardPreservingCloneLocalFKs_ReportsPreSeveredFK(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "clfkrep")
	seedCloneLocalRows(t, ctx, te)
	te.exec(t, ctx, "ALTER TABLE wisp_labels DROP FOREIGN KEY fk_wisp_labels_issue")
	te.exec(t, ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES ('clfkrep-no-such-wisp', 'orphan')")

	var result schema.ResetHardResult
	te.withPinnedConn(t, ctx, func(conn *sql.Conn) {
		var err error
		if result, err = schema.ResetHardPreservingCloneLocalFKs(ctx, conn, ""); err != nil {
			t.Fatalf("ResetHardPreservingCloneLocalFKs: %v", err)
		}
	})
	if len(result.AlreadySevered) != 1 || result.AlreadySevered[0].String() != "wisp_labels.fk_wisp_labels_issue" {
		t.Fatalf("AlreadySevered = %v, want exactly wisp_labels.fk_wisp_labels_issue", result.AlreadySevered)
	}
	if w := result.Warning(); !strings.Contains(w, "wisp_labels.fk_wisp_labels_issue") {
		t.Fatalf("Warning() = %q, want it to name wisp_labels.fk_wisp_labels_issue", w)
	}
	for _, r := range result.Relinked {
		if r.CloneLocalFK.String() == "wisp_labels.fk_wisp_labels_issue" {
			t.Fatalf("pre-severed FK was re-linked: %v", result.Relinked)
		}
	}

	te.assertCloneLocalFKs(t, ctx, "after reset", "wisp_labels.fk_wisp_labels_issue")
	if n := te.count(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 'clfkrep-no-such-wisp'"); n != 1 {
		t.Fatalf("orphan wisp_labels rows after reset = %d, want 1", n)
	}
}

// A reset that removes an issue orphans its events rows (events is
// clone-local, so it survives the reset). The helper deletes exactly those
// rows and re-adds fk_events_issue.
func TestResetHardPreservingCloneLocalFKs_PurgesOrphansTheResetCreated(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "clfkorph")
	seedCloneLocalRows(t, ctx, te)

	var baseline string
	te.queryScalar(t, ctx, "SELECT DOLT_HASHOF('HEAD')", nil, &baseline)

	doomed := &types.Issue{Title: "reset away", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, doomed, "tester"); err != nil {
		t.Fatalf("create doomed issue: %v", err)
	}
	// Written while fk_events_issue is enforcing, so it is valid until the
	// reset removes its issue.
	te.exec(t, ctx,
		"INSERT INTO events (id, issue_id, event_type, actor) VALUES ('33333333-3333-3333-3333-333333333333', ?, 'created', 'test')",
		doomed.ID)
	if err := te.store.Commit(ctx, "test: doomed"); err != nil {
		t.Fatalf("commit doomed: %v", err)
	}
	te.assertCloneLocalFKs(t, ctx, "before reset")

	var result schema.ResetHardResult
	var bogusErr error
	te.withPinnedConn(t, ctx, func(conn *sql.Conn) {
		var err error
		if result, err = schema.ResetHardPreservingCloneLocalFKs(ctx, conn, baseline); err != nil {
			t.Fatalf("ResetHardPreservingCloneLocalFKs: %v", err)
		}
		// Enforcing again on the reset's own session: a bogus audit row is
		// refused.
		_, bogusErr = conn.ExecContext(ctx,
			"INSERT INTO events (id, issue_id, event_type, actor) VALUES ('44444444-4444-4444-4444-444444444444', 'clfkorph-no-such-issue', 'created', 'test')")
	})
	if len(result.AlreadySevered) != 0 {
		t.Fatalf("AlreadySevered = %v, want none", result.AlreadySevered)
	}
	if bogusErr == nil || !strings.Contains(strings.ToLower(bogusErr.Error()), "foreign key") {
		t.Fatalf("bogus events insert after reset: err = %v, want foreign key violation", bogusErr)
	}

	if n := te.count(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", doomed.ID); n != 0 {
		t.Fatalf("issue %s survived the reset; the test needs the reset to remove it", doomed.ID)
	}
	if n := te.count(t, ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", doomed.ID); n != 0 {
		t.Fatalf("%d events row(s) for the reset-removed issue remain, want 0", n)
	}
	var eventsRelinked *schema.RelinkedCloneLocalFK
	for i := range result.Relinked {
		if result.Relinked[i].CloneLocalFK.String() == "events.fk_events_issue" {
			eventsRelinked = &result.Relinked[i]
		}
	}
	if eventsRelinked == nil || eventsRelinked.OrphansRemoved < 1 {
		t.Fatalf("Relinked = %v, want events.fk_events_issue with at least 1 orphan removed", result.Relinked)
	}
	te.assertCloneLocalFKs(t, ctx, "after reset")
}
