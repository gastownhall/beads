//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdUnclaim runs "bd unclaim" with the given args and returns stdout.
// Retries on flock contention.
func bdUnclaim(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"unclaim"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd unclaim %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdUnclaimFail runs "bd unclaim" expecting failure.
func bdUnclaimFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"unclaim"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd unclaim %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedUnclaim(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tu")

	// ===== Basic Success =====

	t.Run("unclaim_from_in_progress", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Unclaim test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		bdUnclaim(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "" {
			t.Errorf("expected assignee to be empty after unclaim, got %q", got.Assignee)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open after unclaim, got %s", got.Status)
		}
	})

	t.Run("unclaim_from_open_stuck_claim", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Stuck claim test", "--type", "task")
		// Manually set assignee without changing status (simulates stuck claim)
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
		bdUnclaim(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "" {
			t.Errorf("expected assignee to be empty after unclaim, got %q", got.Assignee)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open after unclaim, got %s", got.Status)
		}
	})

	// ===== With Reason =====

	t.Run("unclaim_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reason test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		out := bdUnclaim(t, bd, dir, issue.ID, "--reason", "Agent crashed")
		if !strings.Contains(out, "Agent crashed") {
			t.Errorf("expected reason in output, got: %s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "" {
			t.Errorf("expected assignee to be empty after unclaim, got %q", got.Assignee)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open after unclaim, got %s", got.Status)
		}
	})

	// ===== JSON Output =====

	t.Run("unclaim_json_output", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		out := bdUnclaim(t, bd, dir, issue.ID, "--json")
		if !strings.Contains(out, `"assignee":""`) && !strings.Contains(out, `"assignee": null`) {
			t.Errorf("expected empty assignee in JSON output, got: %s", out)
		}
		if !strings.Contains(out, `"status":"open"`) {
			t.Errorf("expected status open in JSON output, got: %s", out)
		}
	})

	// ===== Multiple IDs =====

	t.Run("unclaim_multiple_ids", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi test 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi test 2", "--type", "task")
		bdUpdate(t, bd, dir, issue1.ID, "--claim")
		bdUpdate(t, bd, dir, issue2.ID, "--claim")
		bdUnclaim(t, bd, dir, issue1.ID, issue2.ID)
		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.Assignee != "" {
			t.Errorf("expected assignee to be empty for issue1, got %q", got1.Assignee)
		}
		if got2.Assignee != "" {
			t.Errorf("expected assignee to be empty for issue2, got %q", got2.Assignee)
		}
		if got1.Status != types.StatusOpen {
			t.Errorf("expected status open for issue1, got %s", got1.Status)
		}
		if got2.Status != types.StatusOpen {
			t.Errorf("expected status open for issue2, got %s", got2.Status)
		}
	})

	// ===== Error Cases =====

	t.Run("unclaim_non_existent_issue", func(t *testing.T) {
		out := bdUnclaimFail(t, bd, dir, "nonexistent")
		if !strings.Contains(out, "not found") && !strings.Contains(out, "Error") {
			t.Errorf("expected error about non-existent issue, got: %s", out)
		}
	})

	t.Run("unclaim_closed_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Closed test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		// Close the issue first
		bdClose(t, bd, dir, issue.ID)
		out := bdUnclaimFail(t, bd, dir, issue.ID)
		if !strings.Contains(out, "cannot unclaim closed issue") {
			t.Errorf("expected error about closed issue, got: %s", out)
		}
	})

	t.Run("unclaim_unassigned_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Unassigned test", "--type", "task")
		out := bdUnclaimFail(t, bd, dir, issue.ID)
		if !strings.Contains(out, "is not assigned") {
			t.Errorf("expected error about unassigned issue, got: %s", out)
		}
	})
}
