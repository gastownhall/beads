package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fast CLI tests converted from scripttest suite
// These run with --no-daemon flag to avoid daemon startup overhead

// setupCLITestDB creates a fresh initialized bd database for CLI tests
func setupCLITestDB(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	runBD(t, tmpDir, "init", "--prefix", "test", "--quiet")
	return tmpDir
}

func TestCLI_Ready(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	runBD(t, tmpDir, "create", "Ready issue", "-p", "1")
	out := runBD(t, tmpDir, "ready")
	if !strings.Contains(out, "Ready issue") {
		t.Errorf("Expected 'Ready issue' in output, got: %s", out)
	}
}

func TestCLI_Create(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Test issue", "-p", "1", "--json")
	
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
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	runBD(t, tmpDir, "create", "First", "-p", "1")
	runBD(t, tmpDir, "create", "Second", "-p", "2")
	
	out := runBD(t, tmpDir, "list", "--json")
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
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Issue to update", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	runBD(t, tmpDir, "update", id, "--status", "in_progress")
	
	out = runBD(t, tmpDir, "show", id, "--json")
	var updated []map[string]interface{}
	json.Unmarshal([]byte(out), &updated)
	if updated[0]["status"] != "in_progress" {
		t.Errorf("Expected status 'in_progress', got: %v", updated[0]["status"])
	}
}

func TestCLI_Close(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Issue to close", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	runBD(t, tmpDir, "close", id, "--reason", "Done")
	
	out = runBD(t, tmpDir, "show", id, "--json")
	var closed []map[string]interface{}
	json.Unmarshal([]byte(out), &closed)
	if closed[0]["status"] != "closed" {
		t.Errorf("Expected status 'closed', got: %v", closed[0]["status"])
	}
}

func TestCLI_DepAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	
	out1 := runBD(t, tmpDir, "create", "First", "-p", "1", "--json")
	out2 := runBD(t, tmpDir, "create", "Second", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	out := runBD(t, tmpDir, "dep", "add", id2, id1)
	if !strings.Contains(out, "Added dependency") {
		t.Errorf("Expected 'Added dependency', got: %s", out)
	}
}

func TestCLI_DepRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	
	out1 := runBD(t, tmpDir, "create", "First", "-p", "1", "--json")
	out2 := runBD(t, tmpDir, "create", "Second", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBD(t, tmpDir, "dep", "add", id2, id1)
	out := runBD(t, tmpDir, "dep", "remove", id2, id1)
	if !strings.Contains(out, "Removed dependency") {
		t.Errorf("Expected 'Removed dependency', got: %s", out)
	}
}

func TestCLI_DepTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	
	out1 := runBD(t, tmpDir, "create", "Parent", "-p", "1", "--json")
	out2 := runBD(t, tmpDir, "create", "Child", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBD(t, tmpDir, "dep", "add", id2, id1)
	out := runBD(t, tmpDir, "dep", "tree", id1)
	if !strings.Contains(out, "Parent") {
		t.Errorf("Expected 'Parent' in tree, got: %s", out)
	}
}

func TestCLI_Blocked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	
	out1 := runBD(t, tmpDir, "create", "Blocker", "-p", "1", "--json")
	out2 := runBD(t, tmpDir, "create", "Blocked", "-p", "1", "--json")
	
	var issue1, issue2 map[string]interface{}
	json.Unmarshal([]byte(out1), &issue1)
	json.Unmarshal([]byte(out2), &issue2)
	
	id1 := issue1["id"].(string)
	id2 := issue2["id"].(string)
	
	runBD(t, tmpDir, "dep", "add", id2, id1)
	out := runBD(t, tmpDir, "blocked")
	if !strings.Contains(out, "Blocked") {
		t.Errorf("Expected 'Blocked' in output, got: %s", out)
	}
}

func TestCLI_Stats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	runBD(t, tmpDir, "create", "Issue 1", "-p", "1")
	runBD(t, tmpDir, "create", "Issue 2", "-p", "1")
	
	out := runBD(t, tmpDir, "stats")
	if !strings.Contains(out, "Total") || !strings.Contains(out, "2") {
		t.Errorf("Expected stats to show 2 issues, got: %s", out)
	}
}

func TestCLI_Show(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Show test", "-p", "1", "--json")
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(out), &issue)
	id := issue["id"].(string)
	
	out = runBD(t, tmpDir, "show", id)
	if !strings.Contains(out, "Show test") {
		t.Errorf("Expected 'Show test' in output, got: %s", out)
	}
}

func TestCLI_Export(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	runBD(t, tmpDir, "create", "Export test", "-p", "1")
	
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	runBD(t, tmpDir, "export", "-o", exportFile)
	
	if _, err := os.Stat(exportFile); os.IsNotExist(err) {
		t.Errorf("Export file not created: %s", exportFile)
	}
}

func TestCLI_Import(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	runBD(t, tmpDir, "create", "Import test", "-p", "1")
	
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	runBD(t, tmpDir, "export", "-o", exportFile)
	
	// Create new db and import
	tmpDir2 := t.TempDir()
	runBD(t, tmpDir2, "init", "--prefix", "test", "--quiet")
	runBD(t, tmpDir2, "import", "-i", exportFile)
	
	out := runBD(t, tmpDir2, "list", "--json")
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

func TestCLI_Labels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Label test", "-p", "1", "--json")
	
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	id := issue["id"].(string)
	
	// Add label
	runBD(t, tmpDir, "label", "add", id, "urgent")
	
	// List labels
	out = runBD(t, tmpDir, "label", "list", id)
	if !strings.Contains(out, "urgent") {
		t.Errorf("Expected 'urgent' label, got: %s", out)
	}
	
	// Remove label
	runBD(t, tmpDir, "label", "remove", id, "urgent")
	out = runBD(t, tmpDir, "label", "list", id)
	if strings.Contains(out, "urgent") {
		t.Errorf("Label should be removed, got: %s", out)
	}
}

func TestCLI_PriorityFormats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	
	// Test numeric priority
	out := runBD(t, tmpDir, "create", "Test P0", "-p", "0", "--json")
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	if issue["priority"].(float64) != 0 {
		t.Errorf("Expected priority 0, got: %v", issue["priority"])
	}
	
	// Test P-format priority
	out = runBD(t, tmpDir, "create", "Test P3", "-p", "P3", "--json")
	jsonStart = strings.Index(out, "{")
	jsonOut = out[jsonStart:]
	json.Unmarshal([]byte(jsonOut), &issue)
	if issue["priority"].(float64) != 3 {
		t.Errorf("Expected priority 3, got: %v", issue["priority"])
	}
}

func TestCLI_Reopen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	t.Parallel()
	tmpDir := setupCLITestDB(t)
	out := runBD(t, tmpDir, "create", "Reopen test", "-p", "1", "--json")
	
	jsonStart := strings.Index(out, "{")
	jsonOut := out[jsonStart:]
	var issue map[string]interface{}
	json.Unmarshal([]byte(jsonOut), &issue)
	id := issue["id"].(string)
	
	// Close it
	runBD(t, tmpDir, "close", id)
	
	// Reopen it
	runBD(t, tmpDir, "reopen", id)
	
	out = runBD(t, tmpDir, "show", id, "--json")
	var reopened []map[string]interface{}
	json.Unmarshal([]byte(out), &reopened)
	if reopened[0]["status"] != "open" {
		t.Errorf("Expected status 'open', got: %v", reopened[0]["status"])
	}
}

// Helper to run bd command in tmpDir with --no-daemon
func runBD(t *testing.T, dir string, args ...string) string {
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
