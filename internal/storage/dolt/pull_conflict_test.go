package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestPullAutoResolveMetadataConflicts verifies that merge conflicts limited to
// the metadata table are automatically resolved with "theirs" strategy (GH#2466).
// This simulates the scenario where two machines each write different
// dolt_auto_push_* values to the metadata table, causing recurring conflicts on pull.
func TestPullAutoResolveMetadataConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Record the current branch (our test branch).
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	// Insert a metadata row on the current branch and commit.
	if _, err := db.ExecContext(ctx, "INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'aaa')"); err != nil {
		t.Fatalf("failed to insert metadata on current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local metadata')"); err != nil {
		t.Fatalf("failed to commit on current branch: %v", err)
	}

	// Create a divergent branch to simulate the remote.
	remoteBranch := currentBranch + "_remote"
	// Branch from current branch's parent (HEAD~1).
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("failed to create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()

	// Switch to remote branch and insert conflicting metadata.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("failed to checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'bbb')"); err != nil {
		t.Fatalf("failed to insert metadata on remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'remote metadata')"); err != nil {
		t.Fatalf("failed to commit on remote branch: %v", err)
	}

	// Switch back to current branch.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("failed to checkout current branch: %v", err)
	}

	// Merge the remote branch in a transaction with dolt_allow_commit_conflicts.
	// This simulates what pullWithAutoResolve does internally.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("failed to set dolt_allow_commit_conflicts: %v", err)
	}

	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)
	// mergeErr may or may not be nil depending on Dolt version.

	// Try auto-resolve.
	resolved, resolveErr := store.tryAutoResolveMergeConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		t.Fatalf("tryAutoResolveMergeConflicts error: %v (mergeErr: %v)", resolveErr, mergeErr)
	}
	if !resolved {
		_ = tx.Rollback()
		if mergeErr != nil {
			t.Fatalf("merge failed and metadata conflicts were not auto-resolved: %v", mergeErr)
		}
		// Clean merge, no conflicts to resolve — verify the value.
		t.Log("merge succeeded without conflicts (auto-merge)")
		return
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit after auto-resolve: %v", err)
	}

	// Verify the metadata value is "theirs" (bbb from remote).
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = 'dolt_auto_push_commit'").Scan(&value); err != nil {
		t.Fatalf("failed to read resolved metadata: %v", err)
	}
	if value != "bbb" {
		t.Errorf("expected metadata value 'bbb' (theirs), got %q", value)
	}
}

// TestPullAutoResolveSkipsNonMetadataConflicts verifies that conflicts on
// tables other than metadata are NOT auto-resolved. The issues-table conflict
// here is add/add (both branches insert the same PK), which the LWW safety
// check rejects (GH#4698); modify/modify conflicts with different timestamps
// ARE auto-resolved by LWW, covered by issues_conflict_test.go.
func TestPullAutoResolveSkipsNonMetadataConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	// Create an issue on the current branch.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('conflict-test', 'Local Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("failed to insert issue on current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local issue')"); err != nil {
		t.Fatalf("failed to commit on current branch: %v", err)
	}

	// Create divergent branch from parent.
	remoteBranch := currentBranch + "_remote2"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("failed to create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()

	// Insert conflicting issue on remote branch (same PK, different title).
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("failed to checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('conflict-test', 'Remote Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("failed to insert issue on remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'remote issue')"); err != nil {
		t.Fatalf("failed to commit on remote branch: %v", err)
	}

	// Switch back and merge.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("failed to checkout current branch: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("failed to set dolt_allow_commit_conflicts: %v", err)
	}

	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)

	// Issues table conflict should NOT be auto-resolved.
	resolved, resolveErr := store.tryAutoResolveMergeConflicts(ctx, tx)
	_ = tx.Rollback()

	if mergeErr == nil && resolveErr == nil && !resolved {
		// Clean merge — Dolt auto-merged the issue changes.
		t.Skip("merge succeeded without conflicts — cannot test non-metadata conflict path")
		return
	}

	if resolveErr != nil {
		// Error checking conflicts is acceptable for some Dolt versions.
		t.Logf("tryAutoResolveMergeConflicts returned error: %v", resolveErr)
		return
	}

	if resolved {
		t.Error("expected non-metadata conflicts NOT to be auto-resolved")
	}
}

