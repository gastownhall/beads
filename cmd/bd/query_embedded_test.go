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

// bdQuery runs "bd query" with the given args and returns stdout.
func bdQuery(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"query"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd query %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdQueryJSON runs "bd query --json" and returns parsed results.
func bdQueryJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"query", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd query --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &results); err != nil {
		t.Fatalf("parse query JSON: %v\n%s", err, s)
	}
	return results
}

// bdQueryFail runs "bd query" expecting failure.
func bdQueryFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"query"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd query %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedQuery(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tq")

	// Create test fixtures
	taskHigh := bdCreate(t, bd, dir, "High priority task", "--type", "task", "--priority", "1", "--assignee", "alice")
	bugMed := bdCreate(t, bd, dir, "Medium bug", "--type", "bug", "--priority", "2", "--assignee", "bob")
	featureLow := bdCreate(t, bd, dir, "Low feature", "--type", "feature", "--priority", "3")
	closedTask := bdCreate(t, bd, dir, "Closed query task", "--type", "task")
	bdClose(t, bd, dir, closedTask.ID)

	// ===== Basic Query Expressions =====

	t.Run("query_status_equals", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "status=open")
		for _, r := range results {
			if r["status"] == "closed" {
				t.Error("should not find closed issues with status=open")
			}
		}
		if len(results) < 3 {
			t.Errorf("expected at least 3 open issues, got %d", len(results))
		}
	})

	t.Run("query_type_equals", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "type=bug")
		if len(results) != 1 {
			t.Fatalf("expected 1 bug, got %d", len(results))
		}
		if results[0]["id"] != bugMed.ID {
			t.Errorf("expected %s, got %v", bugMed.ID, results[0]["id"])
		}
	})

	t.Run("query_assignee_equals", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "assignee=alice")
		found := false
		for _, r := range results {
			if r["id"] == taskHigh.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected alice's task in results")
		}
	})

	t.Run("query_priority_greater_than", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "priority>1")
		for _, r := range results {
			pri := int(r["priority"].(float64))
			if pri <= 1 {
				t.Errorf("expected priority > 1, got %d for %s", pri, r["id"])
			}
		}
	})

	t.Run("query_priority_less_equal", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "priority<=2")
		for _, r := range results {
			pri := int(r["priority"].(float64))
			if pri > 2 {
				t.Errorf("expected priority <= 2, got %d for %s", pri, r["id"])
			}
		}
	})

	// ===== Boolean Operators =====

	t.Run("query_and", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "type=task AND priority=1")
		if len(results) != 1 || results[0]["id"] != taskHigh.ID {
			t.Errorf("expected only %s with type=task AND priority=1, got %d results", taskHigh.ID, len(results))
		}
	})

	t.Run("query_or", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "type=bug OR type=feature")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		if !ids[bugMed.ID] || !ids[featureLow.ID] {
			t.Errorf("expected both bug and feature with OR: %v", ids)
		}
	})

	t.Run("query_not", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "NOT type=task")
		for _, r := range results {
			if r["issue_type"] == "task" {
				t.Errorf("should not find tasks with NOT type=task")
			}
		}
	})

	t.Run("query_inequality", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "type!=task")
		for _, r := range results {
			if r["issue_type"] == "task" {
				t.Errorf("should not find tasks with type!=task")
			}
		}
	})

	// ===== Flags =====

	t.Run("query_all_includes_closed", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "--all", "type=task")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		if !ids[closedTask.ID] {
			t.Error("expected closed task with --all flag")
		}
	})

	t.Run("query_limit", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "--limit", "1", "status=open")
		if len(results) > 1 {
			t.Errorf("expected at most 1 result with --limit 1, got %d", len(results))
		}
	})

	t.Run("query_limit_zero", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "--limit", "0", "--all", "priority>=0")
		if len(results) < 4 {
			t.Errorf("expected all issues with --limit 0, got %d", len(results))
		}
	})

	t.Run("query_sort_priority", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "--sort", "priority", "status=open")
		if len(results) >= 2 {
			first := int(results[0]["priority"].(float64))
			last := int(results[len(results)-1]["priority"].(float64))
			if first > last {
				t.Errorf("expected ascending priority, got first=%d last=%d", first, last)
			}
		}
	})

	t.Run("query_sort_reverse", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "--sort", "priority", "--reverse", "status=open")
		if len(results) >= 2 {
			first := int(results[0]["priority"].(float64))
			last := int(results[len(results)-1]["priority"].(float64))
			if first < last {
				t.Errorf("expected descending priority with --reverse, got first=%d last=%d", first, last)
			}
		}
	})

	t.Run("query_long", func(t *testing.T) {
		out := bdQuery(t, bd, dir, "--long", "status=open")
		if len(out) == 0 {
			t.Error("expected non-empty long output")
		}
	})

	t.Run("query_parse_only", func(t *testing.T) {
		out := bdQuery(t, bd, dir, "--parse-only", "type=bug AND priority>1")
		if !strings.Contains(out, "Parsed query") {
			t.Errorf("expected 'Parsed query' in --parse-only output: %s", out)
		}
	})

	// ===== Error Cases =====

	t.Run("query_no_expression", func(t *testing.T) {
		bdQueryFail(t, bd, dir)
	})

	t.Run("query_invalid_expression", func(t *testing.T) {
		bdQueryFail(t, bd, dir, "===invalid===")
	})

	t.Run("query_empty_results", func(t *testing.T) {
		results := bdQueryJSON(t, bd, dir, "assignee=nonexistentperson12345")
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	_ = taskHigh
	_ = bugMed
	_ = featureLow
}

// TestEmbeddedQueryConcurrent exercises query operations concurrently.
func TestEmbeddedQueryConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "qx")

	// Pre-create issues
	for i := 0; i < 15; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent-query-%d", i), "--type", "task", "--priority", fmt.Sprintf("%d", i%4+1))
	}

	type workerResult struct {
		worker int
		err    error
	}

	const numWorkers = 8
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			queries := [][]string{
				{"--json", "status=open"},
				{"--json", "type=task"},
				{"--json", "--sort", "priority", "priority>=1"},
				{"--json", "--limit", "3", "status=open"},
				{"--json", "--reverse", "--sort", "priority", "status=open"},
			}
			q := queries[worker%len(queries)]

			cmd := exec.Command(bd, append([]string{"query"}, q...)...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("query %v: %v\n%s", q, err, out)
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
