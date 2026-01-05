package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShow_ExternalRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	// Build bd binary
	tmpBin := filepath.Join(t.TempDir(), "bd")
	buildCmd := exec.Command("go", "build", "-o", tmpBin, "./")
	buildCmd.Dir = "."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build bd: %v\n%s", err, out)
	}

	// Create temp directory for test database
	tmpDir := t.TempDir()

	// Initialize beads
	initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
	initCmd.Dir = tmpDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	// Create issue with external ref
	createCmd := exec.Command(tmpBin, "--no-daemon", "create", "External ref test", "-p", "1",
		"--external-ref", "https://example.com/spec.md", "--json")
	createCmd.Dir = tmpDir
	createOut, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, createOut)
	}

	var issue map[string]interface{}
	if err := json.Unmarshal(createOut, &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, createOut)
	}
	id := issue["id"].(string)

	// Show the issue and verify external ref is displayed
	showCmd := exec.Command(tmpBin, "--no-daemon", "show", id)
	showCmd.Dir = tmpDir
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}

	out := string(showOut)
	if !strings.Contains(out, "External Ref:") {
		t.Errorf("expected 'External Ref:' in output, got: %s", out)
	}
	if !strings.Contains(out, "https://example.com/spec.md") {
		t.Errorf("expected external ref URL in output, got: %s", out)
	}
}

func TestShow_NoExternalRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	// Build bd binary
	tmpBin := filepath.Join(t.TempDir(), "bd")
	buildCmd := exec.Command("go", "build", "-o", tmpBin, "./")
	buildCmd.Dir = "."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build bd: %v\n%s", err, out)
	}

	tmpDir := t.TempDir()

	// Initialize beads
	initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
	initCmd.Dir = tmpDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	// Create issue WITHOUT external ref
	createCmd := exec.Command(tmpBin, "--no-daemon", "create", "No ref test", "-p", "1", "--json")
	createCmd.Dir = tmpDir
	createOut, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, createOut)
	}

	var issue map[string]interface{}
	if err := json.Unmarshal(createOut, &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, createOut)
	}
	id := issue["id"].(string)

	// Show the issue - should NOT contain External Ref line
	showCmd := exec.Command(tmpBin, "--no-daemon", "show", id)
	showCmd.Dir = tmpDir
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}

	out := string(showOut)
	if strings.Contains(out, "External Ref:") {
		t.Errorf("expected no 'External Ref:' line for issue without external ref, got: %s", out)
	}
}
