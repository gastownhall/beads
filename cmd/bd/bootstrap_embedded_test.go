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

// bdBootstrap runs "bd bootstrap" with the given args and returns stdout.
func bdBootstrap(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd bootstrap %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdBootstrapAllowError(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBootstrapNoWorkspace(t *testing.T) {
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()

	t.Run("default_output", func(t *testing.T) {
		out, err := bdBootstrapAllowError(t, bd, dir)
		if err == nil {
			t.Fatal("expected bd bootstrap to exit non-zero without a workspace")
		}
		if !strings.Contains(out, activeWorkspaceNotFoundMessage()) {
			t.Fatalf("expected no-workspace message, got: %s", out)
		}
		if !strings.Contains(out, "bd where") {
			t.Fatalf("expected bootstrap hint to mention bd where, got: %s", out)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		cmd := exec.Command(bd, "bootstrap", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatal("expected bd bootstrap --json to exit non-zero without a workspace")
		}

		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("expected JSON object in output:\nstdout: %s\nstderr: %s", s, stderr.String())
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &payload); err != nil {
			t.Fatalf("parse bootstrap JSON: %v\nstdout: %s", err, s)
		}
		if action, _ := payload["action"].(string); action != "none" {
			t.Fatalf("action = %q, want %q", action, "none")
		}
		if reason, _ := payload["reason"].(string); reason != activeWorkspaceNotFoundError() {
			t.Fatalf("reason = %q, want %q", reason, activeWorkspaceNotFoundError())
		}
		if suggestion, _ := payload["suggestion"].(string); !strings.Contains(suggestion, "bd where") {
			t.Fatalf("suggestion should mention bd where, got: %q", suggestion)
		}
	})
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
		stdout, stderr, err := runCommandBuffers(t, bcmd)
		if err != nil {
			t.Fatalf("bootstrap init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "Created fresh database") {
			t.Errorf("expected 'Created fresh database':\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
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
		stdout, stderr, err := runCommandBuffers(t, bcmd)
		if err != nil {
			t.Fatalf("bootstrap jsonl-import failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "Imported") {
			t.Errorf("expected 'Imported' in output:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	})

	t.Run("bootstrap_from_git_origin_wires_remote", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "origin.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
		remoteURL := "file://" + bareDir

		sourceDir := t.TempDir()
		initGitRepoAt(t, sourceDir)
		runGitForBootstrapTest(t, sourceDir, "branch", "-M", "main")
		runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", remoteURL)
		runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
		runGitForBootstrapTest(t, sourceDir, "push", "-u", "origin", "main")
		runBDInit(t, bd, sourceDir, "--prefix", "beads", "--skip-hooks", "--skip-agents")
		bdCreate(t, bd, sourceDir, "Seed remote data", "--type", "task")
		bdDolt(t, bd, sourceDir, "push")

		cloneDir := t.TempDir()
		runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
		runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", remoteURL)

		out := bdBootstrap(t, bd, cloneDir, "--yes")
		if !strings.Contains(out, "clone from remote") {
			t.Fatalf("expected bootstrap sync plan, got:\n%s", out)
		}

		remotes := bdDolt(t, bd, cloneDir, "remote", "list")
		if !strings.Contains(remotes, "origin") || !strings.Contains(remotes, remoteURL) {
			t.Fatalf("bootstrap should leave origin configured as a Dolt remote %q; remote list:\n%s", remoteURL, remotes)
		}
		configYAML, err := os.ReadFile(filepath.Join(cloneDir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configYAML), remoteURL) {
			t.Fatalf("bootstrap should persist sync.remote; config.yaml:\n%s", configYAML)
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
