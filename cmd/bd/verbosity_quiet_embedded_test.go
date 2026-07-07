//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedQuietFlagSuppressesSuccessOutput verifies that --quiet suppresses
// success output on create, close, and update while still exiting 0.
//
// Each sub-test: run the command with --quiet, capture stdout+stderr separately,
// assert stdout is empty and exit code is 0. Errors still flow to stderr (not
// tested here — the contract is "success output suppressed", not "all output").
func TestEmbeddedQuietFlagSuppressesSuccessOutput(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
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
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(listStdout.Bytes(), &listed); err != nil {
		t.Fatalf("failed to parse bd list --json output: %v\n%s", err, listStdout.String())
	}
	if len(listed) == 0 || listed[0].ID == "" {
		t.Fatalf("bd list --json returned no usable issue ID:\n%s", listStdout.String())
	}
	issueID := listed[0].ID

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
