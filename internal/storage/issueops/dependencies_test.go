package issueops

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCheckDependencyCycleInTxSelfDependencyIsWrapPreserving proves the
// self-dependency guard now returns a typed sentinel (errors.Is-able) while its
// user-facing message text stays byte-identical to the pre-taxonomy string.
func TestCheckDependencyCycleInTxSelfDependencyIsWrapPreserving(t *testing.T) {
	t.Parallel()

	_, _, tx := beginMockTx(t)
	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-a", Type: types.DepBlocks}

	err := CheckDependencyCycleInTx(context.Background(), tx, dep, nil)
	if err == nil {
		t.Fatal("CheckDependencyCycleInTx(self-dep) = nil, want error")
	}
	if !errors.Is(err, domain.ErrSelfDependency) {
		t.Errorf("errors.Is(err, domain.ErrSelfDependency) = false, want true; err = %v", err)
	}
	// Byte-identical to the pre-taxonomy message: the sentinel is the static
	// prefix rendered by %w, the rest is unchanged.
	const want = "cannot add self-dependency: dep-a cannot depend on itself"
	if err.Error() != want {
		t.Errorf("message = %q, want byte-identical %q", err.Error(), want)
	}
}

// TestCheckDependencyCycleInTxCycleIsWrapPreserving proves the cycle guard now
// returns a typed sentinel while its user-facing message text stays
// byte-identical to the pre-taxonomy string.
func TestCheckDependencyCycleInTxCycleIsWrapPreserving(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-b", Type: types.DepBlocks}

	// WouldCreateSchedulingCycleInTx queries reachability with (dependsOnID, issueID).
	mock.ExpectQuery("WITH RECURSIVE reachable").
		WithArgs("dep-b", "dep-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	err := CheckDependencyCycleInTx(context.Background(), tx, dep, nil)
	if err == nil {
		t.Fatal("CheckDependencyCycleInTx(cycle) = nil, want error")
	}
	if !errors.Is(err, domain.ErrDependencyCycle) {
		t.Errorf("errors.Is(err, domain.ErrDependencyCycle) = false, want true; err = %v", err)
	}
	// Byte-identical to the pre-taxonomy message (the bare sentinel text).
	const want = "adding dependency would create a cycle"
	if err.Error() != want {
		t.Errorf("message = %q, want byte-identical %q", err.Error(), want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestReplaceDependencyTargetNormalizesTargetColumns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		targetCol    string
		rowIssue     any
		rowWisp      any
		wantIssue    any
		wantWisp     any
		wantExternal any
	}{
		{
			name:         "issue target clears stale wisp target",
			targetCol:    "depends_on_issue_id",
			rowIssue:     nil,
			rowWisp:      "old-target",
			wantIssue:    "new-target",
			wantWisp:     nil,
			wantExternal: nil,
		},
		{
			name:         "wisp target clears stale issue target",
			targetCol:    "depends_on_wisp_id",
			rowIssue:     "old-target",
			rowWisp:      nil,
			wantIssue:    nil,
			wantWisp:     "new-target",
			wantExternal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dependencies a")).
				WithArgs("new-target", "new-target", "new-target").
				WillReturnRows(sqlmock.NewRows([]string{"found"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id")).
				WithArgs("old-target", "old-target").
				WillReturnRows(sqlmock.NewRows([]string{
					"issue_id",
					"depends_on_issue_id",
					"depends_on_wisp_id",
					"depends_on_external",
					"type",
					"created_at",
					"created_by",
					"metadata",
					"thread_id",
				}).AddRow("source", tt.rowIssue, tt.rowWisp, nil, "blocks", nil, "tester", "{}", "thread-1"))
			mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dependencies")).
				WithArgs("old-target", "old-target").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)")).
				WithArgs(depid.New("source", "new-target"), "source", tt.wantIssue, tt.wantWisp, tt.wantExternal, "blocks", nil, "tester", "{}", "thread-1").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			if err := replaceDependencyTargetInTx(context.Background(), tx, "dependencies", tt.targetCol, "old-target", "new-target"); err != nil {
				_ = tx.Rollback()
				t.Fatalf("replaceDependencyTargetInTx: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestCycleDetectionTablesUseBothTablesByDefault(t *testing.T) {
	got := cycleDetectionTables()
	want := []string{"dependencies", "wisp_dependencies"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCycleReachabilityQuerySingleTableJoinsDirectly(t *testing.T) {
	query := cycleReachabilityQuery([]string{"wisp_dependencies"})
	if !strings.Contains(query, "JOIN wisp_dependencies d ON d.issue_id = r.node") {
		t.Fatalf("query does not join wisp_dependencies directly:\n%s", query)
	}
	if strings.Contains(query, "JOIN (SELECT") {
		t.Fatalf("single-table cycle query should not materialize a derived dependency table:\n%s", query)
	}
	if !strings.Contains(query, "d.type IN ('blocks', 'conditional-blocks', 'parent-child')") {
		t.Fatalf("query does not filter scheduling-relevant dependency types at the direct join:\n%s", query)
	}
	if strings.Contains(query, "UNION ALL") || strings.Contains(query, "depth") {
		t.Fatalf("cycle query should traverse unique nodes, not enumerate paths:\n%s", query)
	}
}

func TestCycleReachabilityQueryMultipleTablesTraversesUniqueNodes(t *testing.T) {
	query := cycleReachabilityQuery([]string{"dependencies", "wisp_dependencies"})
	if strings.Contains(query, "UNION ALL") || strings.Contains(query, "depth") {
		t.Fatalf("multi-table cycle query should traverse unique nodes, not enumerate paths:\n%s", query)
	}
	if !strings.Contains(query, "FROM dependencies") {
		t.Fatalf("query does not include dependencies table:\n%s", query)
	}
	if !strings.Contains(query, "FROM wisp_dependencies") {
		t.Fatalf("query does not include wisp_dependencies table:\n%s", query)
	}
	if !strings.Contains(query, DepTargetExpr) {
		t.Fatalf("query does not resolve depends_on_id via DepTargetExpr:\n%s", query)
	}
}
