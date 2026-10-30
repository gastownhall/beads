package db

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// newInsertGuardIssue is a minimal issue the upsert Insert accepts.
func newInsertGuardIssue(id string) *types.Issue {
	return &types.Issue{ID: id, Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// TestUpsertInsertOfAGeneratedIDSkipsTheRetiredGuard pins that the upsert
// Insert spends no coordination cell and no occupancy probe on an id the
// create use case minted (a top-level mint or a child counter): the
// generators already skip retired ids, as issueops' create body assumes. The
// first statement is the row write; anything before it fails the test.
func TestUpsertInsertOfAGeneratedIDSkipsTheRetiredGuard(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO issues")).WillReturnError(context.Canceled)

	repo := NewIssueSQLRepository(sqlDB)
	_ = repo.Insert(context.Background(), newInsertGuardIssue("bd-gen"), "actor", domain.InsertIssueOpts{})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a generated-id upsert did not write its row first: %v", err)
	}
}

// TestUpsertInsertOfAnExplicitIDCoordinatesAndProbesFirst pins the other
// half: an id the caller named is locked and probed before the upsert, so a
// retaining sweep cannot commit between the probe and the write.
func TestUpsertInsertOfAnExplicitIDCoordinatesAndProbesFirst(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 'issues' AS src")).WillReturnError(context.Canceled)

	repo := NewIssueSQLRepository(sqlDB)
	_ = repo.Insert(context.Background(), newInsertGuardIssue("bd-1"), "actor", domain.InsertIssueOpts{ExplicitID: true})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("an explicit-id upsert did not coordinate and probe first: %v", err)
	}
}
