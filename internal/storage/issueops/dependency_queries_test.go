package issueops

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// Shared by both bulk-loader query shapes so they can't drift apart.
const dependencyProjectionRegex = `SELECT issue_id, COALESCE\(depends_on_issue_id, depends_on_wisp_id, depends_on_external\) AS depends_on_id, type, created_at, created_by, metadata, thread_id\s+FROM `

func allDependencyRecordsQueryRegex(table string) string {
	return `(?s)` + dependencyProjectionRegex +
		regexp.QuoteMeta(table) + `\s+ORDER BY issue_id, depends_on_id, type, id`
}

func dependencyRecordsForIssuesQueryRegex(table string) string {
	return `(?s)` + dependencyProjectionRegex +
		regexp.QuoteMeta(table) + ` WHERE issue_id IN \(\?\) ORDER BY issue_id, depends_on_id, type, id`
}

func externalBlockingDependencyRecordsQueryRegex(table string) string {
	return `(?s)SELECT issue_id, COALESCE\(depends_on_issue_id, depends_on_wisp_id, depends_on_external\) AS depends_on_id, type, created_at, created_by, metadata, thread_id\s+FROM ` +
		regexp.QuoteMeta(table) + `\s+WHERE depends_on_external LIKE 'external:%'\s+AND type IN \('blocks', 'conditional-blocks', 'waits-for'\)\s+ORDER BY issue_id`
}

func dependencyRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"issue_id",
		"depends_on_id",
		"type",
		"created_at",
		"created_by",
		"metadata",
		"thread_id",
	})
}

