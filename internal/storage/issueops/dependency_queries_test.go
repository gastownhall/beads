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

func allDependencyRecordsQueryRegex(table string) string {
	return `(?s)SELECT issue_id, COALESCE\(depends_on_issue_id, depends_on_wisp_id, depends_on_external\) AS depends_on_id, type, created_at, created_by, metadata, thread_id\s+FROM ` +
		regexp.QuoteMeta(table) + `\s+ORDER BY issue_id`
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

func onlyDependency(t *testing.T, deps map[string][]*types.Dependency, issueID string) *types.Dependency {
	t.Helper()

	got := deps[issueID]
	if len(got) != 1 {
		t.Fatalf("deps[%q] length = %d, want 1: %+v", issueID, len(got), got)
	}
	return got[0]
}
