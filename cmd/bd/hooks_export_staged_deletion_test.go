// hooks_export_staged_deletion_test.go - Regression test for GH#3838
// (reimplemented): exportJSONLForCommit must not re-export and re-stage the
// JSONL export file when the user has staged its deletion (`git rm`).
// Without the guard, the pre-commit hook would silently revert the intended
// deletion by recreating the file and running `git add` on it.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupExportStagedDeletionRepo creates a throwaway git repo containing a
// committed file, and returns its absolute path for the caller to mutate
// and (re)stage as needed.
func setupExportStagedDeletionRepo(t *testing.T) (repoDir, filePath string) {
	t.Helper()

	repoDir = t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	filePath = filepath.Join(repoDir, "issues.jsonl")
	if err := os.WriteFile(filePath, []byte(`{"id":"bd-1"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "issues.jsonl")
	run("commit", "-m", "Initial commit")

	return repoDir, filePath
}

func TestIsExportFileStagedForDeletion_StagedDeletion(t *testing.T) {
	repoDir, filePath := setupExportStagedDeletionRepo(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Simulate `git rm issues.jsonl`: remove from disk and stage the removal.
	run("rm", "issues.jsonl")

	if !isExportFileStagedForDeletion(filePath) {
		t.Fatalf("expected isExportFileStagedForDeletion(%q) to be true after `git rm`, got false", filePath)
	}
}

func TestIsExportFileStagedForDeletion_NoStagedDeletion(t *testing.T) {
	repoDir, filePath := setupExportStagedDeletionRepo(t)
	_ = repoDir

	// File is committed and untouched: not staged for deletion.
	if isExportFileStagedForDeletion(filePath) {
		t.Fatalf("expected isExportFileStagedForDeletion(%q) to be false for an untouched file, got true", filePath)
	}
}

func TestIsExportFileStagedForDeletion_StagedModificationNotDeletion(t *testing.T) {
	repoDir, filePath := setupExportStagedDeletionRepo(t)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Modify and re-stage the file (the ordinary export/git-add path):
	// this must NOT be treated as a staged deletion.
	if err := os.WriteFile(filePath, []byte(`{"id":"bd-1"}`+"\n"+`{"id":"bd-2"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "issues.jsonl")

	if isExportFileStagedForDeletion(filePath) {
		t.Fatalf("expected isExportFileStagedForDeletion(%q) to be false for a staged modification, got true", filePath)
	}
}
