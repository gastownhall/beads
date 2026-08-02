package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueIDRekeysDependencySource verifies that renaming an issue re-derives
// the deterministic primary key of dependency edges where the renamed issue is the
// SOURCE. dependencies.issue_id carries fk_dep_issue ... ON UPDATE CASCADE, so the
// rename cascades the new issue_id into the row, but the cascade does not recompute
// the surrogate id. Without the rekey the row keeps depid.New(oldID, target),
// re-forking the primary key across clones and breaking the same-PK => same-edge
// invariant the pull conflict resolver relies on (#4259 finding 2).
func TestUpdateIssueIDRekeysDependencySource(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	for _, id := range []string{"rk-old", "rk-target"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}

	// Edge where the renamed issue is the SOURCE: rk-old -> rk-target.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rk-old", DependsOnID: "rk-target", Type: types.DepBlocks}, "alice"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	var beforeID string
	if err := db.QueryRowContext(ctx,
		"SELECT id FROM dependencies WHERE issue_id = 'rk-old' AND depends_on_issue_id = 'rk-target'").Scan(&beforeID); err != nil {
		t.Fatalf("read edge id before rename: %v", err)
	}
	if want := depid.New("rk-old", "rk-target"); beforeID != want {
		t.Fatalf("pre-rename id = %q, want %q", beforeID, want)
	}

	// Rename the source issue rk-old -> rk-new.
	if err := store.UpdateIssueID(ctx, "rk-old", "rk-new", &types.Issue{ID: "rk-new", Title: "rk-new"}, "alice"); err != nil {
		t.Fatalf("UpdateIssueID: %v", err)
	}

	var gotID, gotIssue string
	if err := db.QueryRowContext(ctx,
		"SELECT id, issue_id FROM dependencies WHERE depends_on_issue_id = 'rk-target'").Scan(&gotID, &gotIssue); err != nil {
		t.Fatalf("read edge after rename: %v", err)
	}
	if gotIssue != "rk-new" {
		t.Errorf("issue_id = %q after rename, want rk-new", gotIssue)
	}
	if want := depid.New("rk-new", "rk-target"); gotID != want {
		t.Errorf("post-rename dependency id = %q, want deterministic %q (a stale id here is #4259 finding 2)", gotID, want)
	}

	// Exactly one edge — no stale-keyed duplicate left behind.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE depends_on_issue_id = 'rk-target'").Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 edge after rename, got %d", count)
	}
}
