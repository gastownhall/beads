package main

// Tests for the #6099 drop-database recovery path: the pure refusal guards,
// the consent gate (non-interactive refusal, interactive answer table), the
// existence probe, and the one guarded DROP DATABASE statement. The SHOW
// DATABASES probe is stubbed through showDatabasesNamesFn because
// versioncontrolops.DBConn returns *sql.Rows, which no in-memory fake can
// fabricate; everything after the probe runs against a recording conn.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// recordingDropDatabaseConn records what dropDatabase issues. ExecContext is
// the destructive half; the other two methods exist to satisfy
// versioncontrolops.DBConn and fail loudly if the code under test starts
// using them.
type recordingDropDatabaseConn struct {
	t       *testing.T
	queries []string
}

var _ versioncontrolops.DBConn = (*recordingDropDatabaseConn)(nil)

func (c *recordingDropDatabaseConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.queries = append(c.queries, query)
	return nil, nil
}

func (c *recordingDropDatabaseConn) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	c.t.Fatalf("unexpected QueryContext(%q)", query)
	return nil, nil
}

func (c *recordingDropDatabaseConn) QueryRowContext(_ context.Context, query string, _ ...any) *sql.Row {
	c.t.Fatalf("unexpected QueryRowContext(%q)", query)
	return nil
}

// captureStdoutForDropTest runs fn with os.Stdout replaced by a pipe and
// returns what it wrote, serialized under stdioMutex like every other
// std-stream swap in this package.
func captureStdoutForDropTest(t *testing.T, fn func()) string {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	_ = w.Close()
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	_ = r.Close()
	return string(buf[:n])
}

// stubShowDatabases points the SHOW DATABASES probe at a fixed database list
// and reports how many times it was consulted.
func stubShowDatabases(t *testing.T, names []string) *int {
	t.Helper()
	calls := 0
	orig := showDatabasesNamesFn
	showDatabasesNamesFn = func(context.Context, versioncontrolops.DBConn) ([]string, error) {
		calls++
		return names, nil
	}
	t.Cleanup(func() { showDatabasesNamesFn = orig })
	return &calls
}

// TestCheckDropDatabaseGuard pins every refusal: what no invocation may
// drop, each with its actionable way out, and the one name shape that is
// allowed through to the existence probe.
func TestCheckDropDatabaseGuard(t *testing.T) {
	allowed := dropDatabaseGuardInput{
		name:              "old_project",
		workspaceDatabase: "beads",
		globalDatabase:    "beads_global",
	}
	cases := []struct {
		name        string
		in          dropDatabaseGuardInput
		wantMessage []string // substrings of the refusal message
		wantHint    []string // substrings of the hint
	}{
		{
			"system schema mysql",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "mysql"},
			[]string{"system database", `"mysql"`},
			[]string{"required by the Dolt server"},
		},
		{
			"system schema information_schema",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "information_schema"},
			[]string{"system database"},
			nil,
		},
		{
			"system schema performance_schema",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "performance_schema"},
			[]string{"system database"},
			nil,
		},
		{
			"global shared-server database",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "beads_global"},
			[]string{"global shared-server database", `"beads_global"`},
			[]string{"cross-project", "every workspace"},
		},
		{
			"--global flag set",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", globalFlag: true, name: "old_project"},
			[]string{"--global"},
			[]string{"without --global"},
		},
		{
			"--database flag target",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", databaseFlag: "old_project", name: "old_project"},
			[]string{"--database"},
			nil,
		},
		{
			"workspace's own database",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "beads"},
			[]string{"this workspace is initialized against", `"beads"`},
			[]string{".beads", "another workspace"},
		},
		{
			"stale test prefix deflected to clean-databases",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "testdb_1234"},
			[]string{"stale test database", `"testdb_"`},
			[]string{"clean-databases"},
		},
		{
			"empty name",
			dropDatabaseGuardInput{workspaceDatabase: "beads", globalDatabase: "beads_global", name: "   "},
			[]string{"no database name given"},
			[]string{"SHOW DATABASES"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal := checkDropDatabaseGuard(tc.in)
			if refusal == nil {
				t.Fatalf("guard allowed %q, want a refusal", tc.in.name)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(refusal.message, want) {
					t.Errorf("message %q missing %q", refusal.message, want)
				}
			}
			for _, want := range tc.wantHint {
				if !strings.Contains(refusal.hint, want) {
					t.Errorf("hint %q missing %q", refusal.hint, want)
				}
			}
		})
	}
	if refusal := checkDropDatabaseGuard(allowed); refusal != nil {
		t.Errorf("guard refused an unrelated project database: %s / %s", refusal.message, refusal.hint)
	}
}

