//go:build cgo && integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// templateDB holds a pre-initialized bd database directory.
// Created once via sync.Once, then copied for each test to avoid
// running bd init (which creates SQLite DB, config files, etc.) per test.
// This optimization eliminates ~2s per test from repeated initialization.
var (
	templateDBDir  string
	templateDBOnce sync.Once
	templateDBErr  error
)

// initTemplateDB creates a single template database that can be copied for each test.
// Uses exec.Command (subprocess) to avoid polluting Cobra global state.
// The testBD binary is already built once in init().
func initTemplateDB() {
	templateDBOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "bd-cli-template-*")
		if err != nil {
			templateDBErr = fmt.Errorf("failed to create template dir: %w", err)
			return
		}
		templateDBDir = tmpDir

		// Use exec.Command to run bd init in a subprocess.
		// This avoids any Cobra global state pollution that would affect subsequent
		// in-process test runs.
		cmd := exec.Command(testBD, "init", "--prefix", "test", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			templateDBErr = fmt.Errorf("template bd init failed: %v\n%s", err, out)
			return
		}
	})
}

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0750); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0600); err != nil {
				return err
			}
		}
	}
	return nil
}

// setupCLITestDB creates a fresh initialized bd database for CLI tests.
// Uses a cached template directory to avoid running bd init for every test.
// The template is created once via sync.Once and copied for each test.
func setupCLITestDB(t *testing.T) string {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}
	initTemplateDB()
	if templateDBErr != nil {
		t.Fatalf("Template DB initialization failed: %v", templateDBErr)
	}
	tmpDir := createTempDirWithCleanup(t)
	if err := copyDir(templateDBDir, tmpDir); err != nil {
		t.Fatalf("Failed to copy template DB: %v", err)
	}
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

	// Execute command
	err := rootCmd.Execute()

	// Close and clean up all global state to prevent contamination between tests
	if store != nil {
		store.Close()
		store = nil
	}
	// Reset all global flags and state
	dbPath = ""
	actor = ""
	jsonOutput = false
	sandboxMode = false
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

func TestCLI_UpdateEphemeral(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue for ephemeral testing", "-p", "2", "--json")

	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("Failed to parse create output: %v", err)
	}
	id := issue["id"].(string)

	// Mark as ephemeral
	runBDInProcess(t, tmpDir, "update", id, "--ephemeral")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("Failed to parse show output: %v", err)
	}
	if updated[0]["ephemeral"] != true {
		t.Errorf("Expected ephemeral to be true after --ephemeral, got: %v", updated[0]["ephemeral"])
	}
}

func TestCLI_UpdatePersistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)

	// Create ephemeral issue directly
	out := runBDInProcess(t, tmpDir, "create", "Ephemeral issue", "-p", "2", "--ephemeral", "--json")

	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("Failed to parse create output: %v", err)
	}
	id := issue["id"].(string)

	// Verify it's ephemeral
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var initial []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &initial); err != nil {
		t.Fatalf("Failed to parse show output: %v", err)
	}
	if initial[0]["ephemeral"] != true {
		t.Fatalf("Expected issue to be ephemeral initially, got: %v", initial[0]["ephemeral"])
	}

	// Promote to persistent
	runBDInProcess(t, tmpDir, "update", id, "--persistent")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("Failed to parse show output after persistent: %v", err)
	}
	if updated[0]["ephemeral"] == true {
		t.Errorf("Expected ephemeral to be false after --persistent, got: %v", updated[0]["ephemeral"])
	}
}

func TestCLI_UpdateEphemeralMutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue for mutual exclusion test", "-p", "2", "--json")

	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Both flags should error
	_, stderr, err := runBDInProcessAllowError(t, tmpDir, "update", id, "--ephemeral", "--persistent")
	if err == nil {
		t.Errorf("Expected error when both flags specified, got none")
	}
	if !strings.Contains(stderr, "cannot specify both") {
		t.Errorf("Expected mutual exclusion error message, got: %v", stderr)
	}
}

