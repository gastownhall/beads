//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdRunSplit runs bd with args in dir and returns stdout and stderr
// separately, failing the test when the command fails.
func bdRunSplit(t *testing.T, bd, dir string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	outBuf, errBuf, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// hiddenChildrenNote is the first line of the GH#5151 note for id with n
// children.
func hiddenChildrenNote(id string, n int) string {
	return fmt.Sprintf("note: %s has %d child issue(s) not shown here", id, n)
}

// countParentChildRows counts the "  <id>: ... via parent-child" rows a
// human-mode `bd dep list` printed.
func countParentChildRows(stdout string) int {
	n := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "  ") && strings.HasSuffix(line, " via parent-child") {
			n++
		}
	}
	return n
}

// TestEmbeddedDepDefaultDirectionNotesHiddenChildren is the GH#5151 regression
// guard: a hooked molecule root with 26 step children used to look childless
// through the default (down) bd dep list / bd dep tree views, with no hint
// that its children are its inbound parent-child edges.
func TestEmbeddedDepDefaultDirectionNotesHiddenChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hc")

	// R: a hooked ephemeral molecule root with 26 ephemeral steps.
	root := bdCreate(t, bd, dir, "Molecule root", "--type", "epic", "--ephemeral")
	bdUpdate(t, bd, dir, root.ID, "--status", "hooked")
	var steps []string
	for i := 1; i <= 26; i++ {
		step := bdCreate(t, bd, dir, fmt.Sprintf("Step %d", i), "--type", "task", "--ephemeral", "--parent", root.ID)
		steps = append(steps, step.ID)
	}

	listCmd := "To list them, run: bd dep list " + root.ID + " --direction=up --type=parent-child"

	t.Run("list_root_26", func(t *testing.T) {
		stdout, stderr := bdRunSplit(t, bd, dir, "dep", "list", root.ID)
		if want := "\n" + root.ID + " has no dependencies\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
			t.Errorf("stderr missing the 26-child note:\n%s", stderr)
		}
		if !strings.Contains(stderr, listCmd+"\n") {
			t.Errorf("stderr missing %q:\n%s", listCmd, stderr)
		}

		// The printed command lists exactly the 26 steps.
		upOut, _ := bdRunSplit(t, bd, dir, "dep", "list", root.ID, "--direction=up", "--type=parent-child")
		if got := countParentChildRows(upOut); got != 26 {
			t.Errorf("printed command listed %d rows, want 26:\n%s", got, upOut)
		}

		// Explicit --direction=down, --type=parent-child and the unvalidated
		// --direction=both (which issues the down request) note the same.
		for _, extra := range [][]string{{"--direction=down"}, {"--type=parent-child"}, {"--direction=both"}} {
			args := append([]string{"dep", "list", root.ID}, extra...)
			_, stderr := bdRunSplit(t, bd, dir, args...)
			if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
				t.Errorf("%v: stderr missing the 26-child note:\n%s", extra, stderr)
			}
		}
	})

	// Last: it closes a step of the shared fixture. The note counts every
	// child, closed or not, as the printed command lists them.
	t.Run("closed_step_still_counted", func(t *testing.T) {
		bdClose(t, bd, dir, steps[0])
		_, stderr := bdRunSplit(t, bd, dir, "dep", "list", root.ID)
		if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
			t.Errorf("stderr missing the 26-child note after closing a step:\n%s", stderr)
		}
		upOut, _ := bdRunSplit(t, bd, dir, "dep", "list", root.ID, "--direction=up", "--type=parent-child")
		if got := countParentChildRows(upOut); got != 26 {
			t.Errorf("printed command listed %d rows after closing a step, want 26:\n%s", got, upOut)
		}
	})
}
