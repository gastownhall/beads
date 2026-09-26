//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// bdDueSweep runs `bd due sweep --json` and returns the parsed report. The
// clock's whole contract runs through this command, so the tests below drive
// it exactly as the timer does rather than calling the sweep in-process.
func bdDueSweep(t *testing.T, bd, dir string) dueSweepReport {
	t.Helper()
	cmd := exec.Command(bd, "due", "sweep", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd due sweep --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in due sweep output:\n%s", s)
	}
	var report dueSweepReport
	if err := json.Unmarshal([]byte(s[start:]), &report); err != nil {
		t.Fatalf("parse due sweep JSON: %v\n%s", err, s)
	}
	return report
}

// pastDue is a date far enough back that no timezone reading of it lands in
// the future. A bare wall-clock offset of a few hours does not survive the
// CLI's local-time parsing and silently produces a bead that is not overdue
// at all, which makes a sweep test pass for the wrong reason.
func pastDue() string {
	return time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
}

func TestEmbeddedDueSweepFiresAndEscalates(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("an arrived deadline fires once and is rescheduled", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ds")
		issue := bdCreate(t, bd, dir, "Overdue work", "--type", "task", "--due", pastDue())

		report := bdDueSweep(t, bd, dir)
		if !containsString(report.DueIDs, issue.ID) {
			t.Fatalf("due sweep did not fire %s; report = %+v", issue.ID, report)
		}

		after := bdShow(t, bd, dir, issue.ID)
		if after.DueAt == nil || !after.DueAt.After(time.Now().UTC()) {
			t.Errorf("due_at = %v, want a date in the future (the miss must be rescheduled, not left to re-fire)", after.DueAt)
		}
		if after.DueMissed != 1 {
			t.Errorf("due_missed = %d, want 1", after.DueMissed)
		}

		// The reschedule IS the idempotency mechanism: a second sweep with no
		// time passed must find nothing, or every ready read re-fires the same
		// bead forever.
		again := bdDueSweep(t, bd, dir)
		if containsString(again.DueIDs, issue.ID) {
			t.Errorf("second sweep re-fired %s; report = %+v", issue.ID, again)
		}
	})

	t.Run("three misses raise the priority exactly once", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "de")
		issue := bdCreate(t, bd, dir, "Chronically late", "--type", "task",
			"--priority", "2", "--due", pastDue())

		// Each round re-dates the bead into the past, so the sweep sees a
		// genuinely arrived deadline rather than the one it just pushed
		// forward — the shape a real miss streak has.
		var escalatedOn []int
		for round := 1; round <= issueops.DueMissEscalateAt+2; round++ {
			bdUpdate(t, bd, dir, issue.ID, "--due", pastDue())
			report := bdDueSweep(t, bd, dir)
			if containsString(report.EscalatedIDs, issue.ID) {
				escalatedOn = append(escalatedOn, round)
			}
		}

		if len(escalatedOn) != 1 || escalatedOn[0] != issueops.DueMissEscalateAt {
			t.Errorf("escalated on rounds %v, want exactly [%d]", escalatedOn, issueops.DueMissEscalateAt)
		}
		after := bdShow(t, bd, dir, issue.ID)
		if after.Priority != 1 {
			t.Errorf("priority = %d, want 1 (one rung up from 2, raised once)", after.Priority)
		}
		if after.DueMissed != issueops.DueMissEscalateAt+2 {
			t.Errorf("due_missed = %d, want %d", after.DueMissed, issueops.DueMissEscalateAt+2)
		}
	})

	t.Run("a closed bead never fires", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dc")
		issue := bdCreate(t, bd, dir, "Finished late", "--type", "task", "--due", pastDue())
		bdClose(t, bd, dir, issue.ID)

		report := bdDueSweep(t, bd, dir)
		if containsString(report.DueIDs, issue.ID) {
			t.Errorf("due sweep fired closed %s; a deadline on finished work is not a miss", issue.ID)
		}
	})

	t.Run("a workspace with nothing due reports a clean summary", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dq")
		bdCreate(t, bd, dir, "Not yet due", "--type", "task", "--due", "+30d")

		report := bdDueSweep(t, bd, dir)
		if report.DueFired != 0 || report.Escalated != 0 {
			t.Errorf("quiet workspace reported %d due / %d escalated, want 0/0", report.DueFired, report.Escalated)
		}
		// The summary is what an external clock publishes, so an empty sweep
		// must still produce one: a blank line on the rail is indistinguishable
		// from a clock that did not run.
		if report.Summary == "" {
			t.Error("summary is empty on a quiet sweep; a clock with nothing to say must still say it")
		}
	})
}

func containsString(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}
