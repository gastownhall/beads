package issueops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestAddRemoteIfNotExistsRefusesOptionShapedNames pins AC-6 for the
// federation-peer remote helper (GH#7876): an option-shaped peer name is
// refused before DOLT_REMOTE runs. The mock expects only the transaction, so
// any statement the helper issued would fail ExpectationsWereMet.
func TestAddRemoteIfNotExistsRefusesOptionShapedNames(t *testing.T) {
	for _, name := range []string{"--force", "-f", "-"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectRollback()

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			err = AddRemoteIfNotExists(context.Background(), tx, name, "file:///tmp/remote")
			want := `invalid remote name "` + name + `": must not start with '-'`
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestAddRemoteIfNotExistsAcceptsNumericLeadingNames is the control: "1st"
// reaches DOLT_REMOTE unchanged.
func TestAddRemoteIfNotExistsAcceptsNumericLeadingNames(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`CALL DOLT_REMOTE\('add', \?, \?\)`).WithArgs("1st", "file:///tmp/remote").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := AddRemoteIfNotExists(context.Background(), tx, "1st", "file:///tmp/remote"); err != nil {
		t.Fatalf("AddRemoteIfNotExists(1st): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
