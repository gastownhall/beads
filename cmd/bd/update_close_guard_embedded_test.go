//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedUpdateCloseWarnsOnGuardDifference pins GH#5787 issue 3
// end-to-end: `bd update <id> --status closed` closes (existing exit and
// output semantics unchanged) and prints exactly one stderr warning naming
// the issue and the assignee/actor difference `bd close` would have refused —
// while an assignee's own close, a --force close and a restatement stay
// silent.
func TestEmbeddedUpdateCloseWarnsOnGuardDifference(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ucw")

	// actor runs the command as one actor: the update's --actor flag wins
	// only where set; otherwise BEADS_ACTOR decides.
	run := func(actor string, args ...string) (string, string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), "BEADS_ACTOR="+actor)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}
	countWarnings := func(stderr string) int {
		return strings.Count(stderr, "update closed")
	}

	t.Run("bob closes alice's issue with one warning", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Alice's finding", "--assignee", "alice")

		stdout, stderr, err := run("bob", "update", issue.ID, "--status", "closed")
		if err != nil {
			t.Fatalf("bd update --status closed should keep closing; got: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if n := countWarnings(stderr); n != 1 {
			t.Fatalf("expected exactly one close-guard warning, got %d; stderr:\n%s", n, stderr)
		}
		for _, want := range []string{
			"update closed " + issue.ID,
			"bd close would refuse the observed pre-update state",
			`assignee is "alice", actor is "bob"`,
		} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("warning should contain %q; stderr:\n%s", want, stderr)
			}
		}

		show, _, err := run("bob", "show", issue.ID, "--json")
		if err != nil {
			t.Fatalf("bd show failed: %v\n%s", err, show)
		}
		if !strings.Contains(show, "closed") {
			t.Fatalf("issue should be closed after the update; got:\n%s", show)
		}
	})

	t.Run("the assignee's own close is silent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Alice's own work", "--assignee", "alice")
		_, stderr, err := run("alice", "update", issue.ID, "--status", "closed")
		if err != nil {
			t.Fatalf("assignee's own close failed: %v\n%s", err, stderr)
		}
		if n := countWarnings(stderr); n != 0 {
			t.Fatalf("a guard-passing close must be silent; got %d warnings; stderr:\n%s", n, stderr)
		}
	})

	t.Run("force stays silent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Force-closed finding", "--assignee", "alice")
		_, stderr, err := run("bob", "update", issue.ID, "--status", "closed", "--force")
		if err != nil {
			t.Fatalf("forced close failed: %v\n%s", err, stderr)
		}
		if n := countWarnings(stderr); n != 0 {
			t.Fatalf("--force bypasses the same fences in both commands and must be silent; got:\n%s", stderr)
		}
	})

	t.Run("restating closed is silent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Already closed", "--assignee", "alice")
		if _, stderr, err := run("bob", "update", issue.ID, "--status", "closed"); err != nil {
			t.Fatalf("first close failed: %v\n%s", err, stderr)
		}
		_, stderr, err := run("bob", "update", issue.ID, "--status", "closed")
		if err != nil {
			t.Fatalf("restating close failed: %v\n%s", err, stderr)
		}
		if n := countWarnings(stderr); n != 0 {
			t.Fatalf("a closed restatement has no observed pre-update state to warn about; got:\n%s", stderr)
		}
	})
}
