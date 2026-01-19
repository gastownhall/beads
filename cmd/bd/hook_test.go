package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetWorktreeHash(t *testing.T) {
	// Same input should produce same hash
	hash1 := getWorktreeHash("/some/path/to/worktree")
	hash2 := getWorktreeHash("/some/path/to/worktree")
	if hash1 != hash2 {
		t.Errorf("Same path produced different hashes: %s vs %s", hash1, hash2)
	}

	// Different inputs should produce different hashes
	hash3 := getWorktreeHash("/different/path")
	if hash1 == hash3 {
		t.Errorf("Different paths produced same hash: %s", hash1)
	}

	// Hash should be 16 hex chars (8 bytes)
	if len(hash1) != 16 {
		t.Errorf("Hash length = %d, want 16", len(hash1))
	}
}

func TestExportStatePaths(t *testing.T) {
	beadsDir := "/tmp/test/.beads"
	worktreeRoot := "/tmp/test/worktree"

	stateDir := getExportStateDir(beadsDir)
	if stateDir != "/tmp/test/.beads/export-state" {
		t.Errorf("getExportStateDir() = %s, want /tmp/test/.beads/export-state", stateDir)
	}

	statePath := getExportStatePath(beadsDir, worktreeRoot)
	expectedHash := getWorktreeHash(worktreeRoot)
	expectedPath := "/tmp/test/.beads/export-state/" + expectedHash + ".json"
	if statePath != expectedPath {
		t.Errorf("getExportStatePath() = %s, want %s", statePath, expectedPath)
	}
}

func TestSaveAndLoadExportState(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	worktreeRoot := tmpDir

	// Initially no state
	state, err := loadExportState(beadsDir, worktreeRoot)
	if err != nil {
		t.Fatalf("loadExportState() failed: %v", err)
	}
	if state != nil {
		t.Errorf("loadExportState() returned non-nil for missing state")
	}

	// Save state
	now := time.Now().Truncate(time.Second) // Truncate for comparison
	testState := &ExportState{
		WorktreeRoot:     worktreeRoot,
		WorktreeHash:     getWorktreeHash(worktreeRoot),
		LastExportCommit: "abc123def456",
		LastExportTime:   now,
		JSONLHash:        "hashvalue",
	}
	if err := saveExportState(beadsDir, worktreeRoot, testState); err != nil {
		t.Fatalf("saveExportState() failed: %v", err)
	}

	// Load state back
	loaded, err := loadExportState(beadsDir, worktreeRoot)
	if err != nil {
		t.Fatalf("loadExportState() failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadExportState() returned nil")
	}

	// Verify fields
	if loaded.WorktreeRoot != testState.WorktreeRoot {
		t.Errorf("WorktreeRoot = %s, want %s", loaded.WorktreeRoot, testState.WorktreeRoot)
	}
	if loaded.LastExportCommit != testState.LastExportCommit {
		t.Errorf("LastExportCommit = %s, want %s", loaded.LastExportCommit, testState.LastExportCommit)
	}
	if loaded.JSONLHash != testState.JSONLHash {
		t.Errorf("JSONLHash = %s, want %s", loaded.JSONLHash, testState.JSONLHash)
	}
}

func TestExportStateJSON(t *testing.T) {
	state := &ExportState{
		WorktreeRoot:     "/path/to/worktree",
		WorktreeHash:     "abc123",
		LastExportCommit: "def456",
		LastExportTime:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		JSONLHash:        "hash789",
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}

	// Verify JSON contains expected fields
	jsonStr := string(data)
	expectedFields := []string{
		`"worktree_root"`,
		`"worktree_hash"`,
		`"last_export_commit"`,
		`"last_export_time"`,
		`"jsonl_hash"`,
	}
	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("JSON missing field %s: %s", field, jsonStr)
		}
	}

	// Verify omitempty works for empty optional fields
	stateMinimal := &ExportState{
		WorktreeRoot:     "/path",
		LastExportCommit: "abc",
		LastExportTime:   time.Now(),
	}
	dataMinimal, _ := json.Marshal(stateMinimal)
	jsonMinimal := string(dataMinimal)

	// WorktreeHash and JSONLHash should be omitted when empty
	if strings.Contains(jsonMinimal, `"worktree_hash"`) {
		t.Errorf("Empty worktree_hash should be omitted: %s", jsonMinimal)
	}
	if strings.Contains(jsonMinimal, `"jsonl_hash"`) {
		t.Errorf("Empty jsonl_hash should be omitted: %s", jsonMinimal)
	}
}

func TestUpdateExportStateCommit(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	worktreeRoot := tmpDir

	// Create initial state
	initialState := &ExportState{
		WorktreeRoot:     worktreeRoot,
		LastExportCommit: "old-commit",
		LastExportTime:   time.Now().Add(-time.Hour),
		JSONLHash:        "oldhash",
	}
	if err := saveExportState(beadsDir, worktreeRoot, initialState); err != nil {
		t.Fatalf("saveExportState() failed: %v", err)
	}

	// Update just the commit
	updateExportStateCommit(beadsDir, worktreeRoot, "new-commit")

	// Load and verify
	loaded, err := loadExportState(beadsDir, worktreeRoot)
	if err != nil {
		t.Fatalf("loadExportState() failed: %v", err)
	}

	if loaded.LastExportCommit != "new-commit" {
		t.Errorf("LastExportCommit = %s, want new-commit", loaded.LastExportCommit)
	}
	// JSONLHash should be preserved
	if loaded.JSONLHash != "oldhash" {
		t.Errorf("JSONLHash = %s, want oldhash (should be preserved)", loaded.JSONLHash)
	}
}

func TestComputeJSONLHashForHook(t *testing.T) {
	tmpDir := t.TempDir()

	// Non-existent file should return empty string, no error
	hash, err := computeJSONLHashForHook(filepath.Join(tmpDir, "nonexistent.jsonl"))
	if err != nil {
		t.Errorf("computeJSONLHashForHook() error for missing file: %v", err)
	}
	if hash != "" {
		t.Errorf("computeJSONLHashForHook() = %s, want empty for missing file", hash)
	}

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.jsonl")
	content := `{"id": "test-1", "title": "Test"}
{"id": "test-2", "title": "Test 2"}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Should get a hash
	hash, err = computeJSONLHashForHook(testFile)
	if err != nil {
		t.Fatalf("computeJSONLHashForHook() failed: %v", err)
	}
	if hash == "" {
		t.Error("computeJSONLHashForHook() returned empty hash for existing file")
	}

	// Same content should produce same hash
	hash2, _ := computeJSONLHashForHook(testFile)
	if hash != hash2 {
		t.Errorf("Same file produced different hashes: %s vs %s", hash, hash2)
	}

	// Different content should produce different hash
	testFile2 := filepath.Join(tmpDir, "test2.jsonl")
	if err := os.WriteFile(testFile2, []byte(`{"different": true}`), 0644); err != nil {
		t.Fatalf("Failed to write test file 2: %v", err)
	}
	hash3, _ := computeJSONLHashForHook(testFile2)
	if hash == hash3 {
		t.Errorf("Different files produced same hash: %s", hash)
	}
}