func TestCLI_UpdateAppendNotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue for append-notes test", "-p", "2", "--notes", "Original notes", "--json")

	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Test appending notes
	runBDInProcess(t, tmpDir, "update", id, "--append-notes", "Appended content")

	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	notes := updated[0]["notes"].(string)
	if notes != "Original notes\nAppended content" {
		t.Errorf("Expected 'Original notes\\nAppended content', got: %q", notes)
	}

	// Test appending to empty notes
	out = runBDInProcess(t, tmpDir, "create", "Issue with empty notes", "-p", "2", "--json")
	json.Unmarshal([]byte(out), &issue)
	id2 := issue["id"].(string)

	runBDInProcess(t, tmpDir, "update", id2, "--append-notes", "First note")

	out = runBDInProcess(t, tmpDir, "show", id2, "--json")
	json.Unmarshal([]byte(out), &updated)
	notes = updated[0]["notes"].(string)
	if notes != "First note" {
		t.Errorf("Expected 'First note', got: %q", notes)
	}
}

func TestCLI_UpdateAppendNotesMutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)
	out := runBDInProcess(t, tmpDir, "create", "Issue for notes mutual exclusion", "-p", "2", "--json")

	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Both --notes and --append-notes should error
	_, stderr, err := runBDInProcessAllowError(t, tmpDir, "update", id, "--notes", "New notes", "--append-notes", "Appended")
	if err == nil {
		t.Errorf("Expected error when both --notes and --append-notes specified, got none")
	}
	if !strings.Contains(stderr, "cannot specify both --notes and --append-notes") {
		t.Errorf("Expected mutual exclusion error message, got: %v", stderr)
	}
}

func TestCLI_NoteCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := setupCLITestDB(t)

	// Create an issue with initial notes
	out := runBDInProcess(t, tmpDir, "create", "Issue for note test", "-p", "2", "--notes", "Original notes", "--json")
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)

	// Test: bd note <id> <text> appends to existing notes
	runBDInProcess(t, tmpDir, "note", id, "Added via note command")
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	notes := updated[0]["notes"].(string)
	if notes != "Original notes\nAdded via note command" {
		t.Errorf("Expected 'Original notes\\nAdded via note command', got: %q", notes)
	}

	// Test: bd note <id> with multiple words joins them
	runBDInProcess(t, tmpDir, "note", id, "second", "note", "here")
	out = runBDInProcess(t, tmpDir, "show", id, "--json")
	json.Unmarshal([]byte(out), &updated)
	notes = updated[0]["notes"].(string)
	if notes != "Original notes\nAdded via note command\nsecond note here" {
		t.Errorf("Expected three lines, got: %q", notes)
	}

	// Test: bd note on issue with no existing notes
	out = runBDInProcess(t, tmpDir, "create", "Issue with no notes", "-p", "2", "--json")
	json.Unmarshal([]byte(out), &issue)
	id2 := issue["id"].(string)

	runBDInProcess(t, tmpDir, "note", id2, "First note ever")
	out = runBDInProcess(t, tmpDir, "show", id2, "--json")
	json.Unmarshal([]byte(out), &updated)
	notes = updated[0]["notes"].(string)
	if notes != "First note ever" {
		t.Errorf("Expected 'First note ever', got: %q", notes)
	}

	// Test: bd note with no text should fail
	_, stderr, err := runBDInProcessAllowError(t, tmpDir, "note", id)
	if err == nil {
		t.Errorf("Expected error when no note text provided, got none")
	}
	if !strings.Contains(stderr, "no note text provided") {
		t.Errorf("Expected 'no note text provided' error, got: %v", stderr)
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
	cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", testBD, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
}

// runBDExec runs bd via exec.Command for end-to-end testing
// This is kept for a few tests to ensure the actual binary works correctly
func runBDExec(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %v failed: %v\nOutput: %s", args, err, out)
	}
	return string(out)
}

