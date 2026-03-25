//go:build embeddeddolt

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdBootstrap runs "bd bootstrap" with the given args and returns stdout.
func bdBootstrap(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd bootstrap %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedBootstrap(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tb")

	// ===== Bootstrap on existing DB =====

	t.Run("bootstrap_existing_db", func(t *testing.T) {
		out := bdBootstrap(t, bd, dir)
		// Should detect existing database and report status
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty bootstrap output on existing db")
		}
	})

	// ===== Dry Run =====

	t.Run("bootstrap_dry_run", func(t *testing.T) {
		out := bdBootstrap(t, bd, dir, "--dry-run")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty --dry-run output")
		}
	})

	// ===== JSON Output =====

	t.Run("bootstrap_json", func(t *testing.T) {
		out := bdBootstrap(t, bd, dir, "--json")
		// Should produce output without crashing
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty --json output")
		}
	})
}

// TestEmbeddedBootstrapConcurrent exercises bootstrap concurrently.
func TestEmbeddedBootstrapConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bx")

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

			cmd := exec.Command(bd, "bootstrap", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("bootstrap --dry-run (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
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
