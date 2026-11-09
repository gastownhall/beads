package db

import (
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func newMockDependencyRepo(t *testing.T) (sqlmock.Sqlmock, *dependencySQLRepositoryImpl) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return mock, &dependencySQLRepositoryImpl{runner: db}
}

// TestDependencyInsertReadsNullStoredMetadataAsEmptyObject is the serve-mode
// twin of issueops.TestAddDependencyInTxReadsNullStoredMetadataAsEmptyObject.
// The change-free re-add gate widened this SELECT from `type` to `type,
// metadata` on both write planes, and `dependencies.metadata` is nullable
// (`JSON DEFAULT (JSON_OBJECT())`, no NOT NULL), so a row written out of band
// -- bd sql, an external tool, hand SQL -- can hold NULL. Scanning NULL into a
// plain string hard-fails with "converting NULL to string is unsupported",
// which would turn every subsequent re-add of that edge into an error on a
// path that used to be idempotently happy. NULL is the absent-metadata state,
// so it reads as `{}` and the change-free re-add of a metadata-free edge stays
// the no-op it was. thread_id is nullable the same way and read by the same
// gate; a re-add naming no thread keeps a NULL one, so the row holds NULL in
// both columns and the re-add is still change-free.
//
// The repair landed on both planes; without this the two planes are pinned
// asymmetrically and a revert of the serve-mode scan alone has nothing red to
// catch it.
func TestDependencyInsertReadsNullStoredMetadataAsEmptyObject(t *testing.T) {
	mock, repo := newMockDependencyRepo(t)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT type, metadata, thread_id FROM dependencies")).
		WithArgs("dep-a", "dep-b").
		WillReturnRows(sqlmock.NewRows([]string{"type", "metadata", "thread_id"}).AddRow(string(types.DepRelated), nil, nil))

	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-b", Type: types.DepRelated}
	// DepRelated is not a scheduling edge and the hierarchy check is declared
	// done, so the widened gate SELECT is the only statement this call reaches.
	err := repo.Insert(t.Context(), dep, "writer", domain.DepInsertOpts{HierarchyValidated: true})
	if err != nil {
		t.Fatalf("Insert(existing edge with NULL metadata) = %v, want nil: a change-free re-add is a no-op, not an error", err)
	}
	// Scripting only the one read means any UPDATE or journal write this path
	// attempted would fail the run: the no-op is pinned, not merely the
	// absence of an error.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestPickDepTargetColumnCrossPrefixLocal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wisp, issue bool
		want        string
	}{
		{"local issue", false, true, "depends_on_issue_id"},
		{"local wisp", true, false, "depends_on_wisp_id"},
		{"foreign", false, false, "depends_on_external"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, repo := newMockDependencyRepo(t)
			wispRows := sqlmock.NewRows([]string{"1"})
			if tc.wisp {
				wispRows.AddRow(1)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).WithArgs("b-target").WillReturnRows(wispRows)
			if !tc.wisp {
				issueRows := sqlmock.NewRows([]string{"1"})
				if tc.issue {
					issueRows.AddRow(1)
				}
				mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM issues WHERE id = ? LIMIT 1")).WithArgs("b-target").WillReturnRows(issueRows)
			}
			got, err := repo.pickDepTargetColumn(t.Context(), "a-source", "b-target")
			if err != nil || got != tc.want {
				t.Fatalf("pickDepTargetColumn = %q, %v; want %q", got, err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestValidateBlockingHierarchyCrossPrefixLocal pins the domain repository's
// independent end-gate path. Prefix mismatch must not skip a parent that is
// actually present in this database.
func TestValidateBlockingHierarchyCrossPrefixLocal(t *testing.T) {
	mock, repo := newMockDependencyRepo(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs("other-parent").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM issues WHERE id = ? LIMIT 1")).
		WithArgs("other-parent").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("WITH RECURSIVE ancestors").
		WithArgs("bd-child", "other-parent").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

	dep := &types.Dependency{IssueID: "bd-child", DependsOnID: "other-parent", Type: types.DepBlocks}
	err := repo.ValidateBlockingHierarchy(t.Context(), dep)
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("ValidateBlockingHierarchy(cross-prefix local parent) = %v, want hierarchy conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
