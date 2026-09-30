//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdConfigSet writes one workspace config key, the way a team turning the
// invariant on would.
func bdConfigSet(t *testing.T, bd, dir, key, value string) {
	t.Helper()
	cmd := exec.Command(bd, "config", "set", key, value)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd config set %s %s failed: %v\n%s", key, value, err, out)
	}
}

// bdCreateFailDue runs a create expecting a refusal and returns the output so
// the caller can pin WHICH refusal it was.
func bdCreateFailDue(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"create"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd create %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// The default is the compatibility promise: a workspace that has never heard
// of due.required behaves exactly as it did before the switch existed.
func TestEmbeddedDueRequiredDefaultsOff(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "df")

	issue := bdCreate(t, bd, dir, "No deadline needed", "--type", "task")
	if issue.DueAt != nil {
		t.Errorf("due_at = %v on a default workspace, want nil", issue.DueAt)
	}

	// And clearing a due date needs no ceremony while the switch is off.
	dated := bdCreate(t, bd, dir, "Has a deadline", "--type", "task", "--due", "2030-01-01")
	bdUpdate(t, bd, dir, dated.ID, "--due", "")
	if got := bdShow(t, bd, dir, dated.ID); got.DueAt != nil {
		t.Errorf("due_at = %v after a plain clear, want nil", got.DueAt)
	}
}

func TestEmbeddedDueRequiredEnforced(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dq")
	bdConfigSet(t, bd, dir, "due.required", "true")

	t.Run("work_without_a_due_date_is_refused", func(t *testing.T) {
		out := bdCreateFailDue(t, bd, dir, "Needs a deadline", "--type", "task")
		if !strings.Contains(out, "due date is required") {
			t.Errorf("refusal did not name the invariant:\n%s", out)
		}
	})

	t.Run("work_with_a_due_date_is_accepted", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Has a deadline", "--type", "task", "--due", "2030-01-01")
		if issue.DueAt == nil {
			t.Error("due_at is nil on a create that supplied one")
		}
	})

	// An event is a record of something that happened, not work anyone is
	// meant to finish by a date, so the invariant does not reach it.
	t.Run("an_exempt_type_is_still_accepted_without_one", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Something happened", "--type", "event")
		if issue.DueAt != nil {
			t.Errorf("due_at = %v on an exempt type, want nil", issue.DueAt)
		}
	})

	t.Run("a_clear_without_the_gate_is_refused_and_writes_nothing", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Keeps its deadline", "--type", "task", "--due", "2030-01-01")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--due", "")
		if !strings.Contains(out, "--force-no-due") {
			t.Errorf("refusal did not name the gate:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.DueAt == nil {
			t.Error("a refused clear still removed the due date")
		}
	})

	t.Run("the_gate_needs_a_reason_not_just_the_flag", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Also keeps it", "--type", "task", "--due", "2030-01-01")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--due", "", "--force-no-due")
		if !strings.Contains(out, "--reason") {
			t.Errorf("refusal did not ask for a reason:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.DueAt == nil {
			t.Error("a refused clear still removed the due date")
		}
	})

	t.Run("a_gated_clear_succeeds_and_records_why", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Loses its deadline", "--type", "task", "--due", "2030-01-01")
		const why = "tracked upstream in gh-4242"
		bdUpdate(t, bd, dir, issue.ID, "--due", "", "--force-no-due", "--reason", why)
		if got := bdShow(t, bd, dir, issue.ID); got.DueAt != nil {
			t.Fatalf("due_at = %v after a forced clear, want nil", got.DueAt)
		}
		events := bdHistoryJSON(t, bd, dir, issue.ID, "--events")
		found := false
		for _, event := range events {
			for _, value := range event {
				if text, isString := value.(string); isString && strings.Contains(text, why) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no event carrying the reason %q in:\n%+v", why, events)
		}
	})
}
