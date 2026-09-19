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
