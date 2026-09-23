package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestPreCommitExportPathTargetsHookWorktree(t *testing.T) {
	mainBeadsDir := filepath.Join(t.TempDir(), "main", ".beads")
	worktreeRoot := filepath.Join(t.TempDir(), "linked-worktree")

	got := preCommitExportPath(mainBeadsDir, "issues.jsonl", worktreeRoot)
	want := filepath.Join(worktreeRoot, ".beads", "issues.jsonl")
	if got != want {
		t.Fatalf("preCommitExportPath() = %q, want committing worktree path %q", got, want)
	}
}

func TestPreCommitExportEnvPreservesHookSuppression(t *testing.T) {
	env := []string{"PATH=/bin", "BD_GIT_HOOK=1", "GIT_DIR=/repo/.git"}

	got := preCommitExportEnv(env)
	if !slices.Contains(got, "BD_GIT_HOOK=1") {
		t.Fatalf("preCommitExportEnv() = %#v, want BD_GIT_HOOK=1 preserved", got)
	}
}

func TestPreCommitExportPathWithoutHookUsesDiscoveredBeadsDir(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), "repo", ".beads")

	got := preCommitExportPath(beadsDir, "exports/issues.jsonl", "")
	want := filepath.Join(beadsDir, "exports", "issues.jsonl")
	if got != want {
		t.Fatalf("preCommitExportPath() = %q, want %q", got, want)
	}
}
