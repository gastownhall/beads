package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestBuildGitCommitArgs_ConfigOptions(t *testing.T) {
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("git.author", "Test User <test@example.com>")
	config.Set("git.no-gpg-sign", true)

	args := buildGitCommitArgs("/repo", "hello", "--", ".beads")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--author") {
		t.Fatalf("expected --author in args: %v", args)
	}
	if !strings.Contains(joined, "--no-gpg-sign") {
		t.Fatalf("expected --no-gpg-sign in args: %v", args)
	}
	if !strings.Contains(joined, "-m hello") {
		t.Fatalf("expected message in args: %v", args)
	}
}

func TestGitCommitBeadsDir_PathspecDoesNotCommitOtherStagedFiles(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	if err := os.MkdirAll(".beads", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Stage an unrelated file before running gitCommitBeadsDir.
	if err := os.WriteFile("other.txt", []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile other: %v", err)
	}
	_ = exec.Command("git", "add", "other.txt").Run()

	// Create a beads sync file to commit.
	issuesPath := filepath.Join(".beads", "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte("{\"id\":\"test-1\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile issues: %v", err)
	}

	ctx := context.Background()
	if err := gitCommitBeadsDir(ctx, "beads commit"); err != nil {
		t.Fatalf("gitCommitBeadsDir: %v", err)
	}

	// other.txt should still be staged after the beads-only commit.
	out, err := exec.Command("git", "diff", "--cached", "--name-only").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "other.txt" {
		t.Fatalf("expected other.txt still staged, got: %q", out)
	}
}
