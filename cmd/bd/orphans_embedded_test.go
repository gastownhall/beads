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

// bdOrphans runs "bd orphans" with the given args and returns stdout.
func bdOrphans(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"orphans"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd orphans %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedOrphans(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "or")

	// Create some issues so the db isn't empty
	bdCreate(t, bd, dir, "Orphans test issue 1", "--type", "task")
	bdCreate(t, bd, dir, "Orphans test issue 2", "--type", "task")

	// ===== Default =====

	t.Run("orphans_default", func(t *testing.T) {
		out := bdOrphans(t, bd, dir)
		// On a clean db, should report no orphans or empty output
		_ = out
	})

	// ===== --json =====

	t.Run("orphans_json", func(t *testing.T) {
		out := bdOrphans(t, bd, dir, "--json")
		s := strings.TrimSpace(out)
		// orphans --json may return "null", "[]", or a JSON array/object
		if !json.Valid([]byte(s)) {
			t.Errorf("invalid JSON in orphans --json output: %s", s[:min(200, len(s))])
		}
	})

	// ===== --details =====

	t.Run("orphans_details", func(t *testing.T) {
		out := bdOrphans(t, bd, dir, "--details")
		_ = out // Should succeed without crashing
	})
}

// TestEmbeddedOrphansConcurrent exercises orphans concurrently.
func TestEmbeddedOrphansConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ox")

	bdCreate(t, bd, dir, "Orphans concurrent issue", "--type", "task")

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
			cmd := exec.Command(bd, "orphans")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("orphans (worker %d): %v\n%s", worker, err, out)
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
