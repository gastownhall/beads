//go:build integration
// +build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fast CLI tests converted from scripttest suite
// These use in-process testing (calling rootCmd.Execute directly) for speed
// A few tests still use exec.Command for end-to-end validation
//
// Performance improvement (bd-ky74):
//   - Before: exec.Command() tests took 2-4 seconds each (~40s total)
//   - After: in-process tests take <1 second each, ~10x faster
//   - End-to-end test (TestCLI_EndToEnd) still validates binary with exec.Command

var (
	inProcessMutex sync.Mutex // Protects concurrent access to rootCmd and global state
)

// setupCLITestDB creates a fresh initialized bd database for CLI tests
func setupCLITestDB(t *testing.T) string {
	t.Helper()
	tmpDir := createTempDirWithCleanup(t)
	runBDInProcess(t, tmpDir, "init", "--prefix", "test", "--quiet")
	return tmpDir
}

// createTempDirWithCleanup creates a temp directory with non-fatal cleanup
// This prevents test failures from SQLite file lock cleanup issues
func createTempDirWithCleanup(t *testing.T) string {
	t.Helper()
	
	tmpDir, err := os.MkdirTemp("", "bd-cli-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	
	t.Cleanup(func() {
		// Retry cleanup with delays to handle SQLite file locks
		// Don't fail the test if cleanup fails - just log it
		for i := 0; i < 5; i++ {
			err := os.RemoveAll(tmpDir)
			if err == nil {
				return // Success
			}
			if i < 4 {
				time.Sleep(50 * time.Millisecond)
			}
		}
		// Final attempt failed - log but don't fail test
		t.Logf("Warning: Failed to clean up temp dir %s (SQLite file locks)", tmpDir)
	})
	
	return tmpDir
}

// runBDInProcess runs bd commands in-process by calling rootCmd.Execute
// This is ~10-20x faster than exec.Command because it avoids process spawn overhead
func runBDInProcess(t *testing.T, dir string, args ...string) string {
	t.Helper()
	
	// Serialize all in-process test execution to avoid race conditions
	// rootCmd, cobra state, and viper are not thread-safe
	inProcessMutex.Lock()
	defer inProcessMutex.Unlock()
	
	// Add --no-daemon to all commands except init
	if len(args) > 0 && args[0] != "init" {
		args = append([]string{"--no-daemon"}, args...)
	}
	
	// Save original state
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	oldDir, _ := os.Getwd()
	oldArgs := os.Args
	
	// Change to test directory
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Failed to chdir to %s: %v", dir, err)
	}
	
	// Capture stdout/stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	
	// Set args for rootCmd
	rootCmd.SetArgs(args)
	os.Args = append([]string{"bd"}, args...)
	
	// Set environment
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer os.Unsetenv("BEADS_NO_DAEMON")
	
	// Execute command
	err := rootCmd.Execute()
	
	// Close and clean up all global state to prevent contamination between tests
	if store != nil {
		store.Close()
		store = nil
	}
	if daemonClient != nil {
		daemonClient.Close()
		daemonClient = nil
	}
	
	// Reset all global flags and state
	dbPath = ""
	actor = ""
	jsonOutput = false
	noDaemon = false
	noAutoFlush = false
	noAutoImport = false
	sandboxMode = false
	noDb = false
	autoFlushEnabled = true
	storeActive = false
	flushFailureCount = 0
	lastFlushError = nil
	// Shutdown any existing FlushManager
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}
	// Reset context state
	rootCtx = nil
	rootCancel = nil
	
	// Give SQLite time to release file locks before cleanup
	time.Sleep(10 * time.Millisecond)
	
	// Close writers and restore
	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	os.Chdir(oldDir)
	os.Args = oldArgs
	rootCmd.SetArgs(nil)
	
	// Read output (keep stdout and stderr separate)
	var outBuf, errBuf bytes.Buffer
	outBuf.ReadFrom(rOut)
	errBuf.ReadFrom(rErr)
	
	stdout := outBuf.String()
	stderr := errBuf.String()
	
	if err != nil {
		t.Fatalf("bd %v failed: %v\nStdout: %s\nStderr: %s", args, err, stdout, stderr)
	}
	
	// Return only stdout (stderr contains warnings that break JSON parsing)
	return stdout
}

