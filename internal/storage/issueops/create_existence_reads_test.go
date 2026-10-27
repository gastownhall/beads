package issueops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateOnlyCreateReadsEachPlaneOnce pins the existence reads of a
// create-only create with an explicit id: the create-only guard reads both
// planes, and neither the cross-plane collision check nor the insert re-reads
// a plane the guard just found empty. sqlmock refuses any statement not
// expected here, so a re-read fails the test.
func TestCreateOnlyCreateReadsEachPlaneOnce(t *testing.T) {
	mock, db := newBlockedStateResultMock(t)
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM issues WHERE id = ?")).
		WithArgs("bd-wisp-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).
		WithArgs("bd-wisp-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO wisps")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.Background()
	opts := storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true}
	bc := &BatchContext{ConfigPrefix: "bd", Opts: opts}
	issue := &types.Issue{
		ID: "bd-wisp-1", Title: "op", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, NoHistory: true,
	}
	issueTable, _, skip, err := prepareCreateIssueInTx(ctx, db, bc, issue, "tester")
	if err != nil || skip {
		t.Fatalf("prepareCreateIssueInTx = skip %v, err %v; want no skip, no error", skip, err)
	}
	if issueTable != "wisps" {
		t.Fatalf("issueTable = %q, want wisps", issueTable)
	}
	isNew, staleRejected, err := insertIssueIfNewCached(ctx, db, issueTable, issue, opts, nil)
	if err != nil || !isNew || staleRejected {
		t.Fatalf("insertIssueIfNewCached = new %v, stale %v, err %v; want new", isNew, staleRejected, err)
	}
}

// TestConflictSkipCreateStillReadsExistingRow pins the read the create-only
// shortcut must keep: a ConflictSkip insert decides from the existing row's
// presence, so it still reads it.
func TestConflictSkipCreateStillReadsExistingRow(t *testing.T) {
	mock, db := newBlockedStateResultMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).
		WithArgs("bd-wisp-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	issue := &types.Issue{ID: "bd-wisp-1", Title: "op", Status: types.StatusOpen, NoHistory: true}
	opts := storage.BatchCreateOptions{CreateOnly: true, ConflictSkip: true}
	isNew, _, err := insertIssueIfNewCached(context.Background(), db, "wisps", issue, opts, nil)
	if err != nil || isNew {
		t.Fatalf("insertIssueIfNewCached = new %v, err %v; want a skipped existing row", isNew, err)
	}
}
