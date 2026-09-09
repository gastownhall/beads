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
	// A successful rebase never rolled back, so a caller must not be told it did.
	if report.Restored {
		t.Error("report.Restored is true after a successful rebase")
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

	// Idempotency: a second rebase against the now-merged peer is a safe no-op. This
	// is a weak invariant by construction — after the first merge, DOLT_MERGE_BASE
	// ('HEAD', peer) is peer's own head, so HEAD∩peer∖base is empty and no collision
	// can be detected — but it still pins that a re-run neither errors nor mutates.
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

// TestEmbeddedRebaseRemoteDominatesHighWaterSlots pins both #4844 round-3
// blockers: the renumberer must seed free slots from a TRUE high-water mark, not
// just the live issue rows. Under parent P the local side has consumed slots past
// its live issues max in two ways an issues-only scan misses:
//   - a DELETED child (.73): the live max drops to .72 but child_counters stays at
//     73 — a peer may still hold .73, so the slot must not be re-minted;
//   - a session WISP child (.74): it holds slot 74 in the wisps table, invisible to
//     an issues scan, so a durable renumber onto .74 collides across tables with no
//     PK conflict to catch it.
//
// So the true high-water is 74. Remote-dominates renumbers the local .71/.72
// collisions; with the fix they land at .75/.76 (clear of the deleted .73 and the
// wisp .74) and the counter never falls below the wisp high-water. Without the fix
// they land on .73/.74 — reusing the deleted slot and colliding with the wisp.
func TestEmbeddedRebaseRemoteDominatesHighWaterSlots(t *testing.T) {
	te := newTestEnv(t, "rbhw")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-hw"

	peer := seedRebaseCollision(t, ctx, conn, p)

	// Local deletes its highest live child .73 (counter stays 73) and commits.
	if _, err := conn.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", p+".73"); err != nil {
		t.Fatalf("delete local .73: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'local deletes highest child .73, counter stays 73')"); err != nil {
		t.Fatalf("commit delete: %v", err)
	}
	// A session wisp holds slot .74 — working-set only (wisps are dolt-ignored, so
	// never committed). This is a SYNTHETIC state: a wisp minted through the real
	// path (GetNextChildIDTx under a durable parent) would bump child_counters to
	// 74, and the counter read alone would then catch the slot. The counter is
	// deliberately left at 73 here so the wisp slot is visible ONLY via the direct
	// wisps-table scan — that is the cross-table guard this case exercises. True
	// high-water under P is 74 (counter 73, wisp 74), above the live issues max 72.
	insertRebaseWisp(t, ctx, conn, p+".74", "WispSibling")

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (remote-dominates, high-water slots): %v", err)
	}

	// Remote's Tom/Eugene keep the contested .71/.72.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Tom" {
		t.Errorf("%s.71 = %q, want remote's \"Tom\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".72"); got != "Eugene" {
		t.Errorf("%s.72 = %q, want remote's \"Eugene\"", p, got)
	}
	// The deleted slot .73 must not be re-minted as an issue (counter reserved it).
	var at73 int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", p+".73").Scan(&at73); err != nil {
		t.Fatalf("count issue .73: %v", err)
	}
	if at73 != 0 {
		t.Errorf("%s.73 re-minted as an issue (%d rows) — the deleted slot's counter high-water was ignored", p, at73)
	}
	// The wisp still holds .74, and no durable issue was minted over its slot.
	if !wispExists(t, ctx, conn, p+".74") {
		t.Errorf("wisp %s.74 lost", p)
	}
	var issueAt74 int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", p+".74").Scan(&issueAt74); err != nil {
		t.Fatalf("count issue .74: %v", err)
	}
	if issueAt74 != 0 {
		t.Errorf("durable issue minted at %s.74 over the session wisp (cross-table collision)", p)
	}
	// Both local children renumbered above the true high-water.
	if got := titleOf(t, ctx, conn, p+".75"); got != "Local71" {
		t.Errorf("%s.75 = %q, want renumbered \"Local71\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".76"); got != "Local72" {
		t.Errorf("%s.76 = %q, want renumbered \"Local72\"", p, got)
	}
	// Counter sits at the new high-water and never fell below the wisp slot (74).
	if got := lastChildOf(t, ctx, conn, p); got != 76 {
		t.Errorf("child_counters last_child = %d, want 76 (high-water above deleted .73 and wisp .74)", got)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}
	// The renumbered local .71's dependency edge rekeyed to its new id .75.
	assertDepRekeyed(t, ctx, conn, p+".75", p)
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseRemoteDominatesOrphanedCounterConflict pins the guard maphew
// added in bd50374bf and explicitly left untested: a dolt_conflicts_child_counters
// row whose parent this merge leaves deleted. The rebase settle walks every
// conflicted counter row, not just the parents in the collision that triggered it,
// so a second, unrelated parent Q rides along:
//   - base has parent Q with counter=5;
//   - our side (main) deletes Q — child_counters.parent_id -> issues(id) ON DELETE
//     CASCADE (verified present on the embedded engine) drops our Q counter with it;
//   - their side (peer) bumps Q's counter to 6, Q issue untouched;
//   - the merge cleanly deletes Q (theirs unchanged) but leaves a theirs-only
//     conflict on child_counters(Q): ours absent, theirs=6.
//
// resolveChildCountersToHighWater must SKIP that row — re-inserting child_counters(Q)
// would violate fk_counter_parent (Q is gone) and hard-reset the entire rebase to
// the backup tag. The guard skips it and DOLT_CONFLICTS_RESOLVE('--ours') keeps our
// deletion. The P.71/.72 collision that triggers the rebase reconciles as normal.
func TestEmbeddedRebaseRemoteDominatesOrphanedCounterConflict(t *testing.T) {
	te := newTestEnv(t, "rborph")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-orph"
	q := "rbq-orph"

	// Base: the P collision scaffold plus an independent parent Q with counter=5.
	insertRebaseIssue(t, ctx, conn, p, "Parent")
	insertRebaseIssue(t, ctx, conn, p+".70", "Child70")
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p); err != nil {
		t.Fatalf("seed P counter: %v", err)
	}
	insertRebaseIssue(t, ctx, conn, q, "OrphanParent")
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 5)", q); err != nil {
		t.Fatalf("seed Q counter: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed base with P and Q')"); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	peer := "rebasepeer_" + p
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peer); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	// Clone A (main): the P.71/.72/.73 collision, and DELETE Q (cascades its counter).
	insertRebaseIssue(t, ctx, conn, p+".71", "Local71")
	insertRebaseIssue(t, ctx, conn, p+".72", "Local72")
	insertRebaseIssue(t, ctx, conn, p+".73", "Local73")
	if _, err := conn.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", p); err != nil {
		t.Fatalf("bump P counter on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", q); err != nil {
		t.Fatalf("delete Q on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone A: P collision + delete Q')"); err != nil {
		t.Fatalf("commit clone A: %v", err)
	}

	// Clone B (peer): P.71/.72 collision, and bump Q's counter (Q issue untouched).
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peer); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	insertRebaseIssue(t, ctx, conn, p+".71", "Tom")
	insertRebaseIssue(t, ctx, conn, p+".72", "Eugene")
	if _, err := conn.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 6 WHERE parent_id = ?", q); err != nil {
		t.Fatalf("bump Q counter on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", p); err != nil {
		t.Fatalf("bump P counter on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone B: P collision + bump Q counter')"); err != nil {
		t.Fatalf("commit clone B: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// Without the orphan-skip guard this returns an FK error and hard-resets to the
	// backup tag; with it, the rebase completes.
	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (remote-dominates, orphaned counter conflict): %v", err)
	}

	// Q stayed deleted and no counter row was resurrected for it (our deletion kept).
	var qIssues int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", q).Scan(&qIssues); err != nil {
		t.Fatalf("count Q issue: %v", err)
	}
	if qIssues != 0 {
		t.Errorf("orphan parent %s re-appeared in issues (%d rows)", q, qIssues)
	}
	var qCounters int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", q).Scan(&qCounters); err != nil {
		t.Fatalf("count Q counter: %v", err)
	}
	if qCounters != 0 {
		t.Errorf("orphan counter row for deleted parent %s was resurrected (%d rows) — FK guard skipped?", q, qCounters)
	}

	// The P collision still reconciled normally alongside the skipped Q row.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Tom" {
		t.Errorf("%s.71 = %q, want remote's \"Tom\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".74"); got != "Local71" {
		t.Errorf("%s.74 = %q, want renumbered \"Local71\"", p, got)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseRemoteDominatesDurableSubtreeCounter pins the durable
// subtree-parent case: a colliding child that is ITSELF a parent with its own
// child_counters row. Renaming local .71 -> .74 must carry both the durable
// grandchild .71.1 -> .74.1 AND its counter row (parent_id .71 -> .74), or the
// high-water reservation for the renamed subtree is lost and a later delete under
// .74 could re-mint a slot a peer holds — the #4796 hazard one level down. The
// rename routes through UpdateIssueIDInTx (a plain UPDATE of issues.id); the counter
// follows because fk_counter_parent cascades the id change. This asserts that
// behaviour holds rather than leaving the durable subtree-parent path uncovered
// (the pre-existing wisp-subtree test only moves a wisp grandchild, never a counter).
func TestEmbeddedRebaseRemoteDominatesDurableSubtreeCounter(t *testing.T) {
	te := newTestEnv(t, "rbsub")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-sub"

	peer := seedRebaseCollision(t, ctx, conn, p)

	// Local .71 (which remote-dominates renumbers to .74) is itself a parent: a
	// durable child .71.1 and a child_counters row keyed on .71.
	insertRebaseIssue(t, ctx, conn, p+".71.1", "GrandChild")
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 1)", p+".71"); err != nil {
		t.Fatalf("insert .71 counter: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'local: durable grandchild + counter under .71')"); err != nil {
		t.Fatalf("commit grandchild: %v", err)
	}

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (remote-dominates, durable subtree counter): %v", err)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}

	// The durable grandchild moved with its root .71 -> .74; nothing stranded at .71.1.
	if got := titleOf(t, ctx, conn, p+".74.1"); got != "GrandChild" {
		t.Errorf("%s.74.1 = %q, want grandchild moved with its renumbered root", p, got)
	}
	var stranded int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", p+".71.1").Scan(&stranded); err != nil {
		t.Fatalf("count stranded grandchild: %v", err)
	}
	if stranded != 0 {
		t.Errorf("grandchild stranded at %s.71.1 (%d rows) — subtree not carried with the renumbered root", p, stranded)
	}

	// The subtree-internal counter followed the rename: no orphan left at .71, a
	// live counter now keyed on .74. This is what preserves the deleted-child
	// high-water reservation for the renumbered subtree.
	var orphanCounter int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", p+".71").Scan(&orphanCounter); err != nil {
		t.Fatalf("count orphan .71 counter: %v", err)
	}
	if orphanCounter != 0 {
		t.Errorf("child_counters left orphaned at %s.71 (%d rows) after the root was renumbered to .74", p, orphanCounter)
	}
	if got := lastChildOf(t, ctx, conn, p+".74"); got != 1 {
		t.Errorf("child_counters for renumbered root %s.74 = %d, want 1 (migrated from .71)", p, got)
	}
	assertRebaseSettled(t, ctx, conn)
}

