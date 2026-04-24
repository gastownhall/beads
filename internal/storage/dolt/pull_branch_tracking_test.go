package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPullWithAutoResolve_BranchTrackingFallback verifies that when DOLT_PULL
// returns the GH#3144 branch-tracking error (repo_state.json 'branches' map
// is empty because the remote was added via `bd dolt remote add` rather than
// `dolt clone`), pullWithAutoResolve enters the DOLT_FETCH + DOLT_MERGE
// fallback path.
//
// This test covers the fallback error leg (DOLT_FETCH fails because the test
// store has no configured remote). The full success path — where DOLT_FETCH
// and DOLT_MERGE both succeed — is exercised by
// TestPullWithAutoResolve_BranchTrackingSuccess in the integration test file
// (//go:build integration), which requires a remotesapi-accessible Dolt server.
func TestPullWithAutoResolve_BranchTrackingFallback(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a stored procedure that injects the exact Dolt GH#3144 error text.
	// This reproduces the message DOLT_PULL emits when repo_state.json lacks
	// branch-tracking info for the remote, without requiring a real remote.
	const createSP = `
		CREATE PROCEDURE inject_tracking_error()
		BEGIN
			SIGNAL SQLSTATE 'HY000'
			SET MESSAGE_TEXT = 'Error 1105: You asked to pull from the remote origin, but did not specify a branch. Because this is not the default configured remote for your current branch, you must specify a branch.';
		END`
	if _, err := store.db.ExecContext(ctx, createSP); err != nil {
		t.Skipf("stored procedures with SIGNAL not supported by this Dolt version: %v", err)
	}
	t.Cleanup(func() {
		store.db.ExecContext(context.Background(), "DROP PROCEDURE IF EXISTS inject_tracking_error") //nolint:errcheck
	})

	// pullWithAutoResolve executes the query inside a transaction, checks the
	// error with isBranchTrackingError, and — on match — falls back to
	// DOLT_FETCH(s.remote, s.branch). The test store's s.remote is "" (no
	// remote configured), so DOLT_FETCH immediately fails, producing the
	// "fetch from /" error that confirms the fallback was entered.
	err := store.pullWithAutoResolve(ctx, "CALL inject_tracking_error()")

	// The error must come from the DOLT_FETCH attempt, not from the original
	// DOLT_PULL proxy. If the fallback was not triggered, the error would
	// surface a different message (e.g. the raw SIGNAL text).
	if err == nil {
		t.Fatal("expected an error from DOLT_FETCH (no remote configured), got nil")
	}
	if !strings.Contains(err.Error(), "fetch from") {
		t.Errorf("expected 'fetch from' error confirming fallback was triggered; got: %v", err)
	}
}

func TestPullWithAutoResolve_BranchTrackingFallbackSuccess(t *testing.T) {
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runDoltCmdForBranchTracking(t, remoteDir, "init")
	runDoltSQLForBranchTracking(t, remoteDir, `
		CREATE TABLE branch_tracking_marker (id INT PRIMARY KEY, value TEXT);
		INSERT INTO branch_tracking_marker VALUES (1, 'from remote');
		CALL DOLT_ADD('.');
		CALL DOLT_COMMIT('-Am', 'init: branch tracking marker');
	`)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	remoteURL := "file://" + remoteDir
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("add remote via DOLT_REMOTE: %v", err)
	}
	store.remote = "origin"
	store.branch = "main"

	tx, txErr := store.db.BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatalf("begin tx for raw pull check: %v", txErr)
	}
	_, rawPullErr := tx.ExecContext(ctx, "CALL DOLT_PULL(?, ?)", "origin", "main")
	_ = tx.Rollback()
	if rawPullErr == nil {
		t.Skip("DOLT_PULL succeeded without tracking config; fallback path is not needed for this Dolt version")
	}
	if !isBranchTrackingError(rawPullErr) {
		t.Skipf("DOLT_PULL failed with an unexpected non-tracking error: %v", rawPullErr)
	}

	if err := store.pullWithAutoResolve(ctx, "CALL DOLT_PULL(?, ?)", "origin", "main"); err != nil {
		t.Fatalf("pullWithAutoResolve fallback failed: %v", err)
	}

	var got string
	if err := store.db.QueryRowContext(ctx, "SELECT value FROM branch_tracking_marker WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("query pulled marker: %v", err)
	}
	if got != "from remote" {
		t.Fatalf("pulled marker value = %q, want %q", got, "from remote")
	}
}

func runDoltCmdForBranchTracking(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("dolt", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt %v failed in %s: %v\n%s", args, dir, err, output)
	}
}

func runDoltSQLForBranchTracking(t *testing.T, dir, query string) {
	t.Helper()
	cmd := exec.Command("dolt", "sql", "-q", query)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt sql failed in %s: %v\nQuery: %.200s...\n%s", dir, err, query, output)
	}
}
