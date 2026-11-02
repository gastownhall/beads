package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionBackupRefusesSymlink: a committed .beads/pollution-backup.jsonl
// symlink must not redirect the backup write to its target (bd-1tmym4 / #7888).
func TestPollutionBackupRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "pollution-backup.jsonl")
	if err := os.Symlink(victim, backup); err != nil {
		t.Fatal(err)
	}

	polluted := []pollutionResult{{issue: &types.Issue{ID: "bd-1", Title: "test issue"}, score: 1}}
	err := backupPollutedIssues(polluted, backup)

	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original\n" {
		t.Errorf("backup wrote through symlink: victim now %q", got)
	}
	if err == nil {
		t.Errorf("backupPollutedIssues accepted a symlinked backup path")
	}
}
