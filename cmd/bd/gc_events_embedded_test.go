//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// gcEventsRunBD runs an arbitrary bd command in dir and fails the test on a non-zero
// exit.
func gcEventsRunBD(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// The Events phase of bd gc: off until events-retain-days or
// --events-older-than says otherwise, previewed by --dry-run, refused without
// --force, and once run the rows are gone.
func TestEmbeddedGCEventsRetention(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ge")
	issue := bdCreate(t, bd, dir, "GC events retention", "--type", "task")
	gcEventsRunBD(t, bd, dir, "update", issue.ID, "--priority", "1")
	// events.created_at has second precision; "older than now" needs the clock
	// to have moved past the second the rows were written in.
	time.Sleep(1500 * time.Millisecond)

	t.Run("default_keeps_every_event", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt")
		for _, want := range []string{"Phase 2/4: Events", "events-retain-days is 0: keeping every history event"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in output:\n%s", want, out)
			}
		}
	})

	t.Run("skip_events_flag", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt", "--skip-events", "--events-older-than", "0")
		if strings.Contains(out, "Phase 2/4") {
			t.Errorf("--skip-events still ran the phase:\n%s", out)
		}
		if !strings.Contains(out, "Events: skipped") {
			t.Errorf("expected 'Events: skipped' in the summary:\n%s", out)
		}
	})

	t.Run("dry_run_counts_without_deleting", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt", "--events-older-than", "0")
		if !strings.Contains(out, "Would prune ") || !strings.Contains(out, "history event(s) older than 0 days (--events-older-than)") {
			t.Fatalf("expected a would-prune line:\n%s", out)
		}
		again := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt", "--events-older-than", "0")
		if !strings.Contains(again, "Would prune ") {
			t.Fatalf("dry run deleted something; second dry run:\n%s", again)
		}
	})

	t.Run("refuses_without_force", func(t *testing.T) {
		out := bdGCFail(t, bd, dir, "--skip-decay", "--skip-dolt", "--events-older-than", "0")
		if !strings.Contains(out, "would prune") || !strings.Contains(out, "--force") {
			t.Fatalf("expected the force refusal:\n%s", out)
		}
	})

	t.Run("force_prunes_and_a_rerun_finds_nothing", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--force", "--skip-decay", "--skip-dolt", "--events-older-than", "0")
		if !strings.Contains(out, "Pruned ") || !strings.Contains(out, "history event(s) older than 0 days") {
			t.Fatalf("expected a pruned line:\n%s", out)
		}
		if strings.Contains(out, "Pruned 0 ") {
			t.Fatalf("pruned nothing:\n%s", out)
		}
		again := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt", "--events-older-than", "0")
		if !strings.Contains(again, "No history events older than 0 days") {
			t.Fatalf("expected nothing left to prune:\n%s", again)
		}
		// The issue itself is untouched; only its history went.
		gcEventsRunBD(t, bd, dir, "show", issue.ID)
	})

	t.Run("config_knob_sets_the_default", func(t *testing.T) {
		gcEventsRunBD(t, bd, dir, "config", "set", "events-retain-days", "30")
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt")
		if !strings.Contains(out, "No history events older than 30 days (events-retain-days)") {
			t.Fatalf("expected the config-driven cutoff:\n%s", out)
		}
	})
}