func TestCLI_Ready(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	runBDInProcess(t, tmpDir, "create", "Ready issue", "-p", "1")
	out := runBDInProcess(t, tmpDir, "ready")
	if !strings.Contains(out, "Ready issue") {
		t.Errorf("Expected 'Ready issue' in output, got: %s", out)
	}
}

func TestCLI_Create(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Test issue", "-p", "1", "--json")
	
	// Extract JSON from output (may contain warnings before JSON)
	jsonStart := strings.Index(out, "{")
	if jsonStart == -1 {
		t.Fatalf("No JSON found in output: %s", out)
	}
	jsonOut := out[jsonStart:]
	
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOut), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, jsonOut)
	}
	if result["title"] != "Test issue" {
		t.Errorf("Expected title 'Test issue', got: %v", result["title"])
	}
}

func TestCLI_List(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	runBDInProcess(t, tmpDir, "create", "First", "-p", "1")
	runBDInProcess(t, tmpDir, "create", "Second", "-p", "2")
	
	out := runBDInProcess(t, tmpDir, "list", "--json")
	var issues []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(issues))
	}
}

func TestCLI_Update(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue to update", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	runBDInProcess(t, tmpDir, "update", id, "--status", "in_progress")
	
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	if updated[0]["status"] != "in_progress" {
		t.Errorf("Expected status 'in_progress', got: %v", updated[0]["status"])
	}
}

func TestCLI_UpdateLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue for label testing", "-p", "2", "--json")

	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Test adding labels
	runBDInProcess(t, tmpDir, "update", id, "--add-label", "feature", "--add-label", "backend")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	labels := updated[0]["labels"].([]interface{})
	if len(labels) != 2 {
		t.Errorf("Expected 2 labels after add, got: %d", len(labels))
	}
	hasBackend, hasFeature := false, false
	for _, l := range labels {
		if l.(string) == "backend" {
			hasBackend = true
		}
		if l.(string) == "feature" {
			hasFeature = true
		}
	}
	if !hasBackend || !hasFeature {
		t.Errorf("Expected labels 'backend' and 'feature', got: %v", labels)
	}

	// Test removing a label
	runBDInProcess(t, tmpDir, "update", id, "--remove-label", "backend")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	json.Unmarshal([]byte(out), &updated)
	labels = updated[0]["labels"].([]interface{})
	if len(labels) != 1 {
		t.Errorf("Expected 1 label after remove, got: %d", len(labels))
	}
	if labels[0].(string) != "feature" {
		t.Errorf("Expected label 'feature', got: %v", labels[0])
	}

	// Test setting labels (replaces all)
	runBDInProcess(t, tmpDir, "update", id, "--set-labels", "api,database,critical")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	json.Unmarshal([]byte(out), &updated)
	labels = updated[0]["labels"].([]interface{})
	if len(labels) != 3 {
		t.Errorf("Expected 3 labels after set, got: %d", len(labels))
	}
	expectedLabels := map[string]bool{"api": true, "database": true, "critical": true}
	for _, l := range labels {
		if !expectedLabels[l.(string)] {
			t.Errorf("Unexpected label: %v", l)
		}
	}
}

func TestCLI_Close(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue to close", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	runBDInProcess(t, tmpDir, "close", id, "--reason", "Done")
	
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var closed []map[string]interface{}
	json.Unmarshal([]byte(out), &closed)
	if closed[0]["status"] != "closed" {
		t.Errorf("Expected status 'closed', got: %v", closed[0]["status"])
	}
	if closed[0]["close_reason"] != "Done" {
		t.Errorf("Expected close_reason 'Done', got: %v", closed[0]["close_reason"])
	}
}

func TestCLI_DepAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	
	out1 := runBDInProcess(t, tmpDir, "create", "First", "-p", "1", "--json")
	out2 := runBDInProcess(t, tmpDir, "create", "Second", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	out := runBDInProcess(t, tmpDir, "dep", "add", id2, id1)
	if !strings.Contains(out, "Added dependency") {
		t.Errorf("Expected 'Added dependency', got: %s", out)
	}
}

