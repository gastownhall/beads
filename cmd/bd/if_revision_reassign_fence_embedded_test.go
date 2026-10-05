//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestIfRevisionOutranksReassignFenceCLI pins mc-zndi7.74 on the direct
// (embedded) route: a stale --if-revision guard on an assignee edit must
// report precondition_failed/ExitGuardMismatch, never fall through to the
// bd-98s5c live-claim reassign fence's "already claimed" policy refusal --
// even though the issue genuinely IS claimed by someone else by the time this
// request's own CLI-side pre-read runs. That shape (the fence's "before" row
// already showing a foreign holder while the caller's own --if-revision value
// still names the pre-claim revision) is exactly what a lost
// --if-revision race produces: the winner's claim commits between the
// loser's own earlier revision read and the loser's write attempt. Without
// ifRevisionAlreadyStale skipping the fence, the loser's pre-read trips on
// the now-live foreign claim and reports the plain policy refusal (exit 1)
// before the guarded write underneath ever gets a chance to report the
// correctly-ordered precondition failure (exit 13).
func TestIfRevisionOutranksReassignFenceCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ivf")

	t.Run("update_minus_a", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded update vs fence", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		// A foreign actor takes a live claim, advancing the revision past rev0
		// and leaving the row genuinely "already claimed" by someone else.
		bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		// "thief" races against the stale rev0 it read before the claim
		// landed.
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--actor", "thief", "--assignee", "thief", "--if-revision", revStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("lost race exit code = %d, want %d (precondition_failed, not the live-claim refusal)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("expected the --if-revision mismatch copy, got:\n%s", out)
		}
		if strings.Contains(out, "holder") {
			t.Errorf("lost race must not fall through to the live-claim refusal naming the holder, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "holder" {
			t.Errorf("lost race must not have applied: assignee=%q, want holder", got.Assignee)
		}
	})

	t.Run("assign", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded assign vs fence", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, code := bdRunFailCode(t, bd, dir, "assign", issue.ID, "thief", "--actor", "thief", "--if-revision", revStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("lost race exit code = %d, want %d (precondition_failed, not the live-claim refusal)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("expected the --if-revision mismatch copy, got:\n%s", out)
		}
		if strings.Contains(out, "holder") {
			t.Errorf("lost race must not fall through to the live-claim refusal naming the holder, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "holder" {
			t.Errorf("lost race must not have applied: assignee=%q, want holder", got.Assignee)
		}
	})
}
