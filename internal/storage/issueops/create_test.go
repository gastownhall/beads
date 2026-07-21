package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

type utcTimeBetween struct {
	before time.Time
	after  time.Time
}

func (m utcTimeBetween) Match(value driver.Value) bool {
	timestamp, ok := value.(time.Time)
	return ok && timestamp.Location() == time.UTC && !timestamp.Before(m.before) && !timestamp.After(m.after)
}

func offsetTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", value, err)
	}
	return parsed
}

func TestPrepareIssueForInsertNormalizesTimestampsToUTC(t *testing.T) {
	createdAt := offsetTime(t, "2026-07-11T18:40:00+10:00")
	startedAt := offsetTime(t, "2026-07-11T18:41:00+10:00")
	closedAt := offsetTime(t, "2026-07-11T18:42:00+10:00")
	leaseExpiresAt := offsetTime(t, "2026-07-11T18:43:00+10:00")
	heartbeatAt := offsetTime(t, "2026-07-11T18:44:00+10:00")
	dueAt := offsetTime(t, "2026-07-11T18:45:00+10:00")
	deferUntil := offsetTime(t, "2026-07-11T18:46:00+10:00")
	compactedAt := offsetTime(t, "2026-07-11T18:47:00+10:00")
	issue := &types.Issue{
		ID: "test-utc", Title: "UTC", Status: types.StatusClosed, IssueType: types.TypeTask,
		CreatedAt: createdAt, UpdatedAt: createdAt, StartedAt: &startedAt, ClosedAt: &closedAt,
		LeaseExpiresAt: &leaseExpiresAt, HeartbeatAt: &heartbeatAt, DueAt: &dueAt,
		DeferUntil: &deferUntil, CompactedAt: &compactedAt,
	}

	if err := PrepareIssueForInsert(issue, nil, nil); err != nil {
		t.Fatalf("PrepareIssueForInsert: %v", err)
	}
	values := []time.Time{issue.CreatedAt, issue.UpdatedAt, *issue.StartedAt, *issue.ClosedAt,
		*issue.LeaseExpiresAt, *issue.HeartbeatAt, *issue.DueAt, *issue.DeferUntil, *issue.CompactedAt}
	for _, value := range values {
		if value.Location() != time.UTC {
			t.Errorf("timestamp location = %v, want UTC", value.Location())
		}
	}
	if got, want := issue.ClosedAt.Format(time.RFC3339), "2026-07-11T08:42:00Z"; got != want {
		t.Errorf("ClosedAt = %s, want %s", got, want)
	}
}

func TestPersistCommentsNormalizesImportedTimestampToUTC(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	createdAt := offsetTime(t, "2026-07-11T18:42:00+10:00")
	wantUTC := createdAt.UTC()
	issue := &types.Issue{ID: "source", Comments: []*types.Comment{{
		ID: "comment-id", Author: "original-author", Text: "kept", CreatedAt: createdAt,
	}}}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM comments").
		WithArgs("source", "original-author", wantUTC, "kept").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO comments").
		WithArgs("comment-id", "source", "original-author", "kept", wantUTC).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := PersistComments(ctx, tx, issue)
	if err != nil {
		t.Fatalf("PersistComments: %v", err)
	}
	if !result.ChangedTables["comments"] {
		t.Fatalf("ChangedTables = %#v, want comments changed", result.ChangedTables)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

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

	createdAt := offsetTime(t, "2026-07-11T18:41:00+10:00")
	target := &types.Issue{ID: "target", IssueType: types.TypeTask}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "target",
			Type:        types.DepRelated,
			CreatedBy:   "someone.else",
			CreatedAt:   createdAt,
		}},
	}

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("target").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("target").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("INSERT INTO dependencies").
		WithArgs(depid.New("source", "target"), "source", "target", types.DepRelated, "someone.else", createdAt.UTC()).
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

func TestDependencyCreatedAtNormalizesToUTC(t *testing.T) {
	createdAt := offsetTime(t, "2026-07-11T18:41:00+10:00")
	got := dependencyCreatedAt(&types.Dependency{CreatedAt: createdAt})
	if got.Location() != time.UTC {
		t.Fatalf("Location = %v, want UTC", got.Location())
	}
	if want := "2026-07-11T08:41:00Z"; got.Format(time.RFC3339) != want {
		t.Fatalf("dependencyCreatedAt = %s, want %s", got.Format(time.RFC3339), want)
	}

	before := time.Now().UTC().Add(-time.Second)
	defaulted := dependencyCreatedAt(&types.Dependency{})
	after := time.Now().UTC().Add(time.Second)
	if defaulted.Before(before) || defaulted.After(after) || defaulted.Location() != time.UTC {
		t.Fatalf("default dependency time = %v, want current UTC instant", defaulted)
	}
}

func TestAddDependencyInTxBindsCreatedAtUTC(t *testing.T) {
	ctx := context.Background()
	kind := DepTargetIssue
	opts := AddDependencyOpts{
		SourceTable:    "issues",
		TargetTable:    "issues",
		WriteTable:     "dependencies",
		SkipCycleCheck: true,
		TargetKind:     &kind,
	}

	t.Run("preserves supplied instant", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		createdAt := offsetTime(t, "2026-07-11T18:41:00+10:00")
		dep := &types.Dependency{
			IssueID: "source", DependsOnID: "target", Type: types.DepRelated, CreatedAt: createdAt,
		}

		expectAddDependencyInsert(mock, dep, createdAt.UTC())
		if err := AddDependencyInTx(ctx, tx, dep, "current.user", opts); err != nil {
			t.Fatalf("AddDependencyInTx: %v", err)
		}
		finishMockTx(t, mock, tx)
	})

	t.Run("defaults zero time to current UTC", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		dep := &types.Dependency{IssueID: "source", DependsOnID: "target", Type: types.DepRelated}
		before := time.Now().UTC()
		createdAt := utcTimeBetween{before: before, after: before.Add(5 * time.Second)}

		expectAddDependencyInsert(mock, dep, createdAt)
		if err := AddDependencyInTx(ctx, tx, dep, "current.user", opts); err != nil {
			t.Fatalf("AddDependencyInTx: %v", err)
		}
		finishMockTx(t, mock, tx)
	})
}

func expectAddDependencyInsert(mock sqlmock.Sqlmock, dep *types.Dependency, createdAt driver.Value) {
	mock.ExpectQuery("SELECT issue_type FROM issues WHERE id = \\?").
		WithArgs(dep.IssueID).
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow(types.TypeTask))
	mock.ExpectQuery("SELECT issue_type FROM issues WHERE id = \\?").
		WithArgs(dep.DependsOnID).
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow(types.TypeTask))
	mock.ExpectQuery("SELECT type FROM dependencies").
		WithArgs(dep.IssueID, dep.DependsOnID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO dependencies").
		WithArgs(depid.New(dep.IssueID, dep.DependsOnID), dep.IssueID, dep.DependsOnID, dep.Type,
			createdAt, "current.user", "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func finishMockTx(t *testing.T, mock sqlmock.Sqlmock, tx *sql.Tx) {
	t.Helper()
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

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("bb-target").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\?").
		WithArgs("bb-target").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	// No ancestors query: hierarchy cannot cross rig prefixes.
	mock.ExpectQuery("WITH RECURSIVE reachable").
		WithArgs("bb-target", "aa-source").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO dependencies").
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