func TestCLI_DepRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	
	out1 := runBDInProcess(t, tmpDir, "create", "First", "-p", "1", "--json")
	out2 := runBDInProcess(t, tmpDir, "create", "Second", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBDInProcess(t, tmpDir, "dep", "add", id2, id1)
	out := runBDInProcess(t, tmpDir, "dep", "remove", id2, id1)
	if !strings.Contains(out, "Removed dependency") {
		t.Errorf("Expected 'Removed dependency', got: %s", out)
	}
}

func TestCLI_DepTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	
	out1 := runBDInProcess(t, tmpDir, "create", "Parent", "-p", "1", "--json")
	out2 := runBDInProcess(t, tmpDir, "create", "Child", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBDInProcess(t, tmpDir, "dep", "add", id2, id1)
	out := runBDInProcess(t, tmpDir, "dep", "tree", id1)
	if !strings.Contains(out, "Parent") {
		t.Errorf("Expected 'Parent' in tree, got: %s", out)
	}
}

func TestCLI_Blocked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	
	out1 := runBDInProcess(t, tmpDir, "create", "Blocker", "-p", "1", "--json")
	out2 := runBDInProcess(t, tmpDir, "create", "Blocked", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBDInProcess(t, tmpDir, "dep", "add", id2, id1)
	out := runBDInProcess(t, tmpDir, "blocked")
	if !strings.Contains(out, "Blocked") {
		t.Errorf("Expected 'Blocked' in output, got: %s", out)
	}
}

func TestCLI_Stats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	runBDInProcess(t, tmpDir, "create", "Issue 1", "-p", "1")
	runBDInProcess(t, tmpDir, "create", "Issue 2", "-p", "1")
	
	out := runBDInProcess(t, tmpDir, "stats")
	if !strings.Contains(out, "Total") || !strings.Contains(out, "2") {
		t.Errorf("Expected stats to show 2 issues, got: %s", out)
	}
}

func TestCLI_Show(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Show test", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	out = runBDInProcess(t, tmpDir, "show", id)
	if !strings.Contains(out, "Show test") {
		t.Errorf("Expected 'Show test' in output, got: %s", out)
	}
}

func TestCLI_Export(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	runBDInProcess(t, tmpDir, "create", "Export test", "-p", "1")
	
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	runBDInProcess(t, tmpDir, "export", "-o", exportFile)
	
	if _, err := os.Stat(exportFile); os.IsNotExist(err) {
		t.Errorf("Export file not created: %s", exportFile)
	}
}

func TestCLI_Import(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	runBDInProcess(t, tmpDir, "create", "Import test", "-p", "1")
	
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	runBDInProcess(t, tmpDir, "export", "-o", exportFile)
	
	// Create new db and import
	tmpDir2 := createTempDirWithCleanup(t)
	runBDInProcess(t, tmpDir2, "init", "--prefix", "test", "--quiet")
	runBDInProcess(t, tmpDir2, "import", "-i", exportFile)
	
	out := runBDInProcess(t, tmpDir2, "list", "--json")
	var issues []map[string]interface{}
	json.Unmarshal([]byte(out), &issues)
	if len(issues) != 1 {
		t.Errorf("Expected 1 imported issue, got %d", len(issues))
	}
}

var testBD string

func init() {
	// Use existing bd binary from repo root if available, otherwise build once
	bdBinary := "bd"
	if runtime.GOOS == "windows" {
		bdBinary = "bd.exe"
	}
	
	// Check if bd binary exists in repo root (../../bd from cmd/bd/)
	repoRoot := filepath.Join("..", "..")
	existingBD := filepath.Join(repoRoot, bdBinary)
	if _, err := os.Stat(existingBD); err == nil {
		// Use existing binary
		testBD, _ = filepath.Abs(existingBD)
		return
	}
	
	// Fall back to building once (for CI or fresh checkouts)
	tmpDir, err := os.MkdirTemp("", "bd-cli-test-*")
	if err != nil {
		panic(err)
	}
	testBD = filepath.Join(tmpDir, bdBinary)
	cmd := exec.Command("go", "build", "-o", testBD, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
}

// runBDExec runs bd via exec.Command for end-to-end testing
// This is kept for a few tests to ensure the actual binary works correctly
func runBDExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	
	// Add --no-daemon to all commands except init
	if len(args) > 0 && args[0] != "init" {
		args = append([]string{"--no-daemon"}, args...)
	}
	
	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %v failed: %v\nOutput: %s", args, err, out)
	}
	return string(out)
}

