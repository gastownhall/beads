package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	target := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "backup.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	err := backupPollutedIssues(nil, path)
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "sentinel" {
		t.Errorf("sentinel changed: %q, %v", data, readErr)
	}
	if err == nil {
		t.Error("backup accepted symlink")
	}
}

func TestHuskyMigrationSkipsSymlinks(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "dangling"}[dangling], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, ".husky", "_")
			target := filepath.Join(dir, "hooks")
			if err := os.MkdirAll(source, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{
				filepath.Join(source, "h"):             `dirname "$(dirname`,
				filepath.Join(source, "custom"):        "#!/bin/sh\necho shim\n",
				filepath.Join(dir, ".husky", "custom"): "echo user\n",
			} {
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := filepath.Join(dir, "sentinel")
			if !dangling {
				if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(target, "custom")
			if err := os.Symlink(sentinel, link); err != nil {
				t.Fatal(err)
			}
			preservePreexistingHooksAt(target, source, func() string { return "" })
			data, err := os.ReadFile(sentinel)
			if dangling {
				if !os.IsNotExist(err) {
					t.Errorf("dangling target created: %q, %v", data, err)
				}
			} else if err != nil || string(data) != "sentinel" {
				t.Errorf("sentinel changed: %q, %v", data, err)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("symlink not preserved: %v", err)
			}
		})
	}
}
