//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

func TestBackupStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Load from empty dir returns zero state
	state, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if state.LastDoltCommit != "" {
		t.Errorf("expected empty commit, got %q", state.LastDoltCommit)
	}

	// Save and reload
	state.LastDoltCommit = "abc123"
	state.Timestamp = time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC)

	if err := saveBackupState(dir, state); err != nil {
		t.Fatalf("saveBackupState: %v", err)
	}

	loaded, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState after save: %v", err)
	}
	if loaded.LastDoltCommit != "abc123" {
		t.Errorf("commit = %q, want abc123", loaded.LastDoltCommit)
	}
}

func TestBackupAtomicWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	data := []byte(`{"id":"test-1","title":"hello"}` + "\n")
	if err := atomicWriteFile(path, data); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestBackupDir_NoWorkspaceReturnsActiveWorkspaceError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_BACKUP_GIT_REPO", filepath.Join(tmpDir, "not-a-repo"))

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	dir, err := backupDir()
	if err == nil {
		t.Fatalf("backupDir() = %q, want error", dir)
	}
	if !strings.Contains(err.Error(), activeWorkspaceNotFoundError()) {
		t.Fatalf("backupDir() error = %q, want active workspace wording", err)
	}
	if !strings.Contains(err.Error(), diagHint()) {
		t.Fatalf("backupDir() error = %q, want diag hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("backupDir() should not create local .beads, stat err = %v", statErr)
	}
}

func TestBackupDir_UsesWorktreeFallback(t *testing.T) {
	tmpDir := t.TempDir()
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatalf("mkdir main repo: %v", err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(mainBeadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir main beads dir: %v", err)
	}
	_ = os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_BACKUP_GIT_REPO", filepath.Join(tmpDir, "not-a-repo"))

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir() error = %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	wantResolved, err := filepath.EvalSymlinks(filepath.Join(mainBeadsDir, "backup"))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", filepath.Join(mainBeadsDir, "backup"), err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("backupDir() = %q (resolved %q), want resolved %q", dir, gotResolved, wantResolved)
	}
}

// splitJSONL splits JSONL data into individual JSON lines, skipping empty lines.
func splitJSONL(data []byte) []json.RawMessage {
	var result []json.RawMessage
	for _, line := range splitLines(data) {
		if len(line) > 0 {
			result = append(result, json.RawMessage(line))
		}
	}
	return result
}

// splitLines splits data into lines without importing strings.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
