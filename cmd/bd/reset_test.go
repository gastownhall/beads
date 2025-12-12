//go:build integration
// +build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runBDResetExec runs bd reset via exec.Command for clean state isolation
// Reset has persistent flag state that doesn't work well with in-process testing
func runBDResetExec(t *testing.T, dir string, stdin string, args ...string) (string, error) {
	t.Helper()

	// Add --no-daemon to all commands
	args = append([]string{"--no-daemon"}, args...)

	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCLI_ResetDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create some issues
	runBDExec(t, tmpDir, "create", "Issue 1", "-p", "1")
	runBDExec(t, tmpDir, "create", "Issue 2", "-p", "2")

	// Run dry-run reset
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run reset failed: %v\nOutput: %s", err, out)
	}

	// Verify output contains impact summary
	if !strings.Contains(out, "Reset Impact Summary") {
		t.Errorf("Expected 'Reset Impact Summary' in output, got: %s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("Expected 'dry run' in output, got: %s", out)
	}

	// Verify .beads directory still exists (dry run shouldn't delete anything)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error("dry run should not delete .beads directory")
	}

	// Verify we can still list issues
	listOut := runBDExec(t, tmpDir, "list")
	if !strings.Contains(listOut, "Issue 1") {
		t.Errorf("Issues should still exist after dry run, got: %s", listOut)
	}
}

func TestCLI_ResetForce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create some issues
	runBDExec(t, tmpDir, "create", "Issue to delete", "-p", "1")

	// Run reset with --force (no confirmation needed)
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--force")
	if err != nil {
		t.Fatalf("reset --force failed: %v\nOutput: %s", err, out)
	}

	// Verify success message
	if !strings.Contains(out, "Reset complete") {
		t.Errorf("Expected 'Reset complete' in output, got: %s", out)
	}

	// Verify .beads directory was recreated (reinit by default)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error(".beads directory should be recreated after reset")
	}

	// Verify issues are gone (reinit creates empty workspace)
	listOut := runBDExec(t, tmpDir, "list")
	if strings.Contains(listOut, "Issue to delete") {
		t.Errorf("Issues should be deleted after reset, got: %s", listOut)
	}
}

func TestCLI_ResetSkipInit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue
	runBDExec(t, tmpDir, "create", "Test issue", "-p", "1")

	// Run reset with --skip-init
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--force", "--skip-init")
	if err != nil {
		t.Fatalf("reset --skip-init failed: %v\nOutput: %s", err, out)
	}

	// Verify .beads directory doesn't exist
	beadsDir := filepath.Join(tmpDir, ".beads")
	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Error(".beads directory should not exist after reset with --skip-init")
	}

	// Verify output mentions bd init
	if !strings.Contains(out, "bd init") {
		t.Errorf("Expected hint about 'bd init' in output, got: %s", out)
	}
}

func TestCLI_ResetBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create some issues
	runBDExec(t, tmpDir, "create", "Backup test issue", "-p", "1")

	// Run reset with --backup
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--force", "--backup")
	if err != nil {
		t.Fatalf("reset --backup failed: %v\nOutput: %s", err, out)
	}

	// Verify backup was mentioned in output
	if !strings.Contains(out, "Backup created") || !strings.Contains(out, ".beads-backup-") {
		t.Errorf("Expected backup path in output, got: %s", out)
	}

	// Verify a backup directory exists
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	foundBackup := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".beads-backup-") && entry.IsDir() {
			foundBackup = true
			// Verify backup has content
			backupPath := filepath.Join(tmpDir, entry.Name())
			backupEntries, err := os.ReadDir(backupPath)
			if err != nil {
				t.Fatalf("Failed to read backup dir: %v", err)
			}
			if len(backupEntries) == 0 {
				t.Error("Backup directory should not be empty")
			}
			break
		}
	}

	if !foundBackup {
		t.Error("No backup directory found")
	}
}

