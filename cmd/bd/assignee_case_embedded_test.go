//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedAssigneeCaseInsensitiveCLI replays GH#8628 end to end: letter
// case is not part of an identity, so an issue assigned "hierophant" belongs
// to actor "Hierophant" at every ownership gate, without --force. A different
// name stays a stranger: the fold covers letter case and nothing else.
func TestEmbeddedAssigneeCaseInsensitiveCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ac")

	// AC-1: the issue's script. bd close by the same actor under another
	// letter case exits 0; a different name is still refused.
	t.Run("close_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "x", "--assignee", "hierophant")

		out := bdCloseFail(t, bd, dir, issue.ID, "--actor", "hierophant2")
		if !strings.Contains(out, `assignee is "hierophant"`) || !strings.Contains(out, "--force") {
			t.Errorf("close by hierophant2 should be the not-assignee refusal, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status == types.StatusClosed {
			t.Fatalf("refused close by hierophant2 closed the issue")
		}

		bdClose(t, bd, dir, issue.ID, "--actor", "Hierophant")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
		if got.Assignee != "hierophant" {
			t.Errorf("assignee = %q, want the stored spelling hierophant kept", got.Assignee)
		}
	})

	// AC-2: on an issue in_progress and held by "hierophant", actor
	// "Hierophant" re-claims (an idempotent success) and then unclaims,
	// both without --force.
	t.Run("claim_and_unclaim_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "held", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "hierophant", "--claim")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" || got.Status != types.StatusInProgress {
			t.Fatalf("setup claim: assignee=%q status=%s, want hierophant/in_progress", got.Assignee, got.Status)
		}

		bdUpdate(t, bd, dir, issue.ID, "--actor", "Hierophant", "--claim")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" || got.Status != types.StatusInProgress {
			t.Errorf("re-claim by Hierophant: assignee=%q status=%s, want the idempotent hierophant/in_progress", got.Assignee, got.Status)
		}

		bdRunOK(t, bd, dir, "unclaim", issue.ID, "--actor", "Hierophant")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" || got.Status != types.StatusOpen {
			t.Errorf("unclaim by Hierophant: assignee=%q status=%s, want released (empty/open)", got.Assignee, got.Status)
		}
	})

	// AC-3: on an issue in_progress and held by "hierophant", actor
	// "Hierophant" re-spells the assignee without --force and the stored
	// spelling becomes "Hierophant"; a stranger moving the claim to a third
	// name is still refused.
	t.Run("reassign_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "held", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "hierophant", "--assignee", "hierophant", "--status", "in_progress")

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--actor", "bob", "--assignee", "carol")
		if code != 1 || !strings.Contains(out, "cannot reassign") || !strings.Contains(out, "held by") {
			t.Errorf("bob --assignee carol: exit %d, want 1 with the cannot reassign … held by refusal, got:\n%s", code, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" {
			t.Fatalf("refused reassign clobbered the row: assignee=%q, want hierophant", got.Assignee)
		}

		bdUpdate(t, bd, dir, issue.ID, "--actor", "Hierophant", "--assignee", "Hierophant")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "Hierophant" || got.Status != types.StatusInProgress {
			t.Errorf("re-spell by Hierophant: assignee=%q status=%s, want Hierophant/in_progress", got.Assignee, got.Status)
		}
	})
}
