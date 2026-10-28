//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests cover #4992: `bd vc merge --strategy` was dead code because the
// merge ran as a bare CALL DOLT_MERGE under autocommit, so Dolt rejected any
// real conflict (Error 1105, "@autocommit must be disabled ...") before the
// strategy could ever be applied. versioncontrolops.MergeWithStrategy runs
// on a pinned session with the conflict-tolerant flags MergeAndSettle already
// uses, so the strategy actually reaches DOLT_CONFLICTS_RESOLVE.
//
// The conflict shape mirrors TestEmbeddedMergeAndSettleReportsOperatorConflicts
// (pull_settle_test.go): both sides retitle the same issue with the SAME
// updated_at, so the LWW auto-resolver correctly declines it as ambiguous —
// exactly the class of conflict #4992 was filed about.

// seedIssueRetitleConflict seeds issue mgs-1, commits it, branches "mgspeer",
// and retitles the issue differently — "ours" on main, "theirs" on the peer —
// with the SAME updated_at on both sides so automerge's LWW resolver declines
// it. Leaves main checked out on conn's session.
func seedIssueRetitleConflict(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) (peerBranch string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'base', '', '', '', '', 'open', 2, 'task')",
		issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerBranch = issueID + "-peer"
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	if _, err := conn.ExecContext(ctx,
		"UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = ?", issueID); err != nil {
		t.Fatalf("retitle on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'retitle ours')"); err != nil {
		t.Fatalf("commit main retitle: %v", err)
	}

	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = ?", issueID); err != nil {
		t.Fatalf("retitle on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'retitle theirs')"); err != nil {
		t.Fatalf("commit peer retitle: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	return peerBranch
}

func readTitle(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) string {
	t.Helper()
	var title string
	if err := conn.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	return title
}

// TestMergeWithStrategy_TheirsResolvesAndCommits is the flagship #4992 case:
// a real modify/modify conflict on the issues table resolves and commits with
// --strategy theirs, and the merge is concluded (no live conflicts, no open
// merge state) so a subsequent pull/push is not wedged.
func TestMergeWithStrategy_TheirsResolvesAndCommits(t *testing.T) {
	te := newTestEnv(t, "mgstheirs")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	peerBranch := seedIssueRetitleConflict(t, ctx, conn, "mgs-1")

	conflicts, err := versioncontrolops.MergeWithStrategy(ctx, conn, peerBranch, "test <test@example.com>", "theirs")
	if err != nil {
		t.Fatalf("MergeWithStrategy(theirs): %v", err)
	}
	foundIssues := false
	for _, c := range conflicts {
		if c.Field == "issues" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf("returned conflicts %+v do not name the issues table", conflicts)
	}

	if title := readTitle(t, ctx, conn, "mgs-1"); title != "theirs" {
		t.Errorf("title = %q after --strategy theirs, want %q", title, "theirs")
	}

	var liveConflicts int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&liveConflicts); err != nil {
		t.Fatalf("count live conflicts: %v", err)
	}
	if liveConflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after strategy resolution", liveConflicts)
	}

	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

// TestMergeWithStrategy_OursKeepsLocal is the --strategy ours counterpart:
// the local value survives and the merge still commits (a merge that keeps
// "ours" throughout is still a real merge commit, not a no-op).
func TestMergeWithStrategy_OursKeepsLocal(t *testing.T) {
	te := newTestEnv(t, "mgsours")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	peerBranch := seedIssueRetitleConflict(t, ctx, conn, "mgs-2")

	conflicts, err := versioncontrolops.MergeWithStrategy(ctx, conn, peerBranch, "test <test@example.com>", "ours")
	if err != nil {
		t.Fatalf("MergeWithStrategy(ours): %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected the resolved conflict to be reported")
	}

	if title := readTitle(t, ctx, conn, "mgs-2"); title != "ours" {
		t.Errorf("title = %q after --strategy ours, want %q", title, "ours")
	}

	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

// TestMerge_NoStrategyStillErrorsWithEscapeHatchHint pins the "no --strategy"
// contract #4992 asked to preserve: a real conflict is still an error (Merge
// runs as a bare DOLT_MERGE under autocommit, same as plain `dolt merge`),
// but the message now names the --strategy escape hatch instead of leaving
// the operator with only Dolt's raw Error 1105 text.
func TestMerge_NoStrategyStillErrorsWithEscapeHatchHint(t *testing.T) {
	te := newTestEnv(t, "mgsnostrat")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	peerBranch := seedIssueRetitleConflict(t, ctx, conn, "mgs-3")

	_, err := versioncontrolops.Merge(ctx, conn, peerBranch, "test <test@example.com>")
	if err == nil {
		t.Fatal("expected the unresolved conflict to error")
	}
	if !strings.Contains(err.Error(), "--strategy") {
		t.Errorf("error = %v, want it to mention the --strategy escape hatch", err)
	}

	// No trace of a half-applied merge: the autocommit rejection leaves the
	// working set exactly as it was.
	if title := readTitle(t, ctx, conn, "mgs-3"); title != "ours" {
		t.Errorf("title = %q after a rejected merge, want unchanged local value %q", title, "ours")
	}
}

// TestMergeAndSettleWithStrategy_ResolvesDeclinedConflict is the #4992 part 2
// case: bd dolt pull's merge machinery (MergeAndSettle) declines the same
// conflict TryAutoResolveMergeConflicts always declines for issues with
// ambiguous LWW timestamps, but MergeAndSettleWithStrategy resolves it with
// the operator's escape-hatch strategy instead of aborting for manual
// resolution.
func TestMergeAndSettleWithStrategy_ResolvesDeclinedConflict(t *testing.T) {
	te := newTestEnv(t, "mgspull")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	peerBranch := seedIssueRetitleConflict(t, ctx, conn, "mgs-4")

	if err := versioncontrolops.MergeAndSettleWithStrategy(ctx, conn, peerBranch, "theirs"); err != nil {
		t.Fatalf("MergeAndSettleWithStrategy(theirs): %v", err)
	}

	if title := readTitle(t, ctx, conn, "mgs-4"); title != "theirs" {
		t.Errorf("title = %q after pull --strategy theirs, want %q", title, "theirs")
	}

	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

// TestMergeAndSettleWithStrategy_EmptyStrategyMatchesMergeAndSettle proves
// MergeAndSettleWithStrategy(strategy="") is exactly MergeAndSettle: the
// declined conflict still aborts with MergeConflictsError instead of being
// silently resolved.
func TestMergeAndSettleWithStrategy_EmptyStrategyMatchesMergeAndSettle(t *testing.T) {
	te := newTestEnv(t, "mgspullnostrat")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	peerBranch := seedIssueRetitleConflict(t, ctx, conn, "mgs-5")

	err := versioncontrolops.MergeAndSettleWithStrategy(ctx, conn, peerBranch, "")
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("want MergeConflictsError, got: %v", err)
	}

	if title := readTitle(t, ctx, conn, "mgs-5"); title != "ours" {
		t.Errorf("title = %q after aborted pull, want unchanged local value %q", title, "ours")
	}
}

// seedRetitleWithLabelSwap extends seedIssueRetitleConflict with the GH#7811
// wrinkle: the peer branch also swaps the issue's label needs-triage →
// ready-for-agent. The label edit lives in a separate table that merges
// cleanly while the issues retitle conflicts (same updated_at on both sides,
// so the LWW resolver declines it), so any conclusion that commits only the
// resolved table records HEAD's stale copy of labels: the merge commit —
// what `bd dolt push` sends — reverts the peer's change while the working
// set keeps reading the merged value forever. Leaves main checked out.
func seedRetitleWithLabelSwap(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) (peerBranch string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'base', '', '', '', '', 'open', 2, 'task')",
		issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, 'needs-triage')", issueID); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue + label')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerBranch = issueID + "-peer"
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	if _, err := conn.ExecContext(ctx,
		"UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = ?", issueID); err != nil {
		t.Fatalf("retitle on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'retitle ours')"); err != nil {
		t.Fatalf("commit main retitle: %v", err)
	}

	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = ?", issueID); err != nil {
		t.Fatalf("retitle on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"DELETE FROM labels WHERE issue_id = ? AND label = 'needs-triage'", issueID); err != nil {
		t.Fatalf("remove peer label: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, 'ready-for-agent')", issueID); err != nil {
		t.Fatalf("add peer label: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'relabel + retitle theirs')"); err != nil {
		t.Fatalf("commit peer diverge: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	return peerBranch
}

// headLabels reads an issue's labels from the committed HEAD root, not the
// working set: at base the working set holds the merged value HEAD lacks
// (GH#7811's second symptom), so only a HEAD read can see the revert.
func headLabels(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT label FROM labels AS OF 'HEAD' WHERE issue_id = ? ORDER BY label", issueID)
	if err != nil {
		t.Fatalf("query HEAD labels: %v", err)
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan HEAD label: %v", err)
		}
		labels = append(labels, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate HEAD labels: %v", err)
	}
	return labels
}

// mergeOpen reports whether a merge is still open (dolt_merge_status.is_merging).
func mergeOpen(t *testing.T, ctx context.Context, conn *sql.Conn) bool {
	t.Helper()
	var merging bool
	err := conn.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	return merging
}

// assertMergeConcluded asserts the three post-settle invariants GH#7811 needs
// once the merge commit itself is right: nothing left uncommitted, the merge
// state closed, and HEAD a two-parent merge commit (a one-parent "conclusion"
// would mean the tables the merge produced were never committed).
func assertMergeConcluded(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d table(s) left uncommitted after the merge was concluded", dirty)
	}
	if mergeOpen(t, ctx, conn) {
		t.Error("merge left open (dolt_merge_status.is_merging = 1) after settlement")
	}
	var parents int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')").Scan(&parents); err != nil {
		t.Fatalf("count HEAD parents: %v", err)
	}
	if parents != 2 {
		t.Errorf("HEAD (a concluded merge) has %d parent(s), want 2", parents)
	}
}

// TestMergeAndSettleWithStrategy_KeepsCleanMergedTables (bd-cdo628, GH#7811):
// a pull whose only conflict is an issues cell the LWW resolver declines,
// settled with --strategy ours/theirs, must carry the peer's cleanly merged
// label swap into the merge commit — not just the working set.
func TestMergeAndSettleWithStrategy_KeepsCleanMergedTables(t *testing.T) {
	for _, strategy := range []string{"ours", "theirs"} {
		t.Run(strategy, func(t *testing.T) {
			te := newTestEnv(t, "mergekept"+strategy)
			ctx := t.Context()
			conn := openSettleConn(t, ctx, te)

			peerBranch := seedRetitleWithLabelSwap(t, ctx, conn, "mk-1")

			if err := versioncontrolops.MergeAndSettleWithStrategy(ctx, conn, peerBranch, strategy); err != nil {
				t.Fatalf("MergeAndSettleWithStrategy(%s): %v", strategy, err)
			}

			labels := headLabels(t, ctx, conn, "mk-1")
			if len(labels) != 1 || labels[0] != "ready-for-agent" {
				t.Errorf("HEAD labels = %v, want [ready-for-agent]: the merge commit did not carry the peer's label swap", labels)
			}
			var title string
			if err := conn.QueryRowContext(ctx,
				"SELECT title FROM issues AS OF 'HEAD' WHERE id = 'mk-1'").Scan(&title); err != nil {
				t.Fatalf("read HEAD title: %v", err)
			}
			if title != strategy {
				t.Errorf("HEAD title = %q after --strategy %s, want %q", title, strategy, strategy)
			}
			assertMergeConcluded(t, ctx, conn)
		})
	}
}
