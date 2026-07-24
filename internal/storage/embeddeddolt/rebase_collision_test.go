//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests reproduce the #4796 hazard against the REAL embedded Dolt engine:
// two clones that each add children under the same parent while offline mint the
// same "parent.N" ids, so on sync the issues table has an add/add PK collision
// AND the child_counters row both-changed — neither of which the pull settle
// auto-resolves, so the pull aborts. RebaseChildCollisions renumbers the losing
// side and completes the merge. The two-clone divergence is modelled with a peer
// branch (matching seedEmbeddedFKCascadeDivergence); the merge ref is a local
// branch, exercising the identical merge/settle code a real pull runs after
// DOLT_FETCH. This is the 2026-07-14 pa-vubk.71/.72/.73 collision, reproduced.

func insertRebaseIssue(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id, title string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title); err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
}

// seedRebaseCollision builds the collision on main and returns the peer branch
// (clone B), leaving main checked out. Base: parent P with child P.70 and
// counter=70. Clone A (main) adds P.71/P.72/P.73 (a "vip" label on P.71 to prove
// FK cascade carries dependents); clone B adds P.71 "Tom" and P.72 "Eugene".
func seedRebaseCollision(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, p string) (peerBranch string) {
	t.Helper()

	insertRebaseIssue(t, ctx, db, p, "Parent")
	insertRebaseIssue(t, ctx, db, p+".70", "Child70")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed base')"); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	peerBranch = "rebasepeer_" + p
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	// Clone A (main): three new children, counter to 73, label on P.71.
	insertRebaseIssue(t, ctx, db, p+".71", "Local71")
	insertRebaseIssue(t, ctx, db, p+".72", "Local72")
	insertRebaseIssue(t, ctx, db, p+".73", "Local73")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, 'vip')", p+".71"); err != nil {
		t.Fatalf("label local .71: %v", err)
	}
	// A dependency edge from the colliding child to its parent (what
	// `bd create --parent` writes). Its primary key is DERIVED from the child id
	// (depid.New(child, parent)), so both clones mint the SAME dep id for their
	// respective .71 — renumbering must rekey it, or the merge forks the
	// dependencies PK (the #4796 hazard the label alone does not exercise).
	insertRebaseDep(t, ctx, db, p+".71", p)
	if _, err := db.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p); err != nil {
		t.Fatalf("bump local counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone A adds .71/.72/.73')"); err != nil {
		t.Fatalf("commit clone A: %v", err)
	}

	// Clone B (peer): two children colliding on .71/.72, counter to 72.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	insertRebaseIssue(t, ctx, db, p+".71", "Tom")
	insertRebaseIssue(t, ctx, db, p+".72", "Eugene")
	// Same derived dep id as clone A's .71 — this is what collides on merge.
	insertRebaseDep(t, ctx, db, p+".71", p)
	if _, err := db.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p); err != nil {
		t.Fatalf("bump peer counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone B adds .71/.72')"); err != nil {
		t.Fatalf("commit clone B: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	return peerBranch
}

// insertRebaseWisp adds a session-local wisp row (the wisps table mirrors issues
// column-for-column). A wisp id is "active" simply by existing in wisps, which is
// what UpdateIssueIDInTx keys wisp-vs-durable routing off.
func insertRebaseWisp(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id, title string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title); err != nil {
		t.Fatalf("insert wisp %s: %v", id, err)
	}
}

// wispTitleOf reads a wisp's title, failing if the id is absent from the wisps
// table (i.e. it was orphaned under its old id instead of moving with its root).
func wispTitleOf(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id string) string {
	t.Helper()
	var title string
	if err := db.QueryRowContext(ctx, "SELECT title FROM wisps WHERE id = ?", id).Scan(&title); err != nil {
		t.Fatalf("read wisp title of %s (orphaned?): %v", id, err)
	}
	return title
}

func wispExists(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count wisp %s: %v", id, err)
	}
	return n > 0
}

func insertRebaseDep(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, issueID, dependsOn string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at) "+
			"VALUES (?, ?, ?, 'blocks', 'seed', NOW())",
		depid.New(issueID, dependsOn), issueID, dependsOn); err != nil {
		t.Fatalf("insert dep %s->%s: %v", issueID, dependsOn, err)
	}
}

// assertDepRekeyed checks that a dependency edge issueID->dependsOn exists under
// the deterministic id depid.New(issueID, dependsOn) — i.e. the renumber rekeyed
// the surrogate PK to the new target, not left it stale.
func assertDepRekeyed(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, issueID, dependsOn string) {
	t.Helper()
	var target string
	if err := db.QueryRowContext(ctx,
		"SELECT depends_on_issue_id FROM dependencies WHERE id = ?",
		depid.New(issueID, dependsOn)).Scan(&target); err != nil {
		t.Fatalf("dependency %s->%s not found under its derived id (stale surrogate PK?): %v", issueID, dependsOn, err)
	}
	if target != dependsOn {
		t.Errorf("dependency under derived id targets %q, want %q", target, dependsOn)
	}
}

func titleOf(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id string) string {
	t.Helper()
	var title string
	if err := db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", id).Scan(&title); err != nil {
		t.Fatalf("read title of %s: %v", id, err)
	}
	return title
}

func lastChildOf(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, parent string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&n); err != nil {
		t.Fatalf("read counter of %s: %v", parent, err)
	}
	return n
}

func labelIssue(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, label string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx,
		"SELECT issue_id FROM labels WHERE label = ?", label).Scan(&id); err != nil {
		t.Fatalf("read issue carrying label %s: %v", label, err)
	}
	return id
}

