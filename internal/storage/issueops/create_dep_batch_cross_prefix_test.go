package issueops

import (
	"context"
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestPersistDependenciesBatchClassifiesLocalCrossPrefixTargetsWithoutPerEdgeSQL
// pins the batch half of GH#8299: a multi-edge batch answers classification
// from its two up-front presence reads (the wisps set and the issues-exists
// map), so two edges on local cross-prefix targets resolve to
// depends_on_issue_id with no per-edge presence SQL between the batch reads
// and the inserts. The scripted statements are exhaustive: any per-edge
// "SELECT 1 FROM wisps/issues" probe fails as an unexpected query, and on
// base the inserts themselves file the targets external.
func TestPersistDependenciesBatchClassifiesLocalCrossPrefixTargetsWithoutPerEdgeSQL(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	batch := []*types.Issue{
		{
			ID:        "aa-src1",
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "bb-1",
				Type:        types.DepRelated,
			}},
		},
		{
			ID:        "aa-src2",
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "bb-2",
				Type:        types.DepRelated,
			}},
		},
	}

	// The batch's two presence reads: an empty wisps table (one probe), then
	// both cross-prefix targets found in issues.
	mock.ExpectQuery("SELECT 1 FROM wisps LIMIT 1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id FROM issues WHERE id IN \\(\\?,\\?\\)").
		WithArgs("bb-1", "bb-2").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bb-1").AddRow("bb-2"))

	// Then the two inserts, straight into the local issue column.
	mock.ExpectExec("INSERT INTO dependencies \\(id, issue_id, depends_on_issue_id").
		WithArgs(depid.New("aa-src1", "bb-1"), "aa-src1", "bb-1", types.DepRelated, "tester", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO dependencies \\(id, issue_id, depends_on_issue_id").
		WithArgs(depid.New("aa-src2", "bb-2"), "aa-src2", "bb-2", types.DepRelated, "tester", sqlmock.AnyArg(), "{}", "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := PersistDependenciesWithOptionsResult(ctx, tx, batch, "tester", storage.BatchCreateOptions{}); err != nil {
		t.Fatalf("PersistDependenciesWithOptionsResult: %v", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
