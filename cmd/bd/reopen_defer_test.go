// reopen_defer_test.go - Test that reopen clears defer_until.

//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// TestCLI_ReopenClearsDeferUntil tests that reopening a deferred+closed issue
// clears defer_until so the issue appears in bd ready immediately.
//
// This documents the decision: reopen means "I want to work on this NOW",
// so defer_until is cleared.
func TestCLI_ReopenClearsDeferUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)
	id := createExecTestIssue(t, tmpDir, "Deferred then reopened")

	// Defer the issue to far future
	cmd := exec.Command(testBD, "update", id, "--defer", "+8760h") // 1 year
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("defer failed: %v\n%s", err, out)
	}

	// Close it
	cmd = exec.Command(testBD, "close", id)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("close failed: %v\n%s", err, out)
	}

	// Reopen it
	cmd = exec.Command(testBD, "reopen", id)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reopen failed: %v\n%s", err, out)
	}

	// Verify defer_until is cleared
	cmd = exec.Command(testBD, "show", id, "--json")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, out)
	}

	var issues []map[string]interface{}
	json.Unmarshal(out, &issues)
	if len(issues) == 0 {
		t.Fatalf("show returned no issues")
	}

	issue := issues[0]
	if issue["status"] != "open" {
		t.Errorf("expected status=open after reopen, got: %v", issue["status"])
	}
	if deferUntil, ok := issue["defer_until"]; ok && deferUntil != nil && deferUntil != "" {
		t.Errorf("expected defer_until to be cleared after reopen, got: %v", deferUntil)
	}
}
