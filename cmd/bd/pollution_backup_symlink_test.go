package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if !strings.Contains(err.Error(), backup) {
		t.Errorf("refusal does not name the backup path %s: %v", backup, err)
	}
}

// TestPollutionBackupWritesJSONL: the backup streams one JSON object per
// polluted issue and is atomic — a failed write leaves no partial file.
func TestPollutionBackupWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "pollution-backup.jsonl")

	polluted := []pollutionResult{
		{issue: &types.Issue{ID: "bd-1", Title: "test issue one"}, score: 1},
		{issue: &types.Issue{ID: "bd-2", Title: "test issue two"}, score: 1},
	}
	if err := backupPollutedIssues(polluted, backup); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), data)
	}
	var first types.Issue
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not an issue record: %v", err)
	}
	if first.ID != "bd-1" {
		t.Errorf("first line id = %q, want bd-1", first.ID)
	}

	// A symlinked leaf is refused (not followed) — same helper, different
	// entry point, keeps the write atomic rather than partially streamed.
	link := filepath.Join(dir, "linked-backup.jsonl")
	victim := filepath.Join(dir, "victim.txt")
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		if err := backupPollutedIssues(polluted, link); err == nil {
			t.Error("backupPollutedIssues accepted a symlinked backup path")
		} else if !strings.Contains(err.Error(), link) {
			t.Errorf("refusal does not name %s: %v", link, err)
		}
		if got, _ := os.ReadFile(victim); string(got) != "original\n" {
			t.Errorf("victim overwritten through link: %q", got)
		}
	}
}