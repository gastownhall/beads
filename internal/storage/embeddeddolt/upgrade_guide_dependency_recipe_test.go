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

// The upgrade guide's "Dependency target columns" section carries a marked SQL
// recipe — {/* doc-sql: dependency-targets */} — that reconstructs the logical
// depends_on_id over the split target columns. #5107: v1.0.5 removed SQL
// depends_on_id from both dependency tables (0043 and ignored/0005), so that
// recipe is the documented read path for SQL readers. This test executes the
// marked recipe verbatim against a real store seeded through the storage API,
// so the guide cannot drift from the schema again: it fails while the marked
// recipe is absent, and afterwards pins the recipe's rows to the seeded edges
// and to what the storage interface reports for the same sources.
const upgradeGuideDependencyTargetsMarker = "doc-sql: dependency-targets"

// upgradeGuideDependencyRecipe extracts the marked recipe from
// docs/getting-started/upgrading.md: the first ```sql fence after the
// {/* doc-sql: dependency-targets */} marker, verbatim. A missing marker or
// fence is fatal — the guide, not this test, owns the remedy.
func upgradeGuideDependencyRecipe(t *testing.T) string {
	t.Helper()
	guidePath := filepath.Join(bazeltest.RepoRoot(t), "docs", "getting-started", "upgrading.md")
	raw, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read the upgrade guide: %v", err)
	}
	guide := string(raw)
	marker := strings.Index(guide, upgradeGuideDependencyTargetsMarker)
	if marker < 0 {
		t.Fatalf("the upgrade guide has no %q marker: the dependency-target read recipe is missing",
			upgradeGuideDependencyTargetsMarker)
	}
	after := guide[marker+len(upgradeGuideDependencyTargetsMarker):]
	open := strings.Index(after, "```sql")
	if open < 0 {
		t.Fatalf("no ```sql fence after the %q marker in the upgrade guide",
			upgradeGuideDependencyTargetsMarker)
	}
	body := after[open+len("```sql"):]
	end := strings.Index(body, "```")
	if end < 0 {
		t.Fatalf("unterminated ```sql fence after the %q marker in the upgrade guide",
			upgradeGuideDependencyTargetsMarker)
	}
	return body[:end]
}

// TestEmbeddedUpgradeGuideDependencyRecipeMatchesStorage_5107 seeds, through
// the storage API, one acyclic edge for every source-kind x target-kind pair —
// issue->{issue,wisp,external} into dependencies and
// wisp->{issue,wisp,external} into wisp_dependencies, with both nonlocal
// shapes (an external: capability reference and a cross-repository id) — then
// runs the guide's marked recipe verbatim and requires its multiset of
// (issue_id, depends_on_id, type) to equal both the literal fixture and the
// storage API's records: no lost edge, no duplicate.
func TestEmbeddedUpgradeGuideDependencyRecipeMatchesStorage_5107(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	recipe := upgradeGuideDependencyRecipe(t)

	te := newTestEnv(t, "ug51")
	ctx := t.Context()

	// Every local id shares the ug51- prefix so no edge is misclassified as a
	// cross-repository target; the two nonlocal edges opt in deliberately.
	createIssue := func(id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	createWisp := func(id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("create wisp %s: %v", id, err)
		}
	}
	for _, id := range []string{
		"ug51-src-issue", "ug51-tgt-issue",
		"ug51-src-issue2", "ug51-src-issue3", "ug51-tgt-issue2",
	} {
		createIssue(id)
	}
	for _, id := range []string{
		"ug51-tgt-wisp", "ug51-src-wisp", "ug51-tgt-wisp2", "ug51-src-wisp2", "ug51-src-wisp3",
	} {
		createWisp(id)
	}

	type depEdge struct {
		source, target string
		depType        types.DependencyType
	}
	edges := []depEdge{
		{"ug51-src-issue", "ug51-tgt-issue", types.DepBlocks},
		{"ug51-src-issue2", "ug51-tgt-wisp", types.DepRelatesTo},
		{"ug51-src-issue3", "external:remote:payments", types.DepBlocks},
		{"ug51-src-wisp", "ug51-tgt-issue2", types.DepBlocks},
		{"ug51-src-wisp2", "ug51-tgt-wisp2", types.DepDuplicates},
		{"ug51-src-wisp3", "otherdep-777", types.DepBlocks},
	}
	sources := make([]string, len(edges))
	for i, e := range edges {
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: e.source, DependsOnID: e.target, Type: e.depType,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", e.source, e.target, err)
		}
		sources[i] = e.source
	}

	apiRecords, err := te.store.GetDependencyRecordsForIssues(ctx, sources)
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssues: %v", err)
	}

	// The recipe runs against the committed store, as a companion tool would
	// see it, not against the open store's session.
	if err := te.store.Commit(ctx, "seed dependency edges for the guide recipe test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	closeEmbeddedDoltStore(t, te.store)

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	rows, err := db.QueryContext(ctx, recipe)
	if err != nil {
		t.Fatalf("run the guide's marked recipe verbatim: %v", err)
	}
	defer rows.Close()

	triple := func(issueID, dependsOnID, depType string) string {
		return issueID + "\x00" + dependsOnID + "\x00" + depType
	}
	want := make([]string, 0, len(edges))
	for _, e := range edges {
		want = append(want, triple(e.source, e.target, string(e.depType)))
	}
	api := make([]string, 0, len(edges))
	for _, deps := range apiRecords {
		for _, dep := range deps {
			api = append(api, triple(dep.IssueID, dep.DependsOnID, string(dep.Type)))
		}
	}
	var got []string
	for rows.Next() {
		var issueID, dependsOnID, depType sql.NullString
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err != nil {
			t.Fatalf("scan a recipe row: %v", err)
		}
		if !issueID.Valid || !dependsOnID.Valid || !depType.Valid {
			t.Fatalf("recipe returned a NULL projection: (%q, %q, %q)",
				issueID.String, dependsOnID.String, depType.String)
		}
		got = append(got, triple(issueID.String, dependsOnID.String, depType.String))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recipe rows: %v", err)
	}

	slices.Sort(want)
	slices.Sort(api)
	slices.Sort(got)

	if len(got) != len(edges) {
		t.Errorf("recipe returned %d rows, want %d — a lost or duplicated edge:\n%s",
			len(got), len(edges), strings.Join(got, "\n"))
	}
	if !slices.Equal(got, want) {
		t.Errorf("the guide recipe's rows do not match the seeded edges:\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !slices.Equal(api, want) {
		t.Errorf("the storage API's records do not match the seeded edges:\ngot:\n  %s\nwant:\n  %s",
			strings.Join(api, "\n  "), strings.Join(want, "\n  "))
	}
}