// runBDExecAllowErrorWithEnv runs bd via exec.Command with custom env vars,
// returning combined output and any error (does not fail the test on error).
func runBDExecAllowErrorWithEnv(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCLI_EndToEnd performs end-to-end testing using the actual binary
// This ensures the compiled binary works correctly when executed normally
func TestCLI_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
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

// runBDInProcessAllowError is like runBDInProcess but doesn't fail on error
// Returns stdout, stderr, and any error from command execution
func runBDInProcessAllowError(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()

	inProcessMutex.Lock()
	defer inProcessMutex.Unlock()

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	oldDir, _ := os.Getwd()
	oldArgs := os.Args

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Failed to chdir to %s: %v", dir, err)
	}

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	rootCmd.SetArgs(args)
	os.Args = append([]string{"bd"}, args...)

	cmdErr := rootCmd.Execute()

	if store != nil {
		store.Close()
		store = nil
	}
	dbPath = ""
	actor = ""
	jsonOutput = false
	sandboxMode = false
	rootCtx = nil
	rootCancel = nil

	// Give SQLite time to release file locks before cleanup
	time.Sleep(10 * time.Millisecond)

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	os.Chdir(oldDir)
	os.Args = oldArgs
	rootCmd.SetArgs(nil)

	var outBuf, errBuf bytes.Buffer
	outBuf.ReadFrom(rOut)
	errBuf.ReadFrom(rErr)

	return outBuf.String(), errBuf.String(), cmdErr
}

