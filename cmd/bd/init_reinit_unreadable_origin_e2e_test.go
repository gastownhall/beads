//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInitReinitLocalUnreadableOriginE2E reproduces GH#7665: when git
// ls-remote cannot read the origin, plain `bd init` warns and initializes a
// fresh database, but `bd init --reinit-local` refused with "remote 'origin'
// already has Dolt history", which is false. Re-init must either proceed like
// plain init or refuse with an error that does not claim the remote has
// Dolt history.
func TestInitReinitLocalUnreadableOriginE2E(t *testing.T) {
	bd := buildBDUnderTest(t)

	projectDir := t.TempDir()
	home := t.TempDir()
	gitEnv := append(cleanInitE2EEnv(), "HOME="+home, "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	// A missing repository makes ls-remote fail the same way an
	// unauthenticated private repository does, without network access.
	missingOrigin := "file://" + filepath.Join(t.TempDir(), "does-not-exist.git")
	for _, args := range [][]string{
		{"init", "-q"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
		{"remote", "add", "origin", missingOrigin},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #nosec G204 -- bd is a locally built binary, args are literals
	cmd := exec.CommandContext(ctx, bd, "init", "--non-interactive", "--reinit-local",
		"--prefix", "demo", "--skip-hooks", "--skip-agents")
	cmd.Dir = projectDir
	cmd.Env = gitEnv
	out, err := cmd.CombinedOutput()

	if err != nil && strings.Contains(string(out), "already has Dolt history") {
		t.Fatalf("bd init --reinit-local against an unreadable origin must not claim the remote has Dolt history (GH#7665), got %v:\n%s", err, out)
	}
}

// cleanInitE2EEnv drops every BEADS_*/BD_* setting so only the fixture
// chooses the workspace.
func cleanInitE2EEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BEADS_") || strings.HasPrefix(kv, "BD_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}