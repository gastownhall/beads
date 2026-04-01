//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdHuman runs "bd human" with the given args and returns stdout.
func bdHuman(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"human"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd human %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedHuman(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "th")

	// ===== Default Help Output =====

	t.Run("human_default", func(t *testing.T) {
		out := bdHuman(t, bd, dir)
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty human output")
		}
	})

	// ===== List =====

	t.Run("human_list_empty", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "list")
		// No human-labeled issues yet — should succeed without error
		_ = out
	})

	// ===== Stats =====

	t.Run("human_stats", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "stats")
		// Should succeed and produce output
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty stats output")
		}
	})
}

// TestEmbeddedHumanConcurrent exercises human operations concurrently.
func TestEmbeddedHumanConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hx")

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

			var args []string
			switch worker % 2 {
			case 0:
				args = []string{"human", "list"}
			case 1:
				args = []string{"human", "stats"}
			}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("human (worker %d): %v\n%s", worker, err, out)
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