// TestCLI_CreateDryRun tests the --dry-run flag for bd create command (bd-nib2)
func TestCLI_CreateDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}

	t.Run("BasicDryRunPreview", func(t *testing.T) {
		// Note: Not using t.Parallel() because inProcessMutex serializes execution anyway
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run
		out := runBDInProcess(t, tmpDir, "create", "Test dry run issue", "-p", "1", "--dry-run")

		// Verify output contains dry-run indicator
		if !strings.Contains(out, "[DRY RUN]") {
			t.Errorf("Expected '[DRY RUN]' in output, got: %s", out)
		}
		if !strings.Contains(out, "Would create issue") {
			t.Errorf("Expected 'Would create issue' in output, got: %s", out)
		}
		if !strings.Contains(out, "Test dry run issue") {
			t.Errorf("Expected title in output, got: %s", out)
		}
		if !strings.Contains(out, "(will be generated)") {
			t.Errorf("Expected '(will be generated)' for ID, got: %s", out)
		}

		// Verify no issue was actually created
		listOut := runBDInProcess(t, tmpDir, "list", "--json")
		var issues []map[string]interface{}
		json.Unmarshal([]byte(listOut), &issues)
		if len(issues) != 0 {
			t.Errorf("Expected 0 issues after dry-run, got %d", len(issues))
		}
	})

	t.Run("DryRunWithJSONOutput", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run --json
		out := runBDInProcess(t, tmpDir, "create", "JSON dry run test", "-p", "2", "-t", "bug", "--dry-run", "--json")

		// Find JSON in output (may have warnings before it)
		jsonStart := strings.Index(out, "{")
		if jsonStart < 0 {
			t.Fatalf("No JSON found in output: %s", out)
		}
		jsonOut := out[jsonStart:]

		var issue map[string]interface{}
		if err := json.Unmarshal([]byte(jsonOut), &issue); err != nil {
			t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, jsonOut)
		}

		// Verify JSON has empty ID (not a placeholder string)
		id, ok := issue["id"]
		if !ok {
			t.Error("Expected 'id' field in JSON output")
		}
		if id != "" {
			t.Errorf("Expected empty ID in dry-run JSON, got: %v", id)
		}

		// Verify other fields are populated
		if issue["title"] != "JSON dry run test" {
			t.Errorf("Expected title 'JSON dry run test', got: %v", issue["title"])
		}
		if issue["issue_type"] != "bug" {
			t.Errorf("Expected issue_type 'bug', got: %v", issue["issue_type"])
		}
		if issue["priority"].(float64) != 2 {
			t.Errorf("Expected priority 2, got: %v", issue["priority"])
		}

		// Verify no issue was actually created
		listOut := runBDInProcess(t, tmpDir, "list", "--json")
		var issues []map[string]interface{}
		json.Unmarshal([]byte(listOut), &issues)
		if len(issues) != 0 {
			t.Errorf("Expected 0 issues after dry-run, got %d", len(issues))
		}
	})

	t.Run("DryRunWithLabelsAndDeps", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run including labels and deps
		out := runBDInProcess(t, tmpDir, "create", "Issue with extras", "-p", "1",
			"--labels", "urgent,backend",
			"--deps", "blocks:test-123",
			"--dry-run")

		// Verify labels are shown in preview
		if !strings.Contains(out, "Labels:") {
			t.Errorf("Expected 'Labels:' in output, got: %s", out)
		}
		if !strings.Contains(out, "urgent") {
			t.Errorf("Expected 'urgent' label in output, got: %s", out)
		}
		if !strings.Contains(out, "backend") {
			t.Errorf("Expected 'backend' label in output, got: %s", out)
		}

		// Verify dependencies are shown
		if !strings.Contains(out, "Dependencies:") {
			t.Errorf("Expected 'Dependencies:' in output, got: %s", out)
		}
		if !strings.Contains(out, "blocks:test-123") {
			t.Errorf("Expected 'blocks:test-123' dependency in output, got: %s", out)
		}
	})

	t.Run("DryRunWithRigPrefix", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run and --prefix (simulates cross-rig creation)
		// Note: This won't actually route to another rig since we don't have one,
		// but it should show the target rig in the preview
		out := runBDInProcess(t, tmpDir, "create", "Cross-rig issue", "-p", "1",
			"--prefix", "other-rig",
			"--dry-run")

		// Verify target rig is shown in preview
		if !strings.Contains(out, "Target rig:") {
			t.Errorf("Expected 'Target rig:' in output, got: %s", out)
		}
		if !strings.Contains(out, "other-rig") {
			t.Errorf("Expected 'other-rig' in output, got: %s", out)
		}
	})

	t.Run("DryRunWithFileReturnsError", func(t *testing.T) {
		// This test must use exec.Command because FatalError calls os.Exit(1)
		// which would kill the test process if run in-process
		tmpDir := createTempDirWithCleanup(t)

		// Initialize the database first
		initCmd := exec.Command(testBD, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		initCmd.Env = os.Environ()
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Create a dummy markdown file
		mdFile := filepath.Join(tmpDir, "issues.md")
		os.WriteFile(mdFile, []byte("# Test Issue\n\nDescription here"), 0644)

		// Run create with --dry-run and --file (should error)
		cmd := exec.Command(testBD, "create", "--file", mdFile, "--dry-run")
		cmd.Dir = tmpDir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()

		if err == nil {
			t.Error("Expected error when using --dry-run with --file, but got none")
		}

		// Verify error message is informative
		if !strings.Contains(string(out), "--dry-run is not supported with --file") {
			t.Errorf("Expected error about --dry-run with --file, got: %s", out)
		}
	})

	t.Run("DryRunWithEventType", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run and event-specific fields
		out := runBDInProcess(t, tmpDir, "create", "Event issue", "-p", "1",
			"--type", "event",
			"--event-category", "agent.started",
			"--dry-run")

		// Verify event category is shown in preview
		if !strings.Contains(out, "Event category:") {
			t.Errorf("Expected 'Event category:' in output, got: %s", out)
		}
		if !strings.Contains(out, "agent.started") {
			t.Errorf("Expected 'agent.started' in output, got: %s", out)
		}
	})

	t.Run("DryRunWithExplicitID", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Run create with --dry-run and explicit ID
		out := runBDInProcess(t, tmpDir, "create", "Explicit ID issue", "-p", "1",
			"--id", "test-explicit123",
			"--dry-run")

		// Verify explicit ID is shown (not "(will be generated)")
		if strings.Contains(out, "(will be generated)") {
			t.Errorf("Expected explicit ID in output, but got '(will be generated)': %s", out)
		}
		if !strings.Contains(out, "test-explicit123") {
			t.Errorf("Expected 'test-explicit123' in output, got: %s", out)
		}
	})
}

