package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCheckMigrationPostConditions_NoDatabase(t *testing.T) {
	got := CheckMigrationPostConditions(&SharedStore{})
	if got.Status != StatusOK || got.Message != "N/A (no database)" {
		t.Fatalf("check = %q (%s), want OK N/A", got.Status, got.Message)
	}
}

// A probe that fails says the check could not run; it never reads as healthy
// (GH#5269, AC-7).
func TestCheckMigrationPostConditions_CouldNotCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(`.*`).WillReturnError(errors.New("catalog unavailable"))

	got := checkMigrationPostConditions(t.Context(), db)
	if got.Status != StatusWarning ||
		!strings.Contains(got.Message, "Could not check") ||
		!strings.Contains(got.Message, "catalog unavailable") ||
		!strings.Contains(got.Detail, "does not mean the schema is healthy") {
		t.Fatalf("check = %q (%s; %s), want a could-not-check warning", got.Status, got.Message, got.Detail)
	}
}
