//go:build integration

package dolt

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestPullWithAutoResolve_BranchTrackingSuccess is the end-to-end regression
// test for GH#3144. It reproduces the exact failure scenario:
//
//  1. A remote is added via DOLT_REMOTE('add', ...) rather than `dolt clone`,
//     so repo_state.json's 'branches' map is empty (no tracking config).
//  2. DOLT_PULL('origin', 'main') fails with the branch-tracking error.
//  3. pullWithAutoResolve detects the error and falls back to
//     DOLT_FETCH('origin', 'main') + DOLT_MERGE('origin/main').
//  4. The pull succeeds and the local store reflects the remote content.
//
// Prerequisites (all satisfied by the integration CI environment):
//   - `dolt` CLI ≥ 1.81 available in PATH
//   - Loopback TCP available for two dolt sql-server instances
func TestPullWithAutoResolve_BranchTrackingSuccess(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	baseDir := t.TempDir()

	// ── Remote server ──────────────────────────────────────────────────────────
	// Set up a "remote" Dolt repo with a committed beads schema. This represents
	// the centralised NAS repo that `bd dolt remote add` points at.
	remoteDir := filepath.Join(baseDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runCmd(t, remoteDir, "dolt", "init")

	initSQL := `
		CREATE TABLE branch_tracking_marker (id INT PRIMARY KEY, value TEXT);
		INSERT INTO branch_tracking_marker VALUES (1, 'from remote');
		CALL DOLT_ADD('.');
		CALL DOLT_COMMIT('-Am', 'init: branch tracking marker');
	`
	runDoltSQL(t, remoteDir, initSQL)

	remoteSQLPort, err := findFreePort()
	if err != nil {
		t.Fatalf("find free port (SQL): %v", err)
	}
	remotesAPIPort, err := findFreePort()
	if err != nil {
		t.Fatalf("find free port (remotesapi): %v", err)
	}
	stopRemote := startDoltServer(t, remoteDir, remoteSQLPort, remotesAPIPort)
	defer stopRemote()

	// ── Local DoltStore ────────────────────────────────────────────────────────
	// Create a fresh local DoltStore (embedded auto-start). This mirrors what a
	// user gets after `bd init` on a new machine: a local repo with no remote.
	localDir := filepath.Join(baseDir, "local")
	localStore, localCleanup := setupFederationStore(t, ctx, localDir, "local")
	defer localCleanup()

	// Add the remote via DOLT_REMOTE SQL — NOT via dolt clone. This leaves
	// repo_state.json with an empty 'branches' map: the prerequisite for GH#3144.
	remoteURL := fmt.Sprintf("doltremoteapi://127.0.0.1:%d/beads", remotesAPIPort)
	if _, err := localStore.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("failed to add remote: %v", err)
	}
	localStore.remote = "origin"
	localStore.branch = "main"

	// ── Confirm the bug scenario is present ────────────────────────────────────
	// DOLT_PULL('origin', 'main') must fail with the tracking error, otherwise
	// we are not actually testing the fallback — we are testing a working pull.
	// If Dolt has fixed GH#3144 upstream, skip gracefully.
	tx, txErr := localStore.db.BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatalf("begin tx for bug-check: %v", txErr)
	}
	_, rawPullErr := tx.ExecContext(ctx, "CALL DOLT_PULL(?, ?)", "origin", "main")
	_ = tx.Rollback()

	if rawPullErr == nil {
		t.Skip("DOLT_PULL succeeded without tracking config — GH#3144 may be fixed upstream; skipping fallback test")
	}
	if !isBranchTrackingError(rawPullErr) {
		t.Skipf("DOLT_PULL failed with unexpected error (not a tracking error) — skipping: %v", rawPullErr)
	}
	t.Logf("confirmed GH#3144 scenario: DOLT_PULL produced tracking error: %v", rawPullErr)

	// ── Verify the fix: pullWithAutoResolve succeeds via fallback ──────────────
	err = localStore.pullWithAutoResolve(ctx, "CALL DOLT_PULL(?, ?)", "origin", "main")
	if err != nil {
		t.Errorf("pullWithAutoResolve should succeed via DOLT_FETCH+DOLT_MERGE fallback, got: %v", err)
	}
	var got string
	if err := localStore.db.QueryRowContext(ctx, "SELECT value FROM branch_tracking_marker WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("query pulled marker: %v", err)
	}
	if got != "from remote" {
		t.Fatalf("pulled marker value = %q, want %q", got, "from remote")
	}
}

// findFreePort returns an available TCP port on 127.0.0.1.
func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// startDoltServer starts a dolt sql-server subprocess on sqlPort with remotesapi
// on remotesAPIPort and waits until both ports are accepting connections.
// Returns a stop function that kills the server and waits for it to exit.
func startDoltServer(t *testing.T, dir string, sqlPort, remotesAPIPort int) func() {
	t.Helper()

	cmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(sqlPort),
		"--remotesapi-port", strconv.Itoa(remotesAPIPort),
		"--loglevel", "error",
	)
	cmd.Dir = dir

	if err := cmd.Start(); err != nil {
		t.Fatalf("start dolt sql-server: %v", err)
	}

	// Wait for both the SQL port and the remotesapi port to accept connections.
	for _, port := range []int{sqlPort, remotesAPIPort} {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		deadline := time.Now().Add(30 * time.Second)
		for {
			if time.Now().After(deadline) {
				cmd.Process.Kill() //nolint:errcheck
				t.Fatalf("dolt sql-server did not start on %s within 30s", addr)
			}
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				conn.Close()
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	return func() {
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
			cmd.Wait()         //nolint:errcheck
		}
	}
}