// TestCLI_CommentsListMisplacedSyntax ensures "bd comments list" gets a helpful error (GH#3542).
func TestCLI_CommentsListMisplacedSyntax(t *testing.T) {
	t.Parallel()

	tmpDir := setupCLITestDB(t)
	stdout, stderr, err := runBDInProcessAllowError(t, tmpDir, "comments", "list")
	if err == nil {
		t.Fatalf("expected non-zero exit, got stdout=%q stderr=%q", stdout, stderr)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "bd comments") || !strings.Contains(combined, "<issue-id>") {
		t.Fatalf("expected hint with bd comments and issue-id placeholder, got stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestCLI_CommentsAddShortID tests that 'comments add' accepts short IDs (issue #1070)
// Most bd commands accept short IDs (e.g., "5wbm") but comments add previously required
// full IDs (e.g., "mike.vibe-coding-5wbm"). This test ensures short IDs work.
//
// Note: Short IDs work because the code calls utils.ResolvePartialID().
func TestCLI_CommentsAddShortID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}

	t.Run("ShortIDWithCommentsAdd", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Create an issue and get its full ID
		out := runBDInProcess(t, tmpDir, "create", "Issue for comment test", "-p", "1", "--json")

		jsonStart := strings.Index(out, "{")
		if jsonStart < 0 {
			t.Fatalf("No JSON found in output: %s", out)
		}
		jsonOut := out[jsonStart:]

		var issue map[string]interface{}
		if err := json.Unmarshal([]byte(jsonOut), &issue); err != nil {
			t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, jsonOut)
		}

		fullID := issue["id"].(string)
		t.Logf("Created issue with full ID: %s", fullID)

		// Extract short ID (the part after the last hyphen in prefix-hash format)
		// For IDs like "test-abc123", the short ID is "abc123"
		parts := strings.Split(fullID, "-")
		if len(parts) < 2 {
			t.Fatalf("Unexpected ID format: %s", fullID)
		}
		shortID := parts[len(parts)-1]
		t.Logf("Using short ID: %s", shortID)

		// Add a comment using the SHORT ID (not full ID)
		stdout, stderr, err := runBDInProcessAllowError(t, tmpDir, "comments", "add", shortID, "Test comment with short ID")
		if err != nil {
			t.Fatalf("comments add failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "Comment added") {
			t.Errorf("Expected 'Comment added' in output, got: %s", stdout)
		}

		// Verify the comment was actually added by listing comments (use full ID for list)
		stdout, stderr, err = runBDInProcessAllowError(t, tmpDir, "comments", fullID)
		if err != nil {
			t.Fatalf("comments list failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "Test comment with short ID") {
			t.Errorf("Expected comment text in list output, got: %s", stdout)
		}
	})

	t.Run("PartialIDWithCommentsAdd", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Create an issue
		out := runBDInProcess(t, tmpDir, "create", "Issue for partial ID test", "-p", "1", "--json")

		jsonStart := strings.Index(out, "{")
		jsonOut := out[jsonStart:]

		var issue map[string]interface{}
		json.Unmarshal([]byte(jsonOut), &issue)
		fullID := issue["id"].(string)

		// Extract short ID and use only first 4 characters (partial match)
		parts := strings.Split(fullID, "-")
		shortID := parts[len(parts)-1]
		if len(shortID) > 4 {
			shortID = shortID[:4] // Use only first 4 chars for partial match
		}
		t.Logf("Full ID: %s, Partial ID: %s", fullID, shortID)

		// Add comment using partial ID
		stdout, stderr, err := runBDInProcessAllowError(t, tmpDir, "comments", "add", shortID, "Comment via partial ID")
		if err != nil {
			t.Fatalf("comments add with partial ID failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "Comment added") {
			t.Errorf("Expected 'Comment added' in output, got: %s", stdout)
		}
	})

	t.Run("CommentAliasWithShortID", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		// Create an issue
		out := runBDInProcess(t, tmpDir, "create", "Issue for alias test", "-p", "1", "--json")

		jsonStart := strings.Index(out, "{")
		jsonOut := out[jsonStart:]

		var issue map[string]interface{}
		json.Unmarshal([]byte(jsonOut), &issue)
		fullID := issue["id"].(string)

		// Extract short ID
		parts := strings.Split(fullID, "-")
		shortID := parts[len(parts)-1]

		// Use the 'comment' alias (deprecated but should still work)
		stdout, stderr, err := runBDInProcessAllowError(t, tmpDir, "comment", shortID, "Comment via alias with short ID")
		if err != nil {
			t.Fatalf("comment alias failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}

		if !strings.Contains(stdout, "Comment added") {
			t.Errorf("Expected 'Comment added' in output, got: %s", stdout)
		}
	})
}

// TestCLI_CreateRejectsFlagLikeTitles verifies that positional arguments starting
// with - or -- are rejected as likely misinterpreted flags (bd-2c0).
func TestCLI_CreateRejectsFlagLikeTitles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}

	tests := []struct {
		name  string
		title string
	}{
		{"DoubleDashHelp", "--help"},
		{"DoubleDashVersion", "--version"},
		{"SingleDashFlag", "-p"},
		{"DoubleDashArbitrary", "--foo-bar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := createTempDirWithCleanup(t)

			// Initialize the database
			initCmd := exec.Command(testBD, "init", "--prefix", "test", "--quiet")
			initCmd.Dir = tmpDir
			initCmd.Env = os.Environ()
			if out, err := initCmd.CombinedOutput(); err != nil {
				t.Fatalf("init failed: %v\n%s", err, out)
			}

			// Attempt to create with a flag-like positional title
			cmd := exec.Command(testBD, "create", tc.title)
			cmd.Dir = tmpDir
			cmd.Env = os.Environ()
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Errorf("Expected error for flag-like title %q, but got none.\nOutput: %s", tc.title, out)
			}

			if !strings.Contains(string(out), "looks like a flag") {
				t.Errorf("Expected 'looks like a flag' error for %q, got: %s", tc.title, out)
			}
		})
	}

	// Verify that --title flag with dash-prefixed value is still allowed
	t.Run("TitleFlagAllowsDashes", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)

		out := runBDInProcess(t, tmpDir, "create", "--title", "--unusual-title", "-p", "1", "--json")
		if !strings.Contains(out, "--unusual-title") {
			t.Errorf("Expected title '--unusual-title' in output, got: %s", out)
		}
	})
}

