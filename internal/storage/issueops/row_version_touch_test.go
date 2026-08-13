package issueops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestTouchRowVersionUsesResolvedPlaneWithoutRediscovery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE wisps SET row_lock = ?, updated_at = updated_at WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), "shared-id").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := TouchRowVersionInTx(context.Background(), db, "wisps", "shared-id"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTouchRowVersionRejectsUnknownPlane(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := TouchRowVersionInTx(context.Background(), db, "labels", "id"); err == nil {
		t.Fatal("expected invalid plane error")
	}
}
