package dolt

import (
	"strings"
	"testing"
)

func setupIssueMergeConflict(t *testing.T, issueID, baseTitle, baseUpdatedAt, ourTitle, ourUpdatedAt, theirTitle, theirUpdatedAt string, seed bool) (*DoltStore, string) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	db := store.db
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	insertIssue := func(title, updatedAt string) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, updated_at) VALUES (?, ?, '', '', '', '', 'open', 2, 'task', ?)",
			issueID, title, updatedAt); err != nil {
			t.Fatalf("insert issue %s: %v", issueID, err)
		}
	}
	updateIssue := func(title, updatedAt string) {
		if _, err := db.ExecContext(ctx,
			"UPDATE issues SET title = ?, updated_at = ? WHERE id = ?",
			title, updatedAt, issueID); err != nil {
			t.Fatalf("update issue %s: %v", issueID, err)
		}
	}

	if seed {
		insertIssue(baseTitle, baseUpdatedAt)
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
			t.Fatalf("commit seed issue: %v", err)
		}
		updateIssue(ourTitle, ourUpdatedAt)
	} else {
		insertIssue(ourTitle, ourUpdatedAt)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'issue on current')"); err != nil {
		t.Fatalf("commit issue on current: %v", err)
	}

	peerBranch := currentBranch + "_issues_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer branch: %v", err)
	}
	if seed {
		updateIssue(theirTitle, theirUpdatedAt)
	} else {
		insertIssue(theirTitle, theirUpdatedAt)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'issue on peer')"); err != nil {
		t.Fatalf("commit issue on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}

	return store, peerBranch
}

func mergeAndTryAutoResolveIssues(t *testing.T, store *DoltStore, peerBranch string) bool {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("resolver error: %v", err)
	}
	if resolved {
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit after resolve: %v", err)
		}
	} else {
		_ = tx.Rollback()
	}
	return resolved
}

func TestTryAutoResolveMergeConflicts_IssuesLWWTheirsWins(t *testing.T) {
	const issueID = "lww-x"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 11:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 12:00:00", true)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); !resolved {
		t.Fatal("expected issues modify/modify conflict to be auto-resolved")
	}

	ctx, cancel := testContext(t)
	defer cancel()
	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "theirs" {
		t.Errorf("expected peer title %q, got %q", "theirs", title)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&count); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 issue row after auto-resolve, got %d", count)
	}
}

func TestTryAutoResolveMergeConflicts_IssuesLWWOursWins(t *testing.T) {
	const issueID = "lww-y"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 11:00:00",
		"ours", "2026-07-10 13:00:00", "theirs", "2026-07-10 11:00:00", true)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); !resolved {
		t.Fatal("expected issues modify/modify conflict to be auto-resolved")
	}

	ctx, cancel := testContext(t)
	defer cancel()
	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "ours" {
		t.Errorf("expected current title %q, got %q", "ours", title)
	}
}

func TestTryAutoResolveMergeConflicts_IssuesAmbiguousLeftAlone(t *testing.T) {
	store, peerBranch := setupIssueMergeConflict(t, "lww-z",
		"seed", "2026-07-10 11:00:00",
		"ours", "2026-07-10 12:00:00", "theirs", "2026-07-10 12:00:00", true)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); resolved {
		t.Error("expected equal-timestamp issues conflict to be left unresolved")
	}
}

func TestTryAutoResolveMergeConflicts_IssuesAddAddLeftAlone(t *testing.T) {
	store, peerBranch := setupIssueMergeConflict(t, "lww-add",
		"", "",
		"ours", "2026-07-10 12:00:00", "theirs", "2026-07-10 12:00:00", false)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); resolved {
		t.Error("expected issues add/add conflict to be left unresolved")
	}
}

// TestTryAutoResolveMergeConflicts_IssuesConcurrentContestedCellLWW covers the
// one genuine conflict class the field-level merge still has to arbitrate:
// both sides moved updated_at past the merge base AND changed the SAME cell to
// different values. The later writer wins that cell (the ask's LWW rule).
func TestTryAutoResolveMergeConflicts_IssuesConcurrentContestedCellLWW(t *testing.T) {
	const issueID = "lww-concurrent"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 12:00:00", true)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); !resolved {
		t.Fatal("expected a contested cell with distinct timestamps to be auto-resolved by LWW")
	}

	ctx, cancel := testContext(t)
	defer cancel()
	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "theirs" {
		t.Errorf("expected the later writer's title %q, got %q", "theirs", title)
	}
}

// TestTryAutoResolveMergeConflicts_IssuesDisjointFieldsMerge is the flagship
// case (federation ask #1): both sides edited the same issue since the merge
// base but touched DIFFERENT fields. The row conflicts only because every bd
// mutation stamps updated_at, so a row-level resolver would have to drop one
// side's edit — the field-level merge keeps both.
func TestTryAutoResolveMergeConflicts_IssuesDisjointFieldsMerge(t *testing.T) {
	const issueID = "merge-disjoint"
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	db := store.db
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, updated_at) "+
		"VALUES (?, 'seed', '', '', '', '', 'open', 2, 'task', '', '2026-07-10 10:00:00')", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'seed issue')")

	// Our side claims it; their side (branched from the seed) assigns it.
	exec("UPDATE issues SET status = 'in_progress', updated_at = '2026-07-10 11:00:00' WHERE id = ?", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'claim on current')")

	peerBranch := currentBranch + "_disjoint_peer"
	exec("CALL DOLT_BRANCH(?, 'HEAD~1')", peerBranch)
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("UPDATE issues SET assignee = 'alice', updated_at = '2026-07-10 12:00:00' WHERE id = ?", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'assign on peer')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	if resolved := mergeAndTryAutoResolveIssues(t, store, peerBranch); !resolved {
		t.Fatal("expected a disjoint-field issues conflict to be auto-resolved")
	}

	var status, assignee, updatedAt string
	if err := store.db.QueryRowContext(ctx,
		"SELECT status, assignee, updated_at FROM issues WHERE id = ?", issueID).
		Scan(&status, &assignee, &updatedAt); err != nil {
		t.Fatalf("read merged issue: %v", err)
	}
	if status != "in_progress" {
		t.Errorf("our status edit was dropped: got %q", status)
	}
	if assignee != "alice" {
		t.Errorf("their assignee edit was dropped: got %q", assignee)
	}
	if !strings.Contains(updatedAt, "12:00:00") {
		t.Errorf("updated_at must merge to the later timestamp, got %q", updatedAt)
	}
}
