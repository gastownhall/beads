//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedSetStateFailedPatchLeavesNoEvent: when the label patch of
// `bd set-state` fails, the state-change event must not be committed either
// (GH#5637). A value whose dimension:value label overflows labels.label
// VARCHAR(255) while the event title stays under 500 chars makes the patch
// fail after the event create succeeded.
func TestEmbeddedSetStateFailedPatchLeavesNoEvent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sa")
	issue := bdCreate(t, bd, dir, "State atomicity issue", "--type", "task")

	events := func() int {
		return len(bdListJSON(t, bd, dir, "--parent", issue.ID, "--all", "--type", "event"))
	}

	// Control: a successful set-state records exactly one event child.
	bdSetState(t, bd, dir, issue.ID, "phase=planning")
	if got := events(); got != 1 {
		t.Fatalf("after one successful set-state: %d event children, want 1", got)
	}

	longValue := strings.Repeat("v", 300)
	cmd := exec.Command(bd, "set-state", issue.ID, "phase="+longValue)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("set-state with a 302-char label succeeded; the repro needs the label patch to fail\nstdout:\n%s", stdout.String())
	}
	t.Logf("set-state failed as intended: %v\nstderr:\n%s", err, stderr.String())

	if got := bdState(t, bd, dir, issue.ID, "phase"); strings.TrimSpace(got) != "planning" {
		t.Fatalf("phase after failed set-state = %q, want planning", strings.TrimSpace(got))
	}
	if got := events(); got != 1 {
		t.Errorf("after a failed set-state: %d event children, want 1 (the failed transition left an orphaned committed event)", got)
	}
}
