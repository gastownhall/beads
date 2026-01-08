package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// TestFindJSONLPath_RelativeDbPath tests that findJSONLPath() returns an absolute
// path or empty string, never a relative path. This is critical for daemon_sync_branch.go
// which calls filepath.Rel(repoRoot, jsonlPath) - mixing absolute base with relative
// target fails.
//
// Bug: When dbPath is set to a relative fallback (e.g., ".beads/beads.db"),
// findJSONLPath() returns ".beads/issues.jsonl" (relative), causing:
//   "Rel: can't make .beads/issues.jsonl relative to /path/to/repo"
//
// See: https://github.com/steveyegge/beads/issues/959
func TestFindJSONLPath_RelativeDbPath(t *testing.T) {
	// Save/restore global state
	origDbPath := dbPath
	defer func() { dbPath = origDbPath }()

	// Set relative dbPath (triggers bug)
	// This simulates the fallback in main.go:471 when no database is found
	dbPath = filepath.Join(".beads", beads.CanonicalDatabaseName)

	result := findJSONLPath()

	// Bug: returns ".beads/issues.jsonl" (relative)
	// Fixed: returns absolute path or ""
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("findJSONLPath() returned relative path: %q\n"+
			"Expected: absolute path or empty string\n"+
			"This will cause filepath.Rel() to fail in daemon_sync_branch.go",
			result)
	}
}

// TestFindJSONLPath_BEADS_JSONL_Relative tests that BEADS_JSONL env var is
// canonicalized to absolute path even when set to a relative value.
func TestFindJSONLPath_BEADS_JSONL_Relative(t *testing.T) {
	// Save/restore global state
	origDbPath := dbPath
	origEnv := os.Getenv("BEADS_JSONL")
	defer func() {
		dbPath = origDbPath
		if origEnv != "" {
			os.Setenv("BEADS_JSONL", origEnv)
		} else {
			os.Unsetenv("BEADS_JSONL")
		}
	}()

	// Set relative BEADS_JSONL (should be canonicalized)
	dbPath = ""
	os.Setenv("BEADS_JSONL", "./custom/path/issues.jsonl")

	result := findJSONLPath()

	// The env var path should be canonicalized to absolute
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("findJSONLPath() with BEADS_JSONL returned relative path: %q\n"+
			"Expected: absolute path (canonicalized from env var)",
			result)
	}
}

// TestFindJSONLPath_BEADS_DIR_Relative tests that BEADS_DIR env var is
// canonicalized to absolute path even when set to a relative value.
// Note: BEADS_DIR is already canonicalized in FindBeadsDir() (line 428),
// but this test verifies the invariant holds through findJSONLPath().
func TestFindJSONLPath_BEADS_DIR_Relative(t *testing.T) {
	// Save/restore global state
	origDbPath := dbPath
	origBeadsDir := os.Getenv("BEADS_DIR")
	origBeadsJSONL := os.Getenv("BEADS_JSONL")
	defer func() {
		dbPath = origDbPath
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if origBeadsJSONL != "" {
			os.Setenv("BEADS_JSONL", origBeadsJSONL)
		} else {
			os.Unsetenv("BEADS_JSONL")
		}
	}()

	// Create temp directory with .beads structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	// Create issues.jsonl to make it a valid beads directory
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Change to parent of tmpDir so relative path works
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	os.Chdir(filepath.Dir(tmpDir))

	// Set relative BEADS_DIR (should be canonicalized by FindBeadsDir)
	relPath := filepath.Base(tmpDir) + "/.beads"
	os.Setenv("BEADS_DIR", relPath)
	os.Unsetenv("BEADS_JSONL")
	dbPath = ""

	result := findJSONLPath()

	// The path should be absolute (BEADS_DIR canonicalized by FindBeadsDir)
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("findJSONLPath() with relative BEADS_DIR returned relative path: %q\n"+
			"Expected: absolute path (canonicalized from BEADS_DIR)",
			result)
	}
}

// TestFindJSONLPath_EmptyDbPath tests that empty dbPath returns empty string
// or falls back to FindBeadsDir() path (which should be absolute).
func TestFindJSONLPath_EmptyDbPath(t *testing.T) {
	// Save/restore global state
	origDbPath := dbPath
	origEnv := os.Getenv("BEADS_JSONL")
	defer func() {
		dbPath = origDbPath
		if origEnv != "" {
			os.Setenv("BEADS_JSONL", origEnv)
		} else {
			os.Unsetenv("BEADS_JSONL")
		}
	}()

	// Clear both dbPath and BEADS_JSONL
	dbPath = ""
	os.Unsetenv("BEADS_JSONL")

	result := findJSONLPath()

	// Result should be absolute or empty (FindBeadsDir returns absolute or "")
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("findJSONLPath() with empty dbPath returned relative path: %q\n"+
			"Expected: absolute path or empty string",
			result)
	}
}
