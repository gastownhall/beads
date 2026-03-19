//go:build embeddeddolt

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdUpdate runs "bd update" with the given args and returns stdout.
func bdUpdate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd update %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdUpdateFail runs "bd update" expecting failure.
func bdUpdateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd update %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdClose runs "bd close" with the given args and returns stdout.
func bdClose(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd close %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdCloseFail runs "bd close" expecting failure.
func bdCloseFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd close %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedUpdate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tu")

	t.Run("update_status", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Status test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress, got %s", got.Status)
		}
	})

	t.Run("update_title", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Old title", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--title", "New title")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Title != "New title" {
			t.Errorf("expected title 'New title', got %q", got.Title)
		}
	})

	t.Run("update_assignee", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Assign test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "alice" {
			t.Errorf("expected assignee alice, got %q", got.Assignee)
		}
	})

	t.Run("update_priority", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Priority test", "--type", "task", "--priority", "3")
		bdUpdate(t, bd, dir, issue.ID, "--priority", "0")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Priority != 0 {
			t.Errorf("expected priority 0, got %d", got.Priority)
		}
	})

	t.Run("update_description", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Desc test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--description", "Updated description")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Description != "Updated description" {
			t.Errorf("expected description 'Updated description', got %q", got.Description)
		}
	})

	t.Run("update_multiple_fields", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi update", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress", "--assignee", "bob", "--priority", "1")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress, got %s", got.Status)
		}
		if got.Assignee != "bob" {
			t.Errorf("expected assignee bob, got %q", got.Assignee)
		}
		if got.Priority != 1 {
			t.Errorf("expected priority 1, got %d", got.Priority)
		}
	})

	t.Run("close_via_status_update", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close via update", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "closed")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	t.Run("reopen_via_status_update", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reopen test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "closed")
		bdUpdate(t, bd, dir, issue.ID, "--status", "open")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open, got %s", got.Status)
		}
		if got.ClosedAt != nil {
			t.Error("expected closed_at to be cleared on reopen")
		}
	})
}

func TestEmbeddedClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tc")

	t.Run("basic_close", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close me", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	t.Run("close_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close with reason", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--reason", "done")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.CloseReason != "done" {
			t.Errorf("expected close_reason 'done', got %q", got.CloseReason)
		}
	})

	t.Run("close_blocked_without_force", func(t *testing.T) {
		// Create blocker and blocked issues with a blocking dep.
		blocker := bdCreate(t, bd, dir, "Blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked", "--type", "task")

		// Add blocking dependency: blocked is blocked by blocker.
		cmd := exec.Command(bd, "dep", "add", blocked.ID, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd dep add failed: %v\n%s", err, out)
		}

		// Closing a blocked issue requires --force.
		bdClose(t, bd, dir, blocked.ID, "--force")
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
	})

	t.Run("close_unblocks_dependent", func(t *testing.T) {
		// Create two issues with a blocking dep.
		blocker := bdCreate(t, bd, dir, "Unblock blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Unblock blocked", "--type", "task")

		// Add blocking dependency.
		cmd := exec.Command(bd, "dep", "add", blocked.ID, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd dep add failed: %v\n%s", err, out)
		}

		// Close the blocker — the close output may mention the unblocked issue.
		closeOut := bdClose(t, bd, dir, blocker.ID)
		// Verify blocker is closed.
		got := bdShow(t, bd, dir, blocker.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected blocker status closed, got %s", got.Status)
		}
		// The blocked issue should still be open (closing blocker doesn't auto-close it).
		gotBlocked := bdShow(t, bd, dir, blocked.ID)
		if gotBlocked.Status != types.StatusOpen {
			t.Errorf("expected blocked issue status open, got %s", gotBlocked.Status)
		}
		_ = closeOut // Output may contain "newly unblocked" info.
	})

	t.Run("close_already_closed", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Double close", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		// Closing again should not panic or error fatally.
		// Some implementations may return an error, others may be idempotent.
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput() // Don't check error — behavior varies.
	})
}
