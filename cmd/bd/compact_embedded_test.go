package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdCompact runs "bd compact" with the given args and returns stdout.
func bdCompact(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"compact"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd compact %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdCompactFail runs "bd compact" expecting failure.
func bdCompactFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"compact"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd compact %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedCompact(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Dry Run =====

	t.Run("compact_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cd")
		bdCreate(t, bd, dir, "Compact dry-run issue", "--type", "task")

		out := bdCompact(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "DRY RUN") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") {
			t.Errorf("expected dry-run or nothing-to-compact output: %s", out)
		}
	})

	// ===== Nothing to Compact (1 commit) =====

	t.Run("compact_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cn")
		out := bdCompact(t, bd, dir, "--force")
		if !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") && !strings.Contains(out, "Only") {
			t.Errorf("expected nothing-to-compact message: %s", out)
		}
	})

	// ===== No --force Errors =====

	t.Run("compact_no_force", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cf")
		// Create issues to generate commits
		bdCreate(t, bd, dir, "Compact no-force 1", "--type", "task")
		bdCreate(t, bd, dir, "Compact no-force 2", "--type", "task")

		// With --days 0, all commits are "old"
		// May fail with "use --force" hint or succeed with "nothing to compact"
		cmd := exec.Command(bd, "compact", "--days", "0")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			// Succeeded — either nothing to compact or only 1 old commit
			_ = out
		} else {
			// Should contain --force hint
			if !strings.Contains(string(out), "--force") {
				t.Errorf("expected --force hint in error: %s", out)
			}
		}
	})

	// ===== Force with --days 0 =====

	t.Run("compact_force", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cx")
		// Create issues + config changes to build commit history
		bdCreate(t, bd, dir, "Compact force 1", "--type", "task")
		cmd := exec.Command(bd, "config", "set", "compact.test1", "v1")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput()
		cmd = exec.Command(bd, "dolt", "commit", "-m", "config commit 1")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput()
		bdCreate(t, bd, dir, "Compact force 2", "--type", "task")

		// Try compacting with --days 0
		out := bdCompact(t, bd, dir, "--force", "--days", "0")
		// Either compacts or reports nothing to compact — both OK
		if !strings.Contains(out, "Compacted") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") && !strings.Contains(out, "Only") {
			t.Errorf("expected compact result: %s", out)
		}
	})

	// ===== --days Flag =====

	t.Run("compact_days_flag", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dy")
		bdCreate(t, bd, dir, "Compact days issue", "--type", "task")

		out := bdCompact(t, bd, dir, "--dry-run", "--days", "7")
		if !strings.Contains(out, "7 days") && !strings.Contains(out, "Nothing to compact") && !strings.Contains(out, "nothing to compact") {
			t.Errorf("expected days reference in output: %s", out)
		}
	})

	// ===== JSON Output =====

	t.Run("compact_dry_run_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cj")
		bdCreate(t, bd, dir, "Compact JSON issue", "--type", "task")

		cmd := exec.Command(bd, "--json", "compact", "--dry-run")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd --json compact --dry-run failed: %v\n%s", err, out)
		}
		// Should produce some output without crashing
		_ = out
	})
}

// TestEmbeddedCompactConcurrent exercises compact --dry-run concurrently.
func TestEmbeddedCompactConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cc")

	bdCreate(t, bd, dir, "Compact concurrent issue", "--type", "task")

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
			cmd := exec.Command(bd, "compact", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("compact --dry-run (worker %d): %v\n%s", worker, err, out)
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
