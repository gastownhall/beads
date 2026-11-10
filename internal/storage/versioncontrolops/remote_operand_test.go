package versioncontrolops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestRemoteHelpersRefuseOptionShapedNames pins AC-6 for the shared SQL
// helpers (GH#7876): an option-shaped remote or peer name is refused before
// any statement reaches the database. The mock expects nothing, so any SQL
// the helper issued would fail ExpectationsWereMet.
func TestRemoteHelpersRefuseOptionShapedNames(t *testing.T) {
	ctx := context.Background()
	calls := []struct {
		name string
		call func(DBConn, string) error
	}{
		{name: "AddRemote", call: func(db DBConn, n string) error { return AddRemote(ctx, db, n, "file:///tmp/remote", "") }},
		{name: "AddRemote/ref", call: func(db DBConn, n string) error {
			return AddRemote(ctx, db, n, "git+https://example.com/repo.git", "refs/heads/issue-data")
		}},
		{name: "RemoveRemote", call: func(db DBConn, n string) error { return RemoveRemote(ctx, db, n) }},
		{name: "Fetch", call: func(db DBConn, n string) error { return Fetch(ctx, db, n, "") }},
		{name: "Fetch/user", call: func(db DBConn, n string) error { return Fetch(ctx, db, n, "root") }},
		{name: "Push", call: func(db DBConn, n string) error { return Push(ctx, db, n, "main", "") }},
		{name: "ForcePush", call: func(db DBConn, n string) error { return ForcePush(ctx, db, n, "main", "") }},
		{name: "Pull", call: func(db DBConn, n string) error { return Pull(ctx, db, n, "main", "") }},
		{name: "PullWithStrategy", call: func(db DBConn, n string) error {
			return PullWithStrategy(ctx, db, n, "main", "", "theirs")
		}},
	}
	for _, name := range []string{"--force", "-f", "-"} {
		for _, tc := range calls {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				defer db.Close()

				err = tc.call(db, name)
				want := `invalid remote name "` + name + `": must not start with '-'`
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v, want %q", err, want)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestRemoteHelpersAcceptNumericLeadingNames is the control: "1st" reaches
// Dolt unchanged.
func TestRemoteHelpersAcceptNumericLeadingNames(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CALL DOLT_PUSH\(\?, \?\)`).WithArgs("1st", "main").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CALL DOLT_REMOTE\('remove', \?\)`).WithArgs("1st").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := Push(context.Background(), db, "1st", "main", ""); err != nil {
		t.Fatalf("Push(1st): %v", err)
	}
	if err := RemoveRemote(context.Background(), db, "1st"); err != nil {
		t.Fatalf("RemoveRemote(1st): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