func TestCLI_ResetWithConfirmation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue
	runBDExec(t, tmpDir, "create", "Confirm test", "-p", "1")

	// Run reset with confirmation (type 'y')
	out, err := runBDResetExec(t, tmpDir, "y\n", "reset")
	if err != nil {
		t.Fatalf("reset with confirmation failed: %v\nOutput: %s", err, out)
	}

	// Verify success
	if !strings.Contains(out, "Reset complete") {
		t.Errorf("Expected 'Reset complete' in output, got: %s", out)
	}
}

func TestCLI_ResetCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue
	runBDExec(t, tmpDir, "create", "Keep this", "-p", "1")

	// Run reset but cancel (type 'n')
	out, err := runBDResetExec(t, tmpDir, "n\n", "reset")
	// Cancellation is not an error
	if err != nil {
		t.Fatalf("reset cancellation failed: %v\nOutput: %s", err, out)
	}

	// Verify cancelled message
	if !strings.Contains(out, "cancelled") {
		t.Errorf("Expected 'cancelled' in output, got: %s", out)
	}

	// Verify issue still exists
	listOut := runBDExec(t, tmpDir, "list")
	if !strings.Contains(listOut, "Keep this") {
		t.Errorf("Issues should still exist after cancelled reset, got: %s", listOut)
	}
}

func TestCLI_ResetNoBeadsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Create temp dir without .beads
	tmpDir := createTempDirWithCleanup(t)

	// Run reset - should fail
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--force")
	if err == nil {
		t.Error("reset should fail when no .beads directory exists")
	}

	// Verify error message
	if !strings.Contains(out, "no .beads directory found") && !strings.Contains(out, "Error") {
		t.Errorf("Expected error about missing .beads directory, got: %s", out)
	}
}

func TestCLI_ResetWithIssues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create multiple issues with different states
	runBDExec(t, tmpDir, "create", "Open issue 1", "-p", "1")
	runBDExec(t, tmpDir, "create", "Open issue 2", "-p", "2")

	out1 := runBDExec(t, tmpDir, "create", "To close", "-p", "1", "--json")
	id := extractIDFromJSON(t, out1)
	runBDExec(t, tmpDir, "close", id)

	// Run dry-run to see counts
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v\nOutput: %s", err, out)
	}

	// Verify impact shows correct counts
	if !strings.Contains(out, "Issues to delete") {
		t.Errorf("Expected 'Issues to delete' in output, got: %s", out)
	}
	if !strings.Contains(out, "Open:") {
		t.Errorf("Expected 'Open:' count in output, got: %s", out)
	}
	if !strings.Contains(out, "Closed:") {
		t.Errorf("Expected 'Closed:' count in output, got: %s", out)
	}

	// Now do actual reset
	out, err = runBDResetExec(t, tmpDir, "", "reset", "--force")
	if err != nil {
		t.Fatalf("reset failed: %v\nOutput: %s", err, out)
	}

	// Verify all issues are gone
	listOut := runBDExec(t, tmpDir, "list")
	if strings.Contains(listOut, "Open issue") || strings.Contains(listOut, "To close") {
		t.Errorf("All issues should be deleted after reset, got: %s", listOut)
	}
}

func TestCLI_ResetVerbose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue
	runBDExec(t, tmpDir, "create", "Verbose test", "-p", "1")

	// Run reset with --verbose
	out, err := runBDResetExec(t, tmpDir, "", "reset", "--force", "--verbose")
	if err != nil {
		t.Fatalf("reset --verbose failed: %v\nOutput: %s", err, out)
	}

	// Verify verbose output shows more details
	if !strings.Contains(out, "Starting reset") {
		t.Errorf("Expected 'Starting reset' in verbose output, got: %s", out)
	}
}

// extractIDFromJSON extracts an ID from JSON output
func extractIDFromJSON(t *testing.T, out string) string {
	t.Helper()
	// Try both formats: "id":"xxx" and "id": "xxx"
	idx := strings.Index(out, `"id":"`)
	offset := 6
	if idx == -1 {
		idx = strings.Index(out, `"id": "`)
		offset = 7
	}
	if idx == -1 {
		t.Fatalf("No id found in JSON output: %s", out)
	}
	start := idx + offset
	end := strings.Index(out[start:], `"`)
	if end == -1 {
		t.Fatalf("Malformed JSON output: %s", out)
	}
	return out[start : start+end]
}
