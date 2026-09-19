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

// bdRenamePrefix runs "bd rename-prefix" with the given args and returns stdout.
func bdRenamePrefix(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"rename-prefix"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd rename-prefix %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "new-") {
			t.Errorf("expected 'new-' prefix in list output: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "old-") {
			t.Errorf("unexpected 'old-' prefix still in list output: %s", stdout.String())
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after dry-run rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "xx-") {
			t.Errorf("dry-run should not have changed prefix: %s", stdout.String())
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

	// ===== Prefix Shortening (PR #5135 review blocker B1) =====
	// Shortening a multi-part prefix to one of its own leading segments
	// (e.g. "beads-vscode-" -> "beads-") must still rewrite every row —
	// "beads-vscode-1" starting with "beads-" is not the same thing as it
	// already being on the target prefix.

	t.Run("rename_prefix_shortening", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "beads-vscode")
		bdCreate(t, bd, dir, "Shortening test 1", "--type", "task")
		bdCreate(t, bd, dir, "Shortening test 2", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "beads")
		if !strings.Contains(out, "Successfully renamed") {
			t.Errorf("expected 'Successfully renamed' in output: %s", out)
		}

		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// bd create mints hash IDs (beads-cyc), never sequential ones, so assert the
		// rewritten prefix rather than a number — the beads-vscode- check below is what
		// carries the real weight. Mirrors rename_basic, which asserts "new-" for the
		// same reason.
		if !strings.Contains(stdout.String(), "beads-") {
			t.Errorf("expected shortened 'beads-' prefix in list output: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "beads-vscode-") {
			t.Errorf("regression: 'beads-vscode-' IDs were left unrewritten: %s", stdout.String())
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