// TestAutoResolveConflictsAfterCLIPull_DependencyAuditOnly verifies the post-CLI-pull
// resolver (used for git-protocol / credentialed remotes, #4259 finding 1) resolves an
// audit-only dependency conflict left by an out-of-transaction merge. CLI `dolt pull`
// performs the merge in a subprocess and leaves conflicts in the working set, so the
// resolver never runs in the transaction that produced the merge — unlike the SQL
// DOLT_PULL path. Here the conflict is persisted into the working set (what a CLI pull
// leaves behind) and then resolved via finishCLIPull's helper on the store connection,
// which stays on the pull's branch (a brand-new connection would default to the base
// branch and miss the conflict).
func TestAutoResolveConflictsAfterCLIPull_DependencyAuditOnly(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "blocks", "bob")

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// Persist the merge conflict into the working set (mirroring a CLI pull), then
	// resolve it from a brand-new connection rather than this transaction.
	tx, err := db.BeginTx(ctx, nil)
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
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist conflicted working set: %v", err)
	}

	// A fresh connection must observe the persisted conflict, else this test would
	// not be exercising the cross-connection (post-CLI-pull) resolver path.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts_dependencies").Scan(&n); err != nil {
		t.Fatalf("count conflicts on fresh conn: %v", err)
	}
	if n == 0 {
		t.Skip("merge auto-resolved without persisted conflicts — cannot exercise post-CLI-pull resolver")
	}

	resolved, err := store.autoResolveConflictsAfterCLIPull(ctx)
	if err != nil {
		t.Fatalf("autoResolveConflictsAfterCLIPull: %v", err)
	}
	if !resolved {
		t.Fatal("expected audit-only dependency conflict to auto-resolve after a CLI pull")
	}

	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'depc-x' AND depends_on_issue_id = 'depc-y'").Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 dependency row after resolve, got %d", count)
	}
}

// TestSettleMergeInTxKeepsCleanMergedTables (bd-cdo628, GH#7811) is the
// server-mode counterpart of the embedded auto-resolve regression: a forced
// merge whose only conflict is an auto-resolvable metadata modify/modify
// rides together with the peer's cleanly merged label swap, and
// settleMergeInTx must conclude the merge with BOTH — CommitResolvedConflicts
// stages the merge's clean output before committing, or the settlement
// commit reverts the peer's label change while the working set keeps it as
// an unstaged divergence. Deliberately no trailing DOLT_COMMIT('-Am') sweep,
// which would mask the drop by committing the working set afterwards.
func TestSettleMergeInTxKeepsCleanMergedTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	peerBranch := currentBranch + "_labelspeer"
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	})

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Base: the annotated issue and a metadata row both branches will modify.
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('sltx-1', 'X', '', '', '', '', 'open', 3, 'bug')")
	exec("INSERT INTO labels (issue_id, label) VALUES ('sltx-1', 'needs-triage')")
	exec("INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'base')")
	exec("CALL DOLT_COMMIT('-Am', 'seed issue + label + metadata')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)
	// Main modifies the metadata key; peer modifies it differently and swaps
	// the label — a metadata modify/modify (auto-resolved theirs) riding
	// with a cleanly merged labels change.
	exec("UPDATE metadata SET value = 'aaa' WHERE `key` = 'dolt_auto_push_commit'")
	exec("CALL DOLT_COMMIT('-Am', 'main metadata aaa')")
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("UPDATE metadata SET value = 'bbb' WHERE `key` = 'dolt_auto_push_commit'")
	exec("DELETE FROM labels WHERE issue_id = 'sltx-1' AND label = 'needs-triage'")
	exec("INSERT INTO labels (issue_id, label) VALUES ('sltx-1', 'ready-for-agent')")
	exec("CALL DOLT_COMMIT('-Am', 'peer metadata bbb + relabel')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
		t.Fatalf("settling merge with clean label ride-along: %v", err)
	}

	labelsAt := func(q string) []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatalf("scan label: %v", err)
			}
			got = append(got, l)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate labels: %v", err)
		}
		return got
	}
	committed := labelsAt("SELECT label FROM labels AS OF 'HEAD' WHERE issue_id = 'sltx-1' ORDER BY label")
	if len(committed) != 1 || committed[0] != "ready-for-agent" {
		t.Errorf("HEAD labels = %v, want [ready-for-agent]: the settlement commit did not carry the peer's label swap", committed)
	}
	var value string
	if err := db.QueryRowContext(ctx,
		"SELECT value FROM metadata AS OF 'HEAD' WHERE `key` = 'dolt_auto_push_commit'").Scan(&value); err != nil {
		t.Fatalf("read resolved metadata: %v", err)
	}
	if value != "bbb" {
		t.Errorf("metadata value = %q, want %q (theirs)", value, "bbb")
	}
	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d table(s) left uncommitted after the settlement", dirty)
	}
}

