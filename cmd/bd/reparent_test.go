// reparent_test.go - Test that reparented issues don't appear under old parent.

//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCLI_ReparentDottedIDExcludesOldParent tests that after reparenting a
// dotted-ID child to a new parent, it no longer appears under the old parent
// in `bd list --parent`.
//
// The bug: dotted-ID prefix matching (e.g., "parent.1" matches parent "parent")
// continued to show the child under the old parent even after an explicit
// parent-child dependency reparented it elsewhere.
//
// The fix: explicit parent-child dependencies take precedence over dotted-ID
// prefix matching.
func TestCLI_ReparentDottedIDExcludesOldParent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow CLI test in short mode")
	}
	tmpDir := initExecTestDB(t)

	// Create parentA — will get an auto-generated ID like "test-xxxxx"
	parentA := createExecTestIssue(t, tmpDir, "Parent A")

	// Create a dotted-ID child: parentA + ".1" — this triggers prefix matching
	dottedChildID := parentA + ".1"
	child := createExecTestIssueWithID(t, tmpDir, "Dotted Child", dottedChildID)
	if child != dottedChildID {
		t.Fatalf("expected child ID %s, got %s", dottedChildID, child)
	}

	// Create parentB
	parentB := createExecTestIssue(t, tmpDir, "Parent B")

	// Before any deps: dotted child should appear under parentA via prefix match
	assertParentLists(t, tmpDir, parentA, dottedChildID, true,
		"dotted child should appear under parentA via prefix match before reparenting")

	// Reparent: add explicit parent-child dep to parentB
	runBD(t, tmpDir, "dep", "add", dottedChildID, parentB, "--type", "parent-child")

	// After reparenting: child should NOT appear under parentA
	assertParentLists(t, tmpDir, parentA, dottedChildID, false,
		"dotted child should NOT appear under old parent after reparenting to parentB")

	// After reparenting: child SHOULD appear under parentB
	assertParentLists(t, tmpDir, parentB, dottedChildID, true,
		"dotted child should appear under new parent parentB after reparenting")
}

// createExecTestIssueWithID creates an issue with an explicit ID.
func createExecTestIssueWithID(t *testing.T, tmpDir, title, id string) string {
	t.Helper()
	cmd := exec.Command(testBD, "create", title, "-p", "1", "--id", id, "--json")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create with --id %s failed: %v\n%s", id, err, out)
	}
	jsonStart := strings.Index(string(out), "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal(out[jsonStart:], &issue); err != nil {
		t.Fatalf("parse create JSON: %v\n%s", err, out)
	}
	return issue["id"].(string)
}

// runBD runs a bd command and fails the test on error.
func runBD(t *testing.T, tmpDir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(testBD, args...)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// assertParentLists checks whether childID appears in `bd list --parent parentID`.
func assertParentLists(t *testing.T, tmpDir, parentID, childID string, shouldAppear bool, msg string) {
	t.Helper()
	cmd := exec.Command(testBD, "list", "--parent", parentID, "--json")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if shouldAppear {
			t.Fatalf("list --parent %s failed: %v\n%s", parentID, err, out)
		}
		return // empty list may fail; that's fine if we expected absence
	}

	var issues []map[string]interface{}
	if err := json.Unmarshal(out, &issues); err != nil {
		// Might be empty output
		if shouldAppear {
			t.Fatalf("parse list JSON: %v\n%s", err, out)
		}
		return
	}

	found := false
	for _, iss := range issues {
		if iss["id"] == childID {
			found = true
		}
	}
	if shouldAppear && !found {
		t.Errorf("%s (child=%s, parent=%s)", msg, childID, parentID)
	}
	if !shouldAppear && found {
		t.Errorf("%s (child=%s, parent=%s)", msg, childID, parentID)
	}
}
