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

func TestPollutionBackupWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.jsonl")
	issue := &types.Issue{ID: "test-1", Title: "test issue"}
	if err := backupPollutedIssues([]pollutionResult{{issue: issue}}, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want)+"\n" {
		t.Fatalf("backup: %q", data)
	}
	if err := backupPollutedIssues(nil, path); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("empty backup: %q, %v", data, err)
	}
	if err := backupPollutedIssues(nil, filepath.Join(dir, "missing", "backup")); err == nil {
		t.Error("accepted missing parent")
	}
}

func TestPollutionBackupRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	for _, dangling := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "dangling"}[dangling], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "sentinel")
			if !dangling {
				if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "backup.jsonl")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			issue := &types.Issue{ID: "test-1", Title: "test issue"}
			var deleted []string
			n, err := cleanPollutedIssues([]pollutionResult{{issue: issue, score: 1}}, path, func(id string) error {
				deleted = append(deleted, id)
				return nil
			})
			if err == nil {
				t.Fatal("cleanup accepted a symlinked backup")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name %s", err, path)
			}
			if n != 0 || len(deleted) != 0 {
				t.Errorf("deleted %d issues (%v) after the backup failed", n, deleted)
			}
			data, readErr := os.ReadFile(target)
			if dangling {
				if !os.IsNotExist(readErr) {
					t.Errorf("dangling target created: %q, %v", data, readErr)
				}
			} else if readErr != nil || string(data) != "sentinel" {
				t.Errorf("sentinel changed: %q, %v", data, readErr)
			}
		})
	}
}
