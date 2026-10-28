//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDepDefaultDirectionNotesHiddenChildren pins GH#5151: the
// default human `bd dep list <id>` and `bd dep tree <id>` views list OUTBOUND
// edges only, so an anchor whose children reach it through inbound
// parent-child edges — a `bd create --parent` step, a live molecule root's
// ephemeral steps — printed exactly what a childless issue prints, and a
// tool that discriminates real work from phantoms by child-count could refuse
// a root with 26 open children (the report's near-miss). The fix is a
// stderr-only note naming the hidden children and the command that lists
// them; stdout, --json, flags and the default direction semantics are
// unchanged, so every subtest that pins an absence of the note is a guard
// that the note never reaches those paths.
//
// bdDep (dep_embedded_test.go) returns stdout only, so the note assertions
// here run through bdDepInOut, which keeps the two streams apart.
func TestEmbeddedDepDefaultDirectionNotesHiddenChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hc")

	// bdDepInOut runs "bd dep" and returns stdout and stderr apart. It takes
	// the caller's t so a failure fails the subtest that ran the command.
	bdDepInOut := func(t *testing.T, args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(bd, append([]string{"dep"}, args...)...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd dep %s failed: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	// newHookedEpic creates an ephemeral epic set to hooked, the shape of the
	// live molecule root in the GH#5151 report.
	newHookedEpic := func(title string) *types.Issue {
		t.Helper()
		root := bdCreate(t, bd, dir, title, "--type", "epic", "--ephemeral")
		bdUpdate(t, bd, dir, root.ID, "--status", "hooked")
		return root
	}
	newEphemeralStep := func(parent *types.Issue, title string) *types.Issue {
		t.Helper()
		return bdCreate(t, bd, dir, title, "--ephemeral", "--parent", parent.ID)
	}

	// The report's fixture: a hooked ephemeral root with 26 ephemeral step
	// children and no outbound edges of its own.
	root := newHookedEpic("Hidden children root")
	steps := make([]*types.Issue, 26)
	for i := range steps {
		steps[i] = newEphemeralStep(root, fmt.Sprintf("Hidden children step %02d", i))
	}

	t.Run("list_root_26", func(t *testing.T) {
		// AC-1: the default view is unchanged on stdout and notes the 26
		// children on stderr, naming the command that lists them.
		stdout, stderr := bdDepInOut(t, "list", root.ID)
		if want := fmt.Sprintf("\n%s has no dependencies\n", root.ID); stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("stderr should note the 26 hidden children, got:\n%s", stderr)
		}
		wantCmd := "To list them, run: bd dep list " + root.ID + " --direction=up --type=parent-child"
		if !strings.Contains(stderr, wantCmd) {
			t.Errorf("stderr should carry %q, got:\n%s", wantCmd, stderr)
		}

		// The printed command lists exactly the 26 children the note counts.
		up, _ := bdDepInOut(t, "list", root.ID, "--direction", "up", "--type", "parent-child")
		if got := strings.Count(up, "via parent-child"); got != 26 {
			t.Errorf("the printed command listed %d rows, want 26:\n%s", got, up)
		}

		// An explicit --direction=down is the same view and notes too.
		_, stderr = bdDepInOut(t, "list", root.ID, "--direction", "down")
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("explicit --direction=down should note the children too, got:\n%s", stderr)
		}

		// --type=parent-child asks about exactly these edges, so it notes.
		_, stderr = bdDepInOut(t, "list", root.ID, "--type", "parent-child")
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("--type=parent-child should note the children too, got:\n%s", stderr)
		}

		// dep list does not validate --direction; a value that is not "up"
		// issues the down request, and the note follows the request actually
		// issued rather than the flag string.
		_, stderr = bdDepInOut(t, "list", root.ID, "--direction", "both")
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("--direction=both (unvalidated, down request) should note the children too, got:\n%s", stderr)
		}

		// A partial-id argument names the resolved full id, both in the note
		// and in the printed command. The anchor is a durable task with a
		// `bd dep add` child: `bd create --parent` children carry the
		// parent's own id plus a dot, so no strict prefix of such a parent's
		// id can ever resolve to it alone, and hash ids mint 3-8 characters
		// adaptively, so the partial is the id minus its last character —
		// unique among the workspace's hex ids by construction, with the
		// child reminted on the one-in-256 shot at a 3-char id sharing the
		// prefix.
		partialParent := bdCreate(t, bd, dir, "Partial id parent", "--type", "task")
		partial := partialParent.ID[:len(partialParent.ID)-1]
		var partialChild *types.Issue
		for attempt := 0; ; attempt++ {
			partialChild = bdCreate(t, bd, dir, fmt.Sprintf("Partial id child %d", attempt), "--type", "task")
			if !strings.HasPrefix(partialChild.ID, partial) {
				break
			}
		}
		bdDep(t, bd, dir, "add", partialChild.ID, partialParent.ID, "--type", "parent-child")
		_, stderr = bdDepInOut(t, "list", partial)
		if !strings.Contains(stderr, "note: "+partialParent.ID+" has 1 child issue(s) not shown here") {
			t.Errorf("partial-id note should name the resolved id %s, got:\n%s", partialParent.ID, stderr)
		}
		if !strings.Contains(stderr, "To list them, run: bd dep list "+partialParent.ID+" --direction=up --type=parent-child") {
			t.Errorf("partial-id note should print the resolved id in the command, got:\n%s", stderr)
		}

		// The note counts every child, as the printed command lists them: a
		// closed step still counts, and the printed list still shows it.
		bdClose(t, bd, dir, steps[0].ID)
		_, stderr = bdDepInOut(t, "list", root.ID)
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("note should still count 26 after a step closed, got:\n%s", stderr)
		}
		up, _ = bdDepInOut(t, "list", root.ID, "--direction", "up", "--type", "parent-child")
		if got := strings.Count(up, "via parent-child"); got != 26 {
			t.Errorf("the printed command listed %d rows after a step closed, want 26:\n%s", got, up)
		}
	})
	t.Run("tree_root_26", func(t *testing.T) {
		// AC-2: the tree renders the root alone, stdout indistinguishable
		// from a childless control, and stderr notes the 26 children with the
		// command that shows them.
		control := newHookedEpic("Hidden children root") // same title, type, class and status; no children
		stdoutRoot, stderrRoot := bdDepInOut(t, "tree", root.ID)
		stdoutControl, stderrControl := bdDepInOut(t, "tree", control.ID)
		if want := strings.ReplaceAll(stdoutControl, control.ID, root.ID); stdoutRoot != want {
			t.Fatalf("tree stdout for the 26-child root should equal the childless control's:\ngot  %q\nwant %q", stdoutRoot, want)
		}
		if !strings.Contains(stderrRoot, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("stderr should note the 26 hidden children, got:\n%s", stderrRoot)
		}
		wantCmd := "To see them, run: bd dep tree " + root.ID + " --direction=up"
		if !strings.Contains(stderrRoot, wantCmd) {
			t.Errorf("stderr should carry %q, got:\n%s", wantCmd, stderrRoot)
		}
		if strings.Contains(stderrControl, "note:") {
			t.Errorf("the childless control should carry no note, got:\n%s", stderrControl)
		}

		// The printed command shows all 26 steps.
		up, _ := bdDepInOut(t, "tree", root.ID, "--direction", "up")
		for _, step := range steps {
			if !strings.Contains(up, step.ID) {
				t.Errorf("the printed command's tree should reach %s:\n%s", step.ID, up)
			}
		}

		// The note is independent of the walk's filters: --status and
		// --max-depth narrow the rendered tree, not the note's count.
		_, stderr := bdDepInOut(t, "tree", root.ID, "--status", "open")
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("--status=open should note the children too, got:\n%s", stderr)
		}
		_, stderr = bdDepInOut(t, "tree", root.ID, "--max-depth", "1")
		if !strings.Contains(stderr, "note: "+root.ID+" has 26 child issue(s) not shown here") {
			t.Errorf("--max-depth=1 should note the children too, got:\n%s", stderr)
		}
	})
}