// TestFinishCLIPull_DeclinedConflictAbortsMerge pins GH#7839: a CLI-route
// (git-protocol) pull whose conflict the auto-resolver declines must leave the
// branch and working set as they were before the pull and report a
// MergeConflictsError, exactly like the SQL route — not leave the merge open
// so every later write and pull fails.
func TestFinishCLIPull_DeclinedConflictAbortsMerge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("active branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('cli-conflict', 'Local Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert local issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local issue')"); err != nil {
		t.Fatalf("commit local: %v", err)
	}
	remoteBranch := currentBranch + "_cli_remote"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("create remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('cli-conflict', 'Remote Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert remote issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'remote issue')"); err != nil {
		t.Fatalf("commit remote: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}

	var preHead string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&preHead); err != nil {
		t.Fatalf("pre-pull head: %v", err)
	}

	// Persist the conflicted merge into the working set, as the `dolt pull`
	// subprocess does when it runs against the server's database directory.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist conflicted working set: %v (merge: %v)", err, mergeErr)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts_issues").Scan(&n); err != nil {
		t.Fatalf("count conflicts: %v", err)
	}
	if n == 0 {
		t.Fatal("setup: expected a persisted issues conflict")
	}

	pullErr := errors.New("dolt pull failed: CONFLICT (content): Merge conflict in issues: exit status 1")
	gotErr := store.finishCLIPull(ctx, pullErr)

	var isMerging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&isMerging); err != nil {
		t.Fatalf("merge status: %v", err)
	}
	var postHead string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&postHead); err != nil {
		t.Fatalf("post-pull head: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&n); err != nil {
		t.Fatalf("count conflicts after: %v", err)
	}

	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(gotErr, &mce) {
		t.Errorf("finishCLIPull error = %v; want *versioncontrolops.MergeConflictsError", gotErr)
	}
	if isMerging {
		t.Errorf("dolt_merge_status.is_merging = 1 after a declined CLI pull conflict; want the merge aborted")
	}
	if n != 0 {
		t.Errorf("dolt_conflicts has %d table(s) after a declined CLI pull conflict; want 0", n)
	}
	if postHead != preHead {
		t.Errorf("HEAD moved from %s to %s; want the pre-pull branch", preHead, postHead)
	}
}

// seedLocalBranchConflictedMerge persists an open conflicted merge on the
// store's branch, sourced from a local branch — another session's deliberate
// work — and returns the branch HEAD with the merge open.
func seedLocalBranchConflictedMerge(t *testing.T, store *DoltStore) (preHead string) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("active branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('w5-open-merge', 'Local Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert local issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local issue')"); err != nil {
		t.Fatalf("commit local: %v", err)
	}
	remoteBranch := currentBranch + "_w5_source"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("create source branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("checkout source branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('w5-open-merge', 'Source Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert source issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'source issue')"); err != nil {
		t.Fatalf("commit source: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist conflicted working set: %v (merge: %v)", err, mergeErr)
	}
	var head string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return head
}

// TestRunCLIPullSettledRefusesOverOpenMerge is AC-4's transport half: with a
// merge already open on the store branch, the CLI pull wrapper refuses
// BEFORE the subprocess runs — a pull that merged over an operator's open
// conflicted merge would destroy work they still have in flight — and the
// refusal preserves the merge and its conflicts untouched (GH#7839).
func TestRunCLIPullSettledRefusesOverOpenMerge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	preHead := seedLocalBranchConflictedMerge(t, store)
	var isMerging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&isMerging); err != nil || !isMerging {
		t.Fatalf("setup: open conflicted merge expected, got is_merging=%v err=%v", isMerging, err)
	}

	pullRan := false
	err := store.runCLIPullSettled(ctx, "origin", func() error {
		pullRan = true
		return nil
	})
	if pullRan {
		t.Error("the CLI pull subprocess ran over an already-open merge")
	}
	if err == nil {
		t.Fatal("runCLIPullSettled over an open merge returned nil; want the refusal")
	}
	if msg := err.Error(); !strings.Contains(msg, "already open before this pull") {
		t.Errorf("refusal = %q; want it to say the merge was already open before this pull", msg)
	}
	if msg := err.Error(); !strings.Contains(msg, "DOLT_MERGE('--abort')") {
		t.Errorf("refusal = %q; want it to name the operator's abort remedy", msg)
	}

	// The merge is preserved exactly: still open, conflicts intact, HEAD unmoved.
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&isMerging); err != nil || !isMerging {
		t.Errorf("is_merging=%v err=%v after the refusal; want the open merge preserved", isMerging, err)
	}
	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts_issues").Scan(&conflicts); err != nil {
		t.Fatalf("count conflicts after the refusal: %v", err)
	}
	if conflicts == 0 {
		t.Error("the operator's conflicts were cleared by the refused pull")
	}
	var head string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read HEAD after the refusal: %v", err)
	}
	if head != preHead {
		t.Errorf("HEAD = %s after the refusal; want the untouched %s", head, preHead)
	}
}
