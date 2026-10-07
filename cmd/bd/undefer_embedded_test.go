//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedUndefer(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ud")

	// ===== Single Issue =====

	t.Run("undefer_single", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Undefer single", "--type", "task")
		bdDefer(t, bd, dir, issue.ID)
		if s := getIssueStatus(t, bd, dir, issue.ID); s != "deferred" {
			t.Fatalf("expected deferred before undefer, got %q", s)
		}

		out := bdUndefer(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Undeferred") {
			t.Errorf("expected 'Undeferred' in output: %s", out)
		}
		if s := getIssueStatus(t, bd, dir, issue.ID); s != "open" {
			t.Errorf("expected status=open after undefer, got %q", s)
		}
	})

	// ===== Multiple Issues =====

	t.Run("undefer_multiple", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Undefer multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Undefer multi 2", "--type", "task")
		bdDefer(t, bd, dir, issue1.ID, issue2.ID)

		out := bdUndefer(t, bd, dir, issue1.ID, issue2.ID)
		if !strings.Contains(out, issue1.ID) || !strings.Contains(out, issue2.ID) {
			t.Errorf("expected both IDs in output: %s", out)
		}
		for _, id := range []string{issue1.ID, issue2.ID} {
			if s := getIssueStatus(t, bd, dir, id); s != "open" {
				t.Errorf("expected %s status=open, got %q", id, s)
			}
		}
	})

	// ===== Not Deferred =====

	t.Run("undefer_not_deferred", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Undefer not deferred", "--type", "task")
		// Issue is open, not deferred — undefer should print error but not crash
		cmd := exec.Command(bd, "undefer", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "not deferred") {
			t.Errorf("expected 'not deferred' message: %s", out)
		}
	})

	// ===== Stray defer_until on a non-deferred status (ga-bq3w5) =====

	t.Run("undefer_clears_stray_defer_until_when_status_not_deferred", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Stray future defer on open issue", "--type", "task")
		// Reproduces ga-bq3w5: an explicit --status wins over --defer's own
		// status=deferred default (update.go), so this single command leaves
		// status=open with a live future defer_until — exactly the shape that
		// silently starves an issue out of `bd ready` with no status-based
		// signal anywhere.
		cmd := exec.Command(bd, "update", issue.ID, "--status", "open", "--defer", "2099-01-01")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd update --status open --defer failed: %v\n%s", err, out)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "open" || deferUntil == nil {
			t.Fatalf("precondition: expected status=open with defer_until set, got status=%q defer_until=%v", status, deferUntil)
		}
		if ids := wakeReadyIDs(t, bd, dir); ids[issue.ID] {
			t.Fatalf("precondition: issue should be hidden from bd ready by its future defer_until")
		}

		out := bdUndefer(t, bd, dir, issue.ID)
		if strings.Contains(out, "is not deferred") {
			t.Errorf("undefer refused instead of clearing the stray defer_until: %s", out)
		}

		status, deferUntil = showDeferState(t, bd, dir, issue.ID)
		if status != "open" {
			t.Errorf("expected status to stay open, got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after undefer, got %v", deferUntil)
		}
		if ids := wakeReadyIDs(t, bd, dir); !ids[issue.ID] {
			t.Errorf("expected issue back in bd ready after undefer cleared its defer_until")
		}
	})

	// ===== Stray defer_until on a non-open status must not be clobbered =====

	t.Run("undefer_clears_stray_defer_until_without_clobbering_in_progress", func(t *testing.T) {
		// The "open" case above can't distinguish the conditional status
		// write (only flip to open when wasDeferred) from an unconditional
		// one: writing status=open to an already-open issue is a no-op.
		// in_progress makes the distinction observable.
		issue := bdCreate(t, bd, dir, "Stray future defer on in_progress issue", "--type", "task")
		cmd := exec.Command(bd, "update", issue.ID, "--status", "in_progress", "--defer", "2099-01-01")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd update --status in_progress --defer failed: %v\n%s", err, out)
		}
		status, deferUntil := showDeferState(t, bd, dir, issue.ID)
		if status != "in_progress" || deferUntil == nil {
			t.Fatalf("precondition: expected status=in_progress with defer_until set, got status=%q defer_until=%v", status, deferUntil)
		}

		out := bdUndefer(t, bd, dir, issue.ID)
		if strings.Contains(out, "is not deferred") {
			t.Errorf("undefer refused instead of clearing the stray defer_until: %s", out)
		}

		status, deferUntil = showDeferState(t, bd, dir, issue.ID)
		if status != "in_progress" {
			t.Errorf("expected status to stay in_progress (not clobbered to open), got %q", status)
		}
		if deferUntil != nil {
			t.Errorf("expected defer_until cleared after undefer, got %v", deferUntil)
		}
	})
}

// TestEmbeddedUndeferConcurrent exercises undefer operations concurrently.
func TestEmbeddedUndeferConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ux")

	// Pre-create and defer issues
	var issueIDs []string
	for i := 0; i < 8; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("undefer-concurrent-%d", i), "--type", "task")
		bdDefer(t, bd, dir, issue.ID)
		issueIDs = append(issueIDs, issue.ID)
	}

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			id := issueIDs[worker]

			cmd := exec.Command(bd, "undefer", id)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("undefer %s (worker %d): %v\n%s", id, worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()

	var successes int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			continue
		}
		successes++
	}
	if successes == 0 {
		t.Fatal("all workers failed; expected at least 1 success")
	}
	t.Logf("%d/%d workers succeeded (flock contention expected)", successes, numWorkers)

	// Verify only successful workers' issues are open
	for _, r := range results {
		if r.err != nil {
			continue
		}
		id := issueIDs[r.worker]
		status := getIssueStatus(t, bd, dir, id)
		if status != "open" {
			t.Errorf("issue %d (%s): expected status=open, got %q", r.worker, id, status)
		}
	}
}
