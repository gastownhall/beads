//go:build embeddeddolt

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// bdBootstrap runs "bd bootstrap" with the given args and returns stdout.
func bdBootstrap(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd bootstrap %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedBootstrap(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Already Exists =====

	t.Run("bootstrap_existing_db", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "tb")
		out := bdBootstrap(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "already exists") && !strings.Contains(out, "Nothing to do") {
			t.Errorf("expected 'already exists' for initialized db: %s", out)
		}
	})

	// ===== Dry Run (fresh .beads with no db) =====

	t.Run("bootstrap_dry_run_fresh", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = dir
		cmd.CombinedOutput()
		// Create .beads with metadata.json so FindBeadsDir detects it
		beadsDir := filepath.Join(dir, ".beads")
		os.MkdirAll(beadsDir, 0o750)
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644)

		out := bdBootstrap(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "create fresh") && !strings.Contains(out, "init") {
			t.Errorf("expected fresh init plan: %s", out)
		}
	})

	// ===== Full Bootstrap (init action) =====

	t.Run("bootstrap_init", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = dir
		cmd.CombinedOutput()
		beadsDir := filepath.Join(dir, ".beads")
		os.MkdirAll(beadsDir, 0o750)
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644)

		bcmd := exec.Command(bd, "bootstrap")
		bcmd.Dir = dir
		bcmd.Env = bdEnv(dir)
		bcmd.Stdin = strings.NewReader("y\n")
		out, err := bcmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bootstrap init failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Created fresh database") {
			t.Errorf("expected 'Created fresh database': %s", out)
		}
	})

	// ===== JSONL Import =====

	t.Run("bootstrap_jsonl_import", func(t *testing.T) {
		// First create a db and export
		srcDir, _, _ := bdInit(t, bd, "--prefix", "bs")
		bdCreate(t, bd, srcDir, "Export for bootstrap", "--type", "task")
		cmd := exec.Command(bd, "export", "-o", filepath.Join(srcDir, ".beads", "issues.jsonl"))
		cmd.Dir = srcDir
		cmd.Env = bdEnv(srcDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("export failed: %v\n%s", err, out)
		}

		// Create new dir with .beads + issues.jsonl but no database
		dir := t.TempDir()
		gitCmd := exec.Command("git", "init", "-q")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		gitCmd = exec.Command("git", "config", "user.name", "Test")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		gitCmd = exec.Command("git", "config", "user.email", "test@test.com")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		destBeads := filepath.Join(dir, ".beads")
		os.MkdirAll(destBeads, 0o750)
		os.WriteFile(filepath.Join(destBeads, "metadata.json"), []byte("{}"), 0o644)

		// Copy the JSONL file
		data, _ := os.ReadFile(filepath.Join(srcDir, ".beads", "issues.jsonl"))
		os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), data, 0o644)

		// Bootstrap should detect and import JSONL
		bcmd := exec.Command(bd, "bootstrap")
		bcmd.Dir = dir
		bcmd.Env = bdEnv(dir)
		bcmd.Stdin = strings.NewReader("y\n")
		out, err := bcmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bootstrap jsonl-import failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Imported") {
			t.Errorf("expected 'Imported' in output: %s", out)
		}
	})
}

// TestEmbeddedBootstrapConcurrent exercises bootstrap --dry-run concurrently.
func TestEmbeddedBootstrapConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bx")

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
			cmd := exec.Command(bd, "bootstrap", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("bootstrap --dry-run (worker %d): %v\n%s", worker, err, out)
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
