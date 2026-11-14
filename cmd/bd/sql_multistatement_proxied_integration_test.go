//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// These tests exercise `bd sql`, not the managed-local backend lifecycle, so
// they run against the shared per-binary dolt sql-server instead of having
// each `bd init --proxied-server` spawn its own. A spawned backend's port is
// picked bind-and-close at init and bound by dolt seconds later; under the
// parallel proxied tier another process can take it in between, dolt exits
// with "Port N already in use", and the proxy's dial-only readiness check has
// already accepted the foreign listener, so init's first ping dies with
// "unexpected EOF" then "connection refused". The shared server's own startup
// (testutil.localDoltServer) waits for dolt's ready line and retries on
// another port, so it does not have that race.
func TestProxiedServerMultiStatementSQL(t *testing.T) {
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ms")

	// A multi-statement batch is treated as a write: it executes and commits
	// atomically, and reports "OK" with no rows-affected count.
	batch := "CREATE TABLE ms_t (id INT PRIMARY KEY); INSERT INTO ms_t VALUES (1); INSERT INTO ms_t VALUES (2)"
	out, err := bdProxiedRun(t, bd, p.dir, "sql", batch)
	if err != nil {
		t.Fatalf("bd sql multi-statement failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "OK" {
		t.Fatalf("multi-statement output = %q, want %q", got, "OK")
	}

	// Both inserts must have committed.
	db := openProxiedDB(t, p)
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM ms_t").Scan(&n); err != nil {
		t.Fatalf("count ms_t: %v", err)
	}
	if n != 2 {
		t.Fatalf("ms_t row count = %d, want 2 (multi-statement writes not committed)", n)
	}

	// A single write still reports the affected-row count.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "INSERT INTO ms_t VALUES (3)")
	if err != nil {
		t.Fatalf("bd sql single write failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "OK, 1 rows affected" {
		t.Fatalf("single-write output = %q, want %q", got, "OK, 1 rows affected")
	}

	// A single read still renders a result table.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "SELECT COUNT(*) AS c FROM ms_t")
	if err != nil {
		t.Fatalf("bd sql single read failed: %v\n%s", err, out)
	}
	if s := string(out); !strings.Contains(s, "c") || !strings.Contains(s, "3") {
		t.Fatalf("single-read output missing rendered result:\n%s", s)
	}

	// Multi-statement with --json reports a status object, not rows_affected.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--json",
		"INSERT INTO ms_t VALUES (4); INSERT INTO ms_t VALUES (5)")
	if err != nil {
		t.Fatalf("bd sql --json multi-statement failed: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "\"status\"") || !strings.Contains(s, "ok") {
		t.Fatalf("multi-statement --json output = %q, want a status:ok object", s)
	}
	if strings.Contains(s, "rows_affected") {
		t.Fatalf("multi-statement --json should not include rows_affected: %q", s)
	}
}

func TestProxiedServerSQLDatabaseFlag(t *testing.T) {
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedRootProject(t, bd, "df")

	// Stand up a second database with a table, alongside the project database.
	// The server is shared, so the name derives from the project's unique one.
	other := p.database + "_other"
	db := openProxiedDB(t, p)
	for _, q := range []string{
		"CREATE DATABASE " + other,
		"USE " + other + "; CREATE TABLE widgets (id INT PRIMARY KEY, name VARCHAR(32))",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	// A single write routed to the other database with --database.
	out, err := bdProxiedRun(t, bd, p.dir, "sql", "--database", other,
		"INSERT INTO widgets VALUES (1, 'gear')")
	if err != nil {
		t.Fatalf("bd sql --database write failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "OK, 1 rows affected" {
		t.Fatalf("--database write output = %q, want %q", got, "OK, 1 rows affected")
	}

	// The write must have committed in the other database, not the project one.
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+other+".widgets WHERE name = 'gear'").Scan(&n); err != nil {
		t.Fatalf("count %s.widgets: %v", other, err)
	}
	if n != 1 {
		t.Fatalf("%s.widgets rows = %d, want 1 (--database write not committed there)", other, n)
	}

	// A read routed with --database against an unqualified table name resolves
	// in the switched database and renders results.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--database", other,
		"SELECT name FROM widgets WHERE id = 1")
	if err != nil {
		t.Fatalf("bd sql --database read failed: %v\n%s", err, out)
	}
	if s := string(out); !strings.Contains(s, "gear") {
		t.Fatalf("--database read output missing 'gear':\n%s", s)
	}

	// The first excursion's open installed the beads schema in the other
	// database without a project identity (bd-rgvivq AC-2): a second
	// excursion write must keep working against it, and the workspace's
	// identity must not be minted there.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--database", other,
		"INSERT INTO widgets VALUES (2, 'cog')")
	if err != nil {
		t.Fatalf("second bd sql --database write failed: %v\n%s", err, out)
	}
	var total int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+other+".widgets").Scan(&total); err != nil {
		t.Fatalf("count %s.widgets: %v", other, err)
	}
	if total != 2 {
		t.Fatalf("%s.widgets rows = %d, want 2 (repeated --database excursions must keep working)", other, total)
	}
	var minted int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+other+".metadata WHERE `key` = '_project_id'").Scan(&minted); err != nil {
		t.Fatalf("count %s.metadata._project_id: %v", other, err)
	}
	if minted != 0 {
		t.Fatalf("%s.metadata._project_id rows = %d, want 0 (an excursion must not mint an identity)", other, minted)
	}

	// An invalid database identifier is rejected, not interpolated.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--database", "bad;name",
		"SELECT 1")
	if err == nil {
		t.Fatalf("expected --database with invalid identifier to fail; got:\n%s", out)
	}
}

