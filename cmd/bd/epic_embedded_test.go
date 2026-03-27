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

// bdEpic runs "bd epic" with the given args and returns raw stdout.
func bdEpic(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"epic"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd epic %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdEpicJSON runs "bd epic" with --json and parses the result.
func bdEpicJSON(t *testing.T, bd, dir string, args ...string) interface{} {
	t.Helper()
	fullArgs := append([]string{"epic"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd epic --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		t.Fatalf("no JSON in epic output: %s", s)
	}
	var result interface{}
	if err := json.Unmarshal([]byte(s[start:]), &result); err != nil {
		t.Fatalf("parse epic JSON: %v\n%s", err, s)
	}
	return result
}

func TestEmbeddedEpic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ep")

	// Create an epic with children — some closed, some open.
	// Use 3 children so closing one doesn't auto-close the epic.
	epic1 := bdCreate(t, bd, dir, "Epic partially done", "--type", "epic")
	c1 := bdCreate(t, bd, dir, "Epic1 child 1", "--type", "task")
	c2 := bdCreate(t, bd, dir, "Epic1 child 2", "--type", "task")
	c2b := bdCreate(t, bd, dir, "Epic1 child 3", "--type", "task")
	bdDep(t, bd, dir, "add", c1.ID, epic1.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", c2.ID, epic1.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", c2b.ID, epic1.ID, "--type", "parent-child")
	bdClose(t, bd, dir, c1.ID)
	// c2, c2b still open — epic1 is NOT eligible
	_ = c2b

	// ===== epic status =====

	t.Run("status_shows_progress", func(t *testing.T) {
		out := bdEpic(t, bd, dir, "status")
		if !strings.Contains(out, epic1.ID) {
			t.Errorf("expected epic1 in status output: %s", out)
		}
		if !strings.Contains(out, "children closed") || !strings.Contains(out, "/") {
			t.Errorf("expected progress fraction in output: %s", out)
		}
	})

	t.Run("status_eligible_only", func(t *testing.T) {
		// When all children are closed, the epic is auto-closed by bd close.
		// So --eligible-only may find nothing if auto-close already happened.
		// Just verify the flag doesn't crash and filters correctly.
		out := bdEpic(t, bd, dir, "status", "--eligible-only")
		// epic1 has open children — should NOT appear
		if strings.Contains(out, epic1.ID) {
			t.Errorf("epic1 (partially done) should not appear with --eligible-only: %s", out)
		}
	})

	t.Run("status_json_output", func(t *testing.T) {
		result := bdEpicJSON(t, bd, dir, "status")
		arr, ok := result.([]interface{})
		if !ok {
			t.Fatalf("expected JSON array, got %T", result)
		}
		if len(arr) < 1 {
			t.Errorf("expected at least 1 epic, got %d", len(arr))
		}
	})

	t.Run("status_no_open_epics", func(t *testing.T) {
		dir2, _, _ := bdInit(t, bd, "--prefix", "ep2")
		out := bdEpic(t, bd, dir2, "status")
		if !strings.Contains(out, "No open epics") {
			t.Errorf("expected 'No open epics': %s", out)
		}
	})

	// ===== epic close-eligible =====

	t.Run("close_eligible_dry_run", func(t *testing.T) {
		out := bdEpic(t, bd, dir, "close-eligible", "--dry-run")
		// epic1 has open children — should not appear
		if strings.Contains(out, epic1.ID) {
			t.Errorf("epic1 (not eligible) should not appear in dry-run: %s", out)
		}
		// Just verify no crash — auto-close may have already closed eligible epics
	})

	t.Run("close_eligible_closes_epics", func(t *testing.T) {
		// Note: bd close auto-closes the parent epic when the last child closes.
		// So by the time we run close-eligible, the epic is already closed.
		// Verify close-eligible handles this gracefully (no epics to close).
		dir3, _, _ := bdInit(t, bd, "--prefix", "ep3")
		e := bdCreate(t, bd, dir3, "Close me epic", "--type", "epic")
		ch := bdCreate(t, bd, dir3, "Close me child", "--type", "task")
		bdDep(t, bd, dir3, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir3, ch.ID) // This auto-closes the epic

		out := bdEpic(t, bd, dir3, "close-eligible")
		// Epic already auto-closed — close-eligible finds nothing or reports already closed
		_ = e
		if strings.Contains(out, "Error") {
			t.Errorf("unexpected error: %s", out)
		}
	})

	t.Run("close_eligible_json_output", func(t *testing.T) {
		dir4, _, _ := bdInit(t, bd, "--prefix", "ep4")
		e := bdCreate(t, bd, dir4, "JSON close epic", "--type", "epic")
		ch := bdCreate(t, bd, dir4, "JSON close child", "--type", "task")
		bdDep(t, bd, dir4, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir4, ch.ID) // auto-closes epic
		_ = e

		fullArgs := []string{"epic", "close-eligible", "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir4
		cmd.Env = bdEnv(dir4)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("epic close-eligible --json failed: %v\n%s", err, out)
		}
		// Should produce valid JSON (empty array or object)
		s := strings.TrimSpace(string(out))
		start := strings.IndexAny(s, "{[")
		if start < 0 {
			t.Fatalf("no JSON: %s", s)
		}
	})

	t.Run("close_eligible_none", func(t *testing.T) {
		// epic1 has open children — should not be closeable
		out := bdEpic(t, bd, dir, "close-eligible")
		if strings.Contains(out, epic1.ID) {
			t.Errorf("epic1 should not be closed: %s", out)
		}
	})
}

// TestEmbeddedEpicConcurrent exercises epic operations concurrently.
func TestEmbeddedEpicConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "epc")

	// Create several epics with children.
	for i := 0; i < 4; i++ {
		e := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-epic-%d", i), "--type", "epic")
		for j := 0; j < 3; j++ {
			c := bdCreate(t, bd, dir, fmt.Sprintf("epic-%d-child-%d", i, j), "--type", "task")
			bdDep(t, bd, dir, "add", c.ID, e.ID, "--type", "parent-child")
		}
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

			queries := [][]string{
				{"status", "--json"},
				{"status", "--eligible-only", "--json"},
				{"status", "--json"},
				{"close-eligible", "--dry-run", "--json"},
				{"status", "--json"},
				{"status", "--eligible-only", "--json"},
				{"status", "--json"},
				{"close-eligible", "--dry-run", "--json"},
			}
			q := queries[worker%len(queries)]

			args := append([]string{"epic"}, q...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d: %v\n%s", worker, err, out)
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
