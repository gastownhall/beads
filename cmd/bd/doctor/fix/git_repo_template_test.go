package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/beads/internal/testutil"
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
	if err := copyDirRecursive(filepath.Join(src, ".git"), filepath.Join(dst, ".git")); err != nil {
		return err
	}
	return testutil.ForceRepoLocalHooksPath(dst)
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

func TestNewGitRepo_UsesRepoLocalHooksPathDespiteGlobalConfig(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(fakeHome, ".gitconfig"))

	globalHooks := filepath.Join(fakeHome, "global-hooks")
	if err := os.MkdirAll(globalHooks, 0755); err != nil {
		t.Fatalf("failed to create fake global hooks dir: %v", err)
	}

	setGlobal := exec.Command("git", "config", "--global", "core.hooksPath", globalHooks)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("failed to set global core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	repoDir := newGitRepo(t)
	getLocal := exec.Command("git", "config", "--get", "core.hooksPath")
	getLocal.Dir = repoDir
	out, err := getLocal.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read repo core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	if got := strings.TrimSpace(string(out)); got != ".git/hooks" {
		t.Fatalf("core.hooksPath=%q, want %q", got, ".git/hooks")
	}
}
