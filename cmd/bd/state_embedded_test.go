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

// bdState runs "bd state" with the given args and returns stdout.
func bdState(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"state"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd state %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdSetState runs "bd set-state" with the given args and returns stdout.
func bdSetState(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"set-state"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd set-state %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedState(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "st")

	issue := bdCreate(t, bd, dir, "State test issue", "--type", "task")

	// ===== set-state =====

	t.Run("set_state_basic", func(t *testing.T) {
		out := bdSetState(t, bd, dir, issue.ID, "phase=planning")
		if !strings.Contains(out, "planning") {
			t.Logf("set-state output: %s", out)
		}
	})

	t.Run("set_state_json", func(t *testing.T) {
		cmd := exec.Command(bd, "set-state", issue.ID, "env=staging", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd set-state --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start >= 0 {
			var m map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(s[start:]), &m); jsonErr != nil {
				t.Errorf("invalid JSON: %v\n%s", jsonErr, s)
			}
		}
	})

	t.Run("set_state_with_reason", func(t *testing.T) {
		out := bdSetState(t, bd, dir, issue.ID, "risk=high", "--reason", "New vulnerability found")
		_ = out
	})

	t.Run("set_state_overwrites", func(t *testing.T) {
		bdSetState(t, bd, dir, issue.ID, "phase=development")
		bdSetState(t, bd, dir, issue.ID, "phase=testing")

		out := bdState(t, bd, dir, issue.ID, "phase")
		if !strings.Contains(out, "testing") {
			t.Errorf("expected 'testing' after overwrite, got: %s", out)
		}
	})

	// ===== state query =====

	t.Run("state_query", func(t *testing.T) {
		out := bdState(t, bd, dir, issue.ID, "phase")
		if !strings.Contains(out, "testing") {
			t.Logf("state query output: %s", out)
		}
	})

	t.Run("state_query_json", func(t *testing.T) {
		cmd := exec.Command(bd, "state", issue.ID, "phase", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd state --json failed: %v\n%s", err, out)
		}
		_ = out
	})

	t.Run("state_query_nonexistent_dimension", func(t *testing.T) {
		out := bdState(t, bd, dir, issue.ID, "nonexistent")
		// Should return empty/not-set, not error
		_ = out
	})

	// ===== state list =====

	t.Run("state_list", func(t *testing.T) {
		out := bdState(t, bd, dir, "list", issue.ID)
		// Should show the dimensions we set
		if !strings.Contains(out, "phase") {
			t.Logf("state list output: %s", out)
		}
	})

	t.Run("state_list_json", func(t *testing.T) {
		cmd := exec.Command(bd, "state", "list", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd state list --json failed: %v\n%s", err, out)
		}
		_ = out
	})

	t.Run("state_list_no_dimensions", func(t *testing.T) {
		fresh := bdCreate(t, bd, dir, "No state", "--type", "task")
		out := bdState(t, bd, dir, "list", fresh.ID)
		_ = out
	})
}

// TestEmbeddedStateConcurrent exercises set-state concurrently on different dimensions.
func TestEmbeddedStateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sx")

	issue := bdCreate(t, bd, dir, "Concurrent state", "--type", "task")

	const numWorkers = 6

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

			dim := fmt.Sprintf("dim%d=val%d", worker, worker)
			cmd := exec.Command(bd, "set-state", issue.ID, dim)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("set-state %s: %v\n%s", dim, err, out)
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
}