func assertRebaseSettled(t *testing.T, ctx context.Context, db versioncontrolops.DBConn) {
	t.Helper()
	var conflicts int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_conflicts),0) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("expected no conflicts after rebase, got %d", conflicts)
	}
	rows, err := db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var dirty []string
	for rows.Next() {
		var tbl, status string
		var staged bool
		if err := rows.Scan(&tbl, &staged, &status); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty = append(dirty, tbl+"(staged="+boolStr(staged)+",status="+status+")")
	}
	if len(dirty) != 0 {
		t.Errorf("expected clean working set after rebase, got dirty: %v", dirty)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestEmbeddedRebaseRemoteDominates(t *testing.T) {
	te := newTestEnv(t, "rbpr")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-remote"

	peer := seedRebaseCollision(t, ctx, conn, p)

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (remote-dominates): %v", err)
	}

	// Remote (Tom/Eugene) kept .71/.72; local children renumbered to .74/.75.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Tom" {
		t.Errorf("%s.71 = %q, want remote's \"Tom\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".72"); got != "Eugene" {
		t.Errorf("%s.72 = %q, want remote's \"Eugene\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".73"); got != "Local73" {
		t.Errorf("%s.73 = %q, want kept \"Local73\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".74"); got != "Local71" {
		t.Errorf("%s.74 = %q, want renumbered \"Local71\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".75"); got != "Local72" {
		t.Errorf("%s.75 = %q, want renumbered \"Local72\"", p, got)
	}
	// FK cascade carried the label from local .71 to .74.
	if got := labelIssue(t, ctx, conn, "vip"); got != p+".74" {
		t.Errorf("vip label on %q, want %s.74 (moved with renumbered issue)", got, p)
	}
	if got := lastChildOf(t, ctx, conn, p); got != 75 {
		t.Errorf("child_counters last_child = %d, want 75", got)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}
	if report.CountersSet[p] != 75 {
		t.Errorf("report.CountersSet[%s] = %d, want 75", p, report.CountersSet[p])
	}
	if report.BackupTag == "" {
		t.Error("expected a backup tag to be recorded")
	}
	// The parent->child dependency followed the renumber: local's .71 edge rekeyed
	// to .74, remote's .71 (Tom) edge intact — neither left under a stale PK.
	assertDepRekeyed(t, ctx, conn, p+".74", p)
	assertDepRekeyed(t, ctx, conn, p+".71", p)
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseRemoteDominatesWispSubtree covers the wisp-under-durable
// shape maphew flagged on #4844: a durable (synced) root that is renumbered has
// a session-local wisp child. The renumber must carry the wisp child with the
// root — leaving it at oldRoot.* while the root moves to newRoot orphans it and
// diverges identity from hierarchy. It also re-runs the rebase to prove it
// converges (a second pass finds no collisions and renumbers nothing).
func TestEmbeddedRebaseRemoteDominatesWispSubtree(t *testing.T) {
	te := newTestEnv(t, "rbwisp")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-wisp"

	peer := seedRebaseCollision(t, ctx, conn, p)

	// A wisp child hangs under local .71, which remote-dominates will renumber
	// to .74. The wisp must follow: .71.1 -> .74.1.
	insertRebaseWisp(t, ctx, conn, p+".71.1", "WispChild")

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (remote-dominates, wisp subtree): %v", err)
	}

	// Durable root moved to .74; its wisp child moved with it.
	if got := titleOf(t, ctx, conn, p+".74"); got != "Local71" {
		t.Errorf("%s.74 = %q, want renumbered \"Local71\"", p, got)
	}
	if wispExists(t, ctx, conn, p+".71.1") {
		t.Errorf("wisp %s.71.1 still present — orphaned under the freed root id", p)
	}
	if got := wispTitleOf(t, ctx, conn, p+".74.1"); got != "WispChild" {
		t.Errorf("%s.74.1 = %q, want wisp child \"WispChild\" moved with its root", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".71"); got != "Tom" {
		t.Errorf("%s.71 = %q, want remote's \"Tom\"", p, got)
	}
	assertRebaseSettled(t, ctx, conn)

	// Convergence/idempotency: a second rebase against the now-merged peer finds
	// no collisions and renumbers nothing.
	report2, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("second rebase (convergence): %v", err)
	}
	if len(report2.Renumbered) != 0 {
		t.Errorf("second rebase renumbered %d id(s), want 0 (should converge)", len(report2.Renumbered))
	}
	_ = report
}

func TestEmbeddedRebaseLocalDominates(t *testing.T) {
	te := newTestEnv(t, "rbpl")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-local"

	peer := seedRebaseCollision(t, ctx, conn, p)

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, true)
	if err != nil {
		t.Fatalf("rebase (local-dominates): %v", err)
	}

	// Local kept .71/.72; the remote's Tom/Eugene renumbered to .74/.75.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Local71" {
		t.Errorf("%s.71 = %q, want kept \"Local71\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".72"); got != "Local72" {
		t.Errorf("%s.72 = %q, want kept \"Local72\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".74"); got != "Tom" {
		t.Errorf("%s.74 = %q, want renumbered \"Tom\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".75"); got != "Eugene" {
		t.Errorf("%s.75 = %q, want renumbered \"Eugene\"", p, got)
	}
	// Local .71 kept its label (local dominated; row untouched).
	if got := labelIssue(t, ctx, conn, "vip"); got != p+".71" {
		t.Errorf("vip label on %q, want %s.71 (local untouched)", got, p)
	}
	if got := lastChildOf(t, ctx, conn, p); got != 75 {
		t.Errorf("child_counters last_child = %d, want 75", got)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}
	// Local kept .71 (edge intact); remote's Tom moved to .74 (edge rekeyed).
	assertDepRekeyed(t, ctx, conn, p+".71", p)
	assertDepRekeyed(t, ctx, conn, p+".74", p)
	assertRebaseSettled(t, ctx, conn)
}
