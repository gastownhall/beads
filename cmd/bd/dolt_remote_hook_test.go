//go:build cgo && !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDoltRemoteAddRemoveDoesNotDeadlockWithBeadsHooks is a regression test
// for GH#3340.
//
// `bd dolt remote add origin <url>` (and `remove`) auto-commit
// .beads/config.yaml via commitBeadsConfig. If that git commit runs the
// beads pre-commit hook, the hook shells out to `bd export`, which tries to
// acquire the embedded Dolt flock that the parent `bd dolt remote …`
// process already holds — and the whole process tree deadlocks.
//
// All the existing embedded init/remote tests use initGitRepoAt, which sets
// core.hooksPath=/dev/null — so the hook never runs and the bug stays
// hidden. This test deliberately leaves git hooks enabled and runs `bd init`
// without --skip-hooks so the pre-commit hook is installed.
func TestDoltRemoteAddRemoveDoesNotDeadlockWithBeadsHooks(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	bdDir := filepath.Dir(bd)

	dir := t.TempDir()
	// Plain git repo with hooks ENABLED (do not use initGitRepoAt — it
	// disables hooks, which would mask the bug).
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Pre-commit hook shells out to `bd hooks run pre-commit`, which calls
	// `bd export`. Both need bd on PATH.
	env := bdEnv(dir)
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + bdDir + string(os.PathListSeparator) + strings.TrimPrefix(e, "PATH=")
		}
	}

	initCmd := exec.Command(bd, "init", "--quiet", "--prefix", "hookdeadlock", "--skip-agents")
	initCmd.Dir = dir
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed: %v\n%s", err, out)
	}

	hookPath := filepath.Join(dir, ".beads", "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("expected beads pre-commit hook at %s: %v", hookPath, err)
	}

	// git+ssh:// is a Dolt-native scheme: normalizeRemoteURL passes it
	// through unchanged and `dolt remote add` accepts it without trying to
	// connect. We get to the commitBeadsConfig path without depending on
	// network.
	remoteURL := "git+ssh://git@example.com/acme/beads.git"

	runWithTimeout := func(label string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		// Put the subprocess in its own process group so we can kill the
		// whole tree (git → hook → `bd hooks run` → `bd export`) on hang.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		cmd.WaitDelay = 5 * time.Second

		out, err := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("%s hung — GH#3340 deadlock regression: commitBeadsConfig must run `git commit --no-verify` to avoid re-entering bd while embedded Dolt flock is held.\noutput so far:\n%s", label, out)
		}
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", label, err, out)
		}
	}

	runWithTimeout(fmt.Sprintf("bd dolt remote add origin %s", remoteURL),
		"dolt", "remote", "add", "origin", remoteURL)
	runWithTimeout("bd dolt remote remove origin",
		"dolt", "remote", "remove", "origin")
}
