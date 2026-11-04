package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func TestValidateCreateIssuesMixedBucketDependenciesRejectsCrossBucketEdges(t *testing.T) {
	regularA := &types.Issue{ID: "test-regular-a", IssueType: types.TypeTask}
	regularB := &types.Issue{ID: "test-regular-b", IssueType: types.TypeTask}
	wispA := &types.Issue{ID: "test-wisp-a", IssueType: types.TypeTask, Ephemeral: true}
	wispB := &types.Issue{ID: "test-wisp-b", IssueType: types.TypeTask, Ephemeral: true}

	tests := []struct {
		name      string
		issues    []*types.Issue
		wantError bool
	}{
		{
			name: "regular to wisp",
			issues: []*types.Issue{
				{
					ID:        regularA.ID,
					IssueType: types.TypeTask,
					Dependencies: []*types.Dependency{{
						DependsOnID: wispA.ID,
						Type:        types.DepBlocks,
					}},
				},
				wispA,
			},
			wantError: true,
		},
		{
			name: "wisp to regular",
			issues: []*types.Issue{
				regularA,
				{
					ID:        wispA.ID,
					IssueType: types.TypeTask,
					Ephemeral: true,
					Dependencies: []*types.Dependency{{
						DependsOnID: regularA.ID,
						Type:        types.DepBlocks,
					}},
				},
			},
			wantError: true,
		},
		{
			name: "same bucket dependencies",
			issues: []*types.Issue{
				regularB,
				{
					ID:        regularA.ID,
					IssueType: types.TypeTask,
					Dependencies: []*types.Dependency{{
						DependsOnID: regularB.ID,
						Type:        types.DepBlocks,
					}},
				},
				wispB,
				{
					ID:        wispA.ID,
					IssueType: types.TypeTask,
					Ephemeral: true,
					Dependencies: []*types.Dependency{{
						DependsOnID: wispB.ID,
						Type:        types.DepBlocks,
					}},
				},
			},
		},
		{
			name: "out of batch target",
			issues: []*types.Issue{
				{
					ID:        regularA.ID,
					IssueType: types.TypeTask,
					Dependencies: []*types.Dependency{{
						DependsOnID: "test-external-wisp",
						Type:        types.DepBlocks,
					}},
				},
				wispA,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateIssuesMixedBucketDependencies(tt.issues)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
					t.Fatalf("error = %v, want cross-bucket dependency", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func TestPersistDependenciesHonorsImportedCreatedBy(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	target := &types.Issue{ID: "target", IssueType: types.TypeTask}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "target",
			Type:        types.DepRelated,
			CreatedBy:   "someone.else",
		}},
	}

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("target").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("target").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("INSERT INTO dependencies").
		WithArgs(depid.New("source", "target"), "source", "target", types.DepRelated, "someone.else", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{target, source}, "current.user", storage.BatchCreateOptions{})
	if err != nil {
		t.Fatalf("PersistDependenciesWithOptionsResult error = %v, want nil", err)
	}
	if !result.ChangedTables["dependencies"] {
		t.Fatalf("ChangedTables = %#v, want dependencies changed", result.ChangedTables)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesDefaultsCreatedByToActor(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	target := &types.Issue{ID: "target", IssueType: types.TypeTask}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "target",
			Type:        types.DepRelated,
		}},
	}

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("target").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("target").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("INSERT INTO dependencies").
		WithArgs(depid.New("source", "target"), "source", "target", types.DepRelated, "current.user", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{target, source}, "current.user", storage.BatchCreateOptions{})
	if err != nil {
		t.Fatalf("PersistDependenciesWithOptionsResult error = %v, want nil", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesClassifiesBareCrossPrefixTargetAsExternal(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	source := &types.Issue{
		ID:        "sym-3su",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "mkt-456",
			Type:        types.DepRelated,
		}},
	}
	var skipped []string

	// A bare target with a different issue prefix is external. In particular,
	// persistence must not probe either local target table before this insert.
	mock.ExpectExec("INSERT INTO dependencies \\(id, issue_id, depends_on_external").
		WithArgs(depid.New("sym-3su", "mkt-456"), "sym-3su", "mkt-456", types.DepRelated, "tester", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{source}, "tester", storage.BatchCreateOptions{
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped = append(skipped, issueID+" -> "+dependsOnID+": "+reason)
		},
	})
	if err != nil {
		t.Fatalf("PersistDependenciesWithOptionsResult error = %v, want nil", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %#v, want none", skipped)
	}
	if !result.ChangedTables["dependencies"] {
		t.Fatalf("ChangedTables = %#v, want dependencies changed", result.ChangedTables)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesReturnsTargetLookupErrors(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	targetErr := errors.New("target lookup failed")
	issue := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "target",
			Type:        types.DepBlocks,
		}},
	}

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("target").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("target").
		WillReturnError(targetErr)

	err := PersistDependencies(ctx, tx, []*types.Issue{issue}, "tester")
	if err == nil || !strings.Contains(err.Error(), "failed to check dependency target target for source") {
		t.Fatalf("error = %v, want dependency target lookup error", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesSkipsValidationErrorsWhenConfigured(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	issue := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "source",
			Type:        types.DepBlocks,
		}},
	}
	var skipped []string

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("source").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("source").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	result, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped = append(skipped, issueID+" -> "+dependsOnID+": "+reason)
		},
	})
	if err != nil {
		t.Fatalf("PersistDependenciesWithOptionsResult error = %v, want nil", err)
	}
	if len(result.ChangedTables) != 0 {
		t.Fatalf("ChangedTables = %#v, want none", result.ChangedTables)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "source -> source") ||
		!strings.Contains(skipped[0], "cannot depend on itself") {
		t.Fatalf("skipped = %#v, want self-dependency detail", skipped)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesRejectsHierarchyBlocking(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	issue := &types.Issue{
		ID:        "child",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "parent",
			Type:        types.DepConditionalBlocks,
		}},
	}

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("parent").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("parent").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("child", "parent").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("error = %v, want ancestor hierarchy rejection", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesValidatesPlannedHierarchyBeforeBlocking(t *testing.T) {
	// This case pins the per-edge read sequence; its batch-lookup twin is
	// TestPersistDependenciesBatchValidatesPlannedHierarchyBeforeBlocking.
	t.Cleanup(DisableCreateFastPathsForTest())
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	child := &types.Issue{
		ID:        "bd-child",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{DependsOnID: "bd-grand", Type: types.DepBlocks}, // Deliberately first.
			{DependsOnID: "bd-parent", Type: types.DepParentChild},
		},
	}
	parent := &types.Issue{
		ID:        "bd-parent",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "bd-grand",
			Type:        types.DepParentChild,
		}},
	}

	for _, pair := range [][2]string{{"bd-child", "bd-parent"}, {"bd-parent", "bd-grand"}} {
		mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
			WithArgs(pair[1]).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
			WithArgs(pair[1]).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery("WITH RECURSIVE reachable").
			WithArgs(pair[1], pair[0]).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec("INSERT INTO dependencies").
			WithArgs(depid.New(pair[0], pair[1]), pair[0], pair[1], types.DepParentChild, "tester", sqlmock.AnyArg(), "{}", "").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("REPLACE INTO local_metadata").
			WithArgs(dependencyCoordinationKey(pair[1], dependencyCoordinationDurableTier), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("bd-grand").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("bd-grand").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("bd-child", "bd-grand").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{child, parent}, "tester", storage.BatchCreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("error = %v, want planned-ancestor rejection", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPersistDependenciesSkipsHierarchyValidationAcrossPrefixes(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	issue := &types.Issue{
		ID:        "aa-source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "bb-target",
			Type:        types.DepBlocks,
		}},
	}

	// No target or ancestors query: target existence and hierarchy cannot be
	// validated locally across rig prefixes.
	mock.ExpectQuery("WITH RECURSIVE reachable").
		WithArgs("bb-target", "aa-source").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO dependencies \\(id, issue_id, depends_on_external").
		WithArgs(depid.New("aa-source", "bb-target"), "aa-source", "bb-target", types.DepBlocks, "tester", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{})
	if err != nil {
		t.Fatalf("cross-prefix blocking dependency: %v", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The mock half of the missing-parent skip: it pins the STATEMENTS (no counter
// read, no upsert). Its real-backend twin is the conformance case
// ReconcileSkipsMissingParentCounter (backend/conformance/portable.go), which
// pins what a live engine does with the same skip on both Dolt legs.
func TestReconcileChildCountersSkipsMissingParent(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	mock.ExpectQuery("SELECT 1 FROM wisps LIMIT 1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("test-deleted-parent").
		WillReturnError(sql.ErrNoRows)

	changed, err := ReconcileChildCounters(ctx, tx, []*types.Issue{{
		ID:        "test-deleted-parent.7",
		IssueType: types.TypeTask,
	}})
	if err != nil {
		t.Fatalf("ReconcileChildCounters error = %v, want nil", err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed tables = %#v, want none", changed)
	}

	// No counter SELECT or upsert is expected after the missing-parent lookup.
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReconcileChildCountersReturnsParentLookupError(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	lookupErr := errors.New("parent lookup failed")

	mock.ExpectQuery("SELECT 1 FROM wisps LIMIT 1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("test-parent").
		WillReturnError(lookupErr)

	_, err := ReconcileChildCounters(ctx, tx, []*types.Issue{{
		ID:        "test-parent.1",
		IssueType: types.TypeTask,
	}})
	if err == nil || !strings.Contains(err.Error(), "failed to check child counter parent test-parent") || !errors.Is(err, lookupErr) {
		t.Fatalf("error = %v, want contextual parent lookup error", err)
	}

	// A lookup failure must not be mistaken for an absent parent or reach the
	// counter table.
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReconcileChildCountersReturnsWispLookupError(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	lookupErr := errors.New("wisp lookup failed")

	mock.ExpectQuery("SELECT 1 FROM wisps LIMIT 1").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("SELECT id FROM wisps WHERE id IN \\(\\?\\)").
		WithArgs("test-parent").
		WillReturnError(lookupErr)

	_, err := ReconcileChildCounters(ctx, tx, []*types.Issue{{
		ID:        "test-parent.1",
		IssueType: types.TypeTask,
	}})
	if err == nil || !strings.Contains(err.Error(), "failed to route child counter parents") || !errors.Is(err, lookupErr) {
		t.Fatalf("error = %v, want contextual wisp lookup error", err)
	}

	// A failed wisp lookup must stop routing before any issues or counter query.
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPersistDependenciesBatchValidatesPlannedHierarchyBeforeBlocking is the
// batch-lookup twin of TestPersistDependenciesValidatesPlannedHierarchyBeforeBlocking:
// routing and target presence come from one batch read each, edges are read
// only for the batch's own endpoints, and the planned ancestry the
// parent-child phase wrote is what rejects the blocks edge — with no per-edge
// recursive query.
func TestPersistDependenciesBatchValidatesPlannedHierarchyBeforeBlocking(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	child := &types.Issue{
		ID:        "bd-child",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{DependsOnID: "bd-grand", Type: types.DepBlocks}, // Deliberately first.
			{DependsOnID: "bd-parent", Type: types.DepParentChild},
		},
	}
	parent := &types.Issue{
		ID:        "bd-parent",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "bd-grand",
			Type:        types.DepParentChild,
		}},
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id IN (?,?)")).
		WithArgs("bd-grand", "bd-parent").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-grand").AddRow("bd-parent"))
	ids := []driver.Value{"bd-child", "bd-grand", "bd-parent"}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE issue_id IN (?,?,?)")).
			WithArgs(ids...).
			WillReturnRows(sqlmock.NewRows([]string{"issue_id", "target", "type"}))
	}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE " + col + " IN (?,?,?)")).
				WithArgs(ids...).
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "target", "type"}))
		}
	}
	for _, pair := range [][2]string{{"bd-child", "bd-parent"}, {"bd-parent", "bd-grand"}} {
		mock.ExpectExec("INSERT INTO dependencies").
			WithArgs(depid.New(pair[0], pair[1]), pair[0], pair[1], types.DepParentChild, "tester", sqlmock.AnyArg(), "{}", "").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("REPLACE INTO local_metadata").
			WithArgs(dependencyCoordinationKey(pair[1], dependencyCoordinationDurableTier), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}

	_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{child, parent}, "tester", storage.BatchCreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("error = %v, want planned-ancestor rejection", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A failed walk query is not a validation refusal: under the import mode
// (SkipDependencyValidationErrors) the error must abort the pass rather than be
// recorded as a skipped edge. This covers both checks on the per-edge
// statements (a one-edge batch); the batch graph's twin is
// TestPersistDependenciesBatchTolerantReturnsValidationQueryErrors.
func TestPersistDependenciesTolerantReturnsValidationQueryErrors(t *testing.T) {
	walkErr := errors.New("pq: aborted walk (simulated)")
	cases := []struct {
		name    string
		depType types.DependencyType
		walk    string
		args    []driver.Value
		wantMsg string
	}{
		{"hierarchy", types.DepBlocks, "WITH RECURSIVE ancestors", []driver.Value{"bd-child", "bd-parent"}, "failed to check blocker ancestry"},
		// parent-child skips the hierarchy check, so its first walk is the cycle check.
		{"cycle", types.DepParentChild, "WITH RECURSIVE reachable", []driver.Value{"bd-parent", "bd-child"}, "failed to check for dependency cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			issue := &types.Issue{
				ID:        "bd-child",
				IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{{
					DependsOnID: "bd-parent",
					Type:        tc.depType,
				}},
			}
			mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
				WithArgs("bd-parent").
				WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
				WithArgs("bd-parent").
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(tc.walk).WithArgs(tc.args...).WillReturnError(walkErr)

			var skipped []string
			_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{
				SkipDependencyValidationErrors: true,
				OnSkippedDependency: func(issueID, dependsOnID, reason string) {
					skipped = append(skipped, issueID+" -> "+dependsOnID+": "+reason)
				},
			})
			if !errors.Is(err, walkErr) || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error = %v, want the walk error (%q)", err, tc.wantMsg)
			}
			if len(skipped) != 0 {
				t.Fatalf("skipped = %#v, want none: a query error is not a validation refusal", skipped)
			}

			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestIsDependencyValidationRefusal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"cycle", domain.ErrDependencyCycle, true},
		{"wrapped self", fmt.Errorf("%w: x cannot depend on itself", domain.ErrSelfDependency), true},
		{"hierarchy", &domain.DependencyHierarchyConflictError{IssueID: "a", BlockerID: "b"}, true},
		{"wrapped hierarchy", fmt.Errorf("edge: %w", &domain.DependencyHierarchyConflictError{IssueID: "a", BlockerID: "b", BlockerIsAncestor: true}), true},
		{"ancestry query", fmt.Errorf("failed to check blocker ancestry: %w", errors.New("42P19")), false},
		{"cycle query", fmt.Errorf("failed to check for dependency cycle: %w", sql.ErrConnDone), false},
		{"graph read", fmt.Errorf("failed to check for dependency cycle: load dependency edges from dependencies: %w", context.DeadlineExceeded), false},
	}
	for _, tc := range cases {
		if got := isDependencyValidationRefusal(tc.err); got != tc.want {
			t.Errorf("%s: isDependencyValidationRefusal(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestPersistDependenciesBatchTolerantReturnsValidationQueryErrors is the
// batch-lookup twin of TestPersistDependenciesTolerantReturnsValidationQueryErrors.
// With two scheduling edges the walks run on depGraph, which reads a node's
// edges the first time a search reaches it. A stored edge leads each walk off
// the batch's endpoints, and that read fails. The failure must abort the pass:
// load has already marked the node as read, so a pass that carried on would
// answer every later walk through that node from an empty edge set.
func TestPersistDependenciesBatchTolerantReturnsValidationQueryErrors(t *testing.T) {
	walkErr := errors.New("pq: aborted walk (simulated)")
	cases := []struct {
		name    string
		stored  []driver.Value // issue_id, target, type: the edge the walk follows off the endpoints
		wantMsg string
	}{
		// bd-a's stored parent: the ancestry walk from bd-a reads bd-p's edges.
		{"hierarchy", []driver.Value{"bd-a", "bd-p", string(types.DepParentChild)}, "failed to check blocker ancestry"},
		// bd-b's stored blocker: the cycle walk from bd-b reads bd-x's edges.
		{"cycle", []driver.Value{"bd-b", "bd-x", string(types.DepBlocks)}, "failed to check for dependency cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			issue := &types.Issue{
				ID:        "bd-a",
				IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{
					{DependsOnID: "bd-b", Type: types.DepBlocks},
					{DependsOnID: "bd-c", Type: types.DepBlocks},
				},
			}
			expectDepBatchReads(mock, []driver.Value{"bd-b", "bd-c"}, []driver.Value{"bd-a", "bd-b", "bd-c"}, tc.stored)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM dependencies WHERE issue_id IN (?)")).
				WithArgs(tc.stored[1]).
				WillReturnError(walkErr)

			var skipped []string
			_, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", storage.BatchCreateOptions{
				SkipDependencyValidationErrors: true,
				OnSkippedDependency: func(issueID, dependsOnID, reason string) {
					skipped = append(skipped, issueID+" -> "+dependsOnID+": "+reason)
				},
			})
			if !errors.Is(err, walkErr) || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error = %v, want the walk error (%q)", err, tc.wantMsg)
			}
			if len(skipped) != 0 {
				t.Fatalf("skipped = %#v, want none: a query error is not a validation refusal", skipped)
			}

			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// The import mode still tolerates every typed refusal the walks raise, on both
// lookup shapes: the edge is reported as skipped and the pass goes on. This
// pins the other side of isDependencyValidationRefusal. The self-dependency
// case is TestPersistDependenciesSkipsValidationErrorsWhenConfigured.
func TestPersistDependenciesTolerantSkipsTypedRefusals(t *testing.T) {
	tolerant := func(skipped *[]string) storage.BatchCreateOptions {
		return storage.BatchCreateOptions{
			SkipDependencyValidationErrors: true,
			OnSkippedDependency: func(issueID, dependsOnID, reason string) {
				*skipped = append(*skipped, issueID+" -> "+dependsOnID+": "+reason)
			},
		}
	}
	finish := func(t *testing.T, mock sqlmock.Sqlmock, tx *sql.Tx) {
		t.Helper()
		mock.ExpectRollback()
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	}

	type walk struct {
		query string
		args  []driver.Value
		count int
	}
	ancestors := func(node, candidate string, count int) walk {
		return walk{"WITH RECURSIVE ancestors", []driver.Value{node, candidate}, count}
	}
	// The per-edge statements: one blocks edge bd-a -> bd-b.
	for _, tc := range []struct {
		name  string
		walks []walk
		want  string
	}{
		{"ancestor", []walk{ancestors("bd-a", "bd-b", 1)}, "bd-a cannot be blocked by its ancestor bd-b"},
		{"descendant", []walk{ancestors("bd-a", "bd-b", 0), ancestors("bd-b", "bd-a", 1)}, "bd-a cannot be blocked by its descendant bd-b"},
		{"cycle", []walk{
			ancestors("bd-a", "bd-b", 0), ancestors("bd-b", "bd-a", 0),
			{"WITH RECURSIVE reachable", []driver.Value{"bd-b", "bd-a"}, 1},
		}, domain.ErrDependencyCycle.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, mock, tx := beginMockTx(t)
			defer db.Close()
			issue := &types.Issue{
				ID:           "bd-a",
				IssueType:    types.TypeTask,
				Dependencies: []*types.Dependency{{DependsOnID: "bd-b", Type: types.DepBlocks}},
			}
			mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
				WithArgs("bd-b").
				WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
				WithArgs("bd-b").
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			for _, w := range tc.walks {
				mock.ExpectQuery(w.query).WithArgs(w.args...).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(w.count))
			}

			var skipped []string
			if _, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{issue}, "tester", tolerant(&skipped)); err != nil {
				t.Fatalf("error = %v, want the refusal skipped", err)
			}
			if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "bd-a -> bd-b: "+tc.want) {
				t.Fatalf("skipped = %#v, want bd-a -> bd-b: %s", skipped, tc.want)
			}
			finish(t, mock, tx)
		})
	}

	// The batch graph: the planned ancestry of
	// TestPersistDependenciesBatchValidatesPlannedHierarchyBeforeBlocking, whose
	// blocks edge is skipped here instead of failing the batch.
	t.Run("batch ancestor", func(t *testing.T) {
		ctx := context.Background()
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		child := &types.Issue{
			ID:        "bd-child",
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: "bd-grand", Type: types.DepBlocks},
				{DependsOnID: "bd-parent", Type: types.DepParentChild},
			},
		}
		parent := &types.Issue{
			ID:           "bd-parent",
			IssueType:    types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "bd-grand", Type: types.DepParentChild}},
		}
		expectDepBatchReads(mock, []driver.Value{"bd-grand", "bd-parent"}, []driver.Value{"bd-child", "bd-grand", "bd-parent"})
		for _, pair := range [][2]string{{"bd-child", "bd-parent"}, {"bd-parent", "bd-grand"}} {
			mock.ExpectExec("INSERT INTO dependencies").
				WithArgs(depid.New(pair[0], pair[1]), pair[0], pair[1], types.DepParentChild, "tester", sqlmock.AnyArg(), "{}", "").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("REPLACE INTO local_metadata").
				WithArgs(dependencyCoordinationKey(pair[1], dependencyCoordinationDurableTier), sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
		}

		var skipped []string
		if _, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{child, parent}, "tester", tolerant(&skipped)); err != nil {
			t.Fatalf("error = %v, want the refusal skipped", err)
		}
		if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "bd-child -> bd-grand: bd-child cannot be blocked by its ancestor bd-grand") {
			t.Fatalf("skipped = %#v, want the planned-ancestor refusal of bd-child -> bd-grand", skipped)
		}
		finish(t, mock, tx)
	})

	// The batch graph: an in-batch cycle. The first edge lands, and the graph
	// that now holds it refuses the second.
	t.Run("batch cycle", func(t *testing.T) {
		ctx := context.Background()
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		a := &types.Issue{
			ID:           "bd-a",
			IssueType:    types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "bd-b", Type: types.DepBlocks}},
		}
		b := &types.Issue{
			ID:           "bd-b",
			IssueType:    types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "bd-a", Type: types.DepBlocks}},
		}
		expectDepBatchReads(mock, []driver.Value{"bd-b", "bd-a"}, []driver.Value{"bd-a", "bd-b"})
		mock.ExpectExec("INSERT INTO dependencies").
			WithArgs(depid.New("bd-a", "bd-b"), "bd-a", "bd-b", types.DepBlocks, "tester", sqlmock.AnyArg(), "{}", "").
			WillReturnResult(sqlmock.NewResult(0, 1))

		var skipped []string
		if _, err := PersistDependenciesWithOptionsResult(ctx, tx, []*types.Issue{a, b}, "tester", tolerant(&skipped)); err != nil {
			t.Fatalf("error = %v, want the refusal skipped", err)
		}
		if want := "bd-b -> bd-a: " + domain.ErrDependencyCycle.Error(); len(skipped) != 1 || skipped[0] != want {
			t.Fatalf("skipped = %#v, want [%q]", skipped, want)
		}
		finish(t, mock, tx)
	})
}

// expectDepBatchReads expects newDepBatchLookups' reads for a batch with no
// wisps: the wisps probe, the presence of issueTargets in issues, and the edges
// of ids out of (issue_id) and into (each target column) both edge tables.
// The outgoing read of dependencies returns the stored rows.
func expectDepBatchReads(mock sqlmock.Sqlmock, issueTargets, ids []driver.Value, stored ...[]driver.Value) {
	in := func(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(sql.ErrNoRows)
	present := sqlmock.NewRows([]string{"id"})
	for _, id := range issueTargets {
		present.AddRow(id)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id IN (" + in(len(issueTargets)) + ")")).
		WithArgs(issueTargets...).
		WillReturnRows(present)
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		rows := sqlmock.NewRows([]string{"issue_id", "target", "type"})
		if table == "dependencies" {
			for _, row := range stored {
				rows.AddRow(row...)
			}
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE issue_id IN (" + in(len(ids)) + ")")).
			WithArgs(ids...).
			WillReturnRows(rows)
	}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE " + col + " IN (" + in(len(ids)) + ")")).
				WithArgs(ids...).
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "target", "type"}))
		}
	}
}
