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

	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}

	// Verify all are open
	for i, id := range issueIDs {
		status := getIssueStatus(t, bd, dir, id)
		if status != "open" {
			t.Errorf("issue %d (%s): expected status=open, got %q", i, id, status)
		}
	}
}
