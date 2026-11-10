//go:build cgo

package main

// CLI-level refusal gate for `bd dolt drop-database` (#6099): every refusal
// is exercised through the real built binary against the shared test Dolt
// server, must exit non-zero with actionable guidance, and must leave the
// database untouched on the server (still in SHOW DATABASES) — proving no
// DROP ran behind the refusal. The one consented success subtest pins the
// no-purge contract end to end: after bd's drop, DOLT_UNDROP succeeds.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// runBDCommandExpectFailure is runBDCommand's refusal half: the command must
// exit non-zero, and its combined output is the assertion surface.
func runBDCommandExpectFailure(t *testing.T, binPath, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = dir
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
	if err == nil {
		t.Fatalf("%v succeeded, want a refusal:\n%s", args, out)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%v timed out after %s\n%s", args, 10*time.Second, out)
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

// TestDropDatabaseRefusalsLeaveDatabaseIntact drives the real command
// through every refusal the guards define (#6099: the CLI must offer a safe
// way out, not a destructive surprise), and one consented drop whose
// database stays DOLT_UNDROP-recoverable afterwards.
func TestDropDatabaseRefusalsLeaveDatabaseIntact(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	binPath := buildBDUnderTest(t)
	const workspaceDB = "drop_refusal_ws"
	repoDir := t.TempDir()
	writeServerRepo(t, repoDir, workspaceDB, "127.0.0.1", "drop-refusal-test-origin", testDoltServerPort)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// The workspace's configured database exists on the server, so the
	// workspace refusal is not riding on a missing name.
	mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", workspaceDB))
	t.Cleanup(func() {
		mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", workspaceDB))
	})

	cases := []struct {
		name    string
		target  string
		exists  bool     // target is (or should be) listed on the server
		wantOut []string // substrings of the refusal output
	}{
		{
			"protected system schema",
			"mysql",
			true,
			[]string{"system database", `"mysql"`},
		},
		{
			"global shared-server database",
			"beads_global",
			false, // may not exist on the test server; the guard fires first
			[]string{"global shared-server database"},
		},
		{
			"workspace's own configured database",
			workspaceDB,
			true,
			[]string{"this workspace is initialized against", ".beads"},
		},
		{
			"missing database",
			fmt.Sprintf("drop_missing_%d", time.Now().UnixNano()),
			false,
			[]string{"does not exist on this server", "SHOW DATABASES"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runBDCommandExpectFailure(t, binPath, repoDir, nil, "dolt", "drop-database", tc.target)
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("refusal output missing %q:\n%s", want, out)
				}
			}
			// The refusal must not have dropped anything: an existing
			// target is still listed on the server.
			if tc.exists && !showDatabasesNamesTest(t, db)[tc.target] {
				t.Errorf("database %q is gone from SHOW DATABASES after a refusal; the guard dropped it anyway", tc.target)
			}
		})
	}

	t.Run("stale test prefix is deflected to clean-databases", func(t *testing.T) {
		stale := fmt.Sprintf("testdb_droprefusal_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", stale))
		t.Cleanup(func() { mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", stale)) })

		out := runBDCommandExpectFailure(t, binPath, repoDir, nil, "dolt", "drop-database", stale)
		for _, want := range []string{"stale test database", "clean-databases"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal output missing %q:\n%s", want, out)
			}
		}
		if !showDatabasesNamesTest(t, db)[stale] {
			t.Errorf("stale database %q is gone from SHOW DATABASES after a refusal", stale)
		}
	})

	t.Run("no consent without --yes, database intact", func(t *testing.T) {
		target := fmt.Sprintf("drop_noconsent_%d", time.Now().UnixNano())
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", target))
		t.Cleanup(func() { mustExec(t, ctx, db, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", target)) })

		// The harness runs the binary with no TTY on stdin: a
		// non-interactive run without --yes must refuse before any DROP.
		out := runBDCommandExpectFailure(t, binPath, repoDir, nil, "dolt", "drop-database", target)
		for _, want := range []string{"--yes to confirm"} {
			if !strings.Contains(out, want) {
				t.Errorf("consent refusal output missing %q:\n%s", want, out)
			}
		}
		if !showDatabasesNamesTest(t, db)[target] {
			t.Errorf("database %q is gone from SHOW DATABASES after a consent refusal", target)
		}

		// With consent, the drop runs and — the no-purge contract —
		// DOLT_UNDROP can still restore it.
		dropOut := string(runBDCommand(t, binPath, repoDir, nil, "dolt", "drop-database", target, "--yes"))
		if !strings.Contains(dropOut, "Dropped database") {
			t.Fatalf("expected a successful drop under --yes, got:\n%s", dropOut)
		}
		if !strings.Contains(dropOut, "DOLT_UNDROP") {
			t.Errorf("success output should report DOLT_UNDROP recovery:\n%s", dropOut)
		}
		undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", target)); err != nil {
			t.Fatalf("DOLT_UNDROP(%q) failed after bd's drop (no purge must have run): %v", target, err)
		}
	})
}