// insertRebaseIssueWithHash inserts a child with an explicit content_hash and
// pinned timestamps, so two clones can be made to add byte-identical rows. The
// real create path computes content_hash from the substantive fields
// (issueops/create.go), so equal content yields an equal hash across clones;
// pinning it here states that relationship directly instead of depending on the
// hash function's current field set.
func insertRebaseIssueWithHash(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id, title, hash string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, content_hash, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) "+
			"VALUES (?, ?, ?, '', '', '', '', 'open', 2, 'task', '2026-01-01 00:00:00', '2026-01-01 00:00:00')",
		id, hash, title); err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
}

func issueCount(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count issues at %s: %v", id, err)
	}
	return n
}

// seedIdenticalAddAdd builds a divergence where both clones added the SAME id
// twice over: .71 with identical content on both sides (one issue that reached
// two clones out of band — a JSONL import, a restored backup), and .72 with
// genuinely different content (a real collision). Returns the peer branch with
// main checked out.
func seedIdenticalAddAdd(t *testing.T, ctx context.Context, db versioncontrolops.DBConn, p string) (peerBranch string) {
	t.Helper()

	insertRebaseIssue(t, ctx, db, p, "Parent")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed base')"); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	peerBranch = "identpeer_" + p
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	insertRebaseIssueWithHash(t, ctx, db, p+".71", "SharedIssue", "hash-shared")
	insertRebaseIssueWithHash(t, ctx, db, p+".72", "Local72", "hash-local-72")
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone A adds .71 (shared) and .72')"); err != nil {
		t.Fatalf("commit clone A: %v", err)
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	insertRebaseIssueWithHash(t, ctx, db, p+".71", "SharedIssue", "hash-shared")
	insertRebaseIssueWithHash(t, ctx, db, p+".72", "Eugene", "hash-remote-72")
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone B adds .71 (shared) and .72')"); err != nil {
		t.Fatalf("commit clone B: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	return peerBranch
}

// TestEmbeddedRebaseIdenticalAddAddIsNotRenumbered pins the content-equality
// guard maphew asked for on #4844: an id both clones added with the SAME content
// is ONE issue that reached both of them out of band, not two issues contesting a
// slot. Renumbering it forks it in two, irreversibly — no later pass can tell the
// halves were ever the same row. Only the genuinely-different .72 may move.
//
// Without the guard this renumbers BOTH ids and .71 ends up duplicated across
// .71 and .73.
func TestEmbeddedRebaseIdenticalAddAddIsNotRenumbered(t *testing.T) {
	te := newTestEnv(t, "rbident")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-ident"

	peer := seedIdenticalAddAdd(t, ctx, conn, p)

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (identical add/add): %v", err)
	}

	if len(report.Renumbered) != 1 {
		t.Fatalf("report.Renumbered has %d entries, want 1 (only the genuinely-different .72)", len(report.Renumbered))
	}
	if got := report.Renumbered[0].OldID; got != p+".72" {
		t.Errorf("renumbered %q, want %s.72 — the identical .71 must be left to the merge", got, p)
	}
	// The shared issue stayed put and stayed single.
	if got := titleOf(t, ctx, conn, p+".71"); got != "SharedIssue" {
		t.Errorf("%s.71 = %q, want the shared issue left in place", p, got)
	}
	if n := issueCount(t, ctx, conn, p+".71"); n != 1 {
		t.Errorf("%s.71 has %d rows, want 1", p, n)
	}
	// The real collision resolved the ordinary way: remote keeps .72, local moves.
	if got := titleOf(t, ctx, conn, p+".72"); got != "Eugene" {
		t.Errorf("%s.72 = %q, want remote's \"Eugene\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".73"); got != "Local72" {
		t.Errorf("%s.73 = %q, want renumbered \"Local72\"", p, got)
	}
	// Nothing forked the shared issue into a second slot alongside the renumber.
	if n := issueCount(t, ctx, conn, p+".74"); n != 0 {
		t.Errorf("%s.74 exists — the identical .71 was forked into a second row", p)
	}
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseCollidingChildUnderIdenticalParent pins the ORDERING of the
// content-equality guard against the collision-root filter. detectCollisions
// suppresses a colliding id whose ancestor also collides, because the ancestor's
// subtree rewrite will carry it. If the identical add/adds are filtered out AFTER
// that root pass, a genuine collision sitting under an identical parent is
// suppressed by an ancestor that is then never renumbered — and the collision is
// dropped on the floor, exactly the failure `bd dolt rebase` exists to prevent.
//
// Here .71 is identical on both sides and its child .71.1 is not. .71 must stay,
// and .71.1 must be renumbered in its own right.
func TestEmbeddedRebaseCollidingChildUnderIdenticalParent(t *testing.T) {
	te := newTestEnv(t, "rbidentkid")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-identkid"

	insertRebaseIssue(t, ctx, conn, p, "Parent")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed base')"); err != nil {
		t.Fatalf("commit base: %v", err)
	}
	peer := "identkidpeer_" + p
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peer); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	insertRebaseIssueWithHash(t, ctx, conn, p+".71", "SharedParent", "hash-shared-parent")
	insertRebaseIssueWithHash(t, ctx, conn, p+".71.1", "LocalChild", "hash-local-child")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone A')"); err != nil {
		t.Fatalf("commit clone A: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peer); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	insertRebaseIssueWithHash(t, ctx, conn, p+".71", "SharedParent", "hash-shared-parent")
	insertRebaseIssueWithHash(t, ctx, conn, p+".71.1", "RemoteChild", "hash-remote-child")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'clone B')"); err != nil {
		t.Fatalf("commit clone B: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err != nil {
		t.Fatalf("rebase (colliding child under identical parent): %v", err)
	}

	if len(report.Renumbered) != 1 {
		t.Fatalf("report.Renumbered has %d entries, want 1 (the child, not its identical parent)", len(report.Renumbered))
	}
	if got := report.Renumbered[0].OldID; got != p+".71.1" {
		t.Errorf("renumbered %q, want %s.71.1", got, p)
	}
	if got := titleOf(t, ctx, conn, p+".71"); got != "SharedParent" {
		t.Errorf("%s.71 = %q, want the identical parent left in place", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".71.1"); got != "RemoteChild" {
		t.Errorf("%s.71.1 = %q, want remote's child to keep the contested id", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".71.2"); got != "LocalChild" {
		t.Errorf("%s.71.2 = %q, want the local child renumbered clear", p, got)
	}
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseLocalDominatesSwapTempVacancy pins the swap's scratch-id
// allocation. The local-dominates swap parks the remote's subtree on a temporary
// id, and the old fixed "parent.1000000+i" was only checked for being improbable,
// never for being FREE. A pre-existing subtree under that prefix is then captured
// by the final rename — because renameSubtreeInTx moves "root" AND everything
// under "root." — silently re-parenting unrelated issues.
//
// The bystander here occupies parent.1000000.1 without parent.1000000 itself
// existing, which is the case a bare row-existence probe misses: the scratch id
// looks free while its subtree is not. With the vacancy check the swap steps past
// the occupied prefix; without it the bystander is dragged to the remote's new id.
func TestEmbeddedRebaseLocalDominatesSwapTempVacancy(t *testing.T) {
	te := newTestEnv(t, "rbswap")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-swap"

	peer := seedRebaseCollision(t, ctx, conn, p)

	// An unrelated issue sitting inside the naive scratch prefix's subtree. It is a
	// deep id (parent.1000000.1), so it does NOT raise the parent's direct-child
	// high-water and cannot influence which slots the renumber picks.
	bystander := p + ".1000000.1"
	insertRebaseIssue(t, ctx, conn, bystander, "Bystander")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'unrelated issue inside the scratch prefix')"); err != nil {
		t.Fatalf("commit bystander: %v", err)
	}

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, true)
	if err != nil {
		t.Fatalf("rebase (local-dominates, occupied scratch prefix): %v", err)
	}

	// The bystander is exactly where it was, under its own id.
	if got := titleOf(t, ctx, conn, bystander); got != "Bystander" {
		t.Errorf("%s = %q, want \"Bystander\" untouched by the swap", bystander, got)
	}
	if n := issueCount(t, ctx, conn, p+".74.1"); n != 0 {
		t.Errorf("%s.74.1 exists — the swap captured the bystander subtree via its scratch id", p)
	}
	// And the swap itself still did its job.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Local71" {
		t.Errorf("%s.71 = %q, want kept \"Local71\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".74"); got != "Tom" {
		t.Errorf("%s.74 = %q, want renumbered \"Tom\"", p, got)
	}
	if got := titleOf(t, ctx, conn, p+".75"); got != "Eugene" {
		t.Errorf("%s.75 = %q, want renumbered \"Eugene\"", p, got)
	}
	if len(report.Renumbered) != 2 {
		t.Errorf("report.Renumbered has %d entries, want 2", len(report.Renumbered))
	}
	// No scratch id survived the swap as a live issue.
	var leftovers int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id LIKE CONCAT(?, '.1000000') OR id LIKE CONCAT(?, '.1000001')",
		p, p).Scan(&leftovers); err != nil {
		t.Fatalf("count leftover scratch ids: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("%d scratch id(s) left behind as live issues", leftovers)
	}
	assertRebaseSettled(t, ctx, conn)
}

