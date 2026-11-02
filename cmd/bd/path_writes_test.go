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

// TestHuskyMigrationSkipsSymlinks: preserving husky v9 hooks into a target
// whose entry is a live or dangling symlink must warn and skip, never write
// through the link (neither the copy nor the v9 shim replacement).
func TestHuskyMigrationSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	for _, dangling := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "dangling"}[dangling], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, ".husky", "_")
			target := filepath.Join(dir, "hooks")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{
				filepath.Join(source, "h"):              `dirname "$(dirname`,
				filepath.Join(source, "custom"):         "#!/bin/sh\necho shim\n",
				filepath.Join(source, "regular"):        "#!/bin/sh\necho shim\n",
				filepath.Join(dir, ".husky", "custom"):  "echo user\n",
				filepath.Join(dir, ".husky", "regular"): "echo user regular\n",
			} {
				if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := filepath.Join(dir, "sentinel")
			if !dangling {
				if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(target, "custom")
			if err := os.Symlink(sentinel, link); err != nil {
				t.Fatal(err)
			}

			stderr := captureStderr(t, func() {
				preservePreexistingHooksAt(target, source, func() string { return "" })
			})

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
			if !strings.Contains(stderr, link) {
				t.Errorf("no warning naming %s:\n%s", link, stderr)
			}
			// A regular destination still gets the v9 user hook inlined.
			if got, err := os.ReadFile(filepath.Join(target, "regular")); err != nil || !strings.Contains(string(got), "echo user regular") {
				t.Errorf("regular husky hook not migrated: %q, %v", got, err)
			}
		})
	}
}

// TestInstallHooksRefusesManagedSymlinkBeforePreservation: a symlink at a
// managed hook destination refuses the install before hook preservation runs,
// so nothing is created through the link and no other hook is copied.
func TestInstallHooksRefusesManagedSymlinkBeforePreservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	repoDir := newGitRepo(t)
	runInDir(t, repoDir, func() {
		gitHooks := filepath.Join(repoDir, ".git", "hooks")
		if err := os.MkdirAll(gitHooks, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"pre-commit": "#!/bin/sh\necho custom-default-hook\n",
			"commit-msg": "#!/bin/sh\necho commit-msg-hook\n",
		} {
			if err := os.WriteFile(filepath.Join(gitHooks, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		_ = gitCommand("config", "--unset", "core.hooksPath").Run()

		beadsDir := setupBeadsDir(t, repoDir)
		beadsHooks := filepath.Join(beadsDir, "hooks")
		if err := os.MkdirAll(beadsHooks, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		link := filepath.Join(beadsHooks, "pre-commit")
		if err := os.Symlink(sentinel, link); err != nil {
			t.Fatal(err)
		}

		err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("install error = %v, want a symlink refusal", err)
		}
		if _, statErr := os.Lstat(sentinel); !os.IsNotExist(statErr) {
			t.Errorf("link target created: %v", statErr)
		}
		if info, lerr := os.Lstat(link); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("planted link changed: %v, %v", info, lerr)
		}
		if _, statErr := os.Lstat(filepath.Join(beadsHooks, "commit-msg")); !os.IsNotExist(statErr) {
			t.Errorf("hooks preserved before the managed-hook refusal: %v", statErr)
		}
	})
}
