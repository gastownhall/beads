//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedContext(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tx")

	t.Run("context_default", func(t *testing.T) {
		cmd := exec.Command(bd, "context")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "embedded") && !strings.Contains(stdout.String(), ".beads") {
			t.Errorf("expected embedded mode or .beads in context output: %s", stdout.String())
		}
	})

	t.Run("context_json", func(t *testing.T) {
		cmd := exec.Command(bd, "context", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if len(strings.TrimSpace(stdout.String())) == 0 {
			t.Error("expected non-empty context --json output")
		}
	})

	// bd context must resolve from a discoverable .beads directory even when
	// the scope is not inside a git repository — git is a means of finding the
	// repo root, not a hard requirement for this diagnostic (GH#4772).
	t.Run("context_json_without_git_repo", func(t *testing.T) {
		ngDir, _, _ := bdInit(t, bd, "--prefix", "ng")
		if err := os.RemoveAll(filepath.Join(ngDir, ".git")); err != nil {
			t.Fatalf("remove .git: %v", err)
		}

		cmd := exec.Command(bd, "context", "--json")
		cmd.Dir = ngDir
		cmd.Env = bdEnv(ngDir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd context --json failed outside git repo: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "cannot determine repository root") || strings.Contains(out, `"error"`) {
			t.Errorf("expected context to resolve without a git repo, got error output:\n%s", out)
		}
		if !strings.Contains(out, `"beads_dir"`) {
			t.Errorf("expected beads_dir in context output:\n%s", out)
		}
	})
}

func TestEmbeddedContextConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xx")

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
			cmd := exec.Command(bd, "context")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("context (worker %d): %v\n%s", worker, err, out)
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
