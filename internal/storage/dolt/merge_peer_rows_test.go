package dolt

import (
	"reflect"
	"testing"
)

// TestMergeSettleCommitsPeerCleanTables (GH#7757, AC-1) is the server-mode
// twin of the embedded TestEmbeddedMergeAndSettleCommitsPeerCleanTables, on
// both settle routes: two clones comment on one issue, the current branch
// changes its status and the peer its title (distinct updated_at, so the
// field-level merge settles the issues conflict last-write-wins), and the
// peer also adds a label and a user kv row. The settlement commit itself must
// hold both comments, the label, the kv row and the merged issue, with the
// merge closed and nothing unstaged. It reads AS OF 'HEAD' before any
// is_blocked recompute or push, whose commits would sweep the working set
// and hide a merge commit that left the peer's rows behind.
func TestMergeSettleCommitsPeerCleanTables(t *testing.T) {
	for _, route := range []string{"sql", "cli"} {
		t.Run(route, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			db := store.db

			var currentBranch string
			if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
				t.Fatalf("get current branch: %v", err)
			}
			// The subtests share one server database: keep branch and issue
			// names apart.
			issueID, peerBranch := "mpr-"+route, "mprpeer_"+route
			for _, step := range []struct {
				query string
				args  []any
			}{
				{"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, updated_at) " +
					"VALUES (?, 'shared issue', '', '', '', '', 'open', 2, 'task', '2026-01-01 09:00:00')", []any{issueID}},
				{"CALL DOLT_COMMIT('-Am', 'seed shared issue')", nil},
				{"CALL DOLT_BRANCH(?, 'HEAD')", []any{peerBranch}},
				{"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, 'a', 'comment from A', NOW())", []any{issueID}},
				{"UPDATE issues SET status = 'in_progress', updated_at = '2026-01-01 10:00:00' WHERE id = ?", []any{issueID}},
				{"CALL DOLT_COMMIT('-Am', 'clone A comments and edits')", nil},
				{"CALL DOLT_CHECKOUT(?)", []any{peerBranch}},
				{"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, 'b', 'comment from B', NOW())", []any{issueID}},
				{"UPDATE issues SET title = 'renamed on B', updated_at = '2026-01-01 11:00:00' WHERE id = ?", []any{issueID}},
				{"INSERT INTO labels (issue_id, label) VALUES (?, 'from-b')", []any{issueID}},
				{"INSERT INTO config (`key`, value) VALUES ('kv.peer', 'fromB')", nil},
				{"CALL DOLT_COMMIT('-Am', 'clone B comments and edits')", nil},
				{"CALL DOLT_CHECKOUT(?)", []any{currentBranch}},
			} {
				if _, err := db.ExecContext(ctx, step.query, step.args...); err != nil {
					t.Fatalf("%s: %v", step.query, err)
				}
			}

			switch route {
			case "sql":
				if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
					t.Fatalf("settling the conflicted merge: %v", err)
				}
			case "cli":
				// What a CLI pull leaves behind: the conflicted merge
				// persisted in the working set, unsettled.
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin merge tx: %v", err)
				}
				for _, q := range []string{"SET @@dolt_allow_commit_conflicts = 1", "SET @@dolt_force_transaction_commit = 1"} {
					if _, err := tx.ExecContext(ctx, q); err != nil {
						t.Fatalf("%s: %v", q, err)
					}
				}
				if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
					t.Fatalf("forced merge: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit conflicted working set: %v", err)
				}
				resolved, err := store.autoResolveConflictsAfterCLIPull(ctx)
				if err != nil {
					t.Fatalf("autoResolveConflictsAfterCLIPull: %v", err)
				}
				if !resolved {
					t.Fatal("expected the CLI-pull resolver to settle the issues conflict")
				}
			}

			rows, err := db.QueryContext(ctx,
				"SELECT text FROM comments AS OF 'HEAD' WHERE issue_id = ? ORDER BY text", issueID)
			if err != nil {
				t.Fatalf("query HEAD comments: %v", err)
			}
			var comments []string
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					t.Fatalf("scan HEAD comment: %v", err)
				}
				comments = append(comments, s)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate HEAD comments: %v", err)
			}
			_ = rows.Close()
			if want := []string{"comment from A", "comment from B"}; !reflect.DeepEqual(comments, want) {
				t.Errorf("HEAD comments = %v, want %v: the merge commit dropped a clone's comment", comments, want)
			}

			var labels int
			if err := db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = 'from-b'", issueID).Scan(&labels); err != nil {
				t.Fatalf("count HEAD labels: %v", err)
			}
			if labels != 1 {
				t.Error("HEAD lacks the peer's label: the merge commit dropped it")
			}
			var kv string
			if err := db.QueryRowContext(ctx,
				"SELECT value FROM config AS OF 'HEAD' WHERE `key` = 'kv.peer'").Scan(&kv); err != nil {
				t.Fatalf("read the peer's kv row AS OF 'HEAD' (the merge commit does not carry it): %v", err)
			}
			if kv != "fromB" {
				t.Errorf("HEAD config kv.peer = %q, want %q", kv, "fromB")
			}
			var title, status string
			if err := db.QueryRowContext(ctx,
				"SELECT title, status FROM issues AS OF 'HEAD' WHERE id = ?", issueID).Scan(&title, &status); err != nil {
				t.Fatalf("read HEAD issue: %v", err)
			}
			if title != "renamed on B" || status != "in_progress" {
				t.Errorf("HEAD issue = (%q, %q), want (%q, %q)", title, status, "renamed on B", "in_progress")
			}

			var dirty int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
				t.Fatalf("count dolt_status: %v", err)
			}
			if dirty != 0 {
				t.Errorf("%d table(s) left uncommitted after the settlement", dirty)
			}
			var merging bool
			if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
				t.Fatalf("read dolt_merge_status: %v", err)
			}
			if merging {
				t.Error("merge left open (dolt_merge_status.is_merging = 1) after the settlement")
			}
			var parents int
			if err := db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')").Scan(&parents); err != nil {
				t.Fatalf("count HEAD parents: %v", err)
			}
			if parents != 2 {
				t.Errorf("HEAD has %d parent(s), want 2 (a concluded merge)", parents)
			}
		})
	}
}
