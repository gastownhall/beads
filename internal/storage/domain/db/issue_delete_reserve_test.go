package db

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// The journal hooks are context-scoped, so on a bare sqlmock DB the delete's
// only statements are the resolve SELECT, the child-suffix reservation and
// the DELETEs — which is what makes the ordering below assertable.

// TestDeleteReservesTheChildSuffixBeforeTheRowGoes pins that the domain's
// single Delete reserves the freed child suffix BEFORE the row goes, the way
// the issueops delete bodies do: the reservation routes the parent's plane by
// reading the parent, so it must run while the parent's row is still there.
// The mock fails the reservation's first probe, so the test pins the ORDER
// alone — the probe runs before the DELETE — and nothing about the delete's
// outcome.
func TestDeleteReservesTheChildSuffixBeforeTheRowGoes(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(context.Canceled)

	repo := NewIssueSQLRepository(sqlDB)
	_ = repo.Delete(context.Background(), "rd-parent.3", domain.IssueTableOpts{}, "actor")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the delete did not probe the wisp plane before the row went: %v", err)
	}
}

// TestDeleteByIDsReservesTheChildSuffixesBeforeTheRowsGo is the batch twin: a
// hierarchical id in the set makes the reservation run before the batched
// DELETE, and an all-top-level set runs no reservation at all — a plain
// delete pays nothing for the fix.
func TestDeleteByIDsReservesTheChildSuffixesBeforeTheRowsGo(t *testing.T) {
	t.Run("HierarchicalIDReserves", func(t *testing.T) {
		sqlDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues")).WithArgs("rd-parent.3").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rd-parent.3"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
			WillReturnError(context.Canceled)

		repo := NewIssueSQLRepository(sqlDB)
		_, _ = repo.DeleteByIDs(context.Background(), []string{"rd-parent.3"}, domain.IssueTableOpts{}, "actor")
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("the batched delete did not probe the wisp plane before the rows went: %v", err)
		}
	})

	t.Run("PlainIDsDoNotReserve", func(t *testing.T) {
		sqlDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues")).WithArgs("rd-plain").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rd-plain"))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM issues")).WillReturnError(context.Canceled)

		repo := NewIssueSQLRepository(sqlDB)
		_, _ = repo.DeleteByIDs(context.Background(), []string{"rd-plain"}, domain.IssueTableOpts{}, "actor")
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("a top-level-only delete issued statements other than its resolve and DELETE: %v", err)
		}
	})
}
