//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

var expectedBackupSnapshotFiles = []string{
	".beads/backup/backup_state.json",
	".beads/backup/comments.jsonl",
	".beads/backup/config.jsonl",
	".beads/backup/dependencies.jsonl",
	".beads/backup/events.jsonl",
	".beads/backup/labels.jsonl",
	".beads/backup/issues.jsonl",
	".beads/backup/manifest.json",
}

type backupExportGitHarness struct {
	repoDir   string
	remoteDir string
	ctx       context.Context
}

func TestBackupExportGitCreatesBranchAndPushesSnapshot(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "test-exp-1", "Export Git Issue 1")

	result, err := runBackupExportGit(h.ctx, backupExportGitOptions{})
	if err != nil {
		t.Fatalf("runBackupExportGit: %v", err)
	}

	if !result.Exported || !result.Changed || !result.Committed || !result.Pushed {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertCurrentBranch(t, h.repoDir, "main")
	assertBranchHasExpectedBackupFiles(t, h.repoDir, backupGitDefaultBranch)
	assertRemoteBranchExists(t, h.remoteDir, backupGitDefaultBranch)
}

func TestBackupExportGitUpdatesExistingBranch(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "test-upd-1", "First export")

	if _, err := runBackupExportGit(h.ctx, backupExportGitOptions{}); err != nil {
		t.Fatalf("first runBackupExportGit: %v", err)
	}
	firstHead := gitOutput(t, h.repoDir, "rev-parse", backupGitDefaultBranch)

	insertBackupExportGitIssue(t, h.ctx, "test-upd-2", "Second export")
	result, err := runBackupExportGit(h.ctx, backupExportGitOptions{})
	if err != nil {
		t.Fatalf("second runBackupExportGit: %v", err)
	}
	secondHead := gitOutput(t, h.repoDir, "rev-parse", backupGitDefaultBranch)

	if !result.Changed || !result.Committed || !result.Pushed {
		t.Fatalf("unexpected result: %+v", result)
	}
	if secondHead == firstHead {
		t.Fatalf("backup branch head did not change: %s", secondHead)
	}
	content := gitShowFile(t, h.repoDir, backupGitDefaultBranch, ".beads/backup/issues.jsonl")
	if !strings.Contains(content, `"title":"Second export"`) {
		t.Fatalf("issues.jsonl missing updated issue:\n%s", content)
	}
	assertCurrentBranch(t, h.repoDir, "main")
}

func TestBackupExportGitNoChangesSkipsCommitAndPush(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "test-same-1", "No change export")

	if _, err := runBackupExportGit(h.ctx, backupExportGitOptions{}); err != nil {
		t.Fatalf("first runBackupExportGit: %v", err)
	}
	firstHead := gitOutput(t, h.repoDir, "rev-parse", backupGitDefaultBranch)

	result, err := runBackupExportGit(h.ctx, backupExportGitOptions{})
	if err != nil {
		t.Fatalf("second runBackupExportGit: %v", err)
	}
	secondHead := gitOutput(t, h.repoDir, "rev-parse", backupGitDefaultBranch)

	if result.Changed || result.Committed || result.Pushed {
		t.Fatalf("expected no-op result, got %+v", result)
	}
	if firstHead != secondHead {
		t.Fatalf("expected backup branch head to stay at %s, got %s", firstHead, secondHead)
	}
}

func TestBackupExportGitWritesManifest(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "manifest-1", "Manifest issue")

	result, err := runBackupExportGit(h.ctx, backupExportGitOptions{})
	if err != nil {
		t.Fatalf("runBackupExportGit: %v", err)
	}

	manifestPath := filepath.Join(result.BackupDir, backupGitManifestFilename)
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("local backup dir should not contain %s", backupGitManifestFilename)
	}

	content := gitShowFile(t, h.repoDir, backupGitDefaultBranch, filepath.ToSlash(filepath.Join(backupGitPathspec, backupGitManifestFilename)))
	var manifest backupGitManifest
	if err := json.Unmarshal([]byte(content), &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.Format != "bd-backup-git-manifest-v1" {
		t.Fatalf("manifest format = %q", manifest.Format)
	}
	if manifest.BDVersion != Version {
		t.Fatalf("manifest bd_version = %q, want %q", manifest.BDVersion, Version)
	}
	if manifest.LastDoltCommit == "" {
		t.Fatal("manifest missing dolt commit")
	}
	if manifest.Counts.Issues != 1 {
		t.Fatalf("manifest issues = %d, want 1", manifest.Counts.Issues)
	}
	if manifest.SnapshotTimestamp.IsZero() {
		t.Fatal("manifest snapshot timestamp is zero")
	}
}

func TestBackupExportGitDryRunHasNoSideEffects(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "test-dry-1", "Dry run issue")

	result, err := runBackupExportGit(h.ctx, backupExportGitOptions{DryRun: true})
	if err != nil {
		t.Fatalf("runBackupExportGit dry-run: %v", err)
	}

	if !result.DryRun || !result.WouldExport || !result.WouldCopy || !result.WouldCommit || !result.WouldPush {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if gitRefExistsForTest(t, h.repoDir, "refs/heads/"+backupGitDefaultBranch) {
		t.Fatalf("dry-run should not create local branch %s", backupGitDefaultBranch)
	}
	if gitRefExistsForTest(t, h.remoteDir, "refs/heads/"+backupGitDefaultBranch, "--git-dir") {
		t.Fatalf("dry-run should not create remote branch %s", backupGitDefaultBranch)
	}
	assertCurrentBranch(t, h.repoDir, "main")
}

