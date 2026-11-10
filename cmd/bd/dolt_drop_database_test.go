package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
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
			mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
				WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(nil))
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

// TestDropNamedDatabaseRefuses is AC-8 of GH#6099: a named drop refuses with
// guidance, before any prompt and without a DROP, for a protected system
// database, beads_global, the connection's current database, the
// workspace's configured database, the database --database selects (even on
// a maintenance connection with no current database), a test-prefix name
// (clean-databases owns those), a database the server does not have, and
// missing consent.
func TestDropNamedDatabaseRefuses(t *testing.T) {
	server := []string{"information_schema", "mysql", "performance_schema", "sys", "beads_global", "team_global", "cur_proj", "my_proj", "sel_proj", "testdb_abc", "old_proj"}
	for _, tc := range []struct {
		name, target string
		current      string
		consent      bool
		confirm      func(string) (bool, error)
		wantMsg      string
		wantHint     string
	}{
		{name: "system-db", target: "mysql", consent: true, wantMsg: "system database", wantHint: "never drops"},
		{name: "system-db-case", target: "INFORMATION_SCHEMA", consent: true, wantMsg: "system database"},
		{name: "system-db-sys", target: "sys", consent: true, wantMsg: "system database"},
		{name: "beads-global", target: "beads_global", consent: true, wantMsg: "shared-server global database"},
		{name: "configured-global", target: "team_global", consent: true, wantMsg: "shared-server global database"},
		{name: "configured", target: "my_proj", consent: true, wantMsg: "this workspace's database", wantHint: "another workspace"},
		{name: "current", target: "cur_proj", current: "cur_proj", consent: true, wantMsg: "the connection's current database"},
		{name: "selected", target: "sel_proj", consent: true, wantMsg: "selects with --database", wantHint: "without --database"},
		{name: "selected-case", target: "SEL_PROJ", consent: true, wantMsg: "selects with --database"},
		{name: "test-prefix", target: "testdb_abc", consent: true, wantMsg: "test database", wantHint: "bd dolt clean-databases"},
		{name: "missing", target: "nope", consent: true, wantMsg: "does not exist"},
		{name: "no-consent", target: "old_proj", wantMsg: "no confirmation", wantHint: "--yes"},
		{name: "declined", target: "old_proj", confirm: func(string) (bool, error) { return false, nil }, wantMsg: "no confirmation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newDropDatabaseMock(t, server...)
			if tc.current != "" || tc.name == "no-consent" || tc.name == "declined" {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(tc.current))
			}
			prompted := false
			confirm := tc.confirm
			if tc.consent {
				confirm = func(string) (bool, error) { prompted = true; return true, nil }
			}
			_, err := dropNamedDatabase(t.Context(), db, dropDatabaseRequest{
				name:       tc.target,
				configured: "my_proj",
				selected:   "sel_proj",
				globals:    []string{"team_global"},
				confirm:    confirm,
			})
			refusal, ok := err.(*dropDatabaseRefusal)
			if !ok {
				t.Fatalf("dropNamedDatabase(%q) = %v, want a refusal", tc.target, err)
			}
			if !strings.Contains(refusal.message, tc.wantMsg) || !strings.Contains(refusal.hint, tc.wantHint) {
				t.Errorf("refusal = %q / hint %q, want %q / %q", refusal.message, refusal.hint, tc.wantMsg, tc.wantHint)
			}
			if prompted {
				t.Errorf("prompted before refusing %q: a refused drop must not ask", tc.target)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("statements: %v", err)
			}
		})
	}
}

// TestDropNamedDatabaseRefusesEmptyName pins that a blank name is refused
// before any statement reaches the server.
func TestDropNamedDatabaseRefusesEmptyName(t *testing.T) {
	for _, name := range []string{"", "  "} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		_, err = dropNamedDatabase(t.Context(), db, dropDatabaseRequest{name: name, assumeYes: true})
		if refusal, ok := err.(*dropDatabaseRefusal); !ok || !strings.Contains(refusal.message, "no database name") {
			t.Errorf("dropNamedDatabase(%q) = %v, want the no-name refusal", name, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
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
			mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
				WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(nil))
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

// TestDoltDropDatabaseDialsTheSocketTheStoreDials drives bd dolt
// drop-database, and bd dolt clean-databases, which opens the same
// connection, through the binary. Both fake servers refuse every login, so
// nothing is dropped; where each command logged in is what this test
// observes.
//
// With a live unix socket and a resolved port naming two different servers,
// the store dials the socket, so the drop must too: over TCP it listed and
// dropped a same-named database on whatever listened at the port. A server
// only a socket names, at a host bd does not run, is dialed on its socket
// rather than refused for want of a port.
func TestDoltDropDatabaseDialsTheSocketTheStoreDials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	bd := buildBDForInitTests(t)
	commands := [][]string{
		{"dolt", "drop-database", "old_project", "--yes"},
		{"dolt", "clean-databases", "--dry-run"},
	}
	run := func(t *testing.T, root string, env, args []string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = root
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	t.Run("socket beside a port", func(t *testing.T) {
		tcp := newFakeMySQLLogin(t)
		socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
		socket := socketServer.ln.Addr().String()
		root, _, env := externallyManagedStartWorkspace(t, "false")
		env = append(env, "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(tcp.port()), "BEADS_DOLT_SERVER_SOCKET="+socket)
		for _, args := range commands {
			name := "bd " + strings.Join(args, " ")
			before := len(socketServer.Logins())
			out := run(t, root, env, args)
			if len(socketServer.Logins()) == before {
				t.Errorf("%s never logged in on the socket the store dials, %s:\n%s", name, socket, out)
			}
			if n := len(tcp.Logins()); n != 0 {
				t.Errorf("%s logged in over TCP at %s %d time(s); the store dials the live socket %s:\n%s", name, tcp.ln.Addr(), n, socket, out)
			}
			if !strings.Contains(out, "unix:"+socket) {
				t.Errorf("%s does not name unix:%s, the server it reached:\n%s", name, socket, out)
			}
		}
	})

	t.Run("socket only", func(t *testing.T) {
		root, beadsDir, env := externallyManagedStartWorkspace(t, "true")
		env = configureDoltServerHost(t, beadsDir, env, "db.invalid", false)
		socket := diagnosticSocketPath(t)
		fake := newFakeMySQLLoginOn(t, "unix", socket)
		env = append(env, "BEADS_DOLT_SERVER_SOCKET="+socket)
		for _, args := range commands {
			name := "bd " + strings.Join(args, " ")
			before := len(fake.Logins())
			out := run(t, root, env, args)
			if len(fake.Logins()) == before {
				t.Errorf("%s never logged in on the socket the store dials, %s:\n%s", name, socket, out)
			}
			if strings.Contains(out, "port configured") {
				t.Errorf("%s refused for want of a port; the store dials the socket:\n%s", name, out)
			}
		}
	})
}
