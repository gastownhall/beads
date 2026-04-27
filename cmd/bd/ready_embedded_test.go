//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	bdCreate(t, bd, dir, "Ready test issue", "--type", "task")

	// ===== Default =====

	t.Run("ready_default", func(t *testing.T) {
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
		if start < 0 {
			t.Fatalf("no JSON in ready --json output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("invalid JSON in ready output: %s", s[:min(200, len(s))])
		}
	})

	// ===== With Blockers =====

	t.Run("ready_excludes_blocked", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker issue", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked by blocker", "--type", "task")

		// Add blocking dependency: blocked depends on blocker
		cmd := exec.Command(bd, "dep", "add", blocked.ID, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dep add failed: %v\n%s", err, out)
		}

		cmd = exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		// The blocked issue should not appear in ready output
		if strings.Contains(string(out), "Blocked by blocker") {
			t.Errorf("blocked issue should not appear in ready output: %s", out)
		}
	})

	// ===== Exclude Label =====

	t.Run("ready_exclude_label", func(t *testing.T) {
		bdCreate(t, bd, dir, "Triage pending item", "--type", "task", "--label", "triage:pending")
		bdCreate(t, bd, dir, "Normal ready item", "--type", "task")

		cmd := exec.Command(bd, "ready", "--exclude-label", "triage:pending")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready --exclude-label failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "Triage pending item") {
			t.Errorf("triage:pending issue should not appear with --exclude-label: %s", out)
		}
		if !strings.Contains(string(out), "Normal ready item") {
			t.Errorf("normal issue should still appear with --exclude-label: %s", out)
		}
	})

	// ===== -C flag =====

	t.Run("ready_with_C_flag", func(t *testing.T) {
		// Run bd ready from a different directory using -C to point at the beads project
		tmpDir := t.TempDir()
		cmd := exec.Command(bd, "-C", dir, "ready")
		cmd.Dir = tmpDir // Run from a directory with no .beads/
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd -C %s ready failed: %v\n%s", dir, err, out)
		}
		if !strings.Contains(string(out), "Ready test issue") {
			t.Errorf("expected issue in ready -C output: %s", out)
		}
	})

	t.Run("ready_with_C_flag_invalid_path", func(t *testing.T) {
		tmpDir := t.TempDir()
		cmd := exec.Command(bd, "-C", filepath.Join(tmpDir, "missing"), "ready")
		cmd.Dir = tmpDir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("bd -C missing ready succeeded unexpectedly:\n%s", out)
		}
		if !strings.Contains(string(out), "cannot use -C directory") {
			t.Errorf("expected invalid -C path error, got: %s", out)
		}
	})

	t.Run("ready_with_C_flag_does_not_leak_cwd", func(t *testing.T) {
		// Verify that -C does not permanently mutate the process cwd.
		// Two sequential invocations from the same tmpDir: the first uses -C to
		// reach the project; the second omits -C and must fail (no .beads/ in tmpDir),
		// proving BEADS_DIR was not leaked into the test process environment.
		tmpDir := t.TempDir()
		env := bdEnv(dir) // strips all BEADS_* vars

		cmd1 := exec.Command(bd, "-C", dir, "ready")
		cmd1.Dir = tmpDir
		cmd1.Env = env
		if out, err := cmd1.CombinedOutput(); err != nil {
			t.Fatalf("first bd -C ready failed: %v\n%s", err, out)
		}

		cmd2 := exec.Command(bd, "ready")
		cmd2.Dir = tmpDir
		cmd2.Env = env // same env — BEADS_DIR must not have leaked
		out2, err2 := cmd2.CombinedOutput()
		if err2 == nil {
			t.Fatalf("second bd ready (no -C) should have failed in tmpDir, got: %s", out2)
		}
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
