package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// The GH#7757 settlement regression, server-mode twins of the embedded
// TestEmbeddedMergeAndSettleCommitsPeerCleanTables: an issues conflict the
// field merger settles deterministically (LWW on updated_at) riding with
// peer-only comment, label, and user-kv (config kv.*) inserts. Disjoint rows
// merge cleanly but a conflicted merge leaves them UNSTAGED, so a conclusion
// that commits only the resolved issues table records HEAD's copy of
// comments/labels/config in the merge commit a push sends (GH#7811) while
// the shared working set keeps reading the merged rows. Every row is read
// AS OF 'HEAD' — the merge commit itself — so no later commit can mask the
// drop.

// TestMergeSettleCommitsPeerCleanTablesSQL drives the SQL pull settlement
// (settleMergeInTx, the route every SQL pull funnels through) over that
// divergence and asserts the concluded merge commit carries the peer rows.
func TestMergeSettleCommitsPeerCleanTablesSQL(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	peerBranch := currentBranch + "_peerclean"
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
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('pcs-1', 'base', '', '', '', '', 'open', 3, 'bug')")
	exec("CALL DOLT_COMMIT('-Am', 'seed issue')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)
	// Main retitles with the LATER updated_at, so the contested title cell
	// settles last-write-wins to ours deterministically.
	exec("UPDATE issues SET title = 'ours title', updated_at = '2026-01-02 00:00:00' WHERE id = 'pcs-1'")
	exec("CALL DOLT_COMMIT('-Am', 'main retitle')")
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	// The peer retitles the same cell (loses LWW) and inserts the peer-only
	// rows the settlement must carry into the merge commit. comments.id
	// needs an explicit id (0051 dropped the DB default).
	exec("UPDATE issues SET title = 'theirs title', updated_at = '2026-01-01 00:00:00' WHERE id = 'pcs-1'")
	exec("INSERT INTO comments (id, issue_id, author, text, created_at) VALUES ('pcs-comment-peer', 'pcs-1', 'peer', 'comment from peer', NOW())")
	exec("INSERT INTO labels (issue_id, label) VALUES ('pcs-1', 'peer-label')")
	exec("INSERT INTO config (`key`, value) VALUES ('kv.peer', 'peer-value')")
	exec("CALL DOLT_COMMIT('-Am', 'peer retitle + comment + label + kv')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
		t.Fatalf("settling merge with peer clean tables: %v", err)
	}

	assertPeerCleanTablesAtHead(t, ctx, db, "pcs-1", "pcs-comment-peer")
	assertMergeConcludedOnServer(t, ctx, db)
}

// TestMergeSettleCommitsPeerCleanTablesCLI drives the CLI-pull settlement
// (autoResolveConflictsAfterCLIPull, the route a git-protocol remote's
// `dolt pull` leaves behind) over the same divergence: the conflicted merge
// is persisted into the shared working set the way the CLI subprocess leaves
// it, then the resolver concludes it.
func TestMergeSettleCommitsPeerCleanTablesCLI(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	peerBranch := currentBranch + "_peercleancli"
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
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('pcc-1', 'base', '', '', '', '', 'open', 3, 'bug')")
	exec("CALL DOLT_COMMIT('-Am', 'seed issue')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)
	exec("UPDATE issues SET title = 'ours title', updated_at = '2026-01-02 00:00:00' WHERE id = 'pcc-1'")
	exec("CALL DOLT_COMMIT('-Am', 'main retitle')")
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("UPDATE issues SET title = 'theirs title', updated_at = '2026-01-01 00:00:00' WHERE id = 'pcc-1'")
	exec("INSERT INTO comments (id, issue_id, author, text, created_at) VALUES ('pcc-comment-peer', 'pcc-1', 'peer', 'comment from peer', NOW())")
	exec("INSERT INTO labels (issue_id, label) VALUES ('pcc-1', 'peer-label')")
	exec("INSERT INTO config (`key`, value) VALUES ('kv.peer', 'peer-value')")
	exec("CALL DOLT_COMMIT('-Am', 'peer retitle + comment + label + kv')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	// Persist the conflicted merge into the working set (what a CLI pull
	// leaves behind), as in TestAutoResolveConflictsAfterCLIPull_DependencyAuditOnly.
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

	// A fresh connection must observe the persisted conflict, else this test
	// would not be exercising the cross-connection (post-CLI-pull) resolver.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&n); err != nil {
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
		t.Fatal("expected the CLI-pull resolver to settle the issues conflict")
	}

	assertPeerCleanTablesAtHead(t, ctx, db, "pcc-1", "pcc-comment-peer")
	assertMergeConcludedOnServer(t, ctx, db)
}

// assertPeerCleanTablesAtHead asserts the peer's comment, label, and kv
// config row reached the concluded merge commit (read AS OF 'HEAD'), not
// just the working set, and that the contested title settled LWW to ours.
func assertPeerCleanTablesAtHead(t *testing.T, ctx context.Context, db *sql.DB, issueID, commentID string) {
	t.Helper()
	var text string
	if err := db.QueryRowContext(ctx,
		"SELECT text FROM comments AS OF 'HEAD' WHERE id = ?", commentID).Scan(&text); err != nil {
		t.Errorf("the peer's comment is missing from the merge commit (read AS OF 'HEAD': %v)", err)
	} else if text != "comment from peer" {
		t.Errorf("HEAD comment text = %q, want %q", text, "comment from peer")
	}
	var value string
	if err := db.QueryRowContext(ctx,
		"SELECT value FROM config AS OF 'HEAD' WHERE `key` = 'kv.peer'").Scan(&value); err != nil {
		t.Errorf("the peer's kv.peer row is missing from the merge commit (read AS OF 'HEAD': %v)", err)
	} else if value != "peer-value" {
		t.Errorf("HEAD config kv.peer = %q, want %q", value, "peer-value")
	}
	var title string
	if err := db.QueryRowContext(ctx,
		"SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read settled issue AS OF 'HEAD': %v", err)
	} else if title != "ours title" {
		t.Errorf("HEAD title = %q, want %q (the later updated_at wins LWW)", title, "ours title")
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
	labels := labelsAt("SELECT label FROM labels AS OF 'HEAD' WHERE issue_id = '" + issueID + "' ORDER BY label")
	if len(labels) != 1 || labels[0] != "peer-label" {
		t.Errorf("HEAD labels = %v, want [peer-label]: the merge commit did not carry the peer's label", labels)
	}
}

// assertMergeConcludedOnServer asserts the three post-settle invariants once
// the merge commit itself is right: nothing left uncommitted, the merge
// state closed, and HEAD a two-parent merge commit.
func assertMergeConcludedOnServer(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d table(s) left uncommitted after the settlement", dirty)
	}
	var merging bool
	err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if merging {
		t.Error("merge left open (dolt_merge_status.is_merging = 1) after settlement")
	}
	var parents int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')").Scan(&parents); err != nil {
		t.Fatalf("count HEAD parents: %v", err)
	}
	if parents != 2 {
		t.Errorf("HEAD (a concluded merge) has %d parent(s), want 2", parents)
	}
}
