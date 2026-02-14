//go:build cgo && integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShow_ExternalRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Create issue with external ref
	out := runBDInProcess(t, tmpDir, "create", "External ref test", "-p", "1",
		"--external-ref", "https://example.com/spec.md", "--json")

	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON found in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, out)
	}
	id := issue["id"].(string)

	// Show the issue and verify external ref is displayed
	showOut := runBDInProcess(t, tmpDir, "show", id)
	if !strings.Contains(showOut, "External:") {
		t.Errorf("expected 'External:' in output, got: %s", showOut)
	}
	if !strings.Contains(showOut, "https://example.com/spec.md") {
		t.Errorf("expected external ref URL in output, got: %s", showOut)
	}
}

func TestShow_NoExternalRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Create issue WITHOUT external ref
	out := runBDInProcess(t, tmpDir, "create", "No ref test", "-p", "1", "--json")

	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON found in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, out)
	}
	id := issue["id"].(string)

	// Show the issue - should NOT contain External Ref line
	showOut := runBDInProcess(t, tmpDir, "show", id)
	if strings.Contains(showOut, "External:") {
		t.Errorf("expected no 'External:' line for issue without external ref, got: %s", showOut)
	}
}

func TestShow_IDFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Create an issue
	out := runBDInProcess(t, tmpDir, "create", "ID flag test", "-p", "1", "--json")

	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON found in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, out)
	}
	id := issue["id"].(string)

	// Test 1: Using --id flag works
	showOut := runBDInProcess(t, tmpDir, "show", "--id="+id, "--short")
	if !strings.Contains(showOut, id) {
		t.Errorf("expected issue ID in output, got: %s", showOut)
	}

	// Test 2: Multiple --id flags work
	showOut2 := runBDInProcess(t, tmpDir, "show", "--id="+id, "--id="+id, "--short")
	if strings.Count(showOut2, id) != 2 {
		t.Errorf("expected issue ID twice in output, got: %s", showOut2)
	}

	// Test 3: Combining positional and --id flag
	showOut3 := runBDInProcess(t, tmpDir, "show", id, "--id="+id, "--short")
	if strings.Count(showOut3, id) != 2 {
		t.Errorf("expected issue ID twice in output, got: %s", showOut3)
	}

	// Test 4: No args at all should fail
	_, _, err := runBDInProcessAllowError(t, tmpDir, "show")
	if err == nil {
		t.Error("expected error when no ID provided, but command succeeded")
	}
}

func TestShow_NotFoundExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Show nonexistent issue should return error
	_, _, err := runBDInProcessAllowError(t, tmpDir, "show", "test-nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent issue, but command succeeded")
	}
}

func TestShow_NotFoundJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI test in short mode")
	}

	tmpDir := setupCLITestDB(t)

	// Show nonexistent issue with --json should return error
	// and output structured JSON error to stdout
	stdout, _, err := runBDInProcessAllowError(t, tmpDir, "show", "test-nonexistent", "--json")
	if err == nil {
		t.Error("expected error for nonexistent issue with --json, but command succeeded")
	}

	// Verify stdout contains valid JSON with an error field
	if stdout == "" {
		t.Fatal("expected JSON error on stdout, got empty output")
	}
	var errResp map[string]string
	if jsonErr := json.Unmarshal([]byte(stdout), &errResp); jsonErr != nil {
		t.Fatalf("expected valid JSON error response on stdout, got parse error: %v\nStdout: %s", jsonErr, stdout)
	}
	if errResp["error"] == "" {
		t.Errorf("expected non-empty 'error' field in JSON response, got: %s", stdout)
	}
}
