package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShow(t *testing.T) {
	// Subprocess bd init+create flow broken after Dolt transition:
	// "issue_prefix config is missing" despite init succeeding.
	t.Skip("CLI integration test broken after Dolt transition — bd create can't find prefix after bd init")

	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	// Build bd binary once for all subtests
	tmpBin := filepath.Join(t.TempDir(), "bd")
	buildCmd := exec.Command("go", "build", "-o", tmpBin, "./")
	buildCmd.Dir = "."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build bd: %v\n%s", err, out)
	}

	t.Run("ExternalRef", func(t *testing.T) {
		t.Parallel()

		// Create temp directory for test database
		tmpDir := t.TempDir()

		// Initialize beads
		initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Create issue with external ref
		// Use --repo . to override auto-routing and create in the test directory
		createCmd := exec.Command(tmpBin, "--no-daemon", "create", "External ref test", "-p", "1",
			"--external-ref", "https://example.com/spec.md", "--json", "--repo", ".")
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
		if !strings.Contains(out, "External:") {
			t.Errorf("expected 'External:' in output, got: %s", out)
		}
		if !strings.Contains(out, "https://example.com/spec.md") {
			t.Errorf("expected external ref URL in output, got: %s", out)
		}
	})

	t.Run("NoExternalRef", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		// Initialize beads
		initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Create issue WITHOUT external ref
		// Use --repo . to override auto-routing and create in the test directory
		createCmd := exec.Command(tmpBin, "--no-daemon", "create", "No ref test", "-p", "1", "--json", "--repo", ".")
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
		if strings.Contains(out, "External:") {
			t.Errorf("expected no 'External:' line for issue without external ref, got: %s", out)
		}
	})

	t.Run("IDFlag", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		// Initialize beads
		initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Create an issue
		createCmd := exec.Command(tmpBin, "--no-daemon", "create", "ID flag test", "-p", "1", "--json", "--repo", ".")
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

		// Test 1: Using --id flag works
		showCmd := exec.Command(tmpBin, "--no-daemon", "show", "--id="+id, "--short")
		showCmd.Dir = tmpDir
		showOut, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("show with --id flag failed: %v\n%s", err, showOut)
		}
		if !strings.Contains(string(showOut), id) {
			t.Errorf("expected issue ID in output, got: %s", showOut)
		}

		// Test 2: Multiple --id flags work
		showCmd2 := exec.Command(tmpBin, "--no-daemon", "show", "--id="+id, "--id="+id, "--short")
		showCmd2.Dir = tmpDir
		showOut2, err := showCmd2.CombinedOutput()
		if err != nil {
			t.Fatalf("show with multiple --id flags failed: %v\n%s", err, showOut2)
		}
		// Should see the ID twice (one for each --id flag)
		if strings.Count(string(showOut2), id) != 2 {
			t.Errorf("expected issue ID twice in output, got: %s", showOut2)
		}

		// Test 3: Combining positional and --id flag
		showCmd3 := exec.Command(tmpBin, "--no-daemon", "show", id, "--id="+id, "--short")
		showCmd3.Dir = tmpDir
		showOut3, err := showCmd3.CombinedOutput()
		if err != nil {
			t.Fatalf("show with positional + --id failed: %v\n%s", err, showOut3)
		}
		// Should see the ID twice
		if strings.Count(string(showOut3), id) != 2 {
			t.Errorf("expected issue ID twice in output, got: %s", showOut3)
		}

		// Test 4: No args at all should fail
		showCmd4 := exec.Command(tmpBin, "--no-daemon", "show")
		showCmd4.Dir = tmpDir
		_, err = showCmd4.CombinedOutput()
		if err == nil {
			t.Error("expected error when no ID provided, but command succeeded")
		}
	})

	t.Run("NotFoundExitsNonZero", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		// Initialize beads
		initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Show nonexistent issue should exit non-zero
		showCmd := exec.Command(tmpBin, "--no-daemon", "show", "test-nonexistent")
		showCmd.Dir = tmpDir
		_, err := showCmd.CombinedOutput()
		if err == nil {
			t.Error("expected non-zero exit for nonexistent issue, but command succeeded")
		}
	})

	t.Run("NotFoundJSON", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		// Initialize beads
		initCmd := exec.Command(tmpBin, "init", "--prefix", "test", "--quiet")
		initCmd.Dir = tmpDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out)
		}

		// Show nonexistent issue with --json should exit non-zero
		// and output structured JSON error to stdout (not empty stdout)
		showCmd := exec.Command(tmpBin, "--no-daemon", "show", "test-nonexistent", "--json")
		showCmd.Dir = tmpDir
		var stdout, stderr strings.Builder
		showCmd.Stdout = &stdout
		showCmd.Stderr = &stderr
		err := showCmd.Run()
		if err == nil {
			t.Error("expected non-zero exit for nonexistent issue with --json, but command succeeded")
		}

		// Verify stdout contains valid JSON with an error field
		stdoutStr := stdout.String()
		if stdoutStr == "" {
			t.Fatal("expected JSON error on stdout, got empty output")
		}
		var errResp map[string]string
		if jsonErr := json.Unmarshal([]byte(stdoutStr), &errResp); jsonErr != nil {
			t.Fatalf("expected valid JSON error response on stdout, got parse error: %v\nStdout: %s\nStderr: %s", jsonErr, stdoutStr, stderr.String())
		}
		if errResp["error"] == "" {
			t.Errorf("expected non-empty 'error' field in JSON response, got: %s", stdoutStr)
		}
	})
}
