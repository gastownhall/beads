//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
	"github.com/steveyegge/beads/internal/types"
)

// upgradeGuideDependencyRecipeMarker precedes the read recipe in the upgrade
// guide's "Dependency target columns" section (GH#5107).
const upgradeGuideDependencyRecipeMarker = "{/* doc-sql: dependency-targets */}"

// depTriple is one logical dependency edge as a SQL reader sees it.
type depTriple struct {
	issueID     string
	dependsOnID string
	depType     string
}

func (d depTriple) String() string {
	return d.issueID + " -> " + d.dependsOnID + " (" + d.depType + ")"
}

func sortedTriples(in []depTriple) []depTriple {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b depTriple) int {
		return strings.Compare(a.String(), b.String())
	})
	return out
}

// upgradeGuideDependencyRecipe returns the first ```sql fence after the
// recipe marker in docs/getting-started/upgrading.md, verbatim.
func upgradeGuideDependencyRecipe(t *testing.T) string {
	t.Helper()
	guidePath := filepath.Join(bazeltest.RepoRoot(t), "docs", "getting-started", "upgrading.md")
	raw, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read upgrade guide: %v", err)
	}
	guide := string(raw)
	_, afterMarker, ok := strings.Cut(guide, upgradeGuideDependencyRecipeMarker)
	if !ok {
		t.Fatalf("upgrade guide %s has no %q marker for the dependency read recipe", guidePath, upgradeGuideDependencyRecipeMarker)
	}
	_, afterOpen, ok := strings.Cut(afterMarker, "```sql\n")
	if !ok {
		t.Fatalf("upgrade guide has no ```sql fence after %q", upgradeGuideDependencyRecipeMarker)
	}
	recipe, _, ok := strings.Cut(afterOpen, "```")
	if !ok {
		t.Fatalf("upgrade guide's dependency read recipe fence is not closed")
	}
	return recipe
}

// TestEmbeddedUpgradeGuideDependencyRecipeMatchesStorage_5107 runs the upgrade
// guide's dependency read recipe, unchanged, against a fresh store whose
// dependencies and wisp_dependencies tables hold every target kind, and
// requires it to return exactly the edges the fixture wrote and the storage
// API reads back.
func TestEmbeddedUpgradeGuideDependencyRecipeMatchesStorage_5107(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	recipe := upgradeGuideDependencyRecipe(t)

	te := newTestEnv(t, "gd")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "gd-issue-a", Title: "Issue A"},
		{ID: "gd-issue-b", Title: "Issue B"},
		{ID: "gd-wisp-a", Title: "Wisp A", Ephemeral: true},
		{ID: "gd-wisp-b", Title: "Wisp B", Ephemeral: true},
	} {
		issue.Status = types.StatusOpen
		issue.Priority = 2
		issue.IssueType = types.TypeTask
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	// Every source table x target column, plus a cross-repository id, which
	// lands in depends_on_external like an external: reference.
	edges := []struct {
		dep    depTriple
		table  string
		column string
	}{
		{depTriple{"gd-issue-a", "gd-issue-b", string(types.DepBlocks)}, "dependencies", "depends_on_issue_id"},
		{depTriple{"gd-issue-a", "gd-wisp-a", string(types.DepDiscoveredFrom)}, "dependencies", "depends_on_wisp_id"},
		{depTriple{"gd-issue-a", "external:payments:checkout", string(types.DepBlocks)}, "dependencies", "depends_on_external"},
		{depTriple{"gd-issue-b", "zz-other-repo", string(types.DepBlocks)}, "dependencies", "depends_on_external"},
		{depTriple{"gd-wisp-a", "gd-issue-b", string(types.DepBlocks)}, "wisp_dependencies", "depends_on_issue_id"},
		{depTriple{"gd-wisp-a", "gd-wisp-b", string(types.DepDiscoveredFrom)}, "wisp_dependencies", "depends_on_wisp_id"},
		{depTriple{"gd-wisp-b", "external:payments:refunds", string(types.DepBlocks)}, "wisp_dependencies", "depends_on_external"},
	}
	want := make([]depTriple, 0, len(edges))
	for _, e := range edges {
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID:     e.dep.issueID,
			DependsOnID: e.dep.dependsOnID,
			Type:        types.DependencyType(e.dep.depType),
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s): %v", e.dep, err)
		}
		want = append(want, e.dep)
	}
	want = sortedTriples(want)

	records, err := te.store.GetDependencyRecordsForIssues(ctx, []string{"gd-issue-a", "gd-issue-b", "gd-wisp-a", "gd-wisp-b"})
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssues: %v", err)
	}
	var fromAPI []depTriple
	for _, deps := range records {
		for _, d := range deps {
			fromAPI = append(fromAPI, depTriple{d.IssueID, d.DependsOnID, string(d.Type)})
		}
	}
	fromAPI = sortedTriples(fromAPI)
	if !slices.Equal(fromAPI, want) {
		t.Fatalf("storage API edges = %v, want fixture edges %v", fromAPI, want)
	}

	// The recipe runs against the committed store, as a companion tool would
	// see it, not against the open store's session.
	if err := te.store.Commit(ctx, "seed dependency targets"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	closeEmbeddedDoltStore(t, te.store)

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	// The fixture must really span both tables and all three typed columns,
	// or the recipe comparison below proves less than it claims.
	for _, e := range edges {
		var n int
		//nolint:gosec // G201: table and column come from the literal fixture above.
		query := "SELECT COUNT(*) FROM " + e.table + " WHERE issue_id = ? AND " + e.column + " = ? AND type = ?"
		if err := db.QueryRowContext(ctx, query, e.dep.issueID, e.dep.dependsOnID, e.dep.depType).Scan(&n); err != nil {
			t.Fatalf("locate %s in %s.%s: %v", e.dep, e.table, e.column, err)
		}
		if n != 1 {
			t.Fatalf("%s: %d rows in %s.%s, want 1", e.dep, n, e.table, e.column)
		}
	}

	rows, err := db.QueryContext(ctx, recipe)
	if err != nil {
		t.Fatalf("run upgrade guide recipe: %v\n%s", err, recipe)
	}
	fromRecipe := scanDepTriples(t, rows)
	if !slices.Equal(fromRecipe, want) {
		t.Fatalf("upgrade guide recipe edges = %v, want %v", fromRecipe, want)
	}
}

func scanDepTriples(t *testing.T, rows *sql.Rows) []depTriple {
	t.Helper()
	defer func() { _ = rows.Close() }()
	var out []depTriple
	for rows.Next() {
		var d depTriple
		if err := rows.Scan(&d.issueID, &d.dependsOnID, &d.depType); err != nil {
			t.Fatalf("scan recipe row: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recipe rows: %v", err)
	}
	return sortedTriples(out)
}
