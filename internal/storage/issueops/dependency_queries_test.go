package issueops

import (
	"context"
	"errors"
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

// externalBlockingUnionQueryRegex matches the one statement that reads the
// external blockers of both dependency tables: the durable leg tagged plane 0,
// the wisp leg tagged plane 1, ordered so an issue's durable edges come first.
func externalBlockingUnionQueryRegex() string {
	leg := func(table, plane string) string {
		return `SELECT issue_id, COALESCE\(depends_on_issue_id, depends_on_wisp_id, depends_on_external\) AS depends_on_id, type, created_at, created_by, metadata, thread_id, ` +
			plane + ` AS plane\s+FROM ` + regexp.QuoteMeta(table) +
			`\s+WHERE depends_on_external LIKE 'external:%'\s+AND type IN \('blocks', 'conditional-blocks', 'waits-for'\)`
	}
	return `(?s)` + leg("dependencies", "0") + `\s+UNION ALL\s+` + leg("wisp_dependencies", "1") + `\s+ORDER BY issue_id, plane\s*$`
}

func externalBlockingUnionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id", "plane",
	})
}

// The external-blocker read is one statement over both dependency tables. It
// used to be one statement per table, each a round trip.
func TestGetExternalBlockingDependencyRecordsInTxReadsPermanentAndWispEdges(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	now := time.Now()
	mock.ExpectQuery(externalBlockingUnionQueryRegex()).
		WillReturnRows(externalBlockingUnionRows().
			AddRow("perm-source", "external:remote:payments", types.DepBlocks, now, "tester", "{}", "", 0).
			// A duplicate edge stays a duplicate: the read reports rows, it
			// does not collapse them.
			AddRow("perm-source", "external:remote:payments", types.DepBlocks, now, "tester", "{}", "", 0).
			AddRow("shared", "external:remote:billing", types.DepConditionalBlocks, now, "tester", "{}", "", 0).
			AddRow("shared", "external:remote:billing", types.DepWaitsFor, now, "tester", "{}", "", 1).
			AddRow("wisp-source", "external:remote:identity", types.DepWaitsFor, now, "tester", "{}", "", 1))

	got, err := GetExternalBlockingDependencyRecordsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecordsInTx: %v", err)
	}
	if deps := got["perm-source"]; len(deps) != 2 || deps[0].DependsOnID != "external:remote:payments" || deps[1].DependsOnID != "external:remote:payments" {
		t.Fatalf("permanent external edges = %+v, want the duplicate kept", deps)
	}
	if deps := got["shared"]; len(deps) != 2 || deps[0].Type != types.DepConditionalBlocks || deps[1].Type != types.DepWaitsFor {
		t.Fatalf("shared external edges = %+v, want the durable edge then the wisp edge", deps)
	}
	if dep := onlyDependency(t, got, "wisp-source"); dep.DependsOnID != "external:remote:identity" {
		t.Fatalf("wisp external target = %q", dep.DependsOnID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// wisp_dependencies is optional (dolt_ignore'd, dropped when the server's
// working set is replaced). Without it the one statement fails as a whole, so
// the read falls back to the durable table alone, in the same transaction and so
// the same snapshot in which the table was missing.
func TestGetExternalBlockingDependencyRecordsInTxFallsBackWithoutWispDependencies(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(externalBlockingUnionQueryRegex()).
		WillReturnError(errors.New("Error 1146: Table 'db.wisp_dependencies' doesn't exist"))
	mock.ExpectQuery(externalBlockingDependencyRecordsQueryRegex("dependencies")).
		WillReturnRows(dependencyRows().AddRow(
			"perm-source", "external:remote:payments", types.DepBlocks, time.Now(), "tester", "{}", "",
		))

	got, err := GetExternalBlockingDependencyRecordsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("GetExternalBlockingDependencyRecordsInTx: %v", err)
	}
	if dep := onlyDependency(t, got, "perm-source"); dep.DependsOnID != "external:remote:payments" {
		t.Fatalf("permanent external target = %q", dep.DependsOnID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Any other failure of the one statement is the read's failure: it is not
// mistaken for a missing optional table, and no second statement runs.
func TestGetExternalBlockingDependencyRecordsInTxReturnsOtherErrors(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	lost := errors.New("connection reset by peer")
	mock.ExpectQuery(externalBlockingUnionQueryRegex()).WillReturnError(lost)

	if _, err := GetExternalBlockingDependencyRecordsInTx(context.Background(), tx); !errors.Is(err, lost) {
		t.Fatalf("GetExternalBlockingDependencyRecordsInTx = %v, want the statement's error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A missing durable dependencies table is not tolerated, and it is not mistaken
// for the optional one: the read reports it without a second statement. A
// fallback here could succeed against a dependencies table a peer recreated in
// between, and answer without the wisp plane.
func TestGetExternalBlockingDependencyRecordsInTxRequiresTheDurableTable(t *testing.T) {
	t.Parallel()

	for _, missing := range []error{
		errors.New("Error 1146: Table 'db.dependencies' doesn't exist"),
		// A table-not-found error that names no table is not known to be the
		// optional one either.
		errors.New("Error 1146"),
	} {
		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(externalBlockingUnionQueryRegex()).WillReturnError(missing)

		if _, err := GetExternalBlockingDependencyRecordsInTx(context.Background(), tx); !errors.Is(err, missing) {
			t.Fatalf("GetExternalBlockingDependencyRecordsInTx = %v, want %v", err, missing)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	}
}

// The one-statement read used outside a transaction never falls back by itself:
// it reports the missing optional table so the caller can re-read both planes
// inside one transaction.
func TestExternalBlockingDependencyRecordsOneStatementReportsAMissingWispTable(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(externalBlockingUnionQueryRegex()).
		WillReturnError(errors.New("Error 1146 (42S02): table not found: wisp_dependencies"))

	got, err := ExternalBlockingDependencyRecordsOneStatement(context.Background(), db)
	if !IsMissingWispDependencies(err) {
		t.Fatalf("ExternalBlockingDependencyRecordsOneStatement = %v, %v; want the missing wisp_dependencies error", got, err)
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
