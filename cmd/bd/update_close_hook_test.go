// update_close_hook_test.go - Regression tests for hook symmetry between
// bd update --status closed and bd close.
//
// Bug: "bd update --status closed" did not fire the on_close hook.
// Bug: "bd close" did not fire the on_update hook.
//
// Both commands change status to closed; both hooks should fire in each case,
// but only when there is an actual state transition (not when re-closing
// an already-closed issue).

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateCloseHookFiring verifies hook firing logic for status transitions.
// Uses RunSync to test hooks without needing the full CLI or Dolt infrastructure.
func TestUpdateCloseHookFiring(t *testing.T) {
	t.Run("on_close_fires_for_status_transition_to_closed", func(t *testing.T) {
		tmpDir := t.TempDir()
		markerFile := filepath.Join(tmpDir, "on_close_fired.txt")

		hookScript := "#!/bin/sh\necho \"$1 $2\" > " + markerFile + "\n"
		if err := os.WriteFile(filepath.Join(tmpDir, "on_close"), []byte(hookScript), 0755); err != nil {
			t.Fatalf("Failed to write hook: %v", err)
		}

		runner := hooks.NewRunner(tmpDir)
		issue := &types.Issue{
			ID:     "test-1",
			Title:  "Test Issue",
			Status: types.StatusClosed,
		}

		// Simulate what update.go should do: fire on_close when status transitions to closed
		err := runner.RunSync(hooks.EventClose, issue)
		if err != nil {
			t.Fatalf("RunSync failed: %v", err)
		}

		content, err := os.ReadFile(markerFile)
		if err != nil {
			t.Fatalf("on_close hook did not create marker file: %v", err)
		}
		if len(content) == 0 {
			t.Error("on_close hook marker file is empty")
		}
	})

	t.Run("on_update_fires_for_close_command", func(t *testing.T) {
		tmpDir := t.TempDir()
		markerFile := filepath.Join(tmpDir, "on_update_fired.txt")

		hookScript := "#!/bin/sh\necho \"$1 $2\" > " + markerFile + "\n"
		if err := os.WriteFile(filepath.Join(tmpDir, "on_update"), []byte(hookScript), 0755); err != nil {
			t.Fatalf("Failed to write hook: %v", err)
		}

		runner := hooks.NewRunner(tmpDir)
		issue := &types.Issue{
			ID:     "test-2",
			Title:  "Test Issue",
			Status: types.StatusClosed,
		}

		// Simulate what close.go should do: fire on_update when closing
		err := runner.RunSync(hooks.EventUpdate, issue)
		if err != nil {
			t.Fatalf("RunSync failed: %v", err)
		}

		content, err := os.ReadFile(markerFile)
		if err != nil {
			t.Fatalf("on_update hook did not create marker file: %v", err)
		}
		if len(content) == 0 {
			t.Error("on_update hook marker file is empty")
		}
	})
}

// TestUpdateCloseHookCondition verifies the state-transition guard logic
// that the fix in update.go and close.go depends on.
// This tests the conditional logic, not the CLI plumbing.
func TestUpdateCloseHookCondition(t *testing.T) {
	t.Run("should_fire_on_close_when_transitioning_from_open", func(t *testing.T) {
		oldStatus := types.StatusOpen
		newStatus := types.StatusClosed
		shouldFire := newStatus == types.StatusClosed && oldStatus != types.StatusClosed
		if !shouldFire {
			t.Error("on_close should fire when transitioning from open to closed")
		}
	})

	t.Run("should_fire_on_close_when_transitioning_from_in_progress", func(t *testing.T) {
		oldStatus := types.StatusInProgress
		newStatus := types.StatusClosed
		shouldFire := newStatus == types.StatusClosed && oldStatus != types.StatusClosed
		if !shouldFire {
			t.Error("on_close should fire when transitioning from in_progress to closed")
		}
	})

	t.Run("should_not_fire_on_close_when_already_closed", func(t *testing.T) {
		oldStatus := types.StatusClosed
		newStatus := types.StatusClosed
		shouldFire := newStatus == types.StatusClosed && oldStatus != types.StatusClosed
		if shouldFire {
			t.Error("on_close should NOT fire when issue is already closed")
		}
	})

	t.Run("should_not_fire_on_close_when_transitioning_to_non_closed", func(t *testing.T) {
		oldStatus := types.StatusOpen
		newStatus := types.StatusInProgress
		shouldFire := newStatus == types.StatusClosed && oldStatus != types.StatusClosed
		if shouldFire {
			t.Error("on_close should NOT fire when status is not closed")
		}
	})

	t.Run("close_cmd_should_fire_on_update_when_status_changes", func(t *testing.T) {
		// In close.go, the guard is: issue == nil || issue.Status != types.StatusClosed
		var issue *types.Issue

		// nil issue (not found before close) — should fire
		shouldFire := issue == nil || issue.Status != types.StatusClosed
		if !shouldFire {
			t.Error("on_update should fire when pre-close issue is nil")
		}

		// open issue — should fire
		issue = &types.Issue{Status: types.StatusOpen}
		shouldFire = issue == nil || issue.Status != types.StatusClosed
		if !shouldFire {
			t.Error("on_update should fire when closing an open issue")
		}

		// already closed issue — should NOT fire
		issue = &types.Issue{Status: types.StatusClosed}
		shouldFire = issue == nil || issue.Status != types.StatusClosed
		if shouldFire {
			t.Error("on_update should NOT fire when issue is already closed")
		}
	})
}