// TestCLI_EndToEnd performs end-to-end testing using the actual binary
// This ensures the compiled binary works correctly when executed normally
func TestCLI_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	
	tmpDir := createTempDirWithCleanup(t)
	
	// Test full workflow with exec.Command to validate binary
	runBDExec(t, tmpDir, "init", "--prefix", "test", "--quiet")
	
	out := runBDExec(t, tmpDir, "create", "E2E test", "-p", "1", "--json")
	var issue map[string]interface{}
	jsonStart := strings.Index(out, "{")
	json.Unmarshal([]byte(out[jsonStart:]), &issue)
	id := issue["id"].(string)
	
	runBDExec(t, tmpDir, "update", id, "--status", "in_progress")
	runBDExec(t, tmpDir, "close", id, "--reason", "Done")
	
	out = runBDExec(t, tmpDir, "show", id, "--json")
	var closed []map[string]interface{}
	json.Unmarshal([]byte(out), &closed)
	
	if closed[0]["status"] != "closed" {
		t.Errorf("Expected status 'closed', got: %v", closed[0]["status"])
	}
	
	// Test export
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	runBDExec(t, tmpDir, "export", "-o", exportFile)
	
	if _, err := os.Stat(exportFile); os.IsNotExist(err) {
		t.Errorf("Export file not created: %s", exportFile)
	}
}

func TestCLI_Labels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Label test", "-p", "1", "--json")
	
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	id := issue["id"].(string)
	
	// Add label
	runBDInProcess(t, tmpDir, "label", "add", id, "urgent")
	
	// List labels
	out = runBDInProcess(t, tmpDir, "label", "list", id)
	if !strings.Contains(out, "urgent") {
		t.Errorf("Expected 'urgent' label, got: %s", out)
	}
	
	// Remove label
	runBDInProcess(t, tmpDir, "label", "remove", id, "urgent")
	out = runBDInProcess(t, tmpDir, "label", "list", id)
	if strings.Contains(out, "urgent") {
		t.Errorf("Label should be removed, got: %s", out)
	}
}

func TestCLI_PriorityFormats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	
	// Test numeric priority
	out := runBDInProcess(t, tmpDir, "create", "Test P0", "-p", "0", "--json")
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	if issue["priority"].(float64) != 0 {
		t.Errorf("Expected priority 0, got: %v", issue["priority"])
	}
	
	// Test P-format priority
	out = runBDInProcess(t, tmpDir, "create", "Test P3", "-p", "P3", "--json")
	jsonStart = strings.Index(out, "{")
	jsonOut = out[jsonStart:]
	json.Unmarshal([]byte(jsonOut), &issue)
	if issue["priority"].(float64) != 3 {
		t.Errorf("Expected priority 3, got: %v", issue["priority"])
	}

	// Test update with P-format
	id := issue["id"].(string)
	runBDInProcess(t, tmpDir, "update", id, "-p", "P1")
	
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	if updated[0]["priority"].(float64) != 1 {
		t.Errorf("Expected priority 1 after update, got: %v", updated[0]["priority"])
	}
}

func TestCLI_Reopen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Reopen test", "-p", "1", "--json")
	
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	id := issue["id"].(string)
	
	// Close it
	runBDInProcess(t, tmpDir, "close", id)
	
	// Reopen it
	runBDInProcess(t, tmpDir, "reopen", id)
	
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var reopened []map[string]interface{}
	json.Unmarshal([]byte(out), &reopened)
	if reopened[0]["status"] != "open" {
		t.Errorf("Expected status 'open', got: %v", reopened[0]["status"])
	}
}


