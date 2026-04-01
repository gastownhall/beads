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

// bdDolt runs "bd dolt" with the given args and returns stdout.
func bdDolt(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dolt"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dolt %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDoltFail runs "bd dolt" expecting failure and returns stderr+stdout.
func bdDoltFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dolt"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dolt %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedDolt(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")

	// ===== Server-only commands must fail in embedded mode =====

	serverOnlyCmds := []struct {
		name string
		args []string
	}{
		{"start", []string{"start"}},
		{"stop", []string{"stop"}},
		{"status", []string{"status"}},
		{"test", []string{"test"}},
		{"show", []string{"show"}},
		{"set", []string{"set", "host", "127.0.0.1"}},
		{"killall", []string{"killall"}},
		{"clean-databases", []string{"clean-databases"}},
	}

	for _, tc := range serverOnlyCmds {
		t.Run("blocked_"+tc.name, func(t *testing.T) {
			out := bdDoltFail(t, bd, dir, tc.args...)
			if !strings.Contains(out, "not supported in embedded mode") {
				t.Errorf("expected 'not supported in embedded mode' in output for dolt %s: %s", tc.name, out)
			}
		})
	}

	// ===== Working commands =====

	// Create an issue so there are pending changes to commit
	bdCreate(t, bd, dir, "Dolt commit test issue", "--type", "task")

	t.Run("commit", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "commit")
		// Should succeed — either commits or reports nothing to commit
		_ = out
	})

	t.Run("commit_with_message", func(t *testing.T) {
		bdCreate(t, bd, dir, "Another issue for commit", "--type", "task")
		out := bdDolt(t, bd, dir, "commit", "-m", "test commit message")
		_ = out
	})

	// ===== Remote management =====

	t.Run("remote_list_empty", func(t *testing.T) {
		out := bdDolt(t, bd, dir, "remote", "list")
		_ = out // Should succeed even with no remotes
	})

	t.Run("remote_add_and_list", func(t *testing.T) {
		remoteDir := t.TempDir()
		bdDolt(t, bd, dir, "remote", "add", "test-remote", "file://"+remoteDir)
		out := bdDolt(t, bd, dir, "remote", "list")
		if !strings.Contains(out, "test-remote") {
			t.Errorf("expected 'test-remote' in remote list: %s", out)
		}
	})

	t.Run("remote_remove", func(t *testing.T) {
		bdDolt(t, bd, dir, "remote", "remove", "test-remote")
		out := bdDolt(t, bd, dir, "remote", "list")
		if strings.Contains(out, "test-remote") {
			t.Errorf("expected 'test-remote' to be removed: %s", out)
		}
	})

	// ===== Push/Pull with file remote =====

	t.Run("push_to_file_remote", func(t *testing.T) {
		ppDir, _, _ := bdInit(t, bd, "--prefix", "pp")

		remoteDir := t.TempDir()
		bdDolt(t, bd, ppDir, "remote", "add", "origin", "file://"+remoteDir)

		// Create an issue and commit
		bdCreate(t, bd, ppDir, "Push roundtrip issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "roundtrip commit")

		// Push to file remote
		bdDolt(t, bd, ppDir, "push")
	})

	t.Run("push_force", func(t *testing.T) {
		pfDir, _, _ := bdInit(t, bd, "--prefix", "pf")
		remoteDir := t.TempDir()
		bdDolt(t, bd, pfDir, "remote", "add", "origin", "file://"+remoteDir)

		bdCreate(t, bd, pfDir, "Force push issue", "--type", "task")
		bdDolt(t, bd, pfDir, "commit", "-m", "force push commit")
		bdDolt(t, bd, pfDir, "push", "--force")
	})

	t.Run("push_no_remote", func(t *testing.T) {
		// Push without a remote should fail gracefully, not panic
		cmd := exec.Command(bd, "dolt", "push")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Log("push without remote succeeded unexpectedly")
		}
		_ = out
	})

	t.Run("pull_no_remote", func(t *testing.T) {
		cmd := exec.Command(bd, "dolt", "pull")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Log("pull without remote succeeded unexpectedly")
		}
		_ = out
	})

	// ===== Push/Pull between two repos via shared file remote =====

	t.Run("push_then_push_more", func(t *testing.T) {
		// Push twice to verify incremental pushes work
		ppDir, _, _ := bdInit(t, bd, "--prefix", "pm")
		remoteDir := t.TempDir()
		bdDolt(t, bd, ppDir, "remote", "add", "origin", "file://"+remoteDir)

		// First push
		bdCreate(t, bd, ppDir, "First push issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "first commit")
		bdDolt(t, bd, ppDir, "push")

		// Second push
		bdCreate(t, bd, ppDir, "Second push issue", "--type", "task")
		bdDolt(t, bd, ppDir, "commit", "-m", "second commit")
		bdDolt(t, bd, ppDir, "push")
	})
}

// TestEmbeddedDoltConcurrent exercises dolt operations concurrently.
func TestEmbeddedDoltConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dx")

	// Pre-create issues so commits have something to work with
	for i := 0; i < 4; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("dolt-concurrent-%d", i), "--type", "task")
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

			var args []string
			expectFail := false
			switch worker % 3 {
			case 0:
				args = []string{"dolt", "commit"}
			case 1:
				// Server-only command should fail fast
				args = []string{"dolt", "status"}
				expectFail = true
			case 2:
				args = []string{"dolt", "start"}
				expectFail = true
			}

			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()

			if expectFail {
				if err == nil {
					r.err = fmt.Errorf("expected dolt %s to fail in embedded mode", args[1])
				} else if !strings.Contains(string(out), "not supported in embedded mode") {
					r.err = fmt.Errorf("unexpected error from dolt %s: %s", args[1], out)
				}
			} else {
				if err != nil {
					r.err = fmt.Errorf("dolt %s (worker %d): %v\n%s", strings.Join(args[1:], " "), worker, err, out)
				}
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
