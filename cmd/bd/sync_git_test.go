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

func TestGitOriginGetURL(t *testing.T) {
	t.Run("returns origin URL", func(t *testing.T) {
		repoDir := t.TempDir()
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)
		runGitForSyncTest(t, repoDir, "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		url, err := gitOriginGetURL()
		if err != nil {
			t.Fatalf("gitOriginGetURL failed: %v", err)
		}
		if url != bareDir {
			t.Errorf("gitOriginGetURL = %q, want %q", url, bareDir)
		}
	})

	t.Run("returns error for nonexistent remote", func(t *testing.T) {
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

		_, err = gitOriginGetURL()
		if err == nil {
			t.Fatal("expected error for nonexistent remote")
		}
	})
}

func TestGitOriginHasDoltDataRef(t *testing.T) {
	t.Run("returns false for nonexistent ref", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)

		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if gitOriginHasDoltDataRef() {
			t.Fatal("expected false for nonexistent ref")
		}
	})

	t.Run("returns true for existing ref", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)

		// Create a repo, commit, push to create refs/dolt/data
		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init", "-b", "main")
		runGitForSyncTest(t, repoDir, "config", "user.email", "test@test.com")
		runGitForSyncTest(t, repoDir, "config", "user.name", "Test User")
		runGitForSyncTest(t, repoDir, "commit", "--allow-empty", "-m", "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)
		runGitForSyncTest(t, repoDir, "push", "origin", "HEAD:refs/dolt/data")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if !gitOriginHasDoltDataRef() {
			t.Fatal("expected true for existing ref")
		}
	})
}

func TestGitURLToDoltRemote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"http://github.com/org/repo.git", "git+http://github.com/org/repo.git"},
		{"ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := gitURLToDoltRemote(tt.input)
			if got != tt.want {
				t.Errorf("gitURLToDoltRemote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
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
