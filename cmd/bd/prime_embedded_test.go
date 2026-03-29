package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdPrime runs "bd prime" with the given args and returns stdout.
func bdPrime(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prime"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd prime %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedPrime(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tp")

	// ===== Default Output =====

	t.Run("prime_default", func(t *testing.T) {
		out := bdPrime(t, bd, dir)
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime output")
		}
	})

	// ===== Full Flag =====

	t.Run("prime_full", func(t *testing.T) {
		out := bdPrime(t, bd, dir, "--full")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime --full output")
		}
		// Full mode should include command references
		if !strings.Contains(out, "bd") {
			t.Errorf("expected 'bd' command references in --full output: %s", out[:min(200, len(out))])
		}
	})

	// ===== Export Flag =====

	t.Run("prime_export", func(t *testing.T) {
		out := bdPrime(t, bd, dir, "--export")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty prime --export output")
		}
	})

	// ===== Memories Injected =====

	t.Run("prime_memories_injected", func(t *testing.T) {
		// Store a memory
		cmd := exec.Command(bd, "remember", "always use -race flag in tests", "--key", "prime-test-mem")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd remember failed: %v\n%s", err, out)
		}

		// Prime should include the memory
		primeOut := bdPrime(t, bd, dir, "--full")
		if !strings.Contains(primeOut, "race") {
			t.Errorf("expected memory content in prime output: %s", primeOut[:min(500, len(primeOut))])
		}
	})
}

// TestEmbeddedPrimeConcurrent exercises prime operations concurrently.
func TestEmbeddedPrimeConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "px")

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

			cmd := exec.Command(bd, "prime", "--full")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("prime --full (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
			}
			if len(strings.TrimSpace(string(out))) == 0 {
				r.err = fmt.Errorf("prime --full (worker %d): empty output", worker)
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
