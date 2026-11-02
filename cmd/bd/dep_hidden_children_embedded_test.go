//go:build cgo

package main

import (
	"encoding/json"
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
	// C: a childless control with the same title, type, ephemeral flag and
	// status.
	control := bdCreate(t, bd, dir, "Molecule root", "--type", "epic", "--ephemeral")
	bdUpdate(t, bd, dir, control.ID, "--status", "hooked")
	other := bdCreate(t, bd, dir, "Unrelated", "--type", "task")

	listCmd := "To list them, run: bd dep list " + root.ID + " --direction=up --type=parent-child"
	treeCmd := "To see them, run: bd dep tree " + root.ID + " --direction=up"

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

	t.Run("tree_root_26", func(t *testing.T) {
		stdout, stderr := bdRunSplit(t, bd, dir, "dep", "tree", root.ID)
		controlOut, controlErr := bdRunSplit(t, bd, dir, "dep", "tree", control.ID)
		if want := strings.ReplaceAll(controlOut, control.ID, root.ID); stdout != want {
			t.Errorf("stdout differs from the childless control's:\n got %q\nwant %q", stdout, want)
		}
		if strings.Contains(controlErr, "note:") {
			t.Errorf("childless control printed a note:\n%s", controlErr)
		}
		if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
			t.Errorf("stderr missing the 26-child note:\n%s", stderr)
		}
		if !strings.Contains(stderr, treeCmd+"\n") {
			t.Errorf("stderr missing %q:\n%s", treeCmd, stderr)
		}

		// The printed command shows every step.
		upOut, _ := bdRunSplit(t, bd, dir, "dep", "tree", root.ID, "--direction=up")
		for _, id := range steps {
			if !strings.Contains(upOut, id) {
				t.Errorf("printed command does not show step %s:\n%s", id, upOut)
			}
		}

		// --direction=down, --status and --max-depth still count every child.
		for _, extra := range [][]string{{"--direction=down"}, {"--status=open"}, {"--max-depth=1"}} {
			args := append([]string{"dep", "tree", root.ID}, extra...)
			_, stderr := bdRunSplit(t, bd, dir, args...)
			if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
				t.Errorf("%v: stderr missing the 26-child note:\n%s", extra, stderr)
			}
		}
	})

	t.Run("attached_shape", func(t *testing.T) {
		work := bdCreate(t, bd, dir, "Work bead", "--type", "task")
		mol := bdCreate(t, bd, dir, "Attached molecule", "--type", "epic", "--ephemeral", "--parent", work.ID)
		bdUpdate(t, bd, dir, work.ID, "--status", "hooked")
		var molSteps []string
		for i := 1; i <= 3; i++ {
			s := bdCreate(t, bd, dir, fmt.Sprintf("Attached step %d", i), "--type", "task", "--ephemeral", "--parent", mol.ID)
			molSteps = append(molSteps, s.ID)
		}

		for _, view := range []string{"list", "tree"} {
			_, stderr := bdRunSplit(t, bd, dir, "dep", view, work.ID)
			if !strings.Contains(stderr, hiddenChildrenNote(work.ID, 1)) {
				t.Errorf("dep %s W: stderr missing the 1-child note:\n%s", view, stderr)
			}

			stdout, stderr := bdRunSplit(t, bd, dir, "dep", view, mol.ID)
			if !strings.Contains(stdout, work.ID) {
				t.Errorf("dep %s M: stdout should still show the parent %s:\n%s", view, work.ID, stdout)
			}
			for _, id := range molSteps {
				if strings.Contains(stdout, id) {
					t.Errorf("dep %s M: stdout unexpectedly shows step %s:\n%s", view, id, stdout)
				}
			}
			if !strings.Contains(stderr, hiddenChildrenNote(mol.ID, 3)) {
				t.Errorf("dep %s M: stderr missing the 3-child note:\n%s", view, stderr)
			}
		}
		listOut, _ := bdRunSplit(t, bd, dir, "dep", "list", mol.ID)
		if !strings.Contains(listOut, work.ID) || !strings.Contains(listOut, "via parent-child") {
			t.Errorf("dep list M should list W via parent-child:\n%s", listOut)
		}

		upOut, _ := bdRunSplit(t, bd, dir, "dep", "tree", work.ID, "--direction=up")
		for _, id := range append([]string{mol.ID}, molSteps...) {
			if !strings.Contains(upOut, id) {
				t.Errorf("dep tree W --direction=up does not reach %s:\n%s", id, upOut)
			}
		}
	})

	t.Run("no_children", func(t *testing.T) {
		x := bdCreate(t, bd, dir, "Blocker X", "--type", "task")
		d := bdCreate(t, bd, dir, "Dependent D", "--type", "task")
		y := bdCreate(t, bd, dir, "Related Y", "--type", "task")
		bdRunSplit(t, bd, dir, "dep", "add", d.ID, x.ID)
		bdRunSplit(t, bd, dir, "dep", "relate", x.ID, y.ID)
		for _, view := range []string{"list", "tree"} {
			_, stderr := bdRunSplit(t, bd, dir, "dep", view, x.ID)
			if strings.Contains(stderr, "note:") {
				t.Errorf("dep %s X: unexpected note:\n%s", view, stderr)
			}
		}

		// A relate pair plus one child counts only the child.
		bdCreate(t, bd, dir, "Child of X", "--type", "task", "--parent", x.ID)
		_, stderr := bdRunSplit(t, bd, dir, "dep", "list", x.ID)
		if !strings.Contains(stderr, hiddenChildrenNote(x.ID, 1)) {
			t.Errorf("relate pair plus one child: stderr missing the 1-child note:\n%s", stderr)
		}
	})

	t.Run("machine_output", func(t *testing.T) {
		stdout, stderr := bdRunSplit(t, bd, dir, "dep", "list", root.ID, "--json")
		if strings.Contains(stderr, "note:") {
			t.Errorf("list --json: unexpected note:\n%s", stderr)
		}
		if strings.TrimSpace(stdout) != "[]" {
			t.Errorf("list --json stdout = %q, want []", stdout)
		}

		for _, args := range [][]string{{"--json"}, {"--format=json"}} {
			stdout, stderr := bdRunSplit(t, bd, dir, append([]string{"dep", "tree", root.ID}, args...)...)
			if strings.Contains(stderr, "note:") {
				t.Errorf("tree %v: unexpected note:\n%s", args, stderr)
			}
			var nodes []map[string]any
			if err := json.Unmarshal([]byte(stdout), &nodes); err != nil {
				t.Fatalf("tree %v: parse JSON: %v\n%s", args, err, stdout)
			}
			if len(nodes) != 1 || nodes[0]["id"] != root.ID {
				t.Errorf("tree %v: want a one-node array for %s, got:\n%s", args, root.ID, stdout)
			}
		}

		stdout, stderr = bdRunSplit(t, bd, dir, "dep", "tree", root.ID, "--format=mermaid")
		if strings.Contains(stderr, "note:") {
			t.Errorf("tree mermaid: unexpected note:\n%s", stderr)
		}
		if !strings.Contains(stdout, "flowchart") || !strings.Contains(stdout, root.ID) {
			t.Errorf("tree mermaid: want a flowchart naming %s:\n%s", root.ID, stdout)
		}
		for _, id := range steps {
			if strings.Contains(stdout, id) {
				t.Errorf("tree mermaid: want one node, got step %s:\n%s", id, stdout)
			}
		}
	})

	t.Run("other_directions", func(t *testing.T) {
		for _, args := range [][]string{
			{"dep", "list", root.ID, "--direction=up"},
			{"dep", "tree", root.ID, "--direction=up"},
			{"dep", "tree", root.ID, "--direction=both"},
			{"dep", "tree", root.ID, "--reverse"},
			{"dep", "list", root.ID, "--type=blocks"},
			{"dep", "list", root.ID, other.ID},
		} {
			_, stderr := bdRunSplit(t, bd, dir, args...)
			if strings.Contains(stderr, "note:") {
				t.Errorf("%v: unexpected note:\n%s", args, stderr)
			}
		}
	})

	t.Run("partial_id", func(t *testing.T) {
		partial := strings.TrimPrefix(root.ID, "hc-")
		for _, view := range []string{"list", "tree"} {
			_, stderr := bdRunSplit(t, bd, dir, "dep", view, partial)
			if !strings.Contains(stderr, hiddenChildrenNote(root.ID, 26)) {
				t.Errorf("dep %s %s: note should name the full id %s:\n%s", view, partial, root.ID, stderr)
			}
			if !strings.Contains(stderr, "bd dep "+view+" "+root.ID+" --direction=up") {
				t.Errorf("dep %s %s: printed command should name the full id %s:\n%s", view, partial, root.ID, stderr)
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
