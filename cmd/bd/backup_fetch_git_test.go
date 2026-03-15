//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupFetchGitRestoresSnapshotFromRemoteBranch(t *testing.T) {
	source := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, source.ctx, "fetch-1", "Fetch Git Issue")

	if _, err := runBackupExportGit(source.ctx, backupExportGitOptions{}); err != nil {
		t.Fatalf("runBackupExportGit: %v", err)
	}

	targetRepo := newBackupFetchGitTargetRepo(t, source.remoteDir)
	targetStore := newTestStore(t, filepath.Join(targetRepo, ".beads", "dolt"))

	t.Chdir(targetRepo)

	result, err := runBackupFetchGit(context.Background(), targetStore, backupFetchGitOptions{})
	if err != nil {
		t.Fatalf("runBackupFetchGit: %v", err)
	}

	if !result.Fetched || !result.Restored {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Issues != 1 {
		t.Fatalf("issues restored = %d, want 1", result.Issues)
	}
	if !gitRefExistsForTest(t, targetRepo, "refs/remotes/origin/"+backupGitDefaultBranch) {
		t.Fatalf("expected fetched remote ref for %s", backupGitDefaultBranch)
	}
	assertCurrentBranch(t, targetRepo, "main")

	issue, err := targetStore.GetIssue(context.Background(), "fetch-1")
	if err != nil {
		t.Fatalf("GetIssue fetch-1: %v", err)
	}
	if issue.Title != "Fetch Git Issue" {
		t.Fatalf("issue title = %q, want %q", issue.Title, "Fetch Git Issue")
	}
}

func TestBackupFetchGitDryRunHasNoSideEffects(t *testing.T) {
	source := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, source.ctx, "fetch-dry-1", "Fetch dry run issue")

	if _, err := runBackupExportGit(source.ctx, backupExportGitOptions{}); err != nil {
		t.Fatalf("runBackupExportGit: %v", err)
	}

	targetRepo := newBackupFetchGitTargetRepo(t, source.remoteDir)
	targetStore := newTestStore(t, filepath.Join(targetRepo, ".beads", "dolt"))

	t.Chdir(targetRepo)

	result, err := runBackupFetchGit(context.Background(), targetStore, backupFetchGitOptions{DryRun: true})
	if err != nil {
		t.Fatalf("runBackupFetchGit dry-run: %v", err)
	}

	if !result.DryRun || !result.WouldFetch || !result.WouldRestore {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if gitRefExistsForTest(t, targetRepo, "refs/remotes/origin/"+backupGitDefaultBranch) {
		t.Fatalf("dry-run should not fetch remote branch %s", backupGitDefaultBranch)
	}
	assertCurrentBranch(t, targetRepo, "main")

	if _, err := targetStore.GetIssue(context.Background(), "fetch-dry-1"); err == nil {
		t.Fatal("dry-run should not restore issues")
	}
}

func TestBackupFetchGitNotInGitRepo(t *testing.T) {
	initConfigForTest(t)
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := runBackupFetchGit(context.Background(), nil, backupFetchGitOptions{})
	if err == nil {
		t.Fatal("expected error outside git repo")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("expected not-in-git-repo error, got %v", err)
	}
}

func newBackupFetchGitTargetRepo(t *testing.T, remoteDir string) string {
	t.Helper()

	repoDir := newGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("backup fetch git test\n"), 0644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "add", "README.md"); err != nil {
		t.Fatalf("git add README.md: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit initial: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "remote", "add", "origin", remoteDir); err != nil {
		t.Fatalf("git remote add origin: %v", err)
	}
	return repoDir
}
