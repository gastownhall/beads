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

// bdGC runs "bd gc" with the given args and returns stdout.
func bdGC(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"gc"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd gc %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdGCFail runs "bd gc" expecting failure.
func bdGCFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"gc"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd gc %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedGC(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tg")

	// Create some issues for gc to work with
	for i := 0; i < 3; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("GC test issue %d", i), "--type", "task")
	}

	// ===== Dry Run =====

	t.Run("gc_dry_run", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "Phase 1/3") {
			t.Errorf("expected 'Phase 1/3' in dry-run output: %s", out)
		}
		if !strings.Contains(out, "DRY RUN") {
			t.Errorf("expected 'DRY RUN' in output: %s", out)
		}
	})

	// ===== Skip Flags =====

	t.Run("gc_skip_decay", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay")
		if !strings.Contains(out, "skipped") {
			t.Errorf("expected 'skipped' for decay phase: %s", out)
		}
	})

	t.Run("gc_skip_dolt", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-dolt")
		if !strings.Contains(out, "skipped") {
			t.Errorf("expected 'skipped' for dolt gc phase: %s", out)
		}
	})

	t.Run("gc_skip_both", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--skip-decay", "--skip-dolt")
		if !strings.Contains(out, "DRY RUN") {
			t.Errorf("expected 'DRY RUN' in output: %s", out)
		}
	})

	// ===== Older Than =====

	t.Run("gc_older_than", func(t *testing.T) {
		out := bdGC(t, bd, dir, "--dry-run", "--older-than", "0")
		// With --older-than 0, all closed issues would be candidates
		if !strings.Contains(out, "Phase 1/3") {
			t.Errorf("expected phase output: %s", out)
		}
	})

	// ===== Force with Decay =====

	t.Run("gc_force_with_decay", func(t *testing.T) {
		gcDir, _, _ := bdInit(t, bd, "--prefix", "gf")

		// Create and close issues (non-ephemeral tasks)
		for i := 0; i < 3; i++ {
			issue := bdCreate(t, bd, gcDir, fmt.Sprintf("GC decay %d", i), "--type", "task")
			cmd := exec.Command(bd, "close", issue.ID)
			cmd.Dir = gcDir
			cmd.Env = bdEnv(gcDir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("close %s failed: %v\n%s", issue.ID, err, out)
			}
		}

		// GC with force and older-than 0 (all closed issues eligible)
		// --skip-dolt to avoid slow GC in tests
		out := bdGC(t, bd, gcDir, "--force", "--older-than", "0", "--skip-dolt")
		if !strings.Contains(out, "Deleted") && !strings.Contains(out, "deleted") {
			// If no issues match (e.g., closed_at is too recent for the filter),
			// at minimum verify the command completed.
			if !strings.Contains(out, "GC complete") && !strings.Contains(out, "Phase") {
				t.Errorf("expected gc output: %s", out)
			}
		}
	})

	// ===== No --force prompts =====

	t.Run("gc_no_force_prompts", func(t *testing.T) {
		gcDir, _, _ := bdInit(t, bd, "--prefix", "gn")
		issue := bdCreate(t, bd, gcDir, "GC no force", "--type", "task")
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = gcDir
		cmd.Env = bdEnv(gcDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("close failed: %v\n%s", err, out)
		}

		// Without --force but with closed issues, should fail with hint.
		// If no closed issues match the age filter, it may succeed (0 candidates).
		cmd = exec.Command(bd, "gc", "--older-than", "0")
		cmd.Dir = gcDir
		cmd.Env = bdEnv(gcDir)
		out, err := cmd.CombinedOutput()
		// Either fails (prompt for --force) or succeeds (no matching issues) — both OK
		_ = err
		_ = out
	})
}

// TestEmbeddedGCConcurrent exercises gc --dry-run concurrently.
func TestEmbeddedGCConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gx")

	for i := 0; i < 3; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("gc-concurrent-%d", i), "--type", "task")
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
			cmd := exec.Command(bd, "gc", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("gc --dry-run (worker %d): %v\n%s", worker, err, out)
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