// TestCLI_CreateNoHistory tests that the --no-history CLI flag is wired through
// to the created issue (GH#2619). A storage-layer test already covers the DB
// semantics; this test verifies the CLI flag is actually parsed and passed.
func TestCLI_CreateNoHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}

	t.Run("NoHistoryFlagSetOnCreatedIssue", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)
		out := runBDInProcess(t, tmpDir, "create", "No-history agent bead", "-p", "2", "--no-history", "--json")

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, out)
		}
		if result["no_history"] != true {
			t.Errorf("Expected no_history=true on created issue, got: %v", result["no_history"])
		}
		// Must NOT be ephemeral (mutually exclusive)
		if result["ephemeral"] == true {
			t.Errorf("no-history issue must not be ephemeral, but ephemeral=true")
		}
	})

	t.Run("NoHistoryPersistedAfterShow", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)
		out := runBDInProcess(t, tmpDir, "create", "No-history bead for show test", "-p", "2", "--no-history", "--json")

		var created map[string]interface{}
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatalf("Failed to parse create output: %v\nOutput: %s", err, out)
		}
		id := created["id"].(string)

		showOut := runBDInProcess(t, tmpDir, "show", id, "--json")
		var issues []map[string]interface{}
		if err := json.Unmarshal([]byte(showOut), &issues); err != nil {
			t.Fatalf("Failed to parse show output: %v\nOutput: %s", err, showOut)
		}
		if len(issues) == 0 {
			t.Fatalf("show returned no issues for id %s", id)
		}
		if issues[0]["no_history"] != true {
			t.Errorf("Expected no_history=true after show, got: %v", issues[0]["no_history"])
		}
	})

	t.Run("EphemeralAndNoHistoryMutuallyExclusive", func(t *testing.T) {
		tmpDir := setupCLITestDB(t)
		_, stderr, err := runBDInProcessAllowError(t, tmpDir, "create", "Should fail", "--ephemeral", "--no-history")
		if err == nil {
			t.Error("Expected error when combining --ephemeral and --no-history, got none")
		}
		if !strings.Contains(stderr, "mutually exclusive") {
			t.Errorf("Expected 'mutually exclusive' in stderr, got: %s", stderr)
		}
	})
}

