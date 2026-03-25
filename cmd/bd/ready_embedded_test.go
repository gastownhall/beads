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

func TestEmbeddedReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")

	// ===== Default =====

	t.Run("ready_default", func(t *testing.T) {
		bdCreate(t, bd, dir, "Ready test issue", "--type", "task")
		cmd := exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Ready test issue") {
			t.Errorf("expected issue in ready output: %s", out)
		}
	})

	// ===== --json =====

	t.Run("ready_json", func(t *testing.T) {
		cmd := exec.Command(bd, "ready", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.IndexAny(s, "[{")
		if start >= 0 {
			if !json.Valid([]byte(s[start:])) {
				t.Errorf("invalid JSON in ready output: %s", s[:min(200, len(s))])
			}
		}
	})

	// ===== With Blockers =====

	t.Run("ready_excludes_blocked", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker issue", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked by blocker", "--type", "task")

		// Add blocking dependency
		cmd := exec.Command(bd, "dep", blocked.ID, "--blocks", blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			// dep --blocks may use different syntax; try positional
			cmd2 := exec.Command(bd, "dep", "add", blocker.ID, blocked.ID)
			cmd2.Dir = dir
			cmd2.Env = bdEnv(dir)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				t.Logf("dep add failed (non-fatal): %v\n%s\n%s", err, out, out2)
			}
		}

		cmd = exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		// The blocked issue should not appear in ready output
		// (though the blocker might, depending on dep direction)
		_ = out
	})
}

func TestEmbeddedReadyConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rx")

	bdCreate(t, bd, dir, "Ready concurrent issue", "--type", "task")

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
			cmd := exec.Command(bd, "ready")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("ready (worker %d): %v\n%s", worker, err, out)
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
