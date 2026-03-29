package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdRenamePrefix runs "bd rename-prefix" with the given args and returns stdout.
func bdRenamePrefix(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"rename-prefix"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd rename-prefix %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdRenamePrefixFail runs "bd rename-prefix" expecting failure.
func bdRenamePrefixFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"rename-prefix"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd rename-prefix %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedRenamePrefix(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Basic Rename =====

	t.Run("rename_basic", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "old")
		bdCreate(t, bd, dir, "Rename test 1", "--type", "task")
		bdCreate(t, bd, dir, "Rename test 2", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "new")
		if !strings.Contains(out, "Successfully renamed") {
			t.Errorf("expected 'Successfully renamed' in output: %s", out)
		}

		// Verify issues have new prefix
		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		listOut, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd list after rename failed: %v\n%s", err, listOut)
		}
		if !strings.Contains(string(listOut), "new-") {
			t.Errorf("expected 'new-' prefix in list output: %s", listOut)
		}
		if strings.Contains(string(listOut), "old-") {
			t.Errorf("unexpected 'old-' prefix still in list output: %s", listOut)
		}
	})

	// ===== Dry Run =====

	t.Run("rename_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dr")
		bdCreate(t, bd, dir, "Dry run rename", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "xx", "--dry-run")
		if !strings.Contains(out, "DRY RUN") {
			t.Errorf("expected 'DRY RUN' in output: %s", out)
		}

		// Verify prefix was NOT changed
		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		listOut, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd list after dry-run rename failed: %v\n%s", err, listOut)
		}
		if strings.Contains(string(listOut), "xx-") {
			t.Errorf("dry-run should not have changed prefix: %s", listOut)
		}
	})

	// ===== Same Prefix Error =====

	t.Run("rename_same_prefix", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sp")
		bdCreate(t, bd, dir, "Same prefix test", "--type", "task")

		bdRenamePrefixFail(t, bd, dir, "sp")
	})

	// ===== Invalid Prefix =====

	t.Run("rename_invalid_uppercase", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "iv")
		bdRenamePrefixFail(t, bd, dir, "UPPER")
	})

	t.Run("rename_invalid_chars", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ic")
		bdRenamePrefixFail(t, bd, dir, "BAD!")
	})

	// ===== Empty DB =====

	t.Run("rename_empty_db", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "em")
		// No issues — should just update config
		out := bdRenamePrefix(t, bd, dir, "nw")
		if !strings.Contains(out, "No issues") {
			t.Errorf("expected 'No issues' message: %s", out)
		}
	})
}

// TestEmbeddedRenamePrefixConcurrent exercises rename-prefix --dry-run concurrently.
func TestEmbeddedRenamePrefixConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rx")

	bdCreate(t, bd, dir, "Rename concurrent issue", "--type", "task")

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
			newPrefix := fmt.Sprintf("r%d", worker)
			cmd := exec.Command(bd, "rename-prefix", newPrefix, "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("rename-prefix --dry-run (worker %d): %v\n%s", worker, err, out)
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
