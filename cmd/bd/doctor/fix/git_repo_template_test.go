package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Template git repository optimization (bd-ktng):
// Instead of spawning `git init` per test, create a single template repo
// and copy its .git/ directory for each test.

var (
	gitTemplateDir  string
	gitTemplateOnce sync.Once
	gitTemplateErr  error
)

func initGitTemplate() {
	gitTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "git-template-fix-*")
		if err != nil {
			gitTemplateErr = fmt.Errorf("failed to create git template dir: %w", err)
			return
		}
		gitTemplateDir = dir

		cmd := exec.Command("git", "init", "--initial-branch=main")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			gitTemplateErr = fmt.Errorf("git init failed: %w", err)
			return
		}

		for _, args := range [][]string{
			{"config", "user.email", "test@test.com"},
			{"config", "user.name", "Test User"},
		} {
			cmd = exec.Command("git", args...)
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				gitTemplateErr = fmt.Errorf("git %v failed: %w", args, err)
				return
			}
		}
	})
}

// newGitRepo creates a fresh git repository by copying the cached template.
func newGitRepo(t *testing.T) string {
	t.Helper()
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("Git template initialization failed: %v", gitTemplateErr)
	}

	dir := t.TempDir()
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("Failed to copy git template: %v", err)
	}
	return dir
}

func copyGitDir(src, dst string) error {
	return copyDirRecursive(filepath.Join(src, ".git"), filepath.Join(dst, ".git"))
}

func copyDirRecursive(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0750); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0600); err != nil {
				return err
			}
		}
	}
	return nil
}
