package issueops

import (
	"errors"
	"regexp"
	"slices"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func expectHydratedBody(mock sqlmock.Sqlmock, table, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM " + table + " " + sqlbuild.LeaseJoin(table) + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(issueRows().AddRow(issueRowValues(id, "title of "+id)...))
}

func expectLabelRead(mock sqlmock.Sqlmock, table, id string, labels ...string) {
	rows := sqlmock.NewRows([]string{"label"})
	for _, label := range labels {
		rows.AddRow(label)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM " + table + " WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(rows)
}

func expectDependencyRecordRead(mock sqlmock.Sqlmock, table, id string, targets ...string) {
	rows := sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id"})
	for _, target := range targets {
		rows.AddRow(id, target, string(types.DepBlocks), nil, "tester", nil, nil)
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM " + table + " WHERE issue_id IN (?)")).
		WithArgs(id).
		WillReturnRows(rows)
}

func dependencyTargets(deps []*types.Dependency) []string {
	out := make([]string, len(deps))
	for i, dep := range deps {
		out[i] = dep.DependsOnID
	}
	return out
}

// TestHydrateIssueOperationResultReadsRelationsFromTheBodyPlane pins the
// hydration a lifecycle result gets: the body read reports which table
// answered, its labels come with it, and the outgoing dependency records are
// read from that table's dependency table. Nothing probes the wisps table
// again, and no second label read runs. The scripts hold every statement the
// hydration may send; sqlmock refuses any other.
func TestHydrateIssueOperationResultReadsRelationsFromTheBodyPlane(t *testing.T) {
	t.Run("durable body", func(t *testing.T) {
		// Also the overlapping-ID case: the durable row answers first, so the
		// wisp copy's labels and edges are never read.
		mock, tx, counts := countedMockTx(t)
		expectHydratedBody(mock, "issues", "bd-1")
		expectLabelRead(mock, "labels", "bd-1", "durable-label")
		expectDependencyRecordRead(mock, "dependencies", "bd-1", "bd-target")

		issue, err := HydrateIssueOperationResult(t.Context(), tx, "bd-1", false)
		if err != nil {
			t.Fatalf("HydrateIssueOperationResult: %v", err)
		}
		if !slices.Equal(issue.Labels, []string{"durable-label"}) {
			t.Errorf("labels = %v, want [durable-label]", issue.Labels)
		}
		if got := dependencyTargets(issue.Dependencies); !slices.Equal(got, []string{"bd-target"}) {
			t.Errorf("dependency targets = %v, want [bd-target]", got)
		}
		if n := counts.Total(); n != 3 {
			t.Errorf("hydration sent %d statements, want 3", n)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("wisp-only body whose flags would not route it", func(t *testing.T) {
		// issueRowValues leaves ephemeral and no_history NULL, so the row
		// scans with Ephemeral false: only the table that answered says wisp.
		mock, tx, counts := countedMockTx(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
			WithArgs("bd-w").
			WillReturnRows(issueRows())
		expectHydratedBody(mock, "wisps", "bd-w")
		expectLabelRead(mock, "wisp_labels", "bd-w", "wisp-label")
		expectDependencyRecordRead(mock, "wisp_dependencies", "bd-w", "bd-wisp-target")

		issue, err := HydrateIssueOperationResult(t.Context(), tx, "bd-w", false)
		if err != nil {
			t.Fatalf("HydrateIssueOperationResult: %v", err)
		}
		if issue.Ephemeral {
			t.Fatal("fixture row scanned as ephemeral; the case needs a wisp row its flags would not route")
		}
		if !slices.Equal(issue.Labels, []string{"wisp-label"}) {
			t.Errorf("labels = %v, want [wisp-label]", issue.Labels)
		}
		if got := dependencyTargets(issue.Dependencies); !slices.Equal(got, []string{"bd-wisp-target"}) {
			t.Errorf("dependency targets = %v, want [bd-wisp-target]", got)
		}
		if n := counts.Total(); n != 4 {
			t.Errorf("hydration sent %d statements, want 4", n)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestHydrateIssueOperationResultDependencyReadErrors: a durable body never
// reads wisp_dependencies, so a database without it still hydrates. A wisp
// body on such a database fails as it did before, and an unrelated error on
// the dependency read is returned, not swallowed.
func TestHydrateIssueOperationResultDependencyReadErrors(t *testing.T) {
	t.Run("missing wisp_dependencies with a durable body", func(t *testing.T) {
		mock, tx, _ := countedMockTx(t)
		expectHydratedBody(mock, "issues", "bd-1")
		expectLabelRead(mock, "labels", "bd-1")
		expectDependencyRecordRead(mock, "dependencies", "bd-1")

		issue, err := HydrateIssueOperationResult(t.Context(), tx, "bd-1", false)
		if err != nil {
			t.Fatalf("HydrateIssueOperationResult read a table it had no reason to: %v", err)
		}
		if len(issue.Dependencies) != 0 {
			t.Errorf("dependencies = %v, want none", issue.Dependencies)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing wisp_dependencies with a wisp body", func(t *testing.T) {
		mock, tx, _ := countedMockTx(t)
		mock.ExpectQuery(regexp.QuoteMeta(" FROM issues " + sqlbuild.LeaseJoin("issues"))).
			WillReturnRows(issueRows())
		expectHydratedBody(mock, "wisps", "bd-w")
		expectLabelRead(mock, "wisp_labels", "bd-w")
		gone := tableNotFound("wisp_dependencies")
		mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_dependencies WHERE issue_id IN (?)")).WillReturnError(gone)

		_, err := HydrateIssueOperationResult(t.Context(), tx, "bd-w", false)
		if !errors.Is(err, gone) {
			t.Fatalf("HydrateIssueOperationResult = %v, want the missing-table error", err)
		}
	})

	t.Run("unrelated error on the dependency read", func(t *testing.T) {
		mock, tx, _ := countedMockTx(t)
		expectHydratedBody(mock, "issues", "bd-1")
		expectLabelRead(mock, "labels", "bd-1")
		broken := errors.New("connection reset")
		mock.ExpectQuery(regexp.QuoteMeta("FROM dependencies WHERE issue_id IN (?)")).WillReturnError(broken)

		_, err := HydrateIssueOperationResult(t.Context(), tx, "bd-1", false)
		if !errors.Is(err, broken) {
			t.Fatalf("HydrateIssueOperationResult = %v, want the dependency read's error", err)
		}
	})
}
