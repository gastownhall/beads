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
		got, err := confirmDropDatabase(t.Context(), strings.NewReader(in), io.NopCloser(nil), &out, "old_proj")
		if err != nil {
			t.Fatalf("confirmDropDatabase(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("confirmDropDatabase(%q) = %v, want %v", in, got, want)
		}
		for _, text := range []string{"old_proj", "DOLT_UNDROP", "[y/N]"} {
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
