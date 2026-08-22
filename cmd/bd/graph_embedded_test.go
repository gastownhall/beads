//go:build cgo

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd graph %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// runWithReadOnlyStdout gives the child a real stdout handle that cannot
// be written. Unlike a closed pipe, this produces a normal write error on both
// Unix and Windows instead of depending on SIGPIPE behavior.
func runWithReadOnlyStdout(t *testing.T, bd, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	stdoutPath := filepath.Join(t.TempDir(), "stdout.txt")
	if err := os.WriteFile(stdoutPath, nil, 0o600); err != nil {
		t.Fatalf("create read-only stdout target: %v", err)
	}
	stdout, err := os.Open(stdoutPath)
	if err != nil {
		t.Fatalf("open read-only stdout target: %v", err)
	}
	defer func() { _ = stdout.Close() }()

	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stderr.String(), err
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

	t.Run("dot_output_error", func(t *testing.T) {
		stderr, err := runWithReadOnlyStdout(t, bd, dir, bdEnv(dir), "graph", "--dot", epic.ID)
		if err == nil {
			t.Fatalf("graph --dot succeeded with read-only stdout; stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "writing DOT output") {
			t.Fatalf("graph --dot stderr = %q, want writer diagnostic", stderr)
		}
	})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "all_separator_output_error", args: []string{"graph", "--all", "--compact"}},
		{name: "all_open_separator_output_error", args: []string{"graph", "--all", "--open"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := runWithReadOnlyStdout(t, bd, dir, bdEnv(dir), tc.args...)
			if err == nil {
				t.Fatalf("%s succeeded with read-only stdout; stderr:\n%s", strings.Join(tc.args, " "), stderr)
			}
			if !strings.Contains(stderr, "writing graph output") {
				t.Fatalf("%s stderr = %q, want writer diagnostic", strings.Join(tc.args, " "), stderr)
			}
		})
	}

	// ===== --json =====

	t.Run("json_output", func(t *testing.T) {
		fullArgs := []string{"graph", "--json", epic.ID}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("graph --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), epic.ID) {
			t.Errorf("expected epic ID in JSON output: %s", stdout.String())
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