// TestCLI_WispListTypeFilter tests that bd mol wisp list --type filters correctly.
func TestCLI_WispListTypeFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Create two ephemeral wisps of different built-in types
	runBDInProcess(t, tmpDir, "create", "Bug wisp", "--ephemeral", "--type", "bug", "-p", "2")
	runBDInProcess(t, tmpDir, "create", "Task wisp", "--ephemeral", "--type", "task", "-p", "2")

	// --type bug should return only the bug wisp
	out := runBDInProcess(t, tmpDir, "mol", "wisp", "list", "--type", "bug", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, out)
	}
	wisps, ok := result["wisps"].([]interface{})
	if !ok {
		t.Fatalf("Expected 'wisps' array in JSON output, got: %v", result)
	}
	if len(wisps) != 1 {
		t.Errorf("Expected 1 wisp with type=bug, got %d: %v", len(wisps), wisps)
	}
	if len(wisps) == 1 {
		w := wisps[0].(map[string]interface{})
		if w["type"] != "bug" {
			t.Errorf("Expected wisp type=bug, got: %v", w["type"])
		}
	}

	// --type task should return only the task wisp
	out = runBDInProcess(t, tmpDir, "mol", "wisp", "list", "--type", "task", "--json")
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, out)
	}
	wisps, ok = result["wisps"].([]interface{})
	if !ok {
		t.Fatalf("Expected 'wisps' array, got: %v", result)
	}
	if len(wisps) != 1 {
		t.Errorf("Expected 1 wisp with type=task, got %d", len(wisps))
	}
	if len(wisps) == 1 {
		w := wisps[0].(map[string]interface{})
		if w["type"] != "task" {
			t.Errorf("Expected wisp type=task, got: %v", w["type"])
		}
	}

	// No --type filter returns both
	out = runBDInProcess(t, tmpDir, "mol", "wisp", "list", "--json")
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, out)
	}
	wisps = result["wisps"].([]interface{})
	if len(wisps) != 2 {
		t.Errorf("Expected 2 wisps without type filter, got %d", len(wisps))
	}
}

// TestCLI_WispGCExcludeType tests that bd mol wisp gc --exclude-type skips
// wisps of the excluded type during garbage collection.
func TestCLI_WispGCExcludeType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Create two ephemeral wisps of different built-in types.
	// Use --age=0s so that ALL wisps (regardless of actual age) are treated
	// as abandoned GC candidates.
	bugOut := runBDInProcess(t, tmpDir, "create", "Bug wisp to keep", "--ephemeral", "--type", "bug", "-p", "2", "--json")
	taskOut := runBDInProcess(t, tmpDir, "create", "Task wisp to gc", "--ephemeral", "--type", "task", "-p", "2", "--json")

	var bugCreated, taskCreated map[string]interface{}
	if err := json.Unmarshal([]byte(bugOut), &bugCreated); err != nil {
		t.Fatalf("Failed to parse bug create output: %v\nOutput: %s", err, bugOut)
	}
	if err := json.Unmarshal([]byte(taskOut), &taskCreated); err != nil {
		t.Fatalf("Failed to parse task create output: %v\nOutput: %s", err, taskOut)
	}
	bugID := bugCreated["id"].(string)
	taskID := taskCreated["id"].(string)

	// Dry-run GC with --exclude-type bug, --age=0s (treats all wisps as abandoned).
	// The WispGCResult dry-run JSON uses cleaned_ids to list what would be deleted.
	gcOut := runBDInProcess(t, tmpDir, "mol", "wisp", "gc", "--exclude-type", "bug", "--age", "0s", "--dry-run", "--json")
	var gcResult map[string]interface{}
	if err := json.Unmarshal([]byte(gcOut), &gcResult); err != nil {
		t.Fatalf("Failed to parse gc JSON: %v\nOutput: %s", err, gcOut)
	}

	// cleaned_ids holds the IDs that would be deleted (dry_run=true).
	cleanedRaw, _ := gcResult["cleaned_ids"].([]interface{})
	cleanedIDs := make(map[string]bool, len(cleanedRaw))
	for _, v := range cleanedRaw {
		if id, ok := v.(string); ok {
			cleanedIDs[id] = true
		}
	}

	if !cleanedIDs[taskID] {
		t.Errorf("Expected task wisp %s in GC candidates (not excluded), got cleaned_ids: %v", taskID, cleanedRaw)
	}
	if cleanedIDs[bugID] {
		t.Errorf("Bug wisp %s should be excluded from GC via --exclude-type bug, but appeared in cleaned_ids", bugID)
	}
}
