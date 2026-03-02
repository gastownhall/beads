package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsBareGitRepo(t *testing.T) {
	t.Run("returns true in bare repository", func(t *testing.T) {
		repoDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", repoDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if !isBareGitRepo() {
			t.Fatal("isBareGitRepo() = false, want true")
		}
	})

	t.Run("returns false in non-bare repository", func(t *testing.T) {
		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if isBareGitRepo() {
			t.Fatal("isBareGitRepo() = true, want false")
		}
	})

	t.Run("returns false outside git repository", func(t *testing.T) {
		nonRepoDir := t.TempDir()

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(nonRepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if isBareGitRepo() {
			t.Fatal("isBareGitRepo() = true, want false")
		}
	})
}

func runGitForSyncTest(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}
