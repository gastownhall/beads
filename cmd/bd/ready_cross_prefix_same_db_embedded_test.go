//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedReadyCrossPrefixSameDatabaseBlocker pins GH#8299 (bd-6jk1y2): a
// `blocks` edge whose target has a different id prefix but lives in THIS
// database must block the dependent in `bd ready` while the target is open.
func TestEmbeddedReadyCrossPrefixSameDatabaseBlocker(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")

	blocker := bdCreate(t, bd, dir, "Open blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "a-x1", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	cmd := exec.Command(bd, "ready", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var ready []types.IssueWithCounts
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &ready); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, stdout.String())
	}
	sawBlocker := false
	for _, r := range ready {
		if r.ID == dependent.ID {
			t.Fatalf("%s is ready while its same-database blocker %s is open: %s",
				dependent.ID, blocker.ID, stdout.String())
		}
		if r.ID == blocker.ID {
			sawBlocker = true
		}
	}
	if !sawBlocker {
		t.Fatalf("blocker %s missing from ready output: %s", blocker.ID, stdout.String())
	}
}

// readyIDSet runs "bd ready --json" and returns the set of ready issue ids.
func readyIDSet(t *testing.T, bd, dir string) map[string]bool {
	t.Helper()
	cmd := exec.Command(bd, "ready", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var ready []types.IssueWithCounts
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &ready); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, stdout.String())
	}
	ids := make(map[string]bool, len(ready))
	for _, r := range ready {
		ids[r.ID] = true
	}
	return ids
}

// TestEmbeddedCrossPrefixBlockerCloseReopen pins the derived blocked state's
// follow-through (GH#8299 AC-2): closing the local cross-prefix blocker readies
// the dependent, and reopening it blocks the dependent again.
func TestEmbeddedCrossPrefixBlockerCloseReopen(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")

	blocker := bdCreate(t, bd, dir, "Close/reopen blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "a-x2", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	// The dependent is blocked, not ready, while the blocker is open.
	if ids := readyIDSet(t, bd, dir); ids[dependent.ID] {
		t.Fatalf("%s is ready while its same-database blocker %s is open", dependent.ID, blocker.ID)
	}

	// Closing the blocker readies the dependent.
	bdClose(t, bd, dir, blocker.ID)
	if ids := readyIDSet(t, bd, dir); !ids[dependent.ID] {
		t.Fatalf("%s is not ready after its blocker %s was closed", dependent.ID, blocker.ID)
	}

	// Reopening the blocker blocks the dependent again.
	bdReopen(t, bd, dir, blocker.ID)
	if ids := readyIDSet(t, bd, dir); ids[dependent.ID] {
		t.Fatalf("%s is ready after its blocker %s was reopened", dependent.ID, blocker.ID)
	}
}