// TestEmbeddedRebaseRestoredFlagOnFailure pins the signal that tells a caller
// whether a failed rebase rolled back. The reconcile is atomic — on its own
// failure it hard-resets to the backup tag — but a failure AFTER it returns (the
// store wrapper's is_blocked recompute) leaves the merge committed and standing.
// The CLI printed "the database was restored... no changes were kept" for both,
// so the second case sent the operator hunting for changes that were in fact
// live. RebaseReport.Restored separates them.
//
// The failure here is a genuinely unresolvable conflict on a non-kv.memory config
// key, which the settle auto-resolver declines by design, so the merge aborts and
// the restore runs for real.
func TestEmbeddedRebaseRestoredFlagOnFailure(t *testing.T) {
	te := newTestEnv(t, "rbrestore")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	p := "rbp-restore"

	peer := seedRebaseCollision(t, ctx, conn, p)

	// Diverge a config key the auto-resolver will not touch (only kv.memory.* is
	// convergent), so the settle aborts after the renumber has already committed.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peer); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.wedge', 'theirs')"); err != nil {
		t.Fatalf("insert peer config: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer sets a non-memory config key')"); err != nil {
		t.Fatalf("commit peer config: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('kv.custom.wedge', 'ours')"); err != nil {
		t.Fatalf("insert local config: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local sets the same config key differently')"); err != nil {
		t.Fatalf("commit local config: %v", err)
	}

	report, err := versioncontrolops.RebaseChildCollisions(ctx, conn, peer, false)
	if err == nil {
		t.Fatal("expected the rebase to fail on an unresolvable config conflict")
	}
	if report == nil {
		t.Fatal("expected a report alongside the error so the caller can read BackupTag/Restored")
	}
	if !report.Restored {
		t.Error("report.Restored is false after a rolled-back rebase — the caller cannot tell a rollback from a committed rebase whose follow-up failed")
	}
	if report.BackupTag == "" {
		t.Error("expected the backup tag to be recorded on the restored report")
	}

	// Restored means restored: the pre-rebase state is back, byte for byte.
	if got := titleOf(t, ctx, conn, p+".71"); got != "Local71" {
		t.Errorf("%s.71 = %q, want local's pre-rebase \"Local71\"", p, got)
	}
	if n := issueCount(t, ctx, conn, p+".74"); n != 0 {
		t.Errorf("%s.74 exists — the renumber survived a supposedly-restored rebase", p)
	}
	if got := lastChildOf(t, ctx, conn, p); got != 73 {
		t.Errorf("child_counters last_child = %d, want the pre-rebase 73", got)
	}
}
