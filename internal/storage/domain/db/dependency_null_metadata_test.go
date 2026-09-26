package db

import (
	"regexp"
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
// the no-op it was.
//
// The repair landed on both planes; without this the two planes are pinned
// asymmetrically and a revert of the serve-mode scan alone has nothing red to
// catch it.
func TestDependencyInsertReadsNullStoredMetadataAsEmptyObject(t *testing.T) {
	mock, repo := newMockDependencyRepo(t)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT type, metadata FROM dependencies")).
		WithArgs("dep-a", "dep-b").
		WillReturnRows(sqlmock.NewRows([]string{"type", "metadata"}).AddRow(string(types.DepRelated), nil))

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