// TestDropDatabaseGuardRefusalsNeverTouchTheServer pins the guard's position
// in the flow: a refused name never reaches the SHOW DATABASES probe and
// never issues a DROP — the refusal is the whole interaction, exactly as
// #6099 asks (no destructive surprise paths behind the guards).
func TestDropDatabaseGuardRefusalsNeverTouchTheServer(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	probeCalls := stubShowDatabases(t, nil) // fatals if the probe is consulted

	guardIn := dropDatabaseGuardInput{
		workspaceDatabase: "beads",
		globalDatabase:    "beads_global",
		name:              "beads",
	}
	var stderrOut string
	err := func() error {
		var e error
		stderrOut = captureStderrForConsentTest(t, func() {
			e = dropDatabase(t.Context(), conn, dropDatabaseOptions{assumeYes: true, endpoint: "127.0.0.1:3306"}, guardIn)
		})
		return e
	}()
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("err = %v, want an exit-1 refusal", err)
	}
	if *probeCalls != 0 {
		t.Errorf("SHOW DATABASES probe fired %d times on a refused name, want 0", *probeCalls)
	}
	if len(conn.queries) != 0 {
		t.Errorf("issued %d statements on a refused name, want 0: %q", len(conn.queries), conn.queries)
	}
	for _, want := range []string{"this workspace is initialized against", ".beads"} {
		if !strings.Contains(stderrOut, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderrOut)
		}
	}
}

// TestDropDatabaseMissingNameRefuses pins the existence probe: a name that
// is not a database on this server is refused with guidance, and the DROP
// never runs (even under --yes).
func TestDropDatabaseMissingNameRefuses(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	stubShowDatabases(t, []string{"beads", "information_schema", "mysql"})

	guardIn := dropDatabaseGuardInput{
		workspaceDatabase: "beads",
		globalDatabase:    "beads_global",
		name:              "old_project",
	}
	var stderrOut string
	err := func() error {
		var e error
		stderrOut = captureStderrForConsentTest(t, func() {
			e = dropDatabase(t.Context(), conn, dropDatabaseOptions{assumeYes: true, endpoint: "127.0.0.1:3306"}, guardIn)
		})
		return e
	}()
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("err = %v, want an exit-1 refusal for a missing database", err)
	}
	if len(conn.queries) != 0 {
		t.Errorf("issued %d statements for a missing database, want 0: %q", len(conn.queries), conn.queries)
	}
	for _, want := range []string{`"old_project"`, "does not exist on this server", "SHOW DATABASES"} {
		if !strings.Contains(stderrOut, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderrOut)
		}
	}
}

