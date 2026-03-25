//go:build embeddeddolt

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdDefer runs "bd defer" with the given args and returns stdout.
func bdDefer(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"defer"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd defer %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdUndefer runs "bd undefer" with the given args and returns stdout.
func bdUndefer(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"undefer"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd undefer %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// getIssueStatus returns the status of an issue via bd show --json.
func getIssueStatus(t *testing.T, bd, dir, id string) string {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\n%s", id, err, out)
	}
	s := strings.TrimSpace(string(out))
	// show --json may return an array or object
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in show output: %s", s)
	}
	if s[start] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
			t.Fatalf("parse show JSON array: %v\n%s", err, s)
		}
		if len(arr) == 0 {
			t.Fatalf("empty JSON array in show output")
		}
		status, _ := arr[0]["status"].(string)
		return status
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, s)
	}
	status, _ := m["status"].(string)
	return status
}

func TestEmbeddedDefer(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "df")

	// ===== Single Issue =====

	t.Run("defer_single", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer single test", "--type", "task")
		out := bdDefer(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Deferred") {
			t.Errorf("expected 'Deferred' in output: %s", out)
		}
		status := getIssueStatus(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expected status=deferred, got %q", status)
		}
	})

	// ===== Multiple Issues =====

	t.Run("defer_multiple", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Defer multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Defer multi 2", "--type", "task")
		out := bdDefer(t, bd, dir, issue1.ID, issue2.ID)
		if !strings.Contains(out, issue1.ID) || !strings.Contains(out, issue2.ID) {
			t.Errorf("expected both IDs in output: %s", out)
		}
		for _, id := range []string{issue1.ID, issue2.ID} {
			status := getIssueStatus(t, bd, dir, id)
			if status != "deferred" {
				t.Errorf("expected %s status=deferred, got %q", id, status)
			}
		}
	})

	// ===== With --until =====

	t.Run("defer_until", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer until test", "--type", "task")
		out := bdDefer(t, bd, dir, issue.ID, "--until", "+1h")
		if !strings.Contains(out, "Deferred") {
			t.Errorf("expected 'Deferred' in output: %s", out)
		}
		status := getIssueStatus(t, bd, dir, issue.ID)
		if status != "deferred" {
			t.Errorf("expected status=deferred, got %q", status)
		}
	})

	// ===== Already Deferred =====

	t.Run("defer_already_deferred", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer idempotent", "--type", "task")
		bdDefer(t, bd, dir, issue.ID)
		// Defer again — should succeed
		out := bdDefer(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Deferred") {
			t.Errorf("expected 'Deferred' on second defer: %s", out)
		}
	})
}

// TestEmbeddedDeferConcurrent exercises defer operations concurrently.
func TestEmbeddedDeferConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dx")

	// Pre-create issues
	var issueIDs []string
	for i := 0; i < 8; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("defer-concurrent-%d", i), "--type", "task")
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

			cmd := exec.Command(bd, "defer", id)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("defer %s (worker %d): %v\n%s", id, worker, err, out)
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

	// Verify all are deferred
	for i, id := range issueIDs {
		status := getIssueStatus(t, bd, dir, id)
		if status != "deferred" {
			t.Errorf("issue %d (%s): expected status=deferred, got %q", i, id, status)
		}
	}
}
