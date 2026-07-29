package issueops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
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

func TestFilterCreateIssuesMixedBucketDependenciesSkipsWhenConfigured(t *testing.T) {
	regular := &types.Issue{
		ID:        "test-regular-source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "test-wisp-target",
			Type:        types.DepBlocks,
		}},
	}
	wisp := &types.Issue{
		ID:        "test-wisp-target",
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	var skipped []string

	filtered, err := filterCreateIssuesMixedBucketDependencies([]*types.Issue{regular, wisp}, storage.BatchCreateOptions{
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped = append(skipped, issueID+" -> "+dependsOnID+": "+reason)
		},
	})
	if err != nil {
		t.Fatalf("filterCreateIssuesMixedBucketDependencies error = %v, want nil", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("len(filtered) = %d, want 2", len(filtered))
	}
	if len(filtered[0].Dependencies) != 0 {
		t.Fatalf("filtered[0].Dependencies = %#v, want none", filtered[0].Dependencies)
	}
	if len(regular.Dependencies) != 1 {
		t.Fatalf("regular.Dependencies was mutated to %#v, want original dependency preserved", regular.Dependencies)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "test-regular-source -> test-wisp-target") ||
		!strings.Contains(skipped[0], "cross-bucket dependency") {
		t.Fatalf("skipped = %#v, want cross-bucket dependency detail", skipped)
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
		WithArgs(depid.New("source", "target"), "source", "target", types.DepRelated, "someone.else", sqlmock.AnyArg()).
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
		WithArgs(depid.New("source", "target"), "source", "target", types.DepRelated, "current.user", sqlmock.AnyArg()).
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
		WithArgs(depid.New("sym-3su", "mkt-456"), "sym-3su", "mkt-456", types.DepRelated, "tester", sqlmock.AnyArg()).
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
			WithArgs(depid.New(pair[0], pair[1]), pair[0], pair[1], types.DepParentChild, "tester", sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
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
		WithArgs(depid.New("aa-source", "bb-target"), "aa-source", "bb-target", types.DepBlocks, "tester", sqlmock.AnyArg()).
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
