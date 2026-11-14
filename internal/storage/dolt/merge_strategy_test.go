package dolt

import (
	"strings"
	"testing"
)

// These tests cover #4992 for server-mode DoltStore: `bd vc merge --strategy`
// was dead code because Merge ran as a bare CALL DOLT_MERGE under autocommit,
// so Dolt rejected any real conflict (Error 1105, "@autocommit must be
// disabled ...") before the strategy could ever be applied.
// MergeWithStrategy pins a single connection and runs
// versioncontrolops.MergeWithStrategy on it, so the strategy actually reaches
// DOLT_CONFLICTS_RESOLVE. The conflict shape (equal updated_at on both sides)
// is the same ambiguous-LWW class TestTryAutoResolveMergeConflicts_IssuesLWW*
// exercises for the auto-resolve path, chosen so the resolver declines it and
// only the explicit strategy resolves it.

func TestMergeWithStrategy_TheirsResolvesAndCommits(t *testing.T) {
	const issueID = "mgs-svr-theirs"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	conflicts, err := store.MergeWithStrategy(ctx, peerBranch, "theirs")
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

	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "theirs" {
		t.Errorf("title = %q after --strategy theirs, want %q", title, "theirs")
	}

	var dirty int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

func TestMergeWithStrategy_OursKeepsLocal(t *testing.T) {
	const issueID = "mgs-svr-ours"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	conflicts, err := store.MergeWithStrategy(ctx, peerBranch, "ours")
	if err != nil {
		t.Fatalf("MergeWithStrategy(ours): %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected the resolved conflict to be reported")
	}

	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "ours" {
		t.Errorf("title = %q after --strategy ours, want %q", title, "ours")
	}

	var dirty int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

// TestMerge_NoStrategyStillErrorsWithEscapeHatchHint pins the "no --strategy"
// contract #4992 asked to preserve for server-mode Merge: a real conflict is
// still an error, but the message now names the --strategy escape hatch.
func TestMerge_NoStrategyStillErrorsWithEscapeHatchHint(t *testing.T) {
	const issueID = "mgs-svr-nostrat"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	_, err := store.Merge(ctx, peerBranch)
	if err == nil {
		t.Fatal("expected the unresolved conflict to error")
	}
	if !strings.Contains(err.Error(), "--strategy") {
		t.Errorf("error = %v, want it to mention the --strategy escape hatch", err)
	}
}

// TestMergeWithStrategy_ResetsSessionFlags pins the release contract of
// DoltStore.MergeWithStrategy's pinned connection (bd-sesknw / GH#5564): the
// conflict-tolerance flags versioncontrolops.MergeWithStrategy sets
// (@@dolt_allow_commit_conflicts, @@dolt_force_transaction_commit) are session
// state, so the conn must reset them before it returns to the pool — on
// success and on failure alike. The test store's pool holds exactly one
// connection (MaxOpenConns 1, and the default idle count keeps it), so the
// next borrow through store.db sees whatever the merge session left behind.
func TestMergeWithStrategy_ResetsSessionFlags(t *testing.T) {
	const issueID = "mgs-svr-flags"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	readFlags := func() (allow, force bool) {
		t.Helper()
		var a, f int
		if err := store.db.QueryRowContext(ctx,
			"SELECT @@dolt_allow_commit_conflicts, @@dolt_force_transaction_commit").Scan(&a, &f); err != nil {
			t.Fatalf("read session flags: %v", err)
		}
		return a == 1, f == 1
	}

	// A successful strategy merge must not leave the flags armed.
	if _, err := store.MergeWithStrategy(ctx, peerBranch, "theirs"); err != nil {
		t.Fatalf("MergeWithStrategy(theirs): %v", err)
	}
	if allow, force := readFlags(); allow || force {
		t.Errorf("after a successful merge, flags = (allow_commit_conflicts=%v, force_transaction_commit=%v); the pinned conn leaked them into the pool", allow, force)
	}

	// Neither may a failed one: the flags were set before the merge statement
	// ran, so the release path must run on error returns too.
	if _, err := store.MergeWithStrategy(ctx, "no-such-peer-branch", "theirs"); err == nil {
		t.Fatal("merging a nonexistent branch should error")
	}
	if allow, force := readFlags(); allow || force {
		t.Errorf("after a failed merge, flags = (allow_commit_conflicts=%v, force_transaction_commit=%v); the pinned conn leaked them into the pool", allow, force)
	}
}
