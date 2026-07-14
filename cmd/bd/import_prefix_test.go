//go:build cgo && integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLI_Import_ForeignPrefix_E2E pins that `bd import` accepts records whose
// id prefix is not the local workspace's — no flag required.
//
// This is a protocol requirement, not an accident: §J6.6 round-trips a stream
// through an EMPTY store, which necessarily carries its own prefix, and §J7.1
// makes ids opaque strings a reader must not parse semantics out of. Every
// import path passes SkipPrefixValidation (import.go, import_shared.go, repo.go).
//
// This test used to assert the opposite — that import REJECTED a foreign prefix
// unless given --skip-prefix-validation, a flag that no longer exists. Being
// build-tagged out of CI (cgo && integration), it rotted unnoticed; it now pins
// the behavior bd actually has.
func TestCLI_Import_ForeignPrefix_E2E(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test container not available")
	}

	// Step 0: Build the bd binary
	tmpDir := t.TempDir()
	bdName := "bd"
	if runtime.GOOS == "windows" {
		bdName = "bd.exe"
	}
	bdBinary := filepath.Join(tmpDir, bdName)

	buildCmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bdBinary, ".")
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

	// Step 2: A JSONL record minted by a DIFFERENT tracker (foreign prefix)
	legacyIssue := `{"id":"legacy-123","title":"Legacy issue","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`
	jsonlPath := filepath.Join(projDir, "legacy.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(legacyIssue+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write legacy JSONL: %v", err)
	}

	// Step 3: Import must succeed with no special flag
	out, err := runCmd("import", "-i", "legacy.jsonl")
	if err != nil {
		t.Fatalf("Import of a foreign-prefix record failed: %v\nOutput: %s", err, out)
	}

	// Step 4: Verify the issue landed under its original id
	out, err = runCmd("list", "--id", "legacy-123", "--json")
	if err != nil {
		t.Errorf("bd list failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "legacy-123") {
		t.Errorf("Expected legacy-123 to be imported, but list output was: %s", out)
	}
}
