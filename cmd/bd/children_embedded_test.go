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

// bdChildren runs "bd children" with the given args and returns stdout.
func bdChildren(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"children"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd children %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ch")

	t.Run("children_basic", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Parent epic", "--type", "epic")
		child1 := bdCreate(t, bd, dir, "Child task 1", "--type", "task")
		child2 := bdCreate(t, bd, dir, "Child task 2", "--type", "task")
		bdDepAdd(t, bd, dir, child1.ID, parent.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, child2.ID, parent.ID, "--type", "parent-child")

		out := bdChildren(t, bd, dir, parent.ID)
		if !strings.Contains(out, child1.ID) {
			t.Errorf("expected child1 %s in output: %s", child1.ID, out)
		}
		if !strings.Contains(out, child2.ID) {
			t.Errorf("expected child2 %s in output: %s", child2.ID, out)
		}
	})

	t.Run("children_json", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "JSON parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "JSON child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		cmd := exec.Command(bd, "children", parent.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd children --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start >= 0 {
			var issues []map[string]interface{}
			if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
				t.Fatalf("parse children JSON: %v\n%s", err, s)
			}
			if len(issues) != 1 {
				t.Errorf("expected 1 child, got %d", len(issues))
			}
		}
	})

	t.Run("children_empty", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "No children parent", "--type", "task")
		out := bdChildren(t, bd, dir, parent.ID)
		// Should not error, may show empty message
		_ = out
	})

	t.Run("children_includes_all_statuses", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "All status parent", "--type", "epic")
		openChild := bdCreate(t, bd, dir, "Open child", "--type", "task")
		closedChild := bdCreate(t, bd, dir, "Closed child", "--type", "task")
		bdDepAdd(t, bd, dir, openChild.ID, parent.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, closedChild.ID, parent.ID, "--type", "parent-child")
		bdClose(t, bd, dir, closedChild.ID)

		out := bdChildren(t, bd, dir, parent.ID)
		if !strings.Contains(out, openChild.ID) {
			t.Errorf("expected open child in output: %s", out)
		}
		if !strings.Contains(out, closedChild.ID) {
			t.Errorf("expected closed child in output (--all implied): %s", out)
		}
	})

	t.Run("children_nonexistent_parent", func(t *testing.T) {
		cmd := exec.Command(bd, "children", "ch-nonexistent999")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected children of nonexistent to fail, got: %s", out)
		}
	})
}

// TestEmbeddedChildrenConcurrent exercises children listing concurrently.
func TestEmbeddedChildrenConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cx")

	// Create parents with children
	var parentIDs []string
	for i := 0; i < 4; i++ {
		parent := bdCreate(t, bd, dir, fmt.Sprintf("Concurrent parent %d", i), "--type", "epic")
		for j := 0; j < 2; j++ {
			child := bdCreate(t, bd, dir, fmt.Sprintf("Child %d-%d", i, j), "--type", "task")
			bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")
		}
		parentIDs = append(parentIDs, parent.ID)
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

			parentID := parentIDs[worker%len(parentIDs)]
			cmd := exec.Command(bd, "children", parentID, "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("children %s: %v\n%s", parentID, err, out)
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
