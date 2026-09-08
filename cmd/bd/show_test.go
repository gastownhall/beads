//go:build cgo && integration

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShow_ExternalRef(t *testing.T) {

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

	tmpDir := setupCLITestDB(t)

	// Show nonexistent issue should return error
	_, _, err := runBDInProcessAllowError(t, tmpDir, "show", "test-nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent issue, but command succeeded")
	}
}

func TestShow_NotFoundJSON(t *testing.T) {

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
	var errResp map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(stdout), &errResp); jsonErr != nil {
		t.Fatalf("expected valid JSON error response on stdout, got parse error: %v\nStdout: %s", jsonErr, stdout)
	}
	if errField, _ := errResp["error"].(string); errField == "" {
		t.Errorf("expected non-empty 'error' field in JSON response, got: %s", stdout)
	}
}

// TestShow_NotFoundHintExplainsAmbiguity guards ga-m6inyb: a never-existed ID
// and a deleted/purged ID currently print the identical "not found" text, so
// a reader has no way to know the two are different situations (deleted
// records leave no trace in the live table — see internal/storage/dolt
// "dolt_ignored" wisp comments and the tombstone-removal history). The fix
// is not to fabricate a distinction bd cannot safely compute (a history scan
// is real work with a documented multi-second-to-timeout cost even on a
// healthy production database), it's to stop the message from implying a
// certainty bd doesn't have, and point at the (separate, opt-in) tool that
// can dig further.
func TestShow_NotFoundHintExplainsAmbiguity(t *testing.T) {
	tmpDir := setupCLITestDB(t)

	_, stderr, err := runBDInProcessAllowError(t, tmpDir, "show", "test-nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent issue, but command succeeded")
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected 'not found' preserved in stderr, got: %s", stderr)
	}
	if !strings.Contains(stderr, "bd history") {
		t.Errorf("expected stderr to point at 'bd history' as the way to check further, got: %s", stderr)
	}
	if !strings.Contains(stderr, "deleted") && !strings.Contains(stderr, "purged") {
		t.Errorf("expected stderr to acknowledge the ID could be a deleted/purged record, not just 'never existed', got: %s", stderr)
	}
}

// TestShow_DeletedIssueGetsSameHonestNotFoundMessage proves the hint applies
// to a REAL deleted issue, not just a fabricated ID: bd delete physically
// removes the row (and its events) from the live tables, so bd show on it is
// observationally identical to an ID that never existed. The message must
// not claim otherwise.
func TestShow_DeletedIssueGetsSameHonestNotFoundMessage(t *testing.T) {
	tmpDir := setupCLITestDB(t)

	out := runBDInProcess(t, tmpDir, "create", "Will be deleted", "-p", "2", "--json")
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("no JSON found in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issue); err != nil {
		t.Fatalf("failed to parse create output: %v, output: %s", err, out)
	}
	id := issue["id"].(string)

	runBDInProcess(t, tmpDir, "delete", id, "--force")

	_, stderr, err := runBDInProcessAllowError(t, tmpDir, "show", id)
	if err == nil {
		t.Fatal("expected error for deleted issue, but command succeeded")
	}
	if !strings.Contains(stderr, "bd history") {
		t.Errorf("expected stderr to point at 'bd history' for a deleted issue, got: %s", stderr)
	}
}

// TestShow_NotFoundJSONIncludesHint checks the --json error envelope gets an
// additive "hint" field carrying the same honesty caveat, while leaving the
// existing "error" string untouched for any script matching on it today.
func TestShow_NotFoundJSONIncludesHint(t *testing.T) {
	tmpDir := setupCLITestDB(t)

	stdout, _, err := runBDInProcessAllowError(t, tmpDir, "show", "test-nonexistent", "--json")
	if err == nil {
		t.Fatal("expected error for nonexistent issue with --json, but command succeeded")
	}
	var errResp map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(stdout), &errResp); jsonErr != nil {
		t.Fatalf("expected valid JSON error response, got parse error: %v\nStdout: %s", jsonErr, stdout)
	}
	if errField, _ := errResp["error"].(string); errField != "no issues found matching the provided IDs" {
		t.Errorf("expected unchanged 'error' field text for backward compat, got: %q", errField)
	}
	hint, _ := errResp["hint"].(string)
	if hint == "" {
		t.Fatalf("expected non-empty 'hint' field explaining the ambiguity, got response: %s", stdout)
	}
	if !strings.Contains(hint, "bd history") {
		t.Errorf("expected hint to point at 'bd history', got: %s", hint)
	}
}
