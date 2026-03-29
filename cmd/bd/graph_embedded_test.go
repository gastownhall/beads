package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdGraph runs "bd graph" with the given args and returns raw stdout.
func bdGraph(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"graph"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd graph %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedGraph(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gr")

	// Create an epic with dependencies for graph testing.
	epic := bdCreate(t, bd, dir, "Graph epic", "--type", "epic")
	taskA := bdCreate(t, bd, dir, "Graph task A", "--type", "task")
	taskB := bdCreate(t, bd, dir, "Graph task B", "--type", "task")
	taskC := bdCreate(t, bd, dir, "Graph task C", "--type", "task")
	bdDep(t, bd, dir, "add", taskA.ID, epic.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", taskB.ID, epic.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", taskC.ID, taskA.ID) // C depends on A

	standalone := bdCreate(t, bd, dir, "Standalone issue", "--type", "task")

	// ===== Single issue graph =====

	t.Run("single_issue", func(t *testing.T) {
		out := bdGraph(t, bd, dir, epic.ID)
		if !strings.Contains(out, epic.ID) {
			t.Errorf("expected epic ID in graph: %s", out)
		}
	})

	// ===== --all =====

	t.Run("all_issues", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--all")
		if !strings.Contains(out, epic.ID) {
			t.Errorf("expected epic in --all graph: %s", out)
		}
	})

	// ===== --compact =====

	t.Run("compact_format", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--compact", epic.ID)
		if len(out) == 0 {
			t.Error("expected non-empty compact output")
		}
	})

	// ===== --box =====

	t.Run("box_format", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--box", epic.ID)
		if len(out) == 0 {
			t.Error("expected non-empty box output")
		}
	})

	// ===== --dot =====

	t.Run("dot_format", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--dot", epic.ID)
		if !strings.Contains(out, "digraph") {
			t.Errorf("expected 'digraph' in DOT output: %s", out)
		}
	})

	// ===== --html =====

	t.Run("html_format", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--html", epic.ID)
		if !strings.Contains(out, "<html") && !strings.Contains(out, "<!DOCTYPE") {
			t.Errorf("expected HTML output: %s", out[:min(200, len(out))])
		}
	})

	// ===== --json =====

	t.Run("json_output", func(t *testing.T) {
		fullArgs := []string{"graph", "--json", epic.ID}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("graph --json failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), epic.ID) {
			t.Errorf("expected epic ID in JSON output: %s", out)
		}
	})

	// ===== Issue with no dependencies =====

	t.Run("no_dependencies", func(t *testing.T) {
		out := bdGraph(t, bd, dir, standalone.ID)
		// Should still produce output for the standalone issue
		if !strings.Contains(out, standalone.ID) {
			t.Errorf("expected standalone ID in graph: %s", out)
		}
	})

	// ===== --all --compact =====

	t.Run("all_compact", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--all", "--compact")
		if len(out) == 0 {
			t.Error("expected non-empty all+compact output")
		}
	})

	// ===== --all --dot =====

	t.Run("all_dot", func(t *testing.T) {
		out := bdGraph(t, bd, dir, "--all", "--dot")
		if !strings.Contains(out, "digraph") {
			t.Errorf("expected 'digraph' in all+dot: %s", out)
		}
	})
}

// TestEmbeddedGraphConcurrent exercises graph operations concurrently.
func TestEmbeddedGraphConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "grc")

	// Create issues with deps.
	var ids []string
	for i := 0; i < 8; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-graph-%d", i), "--type", "task")
		ids = append(ids, issue.ID)
	}
	for i := 0; i < 4; i++ {
		bdDep(t, bd, dir, "add", ids[i*2], ids[i*2+1])
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

			formats := [][]string{
				{ids[worker]},
				{"--compact", ids[worker]},
				{"--dot", ids[worker]},
				{"--box", ids[worker]},
				{"--all"},
				{"--all", "--compact"},
				{"--all", "--dot"},
				{ids[worker]},
			}
			f := formats[worker%len(formats)]

			args := append([]string{"graph"}, f...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d graph: %v\n%s", worker, err, out)
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
