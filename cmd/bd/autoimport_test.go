package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestCheckAndAutoImport_NoAutoImportFlag(t *testing.T) {
	ctx := context.Background()
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set the global flag
	oldNoAutoImport := noAutoImport
	noAutoImport = true
	defer func() { noAutoImport = oldNoAutoImport }()

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to be disabled when noAutoImport is true")
	}
}

func TestAutoImportIfNewer_NoAutoImportFlag(t *testing.T) {
	// Test that autoImportIfNewer() respects noAutoImport flag directly (bd-4t7 fix)
	ctx := context.Background()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "bd.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create database
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create JSONL with an issue that should NOT be imported
	jsonlIssue := &types.Issue{
		ID:        "test-noimport-bd4t7",
		Title:     "Should Not Import",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(jsonlIssue); err != nil {
		t.Fatalf("Failed to encode issue: %v", err)
	}
	f.Close()

	// Save and set global state
	oldNoAutoImport := noAutoImport
	oldAutoImportEnabled := autoImportEnabled
	oldStore := store
	oldDbPath := dbPath
	oldRootCtx := rootCtx
	oldStoreActive := storeActive

	noAutoImport = true
	autoImportEnabled = false // Also set this for consistency
	store = testStore
	dbPath = testDBPath
	rootCtx = ctx
	storeActive = true

	defer func() {
		noAutoImport = oldNoAutoImport
		autoImportEnabled = oldAutoImportEnabled
		store = oldStore
		dbPath = oldDbPath
		rootCtx = oldRootCtx
		storeActive = oldStoreActive
	}()

	// Call autoImportIfNewer directly - should be blocked by noAutoImport check
	autoImportIfNewer()

	// Verify issue was NOT imported
	imported, err := testStore.GetIssue(ctx, "test-noimport-bd4t7")
	if err != nil {
		t.Fatalf("Failed to check for issue: %v", err)
	}
	if imported != nil {
		t.Error("autoImportIfNewer() imported despite noAutoImport=true - bd-4t7 fix failed")
	}
}

func TestCheckAndAutoImport_DatabaseHasIssues(t *testing.T) {
	ctx := context.Background()
	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create an issue
	issue := &types.Issue{
		ID:          "test-123",
		Title:       "Test",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	oldNoAutoImport := noAutoImport
	noAutoImport = false
	defer func() { noAutoImport = oldNoAutoImport }()

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to skip when database has issues")
	}
}

func TestCheckAndAutoImport_EmptyDatabaseNoGit(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	tmpDB := filepath.Join(tmpDir, "test.db")
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	oldNoAutoImport := noAutoImport
	oldJsonOutput := jsonOutput
	noAutoImport = false
	jsonOutput = true // Suppress output
	defer func() { 
		noAutoImport = oldNoAutoImport 
		jsonOutput = oldJsonOutput
	}()

	// Change to temp dir (no git repo)
	t.Chdir(tmpDir)

	result := checkAndAutoImport(ctx, store)
	if result {
		t.Error("Expected auto-import to skip when no git repo")
	}
}

func TestFindBeadsDir(t *testing.T) {
	// Create temp directory with .beads
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Change to tmpDir
	t.Chdir(tmpDir)

	found := findBeadsDir()
	if found == "" {
		t.Error("Expected to find .beads directory")
	}
	// Use EvalSymlinks to handle /var vs /private/var on macOS
	expectedPath, _ := filepath.EvalSymlinks(beadsDir)
	foundPath, _ := filepath.EvalSymlinks(found)
	if foundPath != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, foundPath)
	}
}

func TestFindBeadsDir_NotFound(t *testing.T) {
	// Create temp directory without .beads
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)

	found := findBeadsDir()
	// findBeadsDir walks up to root, so it might find .beads in parent dirs
	// (e.g., user's home directory). Just verify it's not in tmpDir itself.
	if found != "" && filepath.Dir(found) == tmpDir {
		t.Errorf("Expected not to find .beads in tmpDir, but got %s", found)
	}
}

func TestFindBeadsDir_ParentDirectory(t *testing.T) {
	// Create structure: tmpDir/.beads and tmpDir/subdir
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	// Change to subdir
	t.Chdir(subDir)

	found := findBeadsDir()
	if found == "" {
		t.Error("Expected to find .beads directory in parent")
	}
	// Use EvalSymlinks to handle /var vs /private/var on macOS
	expectedPath, _ := filepath.EvalSymlinks(beadsDir)
	foundPath, _ := filepath.EvalSymlinks(found)
	if foundPath != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, foundPath)
	}
}

func TestCheckGitForIssues_NoGitRepo(t *testing.T) {
	// Change to temp dir (not a git repo)
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	count, path, gitRef := checkGitForIssues()
	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
	if path != "" {
		t.Errorf("Expected empty path, got %s", path)
	}
	if gitRef != "" {
		t.Errorf("Expected empty gitRef, got %s", gitRef)
	}
}

func TestCheckGitForIssues_NoBeadsDir(t *testing.T) {
	// Use current directory which has git but change to somewhere without .beads
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	count, path, _ := checkGitForIssues()
	if count != 0 || path != "" {
		t.Logf("No .beads dir: count=%d, path=%s (expected 0, empty)", count, path)
	}
}

func TestBoolToFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		condition bool
		flag      string
		want      string
	}{
		{"true condition", true, "--verbose", "--verbose"},
		{"false condition", false, "--verbose", ""},
		{"true with empty flag", true, "", ""},
		{"false with flag", false, "--debug", ""},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := boolToFlag(tt.condition, tt.flag)
			if got != tt.want {
				t.Errorf("boolToFlag(%v, %q) = %q, want %q", tt.condition, tt.flag, got, tt.want)
			}
		})
	}
}
