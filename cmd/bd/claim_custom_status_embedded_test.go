//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedClaimFromCustomStatus is the regression test for
// gastownhall/beads#4164: claim hardcoded "status = 'open'", so issues parked
// in a project's custom claimable status (e.g. "ready") could never be claimed.
// claim.from-statuses makes the claimable set configurable while keeping "open"
// always claimable (default behavior unchanged).
func TestEmbeddedClaimFromCustomStatus(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "claim")

	// Register a custom "ready" status so issues can be parked there.
	if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "status.custom", "ready:active"); err != nil {
		t.Fatalf("config set status.custom failed: %v\n%s", err, out)
	}

	t.Run("default_open_only_claimable", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Default claimable", "--type", "task")

		// Park it in the custom "ready" status.
		bdUpdate(t, bd, dir, issue.ID, "--status", "ready")
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.Status("ready") {
			t.Fatalf("expected status ready after update, got %s", got.Status)
		}

		// With claim.from-statuses unset, only "open" is claimable, so claiming
		// a "ready" issue must fail — this pins the backward-compatible default.
		out := bdUpdateFail(t, bd, dir, issue.ID, "--claim")
		if !strings.Contains(out, "claimable") {
			t.Errorf("expected not-claimable error, got: %s", out)
		}
	})

	t.Run("custom_status_claimable_when_configured", func(t *testing.T) {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "claim.from-statuses", "ready"); err != nil {
			t.Fatalf("config set claim.from-statuses failed: %v\n%s", err, out)
		}

		issue := bdCreate(t, bd, dir, "Configured claimable", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "ready")

		// Now "ready" is claimable: claim should succeed and transition to in_progress.
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee == "" {
			t.Error("expected assignee to be set after claiming a configured custom status")
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress after claim, got %s", got.Status)
		}
	})

	t.Run("open_still_claimable_with_config", func(t *testing.T) {
		// "open" must remain claimable even when claim.from-statuses lists only
		// custom statuses (open is always implicitly included).
		issue := bdCreate(t, bd, dir, "Open still claimable", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusInProgress {
			t.Errorf("expected open issue to remain claimable, got status %s", got.Status)
		}
	})
}
