//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLI_Import_PrefixValidation_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}

	// Step 0: Build the bd binary
	tmpDir := t.TempDir()
	bdName := "bd"
	if runtime.GOOS == "windows" {
		bdName = "bd.exe"
	}
	bdBinary := filepath.Join(tmpDir, bdName)
	
	buildCmd := exec.Command("go", "build", "-o", bdBinary, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build bd: %v\nOutput: %s", err, out)
	}

	// Step 1: Setup a database with a specific prefix
	projDir := filepath.Join(tmpDir, "proj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatalf("Failed to create proj dir: %v", err)
	}
	
	runCmd := func(args ...string) (string, error) {
		cmd := exec.Command(bdBinary, args...)
		cmd.Dir = projDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := runCmd("init", "--prefix", "current", "--quiet"); err != nil {
		t.Fatalf("bd init failed: %v\nOutput: %s", err, out)
	}

	// Step 2: Create a JSONL file with a mismatched prefix
	legacyIssue := `{"id":"legacy-123","title":"Legacy issue","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`
	jsonlPath := filepath.Join(projDir, "legacy.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(legacyIssue+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write legacy JSONL: %v", err)
	}

	// Step 3: Attempt import without flag - should fail
	out, err := runCmd("import", "-i", "legacy.jsonl")
	if err == nil {
		t.Error("Expected import to fail without --skip-prefix-validation")
	}
	if !strings.Contains(out, "prefix validation failed") {
		t.Errorf("Expected prefix validation error, got: %s", out)
	}

	// Step 4: Attempt import with --skip-prefix-validation - should succeed
	out, err = runCmd("import", "-i", "legacy.jsonl", "--skip-prefix-validation")
	if err != nil {
		t.Errorf("Import failed with --skip-prefix-validation: %v\nOutput: %s", err, out)
	}

	// Step 5: Verify issue was imported
	out, err = runCmd("list", "--id", "legacy-123", "--json")
	if err != nil {
		t.Errorf("bd list failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "legacy-123") {
		t.Errorf("Expected legacy-123 to be imported, but list output was: %s", out)
	}
}