func TestBackupExportGitLeavesUnrelatedWorkingTreeChangesUntouched(t *testing.T) {
	h := setupBackupExportGitHarness(t)
	insertBackupExportGitIssue(t, h.ctx, "test-dirty-1", "Dirty tree issue")

	readmePath := filepath.Join(h.repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("modified locally\n"), 0644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}

	if _, err := runBackupExportGit(h.ctx, backupExportGitOptions{}); err != nil {
		t.Fatalf("runBackupExportGit: %v", err)
	}

	status := gitOutput(t, h.repoDir, "status", "--short")
	if !strings.Contains(status, " M README.md") {
		t.Fatalf("expected README.md to remain modified, got:\n%s", status)
	}

	changedFiles := nonEmptyLines(gitOutput(t, h.repoDir, "show", "--name-only", "--pretty=format:", backupGitDefaultBranch))
	if len(changedFiles) == 0 {
		t.Fatal("expected committed files on backup branch")
	}
	for _, changed := range changedFiles {
		if !strings.HasPrefix(changed, backupGitPathspec+"/") {
			t.Fatalf("backup branch committed unrelated path %q", changed)
		}
	}
}

func TestBackupExportGitNotInGitRepo(t *testing.T) {
	initConfigForTest(t)
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := runBackupExportGit(context.Background(), backupExportGitOptions{})
	if err == nil {
		t.Fatal("expected error outside git repo")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("expected not-in-git-repo error, got %v", err)
	}
}

func setupBackupExportGitHarness(t *testing.T) *backupExportGitHarness {
	t.Helper()
	ensureTestMode(t)
	saveAndRestoreGlobals(t)

	repoDir := newGitRepo(t)
	t.Chdir(repoDir)
	initConfigForTest(t)

	// Establish an initial commit so git worktree add can branch from HEAD.
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("backup export git test\n"), 0644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "add", "README.md"); err != nil {
		t.Fatalf("git add README.md: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit initial: %v", err)
	}

	remoteDir := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(filepath.Dir(remoteDir), 0755); err != nil {
		t.Fatalf("MkdirAll remote parent: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "init", "--bare", remoteDir); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	if err := runCommandInDir(repoDir, "git", "remote", "add", "origin", remoteDir); err != nil {
		t.Fatalf("git remote add origin: %v", err)
	}

	dbPath := filepath.Join(repoDir, ".beads", "dolt")
	s := newTestStore(t, dbPath)
	store = s
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	return &backupExportGitHarness{
		repoDir:   repoDir,
		remoteDir: remoteDir,
		ctx:       context.Background(),
	}
}

func insertBackupExportGitIssue(t *testing.T, ctx context.Context, id, title string) {
	t.Helper()
	db := store.(storage.RawDBAccessor).DB()
	if _, err := db.ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, title, "desc", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", fmt.Sprintf("add %s", id)); err != nil {
		t.Fatalf("dolt commit for %s: %v", id, err)
	}
}

func assertBranchHasExpectedBackupFiles(t *testing.T, repoDir, branch string) {
	t.Helper()
	got := nonEmptyLines(gitOutput(t, repoDir, "ls-tree", "-r", "--name-only", branch, "--", backupGitPathspec))
	slices.Sort(got)
	want := append([]string(nil), expectedBackupSnapshotFiles...)
	slices.Sort(want)
	if len(got) != len(want) {
		t.Fatalf("backup branch files = %v, want %v", got, want)
	}
	for i, expected := range want {
		if got[i] != expected {
			t.Fatalf("backup branch file[%d] = %q, want %q", i, got[i], expected)
		}
	}
}

func assertRemoteBranchExists(t *testing.T, remoteDir, branch string) {
	t.Helper()
	if !gitRefExistsForTest(t, remoteDir, "refs/heads/"+branch, "--git-dir") {
		t.Fatalf("expected remote branch %s to exist", branch)
	}
}

func assertCurrentBranch(t *testing.T, repoDir, want string) {
	t.Helper()
	got := gitOutput(t, repoDir, "branch", "--show-current")
	if got != want {
		t.Fatalf("current branch = %q, want %q", got, want)
	}
}

func gitShowFile(t *testing.T, repoDir, branch, path string) string {
	t.Helper()
	return gitOutput(t, repoDir, "show", branch+":"+path)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func gitRefExistsForTest(t *testing.T, dir, ref string, prefixArgs ...string) bool {
	t.Helper()
	args := append([]string{}, prefixArgs...)
	if len(prefixArgs) == 1 && prefixArgs[0] == "--git-dir" {
		args = append(args, dir)
	}
	args = append(args, "show-ref", "--verify", "--quiet", ref)
	cmd := exec.Command("git", args...)
	if len(prefixArgs) == 0 {
		cmd.Dir = dir
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false
		}
		t.Fatalf("git %v failed: %v", args, err)
	}
	return true
}

func nonEmptyLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
