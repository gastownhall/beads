package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// newDropDatabaseMock returns a sqlmock connection whose SHOW DATABASES lists
// names. Expectations are strict and ordered, so any statement the test did
// not expect (a second DROP, a DOLT_PURGE_DROPPED_DATABASES) fails it.
func newDropDatabaseMock(t *testing.T, names ...string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows := sqlmock.NewRows([]string{"Database"})
	for _, n := range names {
		rows.AddRow(n)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW DATABASES")).WillReturnRows(rows)
	return db, mock
}

// TestDropNamedDatabaseDropsOnlyThatDatabase is AC-7 of GH#6099: with
// consent, exactly one DROP DATABASE runs, for the named database, and no
// purge: the result says the drop is recoverable with DOLT_UNDROP.
func TestDropNamedDatabaseDropsOnlyThatDatabase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yes     bool
		confirm func(string) (bool, error)
	}{
		{name: "yes", yes: true},
		{name: "interactive-yes", confirm: func(string) (bool, error) { return true, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newDropDatabaseMock(t, "information_schema", "mysql", "other_proj", "old_proj", "my_proj")
			mock.ExpectExec(regexp.QuoteMeta("DROP DATABASE `old_proj`")).WillReturnResult(sqlmock.NewResult(0, 0))

			res, err := dropNamedDatabase(t.Context(), db, dropDatabaseRequest{
				name:       "old_proj",
				configured: "my_proj",
				assumeYes:  tc.yes,
				confirm:    tc.confirm,
			})
			if err != nil {
				t.Fatalf("dropNamedDatabase: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"dropped":"old_proj","recoverable_via_undrop":true}`; string(got) != want {
				t.Errorf("JSON = %s, want %s", got, want)
			}
		})
	}
}

// TestConfirmDropDatabasePrompt pins the interactive consent: only y/yes
// drops, anything else (including EOF) declines.
func TestConfirmDropDatabasePrompt(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "": false, "drop\n": false} {
		var out strings.Builder
		got, err := confirmDropDatabase(t.Context(), strings.NewReader(in), io.NopCloser(nil), &out, "old_proj", "db.example.com:3307")
		if err != nil {
			t.Fatalf("confirmDropDatabase(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("confirmDropDatabase(%q) = %v, want %v", in, got, want)
		}
		for _, text := range []string{"old_proj", "db.example.com:3307", "DOLT_UNDROP", "[y/N]"} {
			if !strings.Contains(out.String(), text) {
				t.Errorf("prompt for %q does not mention %q:\n%s", in, text, out.String())
			}
		}
	}
}

// TestDoltDropDatabaseReportsUndropRecovery pins the human report: the
// recovery command, and that disk is reclaimed only by the server-global
// clean-databases --purge-dropped, which this command never runs.
func TestDoltDropDatabaseReportsUndropRecovery(t *testing.T) {
	text := dropDatabaseReport(dropDatabaseResult{Dropped: "old_proj", RecoverableViaUndrop: true})
	for _, want := range []string{"Dropped database old_proj", "CALL DOLT_UNDROP('old_proj')", "bd dolt clean-databases --purge-dropped"} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
}

// TestDropDatabaseExecutesOneQuotedDrop pins the identifier quoting: a name
// holding a backtick reaches the server as one quoted identifier in exactly
// one DROP (the mock is strict, so a raw interpolation or a second statement
// fails), and the recovery text writes it as a SQL string literal.
func TestDropDatabaseExecutesOneQuotedDrop(t *testing.T) {
	for _, tc := range []struct{ name, drop, undrop string }{
		{"old_project", "DROP DATABASE `old_project`", "CALL DOLT_UNDROP('old_project')"},
		{"evil`; DROP TABLE x", "DROP DATABASE `evil``; DROP TABLE x`", "CALL DOLT_UNDROP('evil`; DROP TABLE x')"},
		{"it's", "DROP DATABASE `it's`", "CALL DOLT_UNDROP('it''s')"},
		{`back\slash`, "DROP DATABASE `back\\slash`", `CALL DOLT_UNDROP('back\\slash')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newDropDatabaseMock(t, "beads", tc.name)
			mock.ExpectExec(regexp.QuoteMeta(tc.drop)).WillReturnResult(sqlmock.NewResult(0, 0))

			res, err := dropNamedDatabase(t.Context(), db, dropDatabaseRequest{name: tc.name, configured: "beads", assumeYes: true})
			if err != nil {
				t.Fatalf("dropNamedDatabase: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if report := dropDatabaseReport(res); !strings.Contains(report, tc.undrop) {
				t.Errorf("report missing %q:\n%s", tc.undrop, report)
			}
			var out strings.Builder
			if _, err := confirmDropDatabase(t.Context(), strings.NewReader("n\n"), io.NopCloser(nil), &out, tc.name, "127.0.0.1:3307"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.undrop) {
				t.Errorf("prompt missing %q:\n%s", tc.undrop, out.String())
			}
		})
	}
}
