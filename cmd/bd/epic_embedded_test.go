//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdEpic runs "bd epic" with the given args and returns raw stdout.
func bdEpic(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"epic"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd epic %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdEpicJSON runs "bd epic" with --json and parses the result.
func bdEpicJSON(t *testing.T, bd, dir string, args ...string) interface{} {
	t.Helper()
	fullArgs := append([]string{"epic"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd epic --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
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
		// --eligible-only should only show epics with all children complete.
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
	})

	t.Run("close_eligible_closes_epics", func(t *testing.T) {
		dir3, _, _ := bdInit(t, bd, "--prefix", "ep3")
		e := bdCreate(t, bd, dir3, "Close me epic", "--type", "epic")
		ch := bdCreate(t, bd, dir3, "Close me child", "--type", "task")
		bdDep(t, bd, dir3, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir3, ch.ID)

		got := bdShow(t, bd, dir3, e.ID)
		if got.Status != types.StatusOpen {
			t.Fatalf("expected epic to remain open until close-eligible runs, got %s", got.Status)
		}

		out := bdEpic(t, bd, dir3, "close-eligible")
		if strings.Contains(out, "Error") {
			t.Errorf("unexpected error: %s", out)
		}

		got = bdShow(t, bd, dir3, e.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected close-eligible to close the epic, got %s", got.Status)
		}
	})

	t.Run("close_eligible_json_output", func(t *testing.T) {
		dir4, _, _ := bdInit(t, bd, "--prefix", "ep4")
		e := bdCreate(t, bd, dir4, "JSON close epic", "--type", "epic")
		ch := bdCreate(t, bd, dir4, "JSON close child", "--type", "task")
		bdDep(t, bd, dir4, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir4, ch.ID)
		_ = e

		fullArgs := []string{"epic", "close-eligible", "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir4
		cmd.Env = bdEnv(dir4)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("epic close-eligible --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// Should produce valid JSON (empty array or object)
		s := strings.TrimSpace(stdout.String())
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

	// ===== epic close-eligible --reason (GH#4817) =====

	t.Run("close_eligible_reason_flag_persists", func(t *testing.T) {
		dir5, _, _ := bdInit(t, bd, "--prefix", "ep5")
		e := bdCreate(t, bd, dir5, "Reason epic", "--type", "epic")
		ch := bdCreate(t, bd, dir5, "Reason child", "--type", "task")
		bdDep(t, bd, dir5, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir5, ch.ID)

		bdEpic(t, bd, dir5, "close-eligible", "--reason", "Milestone shipped in v2.3")

		got := bdShow(t, bd, dir5, e.ID)
		if got.Status != types.StatusClosed {
			t.Fatalf("expected epic closed, got %s", got.Status)
		}
		if got.CloseReason != "Milestone shipped in v2.3" {
			t.Errorf("close_reason = %q, want %q", got.CloseReason, "Milestone shipped in v2.3")
		}
	})

	t.Run("close_eligible_default_reason_unchanged", func(t *testing.T) {
		dir6, _, _ := bdInit(t, bd, "--prefix", "ep6")
		e := bdCreate(t, bd, dir6, "Default reason epic", "--type", "epic")
		ch := bdCreate(t, bd, dir6, "Default reason child", "--type", "task")
		bdDep(t, bd, dir6, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir6, ch.ID)

		bdEpic(t, bd, dir6, "close-eligible")

		got := bdShow(t, bd, dir6, e.ID)
		if got.CloseReason != "All children completed" {
			t.Errorf("close_reason = %q, want default %q", got.CloseReason, "All children completed")
		}
	})

	t.Run("close_eligible_dry_run_shows_reason", func(t *testing.T) {
		dir7, _, _ := bdInit(t, bd, "--prefix", "ep7")
		e := bdCreate(t, bd, dir7, "Dry-run reason epic", "--type", "epic")
		ch := bdCreate(t, bd, dir7, "Dry-run reason child", "--type", "task")
		bdDep(t, bd, dir7, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir7, ch.ID)

		out := bdEpic(t, bd, dir7, "close-eligible", "--dry-run", "--reason", "planned reason")
		if !strings.Contains(out, "Would close") || !strings.Contains(out, "planned reason") {
			t.Errorf("dry-run output should preview the reason, got: %s", out)
		}
		got := bdShow(t, bd, dir7, e.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("dry-run must not close the epic, got %s", got.Status)
		}
	})

	t.Run("close_eligible_json_includes_reason", func(t *testing.T) {
		dir8, _, _ := bdInit(t, bd, "--prefix", "ep8")
		e := bdCreate(t, bd, dir8, "JSON reason epic", "--type", "epic")
		ch := bdCreate(t, bd, dir8, "JSON reason child", "--type", "task")
		bdDep(t, bd, dir8, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir8, ch.ID)

		result := bdEpicJSON(t, bd, dir8, "close-eligible", "--reason", "JSON reason")
		obj, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected JSON object, got %T", result)
		}
		if reason, _ := obj["reason"].(string); reason != "JSON reason" {
			t.Errorf("reason = %q, want %q", reason, "JSON reason")
		}
		closed, _ := obj["closed"].([]interface{})
		found := false
		for _, id := range closed {
			if id == e.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("closed = %v, want it to contain %s", closed, e.ID)
		}
	})

	t.Run("close_eligible_human_output_ends_with_reason", func(t *testing.T) {
		dir9, _, _ := bdInit(t, bd, "--prefix", "ep9")
		e := bdCreate(t, bd, dir9, "Output epic", "--type", "epic")
		ch := bdCreate(t, bd, dir9, "Output child", "--type", "task")
		bdDep(t, bd, dir9, "add", ch.ID, e.ID, "--type", "parent-child")
		bdClose(t, bd, dir9, ch.ID)

		out := bdEpic(t, bd, dir9, "close-eligible", "--reason", "ship it")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		lastLine := lines[len(lines)-1]
		if !strings.Contains(lastLine, "ship it") {
			t.Errorf("final output line should contain the reason, got: %q", lastLine)
		}
		if !strings.Contains(lastLine, "Closed") {
			t.Errorf("final output line should contain Closed summary, got: %q", lastLine)
		}
		if !strings.Contains(out, e.ID) {
			t.Errorf("output should list closed epic ID %s, got: %s", e.ID, out)
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