func TestGetAllDependencyRecordsInTxReadsPermanentAndWispDependencies(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	now := time.Now()
	mock.ExpectQuery(allDependencyRecordsQueryRegex("dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"perm-source", "perm-target", types.DepBlocks, now, "tester", "{}", "thread-perm",
		))
	mock.ExpectQuery(allDependencyRecordsQueryRegex("wisp_dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"wisp-source", "wisp-target", types.DepParentChild, now, "tester", "{}", "thread-wisp",
		))

	got, err := GetAllDependencyRecordsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("GetAllDependencyRecordsInTx: %v", err)
	}
	if dep := onlyDependency(t, got, "perm-source"); dep.DependsOnID != "perm-target" {
		t.Fatalf("perm dependency target = %q, want perm-target", dep.DependsOnID)
	}
	if dep := onlyDependency(t, got, "wisp-source"); dep.DependsOnID != "wisp-target" {
		t.Fatalf("wisp dependency target = %q, want wisp-target", dep.DependsOnID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetAllDependencyRecordsInTxToleratesMissingWispDependencyTable(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(allDependencyRecordsQueryRegex("dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"perm-source", "perm-target", types.DepBlocks, time.Now(), "tester", "{}", "",
		))
	mock.ExpectQuery(allDependencyRecordsQueryRegex("wisp_dependencies")).
		WillReturnError(errors.New("Error 1146: Table 'db.wisp_dependencies' doesn't exist"))

	got, err := GetAllDependencyRecordsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("GetAllDependencyRecordsInTx: %v", err)
	}
	if dep := onlyDependency(t, got, "perm-source"); dep.DependsOnID != "perm-target" {
		t.Fatalf("perm dependency target = %q, want perm-target", dep.DependsOnID)
	}
	if _, ok := got["wisp-source"]; ok {
		t.Fatal("unexpected wisp dependency records from missing table")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetExternalBlockingDependencyRecordsInTxReadsPermanentAndWispEdges(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	now := time.Now()
	mock.ExpectQuery(externalBlockingDependencyRecordsQueryRegex("dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"perm-source", "external:remote:payments", types.DepBlocks, now, "tester", "{}", "",
		))
	mock.ExpectQuery(externalBlockingDependencyRecordsQueryRegex("wisp_dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"wisp-source", "external:remote:identity", types.DepWaitsFor, now, "tester", "{}", "",
		))

	got, err := GetExternalBlockingDependencyRecordsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecordsInTx: %v", err)
	}
	if dep := onlyDependency(t, got, "perm-source"); dep.DependsOnID != "external:remote:payments" {
		t.Fatalf("permanent external target = %q", dep.DependsOnID)
	}
	if dep := onlyDependency(t, got, "wisp-source"); dep.DependsOnID != "external:remote:identity" {
		t.Fatalf("wisp external target = %q", dep.DependsOnID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func onlyDependency(t *testing.T, deps map[string][]*types.Dependency, issueID string) *types.Dependency {
	t.Helper()

	got := deps[issueID]
	if len(got) != 1 {
		t.Fatalf("deps[%q] length = %d, want 1: %+v", issueID, len(got), got)
	}
	return got[0]
}

// TestGetDependencyRecordsForIssuesOrdersByDependsOnID asserts the IN-list bulk
// loader requests a total ORDER BY (issue_id, depends_on_id, type) and preserves
// that row order in the returned slice — the property #4749 needs for stable export.
func TestGetDependencyRecordsForIssuesOrdersByDependsOnID(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The FromTable fast path skips PartitionWispIDsInTx, so no partition query.
	now := time.Now()
	depsForIssuesRegex := dependencyRecordsForIssuesQueryRegex("dependencies")
	mock.ExpectQuery(depsForIssuesRegex).
		WithArgs("src").
		WillReturnRows(dependencyRows().
			AddRow("src", "a-target", types.DepBlocks, now, "t", "{}", "").
			AddRow("src", "z-target", types.DepBlocks, now, "t", "{}", ""))

	got, err := GetDependencyRecordsForIssuesFromTableInTx(context.Background(), db, "dependencies", []string{"src"})
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssuesFromTableInTx: %v", err)
	}
	deps := got["src"]
	if len(deps) != 2 {
		t.Fatalf("len = %d, want 2", len(deps))
	}
	if deps[0].DependsOnID != "a-target" || deps[1].DependsOnID != "z-target" {
		t.Fatalf("order = [%q, %q], want [a-target, z-target]", deps[0].DependsOnID, deps[1].DependsOnID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// bd-cif6y4 (#3445): wisp_dependencies is an optional table (dolt_ignore'd,
// dropped when the server's working set is replaced). The dependents listing
// behind `bd show` CHILDREN must treat it as empty like the bulk loaders do;
// an error here makes show.go drop the whole CHILDREN section.
func TestGetDependentsWithMetadataInTxToleratesMissingWispDependencyTable(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT issue_id, type FROM dependencies WHERE`).
		WithArgs("epic-1").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "type"}))
	mock.ExpectQuery(`SELECT issue_id, type FROM wisp_dependencies WHERE`).
		WithArgs("epic-1").
		WillReturnError(errors.New("Error 1146: Table 'db.wisp_dependencies' doesn't exist"))

	if _, err := GetDependentsWithMetadataInTx(context.Background(), tx, "epic-1"); err != nil {
		t.Fatalf("GetDependentsWithMetadataInTx with missing wisp_dependencies: %v (want the table treated as empty)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// bd-uschhs (#5850): an edge whose target has no local row (a cross-repo or
// `external:` target in depends_on_external) must not be silently dropped by
// the dependency listing while every count keeps it.
//
// The triage repro for this issue expected the OLD reader to answer with a
// placeholder entry carrying only the target id and type. The approved design
// (C2) supersedes that shape: a placeholder Issue would leak into every
// issue-only consumer of this reader — sync writers that would push a stub
// outward, traversal that would walk into it, Relations, whose RelatedIssue
// contract excludes placeholders — so the edge is instead reported out-of-band
// as an exact stored record by the combined reader
// GetDependenciesWithUnresolvableInTx, and the old reader keeps its
// issue-only contract. This test pins both halves of that decision: the
// combined read keeps the unresolvable target as the exact stored record
// (with its type and metadata, never dropped), and the old metadata reader
// equals the combined read's resolved portion.
func TestGetDependenciesWithMetadataInTxKeepsUnresolvableTarget(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const target = "external:liveop:liveop-kmf"
	stored := &types.Dependency{
		IssueID: anchor, DependsOnID: target, Type: types.DepTracks,
		CreatedAt: time.Unix(1700000000, 0).UTC(), CreatedBy: "porter",
		Metadata: `{"score":0.9}`, ThreadID: "thread-1",
	}

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, []anchorEdgeRow{{target, "tracks"}}, nil)
	expectEdgeHydration(mock, []string{target}, nil)
	expectUnresolvedRecordBatches(mock, anchor, []string{target},
		unresolvedRecordPlan{recs: map[string]*types.Dependency{target: stored}},
		unresolvedRecordPlan{})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %+v, want none: the target has no row in this database", resolved)
	}
	if len(unresolvable) != 1 {
		t.Fatalf("got %d unresolved records, want 1 for the unresolvable target %q (edge silently dropped)", len(unresolvable), target)
	}
	got := unresolvable[0]
	if got.DependsOnID != target || got.Type != types.DepTracks || got.IssueID != anchor ||
		!got.CreatedAt.Equal(stored.CreatedAt) || got.Metadata != stored.Metadata {
		t.Fatalf("record = %+v, want the exact stored edge {target:%q type:%q}", got, target, types.DepTracks)
	}

	// The old metadata reader equals the combined read's resolved portion: it
	// still answers with real issue rows only.
	_, mock, tx = beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, []anchorEdgeRow{{target, "tracks"}}, nil)
	expectEdgeHydration(mock, []string{target}, nil)
	old, err := GetDependenciesWithMetadataInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadataInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (old reader): %v", err)
	}
	if !reflect.DeepEqual(old, resolved) {
		t.Fatalf("old reader = %+v, want the combined read's resolved portion %+v", old, resolved)
	}
}