// TestProxiedServerDatabaseFlagForeignProjectIdentity is the CLI-level
// bd-rgvivq guard (gastownhall/beads#5572): from a plain proxied workspace,
// every command shape that opens the provider against another project's
// beads database via --database/--db is refused with the identity mismatch
// before the command runs, and BEADS_SKIP_IDENTITY_CHECK=1 opens it
// deliberately. Both projects init against the shared server as root, the
// one credential that can see both databases.
func TestProxiedServerDatabaseFlagForeignProjectIdentity(t *testing.T) {
	bd := buildEmbeddedBD(t)
	p1 := newSharedProxiedRootProject(t, bd, "fx1")
	p2 := newSharedProxiedRootProject(t, bd, "fx2")

	db := openSharedServerRootDB(t)
	var storedID string
	if err := db.QueryRowContext(context.Background(),
		"SELECT value FROM "+p2.database+".metadata WHERE `key` = '_project_id'").Scan(&storedID); err != nil {
		t.Fatalf("read %s.metadata._project_id: %v", p2.database, err)
	}
	localID := readProxiedProjectID(t, p1)
	if localID == "" || storedID == "" || localID == storedID {
		t.Fatalf("the two projects must carry distinct identities: workspace %q vs %s stored %q", localID, p2.database, storedID)
	}

	// A read, a write, a preview (name-form --db), and raw SQL: every open
	// posture refuses the foreign database, naming both identities and the
	// database it refused to connect to.
	for _, args := range [][]string{
		{"--database", p2.database, "list"},
		{"--database", p2.database, "create", "should not land"},
		{"--db", p2.database, "create", "also not", "--dry-run"},
		{"sql", "--database", p2.database, "SELECT 1"},
	} {
		out, err := bdProxiedRun(t, bd, p1.dir, args...)
		if err == nil {
			t.Fatalf("bd %s should have been refused; got:\n%s", strings.Join(args, " "), out)
		}
		for _, want := range []string{"PROJECT IDENTITY MISMATCH", localID, storedID, p2.database} {
			if !strings.Contains(string(out), want) {
				t.Errorf("bd %s refusal missing %q:\n%s", strings.Join(args, " "), want, out)
			}
		}
	}

	// The refusals landed before any mutation: the foreign project's issue
	// table gained nothing from this workspace.
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+p2.database+".issues").Scan(&n); err != nil {
		t.Fatalf("count %s.issues: %v", p2.database, err)
	}
	if n != 0 {
		t.Fatalf("%s.issues rows = %d, want 0 (a refused invocation must not write)", p2.database, n)
	}

	// The explicit bypass opens the foreign database deliberately.
	out, _, err := bdProxiedRunEnv(t, bd, p1.dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"},
		"sql", "--database", p2.database, "SELECT COUNT(*) AS c FROM issues")
	if err != nil {
		t.Fatalf("BEADS_SKIP_IDENTITY_CHECK=1 bd sql --database should succeed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "c") {
		t.Fatalf("bypassed bd sql output missing rendered result:\n%s", out)
	}
}

// readProxiedProjectID reads a proxied project's locally recorded workspace
// identity out of its metadata.json.
func readProxiedProjectID(t *testing.T, p proxiedProject) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(p.dir, ".beads", configfile.ConfigFileName))
	if err != nil {
		t.Fatalf("read %s metadata.json: %v", p.dir, err)
	}
	var cfg struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse %s metadata.json: %v", p.dir, err)
	}
	return cfg.ProjectID
}

// openSharedServerRootDB connects straight to the lane's shared dolt
// sql-server as root — the credential a sharedProxiedRoot project's own proxy
// uses — for cross-database assertions. It does not go through a project's
// proxy: that proxy idles out and exits (removing its pidfile) once another
// project's init keeps the test away from it for long enough.
func openSharedServerRootDB(t *testing.T) *sql.DB {
	t.Helper()
	port := requireSharedProxiedServer(t)
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
	if err != nil {
		t.Fatalf("open shared server as root: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping shared server: %v", err)
	}
	return db
}
