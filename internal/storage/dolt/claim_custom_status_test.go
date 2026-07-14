package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestClaimIssue_CustomStatuses verifies that the bd update <id> --claim path
// (ClaimIssue) honors configured custom statuses (GH#4164): an issue sitting in
// a custom "active"-category status is claimable, while issues in wip/done/
// frozen custom statuses remain non-claimable so the anti-steal protection from
// GH#3570 is preserved. Built-in "open" must stay claimable (regression guard).
func TestClaimIssue_CustomStatuses(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// ready -> active (should be claimable), reviewing -> wip (should not be).
	if err := store.SetConfig(ctx, "status.custom", "ready:active,reviewing:wip"); err != nil {
		t.Fatalf("failed to set custom statuses: %v", err)
	}

	// mk creates the issue as open, then transitions it to the target status.
	// The create path validates status against the comma-split config string,
	// which does not parse "name:category" entries; UpdateIssue does not
	// re-validate status, matching how custom-status issues exist in the DB.
	mk := func(id string, status types.Status) {
		t.Helper()
		iss := &types.Issue{
			ID:        id,
			Title:     "Issue " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", id, err)
		}
		if status != types.StatusOpen {
			if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": string(status)}, "tester"); err != nil {
				t.Fatalf("failed to move %s to status %s: %v", id, status, err)
			}
		}
	}

	mk("cc-open", types.StatusOpen)
	mk("cc-ready", types.Status("ready"))
	mk("cc-reviewing", types.Status("reviewing"))

	// AC: claim succeeds from a custom active status and transitions to in_progress.
	if err := store.ClaimIssue(ctx, "cc-ready", "agent-a"); err != nil {
		t.Fatalf("claim from custom active status should succeed, got: %v", err)
	}
	got, err := store.GetIssue(ctx, "cc-ready")
	if err != nil {
		t.Fatalf("get cc-ready: %v", err)
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("cc-ready status = %q, want in_progress", got.Status)
	}
	if got.Assignee != "agent-a" {
		t.Errorf("cc-ready assignee = %q, want agent-a", got.Assignee)
	}

	// Regression: built-in open remains claimable.
	if err := store.ClaimIssue(ctx, "cc-open", "agent-a"); err != nil {
		t.Fatalf("claim from open should succeed, got: %v", err)
	}

	// AC: claim is rejected from a custom wip status (anti-steal preserved).
	err = store.ClaimIssue(ctx, "cc-reviewing", "agent-a")
	if err == nil {
		t.Fatalf("claim from custom wip status should fail, got nil")
	}
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("claim from wip status: got %v, want ErrNotClaimable", err)
	}
	// The rejected issue must be left untouched.
	got, err = store.GetIssue(ctx, "cc-reviewing")
	if err != nil {
		t.Fatalf("get cc-reviewing: %v", err)
	}
	if got.Status != types.Status("reviewing") {
		t.Errorf("cc-reviewing status = %q, want reviewing (unchanged)", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("cc-reviewing assignee = %q, want empty (unchanged)", got.Assignee)
	}
}

// TestClaimIssue_CustomActiveAntiSteal verifies that allowing claims from custom
// active statuses (GH#4164) does not weaken the GH#3570 anti-steal guarantee:
// once claimed by one actor, a different actor cannot re-claim, while a re-claim
// by the same actor remains an idempotent success.
func TestClaimIssue_CustomActiveAntiSteal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "status.custom", "ready:active"); err != nil {
		t.Fatalf("failed to set custom statuses: %v", err)
	}

	iss := &types.Issue{
		ID:        "cc-steal",
		Title:     "Steal target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.UpdateIssue(ctx, "cc-steal", map[string]interface{}{"status": "ready"}, "tester"); err != nil {
		t.Fatalf("move to ready: %v", err)
	}

	if err := store.ClaimIssue(ctx, "cc-steal", "agent-a"); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}

	// A different actor must be rejected.
	if err := store.ClaimIssue(ctx, "cc-steal", "agent-b"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("second-actor claim: got %v, want ErrAlreadyClaimed", err)
	}

	// Same actor re-claim is an idempotent success.
	if err := store.ClaimIssue(ctx, "cc-steal", "agent-a"); err != nil {
		t.Errorf("idempotent re-claim by same actor should succeed, got: %v", err)
	}
}
