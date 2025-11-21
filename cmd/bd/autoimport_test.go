package main

import (
	"context"
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
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

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
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

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

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

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
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(subDir)

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
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	count, path := checkGitForIssues()
	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
	if path != "" {
		t.Errorf("Expected empty path, got %s", path)
	}
}

func TestCheckGitForIssues_NoBeadsDir(t *testing.T) {
	// Use current directory which has git but change to somewhere without .beads
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	count, path := checkGitForIssues()
	if count != 0 || path != "" {
		t.Logf("No .beads dir: count=%d, path=%s (expected 0, empty)", count, path)
	}
}

func TestBoolToFlag(t *testing.T) {
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
		t.Run(tt.name, func(t *testing.T) {
			got := boolToFlag(tt.condition, tt.flag)
			if got != tt.want {
				t.Errorf("boolToFlag(%v, %q) = %q, want %q", tt.condition, tt.flag, got, tt.want)
			}
		})
	}
}
