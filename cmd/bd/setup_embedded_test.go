package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdSetup runs "bd setup" with the given args and returns stdout.
func bdSetup(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"setup"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd setup %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedSetup(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ts")

	t.Run("setup_list", func(t *testing.T) {
		out := bdSetup(t, bd, dir, "--list")
		if !strings.Contains(out, "cursor") && !strings.Contains(out, "claude") {
			t.Errorf("expected recipe names in setup --list: %s", out)
		}
	})

	t.Run("setup_install_and_check", func(t *testing.T) {
		bdSetup(t, bd, dir, "cursor")
		out := bdSetup(t, bd, dir, "cursor", "--check")
		_ = out // Should succeed
	})

	t.Run("setup_remove", func(t *testing.T) {
		bdSetup(t, bd, dir, "cursor", "--remove")
		// After remove, check should indicate not installed or succeed gracefully
		cmd := exec.Command(bd, "setup", "cursor", "--check")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		_, _ = cmd.CombinedOutput() // May succeed or fail — just verify no crash
	})
}

func TestEmbeddedSetupConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sx")

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
			cmd := exec.Command(bd, "setup", "--list")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("setup --list (worker %d): %v\n%s", worker, err, out)
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
