package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/testutil"
)

// legacyGuardWorkspace is a Git worktree with a .beads dir whose .gitignore
// is gitignore (none when empty).
func legacyGuardWorkspace(t *testing.T, gitignore string) (repo, beadsDir string) {
	t.Helper()
	repo = t.TempDir()
	initGitRepoAt(t, repo)
	beadsDir = filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if gitignore != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte(gitignore), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo, beadsDir
}

func runGitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func legacyRootPath(beadsDir string) string {
	return filepath.Join(beadsDir, legacyProxiedRootName)
}

// assertLegacyRootAbsent fails if anything exists at the legacy root.
func assertLegacyRootAbsent(t *testing.T, beadsDir string) {
	t.Helper()
	if _, err := os.Lstat(legacyRootPath(beadsDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard created %s (lstat error: %v)", legacyRootPath(beadsDir), err)
	}
}

// assertLegacyRootArmed fails unless the legacy root is a git-ignored regular
// file explaining the upgrade remedy.
func assertLegacyRootArmed(t *testing.T, repo, beadsDir string) {
	t.Helper()
	path := legacyRootPath(beadsDir)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("legacy root %s is not a regular file: %v, %v", path, info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("legacy root mode = %04o, want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proxieddb: not a directory", "upgrade every bd", "Written by bd " + Version} {
		if !strings.Contains(string(body), want) {
			t.Errorf("legacy root text missing %q:\n%s", want, body)
		}
	}
	if out, err := runGitIn(t, repo, "check-ignore", "-q", "--", ".beads/"+legacyProxiedRootName); err != nil {
		t.Errorf("git check-ignore on the marker: %v\n%s", err, out)
	}
	if out, err := runGitIn(t, repo, "status", "--porcelain", "--untracked-files=all"); err != nil || strings.Contains(out, legacyProxiedRootName) {
		t.Errorf("git status shows the marker (or failed): %v\n%s", err, out)
	}
}

// TestLegacyProxiedRootGuard pins GH#5104's marker: an absent legacy root in
// a workspace that ignores it becomes a git-ignored regular file explaining
// the upgrade remedy.
func TestLegacyProxiedRootGuard(t *testing.T) {
	t.Run("template gitignore arms an absent root", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
		armLegacyProxiedRootGuard(beadsDir)
		assertLegacyRootArmed(t, repo, beadsDir)

		// Re-arming an armed root changes nothing.
		before, _ := os.ReadFile(legacyRootPath(beadsDir))
		armLegacyProxiedRootGuard(beadsDir)
		if after, _ := os.ReadFile(legacyRootPath(beadsDir)); !bytes.Equal(after, before) {
			t.Fatalf("re-arming rewrote the marker")
		}
	})

	t.Run("concurrent arming leaves one complete marker", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, "proxieddb\n")
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { armLegacyProxiedRootGuard(beadsDir) })
		}
		wg.Wait()
		assertLegacyRootArmed(t, repo, beadsDir)
		if body, _ := os.ReadFile(legacyRootPath(beadsDir)); string(body) != legacyProxiedRootGuardText() {
			t.Fatalf("concurrent arming left a partial marker:\n%s", body)
		}
	})
}

// TestLegacyProxiedRootGuardAfterOpen pins the wiring: the guard arms after a
// successful proxied open, and a failed open leaves the legacy root alone.
func TestLegacyProxiedRootGuardAfterOpen(t *testing.T) {
	for name, tc := range map[string]struct {
		openErr error
		armed   bool
	}{
		"success": {armed: true},
		"failure": {openErr: errors.New("open failed")},
	} {
		t.Run(name, func(t *testing.T) {
			repo, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
			armLegacyProxiedRootGuardAfterOpen(beadsDir, tc.openErr)
			if tc.armed {
				assertLegacyRootArmed(t, repo, beadsDir)
			} else {
				assertLegacyRootAbsent(t, beadsDir)
			}
		})
	}
}
