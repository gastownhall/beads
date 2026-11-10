package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestAddDependencyCrossPrefixSameDatabase is GH#8299 on every Dolt write
// entry point: one database can hold issues of several prefixes, and an edge
// onto a target this database holds must land in that target's typed column
// whatever its prefix. Filed in depends_on_external instead, the edge is
// invisible to readiness (which joins the typed columns), so the dependent
// stays ready while its blocker is open.
func TestAddDependencyCrossPrefixSameDatabase(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(t *testing.T, ids ...string) {
		t.Helper()
		var issues []*types.Issue
		for _, id := range ids {
			issues = append(issues, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
		}
		if err := store.CreateIssuesWithFullOptions(ctx, issues, "tester",
			storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("seed %v: %v", ids, err)
		}
	}
	seedWisp := func(t *testing.T, id string) {
		t.Helper()
		if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("seed wisp %s: %v", id, err)
		}
	}

	for _, tc := range []struct {
		name string
		// add wires source → target through one entry point and names the
		// table the edge lands in and the column the target belongs in.
		add func(t *testing.T, tag string) (table, source, target, column string)
	}{
		{"store issue target", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-tgt"
			seed(t, src, tgt)
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_issue_id"
		}},
		{"store wisp target", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-wisp-tgt"
			seed(t, src)
			seedWisp(t, tgt)
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_wisp_id"
		}},
		{"store wisp source", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-wisp-src", "xb"+tag+"-tgt"
			seedWisp(t, src)
			seed(t, tgt)
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			return "wisp_dependencies", src, tgt, "depends_on_issue_id"
		}},
		{"transaction issue target", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-tgt"
			seed(t, src, tgt)
			if err := store.RunInTransaction(ctx, "test: tx cross-prefix dep", func(tx storage.Transaction) error {
				return tx.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester")
			}); err != nil {
				t.Fatalf("RunInTransaction AddDependency: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_issue_id"
		}},
		{"transaction wisp target created in the same transaction", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-wisp-tgt"
			seed(t, src)
			if err := store.RunInTransaction(ctx, "test: tx cross-prefix wisp dep", func(tx storage.Transaction) error {
				if err := tx.CreateIssues(ctx, []*types.Issue{{
					ID: tgt, Title: tgt, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
				}}, "tester"); err != nil {
					return err
				}
				return tx.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}, "tester")
			}); err != nil {
				t.Fatalf("RunInTransaction create + AddDependency: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_wisp_id"
		}},
		{"dependency editor", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-tgt"
			seed(t, src, tgt)
			editor, err := store.DependencyEditor()
			if err != nil {
				t.Fatalf("DependencyEditor(): %v", err)
			}
			if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
				Actor: "tester",
				Edges: []publicops.DependencyEdge{{IssueID: src, DependsOnID: tgt, Type: publicops.DepBlocks}},
			}); err != nil {
				t.Fatalf("AddDependencies: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_issue_id"
		}},
		{"batch applier", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-tgt"
			seed(t, src, tgt)
			applier, err := store.BatchApplier()
			if err != nil {
				t.Fatalf("BatchApplier(): %v", err)
			}
			if _, err := applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
				Actor: "tester",
				Items: []publicops.ApplyItem{{
					Kind: publicops.ItemDepAdd,
					DepAdd: &publicops.DepAddItem{
						Source: publicops.Ref{ID: src},
						Target: publicops.Ref{ID: tgt},
						Type:   publicops.DepBlocks,
					},
				}},
			}); err != nil {
				t.Fatalf("ApplyBatch: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_issue_id"
		}},
		{"create with deps", func(t *testing.T, tag string) (string, string, string, string) {
			src, tgt := "xa"+tag+"-src", "xb"+tag+"-tgt"
			seed(t, tgt)
			if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
				ID: src, Title: src, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{{DependsOnID: tgt, Type: types.DepBlocks}},
			}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
				t.Fatalf("create with deps: %v", err)
			}
			return "dependencies", src, tgt, "depends_on_issue_id"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := sanitizeTag(tc.name)
			table, src, tgt, column := tc.add(t, tag)
			requireDepTargetColumn(ctx, t, store.db, table, src, tgt, column)
		})
	}
}

// sanitizeTag turns a subtest name into an id-safe fragment without hyphens,
// so the prefix of every id stays the part before its first hyphen.
func sanitizeTag(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			out = append(out, c)
		}
	}
	return string(out)
}

// requireDepTargetColumn asserts the source → target edge in table holds the
// target in column and nowhere else, so depends_on_external is NULL for a
// target this database holds.
func requireDepTargetColumn(ctx context.Context, t *testing.T, db *sql.DB, table, source, target, column string) {
	t.Helper()
	var issueCol, wispCol, external sql.NullString
	//nolint:gosec // G202: table is one of two hardcoded constants in this file's callers.
	err := db.QueryRowContext(ctx, "SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+
		" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		source, target).Scan(&issueCol, &wispCol, &external)
	if err != nil {
		t.Fatalf("read %s edge %s -> %s: %v", table, source, target, err)
	}
	got := map[string]sql.NullString{
		"depends_on_issue_id": issueCol,
		"depends_on_wisp_id":  wispCol,
		"depends_on_external": external,
	}
	for col, v := range got {
		want := col == column
		if v.Valid != want || (want && v.String != target) {
			t.Errorf("%s edge %s -> %s: %s = %v, want the target only in %s (issue=%v wisp=%v external=%v)",
				table, source, target, col, v, column, issueCol, wispCol, external)
		}
	}
}

// TestMergeFilesACrossPrefixEdgeOntoAMergedTarget is the merge half of
// GH#8299 on the Dolt store: this clone adds an edge onto an id it does not
// hold, so the edge is filed external, and a peer creates that id. The merge
// that brings the issue in files the edge into depends_on_issue_id, blocks its
// source, and commits the moved row with the recompute.
func TestMergeFilesACrossPrefixEdgeOntoAMergedTarget(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// setupTestStore isolates tests by branch on a shared database: stay on
	// this test's branch, as TestMergeRecomputesIsBlocked does.
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}
	peerBranch := currentBranch + "-xpeer"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("%s: %v", query, err)
		}
	}

	const source, target = "xa-msrc", "xb-mtgt"
	if err := store.CreateIssue(ctx, &types.Issue{ID: source, Title: source, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", source, err)
	}
	exec("CALL DOLT_COMMIT('-Am', 'seed the source')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)

	if err := store.AddDependency(ctx, &types.Dependency{IssueID: source, DependsOnID: target, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency %s -> %s: %v", source, target, err)
	}
	requireDepTargetColumn(ctx, t, db, "dependencies", source, target, "depends_on_external")
	exec("CALL DOLT_COMMIT('-Am', 'edge onto an id not held yet')")

	// The peer creates the target: a merged-in write, past every local
	// write path.
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES (?, ?, '', '', '', '')", target, target)
	exec("CALL DOLT_COMMIT('-Am', 'peer creates the target')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	conflicts, err := store.Merge(ctx, peerBranch)
	if err != nil {
		t.Fatalf("merge peer branch: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected a conflict-free merge, got %d conflicts", len(conflicts))
	}
	requireDepTargetColumn(ctx, t, db, "dependencies", source, target, "depends_on_issue_id")
	if !isBlocked(ctx, t, db, source) {
		t.Errorf("%s is not blocked by the open %s the merge brought in", source, target)
	}
	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('issues', 'dependencies')").Scan(&dirty); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d graph table(s) left uncommitted after the merge", dirty)
	}
}
