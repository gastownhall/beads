//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestQuietFlagSuppressesSuccessOutput verifies that --quiet suppresses success
// output on create, close, and update while still exiting 0.
//
// Each sub-test: run the command with --quiet, capture stdout+stderr separately,
// assert stdout is empty and exit code is 0. Errors still flow to stderr (not
// tested here — the contract is "success output suppressed", not "all output").
func TestQuietFlagSuppressesSuccessOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow embedded-bd quiet-mode tests in short mode")
	}

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	env := bdEnv(dir)

	// Initialize a beads store in the temp dir.
	initCmd := exec.Command(bd, "init", "--prefix", "q", "--quiet")
	initCmd.Dir = dir
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed: %v\n%s", err, out)
	}

	t.Run("create", func(t *testing.T) {
		cmd := exec.Command(bd, "--quiet", "create", "quiet-create-test-title", "-p", "2")
		cmd.Dir = dir
		cmd.Env = env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd --quiet create failed: %v\nstdout:%s\nstderr:%s", err, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "" {
			t.Errorf("--quiet create: expected empty stdout, got:\n%s", got)
		}
	})

	// Retrieve the issue ID created above for close/update tests.
	listCmd := exec.Command(bd, "list", "--json", "--limit", "1")
	listCmd.Dir = dir
	listCmd.Env = env
	listStdout, _, err := runCommandBuffers(t, listCmd)
	if err != nil {
		t.Fatalf("bd list --json failed: %v", err)
	}
	issueID := ""
	for _, line := range strings.Split(listStdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"id"`) {
			parts := strings.SplitN(line, `"`, 4)
			if len(parts) >= 4 {
				issueID = parts[3]
			}
			break
		}
	}
	if issueID == "" {
		// Fallback: scan for first `"id": "..."` pattern
		s := listStdout.String()
		if idx := strings.Index(s, `"id": "`); idx >= 0 {
			rest := s[idx+7:]
			if end := strings.Index(rest, `"`); end >= 0 {
				issueID = rest[:end]
			}
		}
	}
	if issueID == "" {
		t.Skip("could not determine created issue ID; skipping close/update quiet tests")
	}

	t.Run("update", func(t *testing.T) {
		cmd := exec.Command(bd, "--quiet", "update", issueID, "--priority", "1")
		cmd.Dir = dir
		cmd.Env = env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd --quiet update failed: %v\nstdout:%s\nstderr:%s", err, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "" {
			t.Errorf("--quiet update: expected empty stdout, got:\n%s", got)
		}
	})

	t.Run("close", func(t *testing.T) {
		cmd := exec.Command(bd, "--quiet", "close", issueID, "--reason", "quiet-test-done")
		cmd.Dir = dir
		cmd.Env = env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd --quiet close failed: %v\nstdout:%s\nstderr:%s", err, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "" {
			t.Errorf("--quiet close: expected empty stdout, got:\n%s", got)
		}
	})
}
