//go:build cgo

package main

// CLI-level refusal gate for `bd dolt drop-database` (#6099): every refusal
// is exercised through the real built binary against the shared test Dolt
// server, must exit non-zero with actionable guidance, and must leave the
// target untouched on the server (still in SHOW DATABASES) — proving no DROP
// ran behind the refusal — and must not purge: a database dropped before the
// refusals stays DOLT_UNDROP-recoverable after them.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// runBDCommandExpectFailure is runBDCommand's refusal half: the command must
// exit non-zero, and its combined output is the assertion surface. stdin is
// the command's input; empty means EOF.
func runBDCommandExpectFailure(t *testing.T, binPath, dir, stdin string, extraEnv []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"BEADS_DOLT_SERVER_DATABASE=",
		"BEADS_DOLT_SERVER_HOST=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%v timed out after %s\n%s", args, 10*time.Second, out)
	}
	if err == nil {
		t.Fatalf("%v succeeded, want a refusal:\n%s", args, out)
	}
	return string(out)
}

// showDatabasesNamesTest lists the server's databases through the shared
// test server connection.
func showDatabasesNamesTest(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan database name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("SHOW DATABASES iteration: %v", err)
	}
	return names
}

// TestDropDatabaseRefusalsLeaveDatabaseIntact is AC-8 of GH#6099 through the
// real command: each refusal exits non-zero with its guidance, runs no DROP
// and no purge, and the one consented drop stays DOLT_UNDROP-recoverable.
func TestDropDatabaseRefusalsLeaveDatabaseIntact(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	binPath := buildBDUnderTest(t)
	stamp := time.Now().UnixNano()
	workspaceDB := fmt.Sprintf("drop_refusal_ws_%d", stamp)
	customGlobal := fmt.Sprintf("drop_refusal_global_%d", stamp)
	target := fmt.Sprintf("drop_refusal_target_%d", stamp)
	residue := fmt.Sprintf("drop_refusal_residue_%d", stamp)
	stale := fmt.Sprintf("testdb_droprefusal_%d", stamp)
	repoDir := t.TempDir()
	beadsDir := writeServerRepo(t, repoDir, workspaceDB, "127.0.0.1", "drop-refusal-test-origin", testDoltServerPort)
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	cfg.GlobalDoltDatabase = customGlobal
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// Every refusal target exists on the server, so no refusal rides on a
	// missing name; residue is dropped up front so a purge behind any
	// refusal would show as a failed DOLT_UNDROP at the end.
	for _, name := range []string{workspaceDB, customGlobal, target, residue, stale} {
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", name))
	}
	t.Cleanup(func() {
		for _, name := range []string{workspaceDB, customGlobal, target, residue, stale} {
			_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		}
	})
	mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE `%s`", residue))

	interactive := []string{"BD_NON_INTERACTIVE=0"}
	cases := []struct {
		name    string
		target  string
		stdin   string
		env     []string
		args    []string
		wantOut []string // substrings of the refusal output
	}{
		{name: "protected system schema", target: "mysql", args: []string{"--yes"}, wantOut: []string{"system database", "mysql"}},
		{name: "custom global", target: customGlobal, args: []string{"--yes"}, wantOut: []string{"shared-server global database"}},
		{name: "workspace's own database", target: workspaceDB, args: []string{"--yes"}, wantOut: []string{"this workspace's database", "another workspace"}},
		{name: "selected with --database", target: target, args: []string{"--yes", "--database", target}, wantOut: []string{"--database"}},
		{name: "stale test prefix", target: stale, args: []string{"--yes"}, wantOut: []string{"test database", "clean-databases"}},
		{name: "missing database", target: fmt.Sprintf("drop_missing_%d", stamp), args: []string{"--yes"}, wantOut: []string{"does not exist", "SHOW DATABASES"}},
		{name: "non-interactive without --yes", target: target, wantOut: []string{"no confirmation", "--yes"}},
		{name: "json on a terminal without --yes", target: target, stdin: "y\n", env: interactive, args: []string{"--json"}, wantOut: []string{"no confirmation", "--yes"}},
		{name: "declined", target: target, stdin: "n\n", env: interactive, wantOut: []string{"[y/N]", "no confirmation"}},
		{name: "EOF at the prompt", target: target, env: interactive, wantOut: []string{"[y/N]", "no confirmation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"dolt", "drop-database", tc.target}, tc.args...)
			out := runBDCommandExpectFailure(t, binPath, repoDir, tc.stdin, tc.env, args...)
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("refusal output missing %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "Dropped database") {
				t.Errorf("refusal reported a drop:\n%s", out)
			}
			if !strings.HasPrefix(tc.target, "drop_missing_") && !showDatabasesNamesTest(t, db)[tc.target] {
				t.Errorf("database %q is gone from SHOW DATABASES after a refusal; the guard dropped it anyway", tc.target)
			}
		})
	}

	// With consent the drop runs, and the no-purge contract holds for it and
	// for the residue dropped before every refusal above.
	dropOut := string(runBDCommand(t, binPath, repoDir, nil, "dolt", "drop-database", target, "--yes"))
	if !strings.Contains(dropOut, "Dropped database") || !strings.Contains(dropOut, "DOLT_UNDROP") {
		t.Fatalf("expected a successful drop reporting DOLT_UNDROP recovery under --yes, got:\n%s", dropOut)
	}
	for _, name := range []string{target, residue} {
		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", name))
		cancel()
		if err != nil {
			t.Fatalf("DOLT_UNDROP(%q) failed: something purged the server's dropped databases: %v", name, err)
		}
	}
}