// TestDropDatabaseRequiresYesWhenNonInteractive pins the consent gate for
// scripts and agents: a non-interactive run without --yes refuses before any
// DROP, with the reset-data-style --yes remedy.
func TestDropDatabaseRequiresYesWhenNonInteractive(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	stubShowDatabases(t, []string{"beads", "old_project"})

	guardIn := dropDatabaseGuardInput{
		workspaceDatabase: "beads",
		globalDatabase:    "beads_global",
		name:              "old_project",
	}
	var stderrOut string
	err := func() error {
		var e error
		stderrOut = captureStderrForConsentTest(t, func() {
			e = dropDatabase(t.Context(), conn,
				dropDatabaseOptions{assumeYes: false, interactive: false, endpoint: "127.0.0.1:3306"}, guardIn)
		})
		return e
	}()
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("err = %v, want an exit-1 consent refusal", err)
	}
	if len(conn.queries) != 0 {
		t.Errorf("issued %d statements without consent, want 0: %q", len(conn.queries), conn.queries)
	}
	for _, want := range []string{"old_project", "--yes to confirm"} {
		if !strings.Contains(stderrOut, want) {
			t.Errorf("consent refusal missing %q:\n%s", want, stderrOut)
		}
	}
}

// TestDropDatabasePromptDisclosesBeforeAsking pins the interactive prompt:
// the database name and the server endpoint are shown before the (y/N) ask,
// with the DOLT_UNDROP recovery story, and the default answer is NO.
func TestDropDatabasePromptDisclosesBeforeAsking(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	stubShowDatabases(t, []string{"beads", "old_project"})

	guardIn := dropDatabaseGuardInput{
		workspaceDatabase: "beads",
		globalDatabase:    "beads_global",
		name:              "old_project",
	}
	var stdout string
	err := func() error {
		var e error
		stdout = captureStdoutForDropTest(t, func() {
			e = dropDatabase(t.Context(), conn,
				dropDatabaseOptions{interactive: true, endpoint: "127.0.0.1:54321", stdin: strings.NewReader("n\n")}, guardIn)
		})
		return e
	}()
	if err != nil {
		t.Fatalf("dropDatabase: %v", err)
	}
	for _, want := range []string{"old_project", "127.0.0.1:54321", "DOLT_UNDROP", "Proceed? (y/N)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("prompt output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "Canceled.") {
		t.Errorf("decline should print Canceled.:\n%s", stdout)
	}
	if len(conn.queries) != 0 {
		t.Errorf("issued %d statements after a decline, want 0: %q", len(conn.queries), conn.queries)
	}
}

