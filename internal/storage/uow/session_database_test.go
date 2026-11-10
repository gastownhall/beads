package uow

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// sessionDatabaseMock stands in for a front door that answers SELECT DATABASE()
// with whatever it actually put the session on, which may not be what bd asked
// for.
func sessionDatabaseMock(t *testing.T, answer any) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(answer))
	return db, mock
}

func TestAssertSessionDatabase(t *testing.T) {
	ctx := context.Background()

	t.Run("session on the requested database passes", func(t *testing.T) {
		db, mock := sessionDatabaseMock(t, "team_b")
		if err := assertSessionDatabase(ctx, db, "team_b"); err != nil {
			t.Fatalf("assertSessionDatabase() = %v, want nil", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	// MySQL schema names are case-insensitive, so a server that normalizes the
	// name is not a mismatch.
	t.Run("case-folded match passes", func(t *testing.T) {
		db, _ := sessionDatabaseMock(t, "TEAM_B")
		if err := assertSessionDatabase(ctx, db, "team_b"); err != nil {
			t.Fatalf("assertSessionDatabase() = %v, want nil", err)
		}
	})

	// The field bug: a credential scoped to team_a asks for team_b, the front
	// door accepts the name and serves team_a. Before the assertion, every
	// later unqualified read was attributed to team_b.
	t.Run("scoped front door is caught and both databases are named", func(t *testing.T) {
		db, _ := sessionDatabaseMock(t, "team_a")
		err := assertSessionDatabase(ctx, db, "team_b")
		if err == nil {
			t.Fatal("assertSessionDatabase() = nil, want a mismatch error")
		}
		for _, want := range []string{
			`connected this session to database "team_a"`,
			`not the requested "team_b"`,
			"scoped",
			"ask the server administrator",
			"re-run init without --database",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("mismatch message %q missing %q", err.Error(), want)
			}
		}
	})

	t.Run("no session database is reported as not provisioned or not granted", func(t *testing.T) {
		for _, answer := range []any{nil, ""} {
			db, _ := sessionDatabaseMock(t, answer)
			err := assertSessionDatabase(ctx, db, "team_b")
			if err == nil {
				t.Fatalf("assertSessionDatabase(%v) = nil, want an error", answer)
			}
			for _, want := range []string{"no database", "not provisioned", "has not been granted access"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("message %q missing %q", err.Error(), want)
				}
			}
		}
	})

	t.Run("query failure surfaces the driver error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New(): %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cause := errors.New("connection reset by peer")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).WillReturnError(cause)

		got := assertSessionDatabase(ctx, db, "team_b")
		if !errors.Is(got, cause) {
			t.Fatalf("assertSessionDatabase() = %v, want it to wrap %v", got, cause)
		}
	})

	// Vendor neutrality for the new strings.
	t.Run("messages name no product", func(t *testing.T) {
		db, _ := sessionDatabaseMock(t, "team_a")
		err := assertSessionDatabase(ctx, db, "team_b")
		for _, banned := range []string{"beads-team-server", "bts "} {
			if strings.Contains(err.Error(), banned) {
				t.Fatalf("message must not name a product (%q): %v", banned, err)
			}
		}
	})
}
