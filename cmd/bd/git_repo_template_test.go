package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Template git repository optimization (bd-ktng):
// Instead of spawning `git init` per test (~20-30ms each, 60+ calls = 1-2s),
// create a single template repo and copy its .git/ directory for each test.
// Directory copy is ~100x faster than subprocess spawn + git initialization.

var (
	gitTemplateDir  string
	gitTemplateOnce sync.Once
	gitTemplateErr  error
)

// initGitTemplate creates a template git repository once via sync.Once.
// The template includes git init --initial-branch=main plus user.email/user.name config.
func initGitTemplate() {
	gitTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "git-template-*")
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

// newGitRepo creates a fresh git repository by copying the cached template's .git/ directory.
// Returns the path to the new directory. Cleanup is handled by t.TempDir().
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

// copyGitDir copies the .git directory from src to dst.
func copyGitDir(src, dst string) error {
	return copyDirRecursive(filepath.Join(src, ".git"), filepath.Join(dst, ".git"))
}

// copyDirRecursive recursively copies a directory tree.
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