// TestDropDatabasePromptAnswerTable pins the prompt semantics through
// confirmDropDatabase: only an explicit affirmative drops; everything else
// declines; EOF is an error, never consent.
func TestDropDatabasePromptAnswerTable(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantGrant bool
		wantErr   bool
	}{
		{"y grants", "y\n", true, false},
		{"yes grants", "yes\n", true, false},
		{"uppercase YES grants", "YES\n", true, false},
		{"n declines", "n\n", false, false},
		{"empty input declines", "\n", false, false},
		{"unrelated word declines", "maybe\n", false, false},
		{"EOF is an error, never consent", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			granted, err := confirmDropDatabase(
				dropDatabaseOptions{interactive: true, endpoint: "127.0.0.1:3306", stdin: strings.NewReader(tc.input)},
				"old_project")
			if granted != tc.wantGrant {
				t.Errorf("granted = %v, want %v", granted, tc.wantGrant)
			}
			if tc.wantErr && err == nil {
				t.Error("err = nil, want an error (EOF must not be read as consent)")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
	t.Run("assume-yes never reads stdin", func(t *testing.T) {
		var stdout string
		var granted bool
		var err error
		stdout = captureStdoutForDropTest(t, func() {
			granted, err = confirmDropDatabase(
				dropDatabaseOptions{assumeYes: true, interactive: true, endpoint: "127.0.0.1:3306", stdin: strings.NewReader("")},
				"old_project")
		})
		if err != nil || !granted {
			t.Fatalf("granted=%v err=%v, want granted with no prompt under --yes", granted, err)
		}
		if strings.Contains(stdout, "Proceed?") {
			t.Errorf("--yes must not prompt, got:\n%s", stdout)
		}
	})
}

// TestDropDatabaseExecutesOneQuotedDrop pins the destructive half: a
// consented drop of an existing, unguarded database issues exactly one
// statement, the identifier goes through the quoting helper (expectations
// are exact strings, so a raw %s interpolation fails the backtick case
// here), and the human output carries the DOLT_UNDROP recovery note — never
// a purge.
func TestDropDatabaseExecutesOneQuotedDrop(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	stubShowDatabases(t, []string{"beads", "old_project", "evil`; DROP TABLE x"})

	for _, name := range []string{"old_project", "evil`; DROP TABLE x"} {
		t.Run(name, func(t *testing.T) {
			before := len(conn.queries)
			var stdout string
			err := func() error {
				var e error
				stdout = captureStdoutForDropTest(t, func() {
					e = dropDatabase(t.Context(), conn,
						dropDatabaseOptions{assumeYes: true, endpoint: "127.0.0.1:3306"}, dropDatabaseGuardInput{
							workspaceDatabase: "beads",
							globalDatabase:    "beads_global",
							name:              name,
						})
				})
				return e
			}()
			if err != nil {
				t.Fatalf("dropDatabase: %v", err)
			}
			issued := conn.queries[before:]
			if len(issued) != 1 {
				t.Fatalf("issued %d statements (%q), want exactly one DROP", len(issued), issued)
			}
			want := fmt.Sprintf("DROP DATABASE `%s`", strings.ReplaceAll(name, "`", "``"))
			if issued[0] != want {
				t.Errorf("statement = %q, want %q", issued[0], want)
			}
			for _, wantOut := range []string{"Dropped database", "DOLT_UNDROP", "clean-databases --purge-dropped"} {
				if !strings.Contains(stdout, wantOut) {
					t.Errorf("success output missing %q:\n%s", wantOut, stdout)
				}
			}
		})
	}
}

// TestDropDatabaseJSONOutput pins the --json wire shape: the payload names
// the dropped database and its DOLT_UNDROP recoverability (the schema_version
// wrapper is the standard bd --json envelope).
func TestDropDatabaseJSONOutput(t *testing.T) {
	conn := &recordingDropDatabaseConn{t: t}
	stubShowDatabases(t, []string{"beads", "old_project"})

	var stdout string
	err := func() error {
		var e error
		stdout = captureStdoutForDropTest(t, func() {
			e = dropDatabase(t.Context(), conn,
				dropDatabaseOptions{assumeYes: true, endpoint: "127.0.0.1:3306", json: true}, dropDatabaseGuardInput{
					workspaceDatabase: "beads",
					globalDatabase:    "beads_global",
					name:              "old_project",
				})
		})
		return e
	}()
	if err != nil {
		t.Fatalf("dropDatabase: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if payload["dropped"] != "old_project" {
		t.Errorf("dropped = %v, want old_project in %v", payload["dropped"], payload)
	}
	if recoverable, ok := payload["recoverable_via_undrop"].(bool); !ok || !recoverable {
		t.Errorf("recoverable_via_undrop = %v, want true in %v", payload["recoverable_via_undrop"], payload)
	}
}

// TestPromptDropDatabaseConsentFnAnswerTable pins the seam's own answer
// parsing directly (the reader-level half confirmDropDatabase delegates to).
func TestPromptDropDatabaseConsentFnAnswerTable(t *testing.T) {
	granted, err := promptDropDatabaseConsentFn(bufio.NewReader(strings.NewReader("  Y  \n")))
	if err != nil || !granted {
		t.Errorf("granted=%v err=%v, want padded Y to grant", granted, err)
	}
	granted, err = promptDropDatabaseConsentFn(bufio.NewReader(strings.NewReader("no\n")))
	if err != nil || granted {
		t.Errorf("granted=%v err=%v, want no to decline", granted, err)
	}
	if _, err := promptDropDatabaseConsentFn(bufio.NewReader(strings.NewReader(""))); err == nil {
		t.Error("EOF must surface as an error, not consent")
	}
}
