package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/uow"
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

// TestLegacyProxiedRootGuard pins GH#5104's marker: it is created only at an
// absent, effectively git-ignored legacy root, and every other entry or
// workspace is left exactly as it was.
func TestLegacyProxiedRootGuard(t *testing.T) {
	t.Run("template gitignore arms an absent root", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootArmed(t, repo, beadsDir)

		// Re-arming an armed root changes nothing.
		before, _ := os.ReadFile(legacyRootPath(beadsDir))
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		if after, _ := os.ReadFile(legacyRootPath(beadsDir)); !bytes.Equal(after, before) {
			t.Fatalf("re-arming rewrote the marker")
		}
	})

	t.Run("concurrent arming leaves one complete marker", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, "proxieddb\n")
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { armLegacyProxiedRootGuard(t.Context(), beadsDir) })
		}
		wg.Wait()
		assertLegacyRootArmed(t, repo, beadsDir)
		if body, _ := os.ReadFile(legacyRootPath(beadsDir)); string(body) != legacyProxiedRootGuardText() {
			t.Fatalf("concurrent arming left a partial marker:\n%s", body)
		}
	})

	t.Run("existing entries are never replaced", func(t *testing.T) {
		cases := map[string]func(t *testing.T, path string){
			"regular file": func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("someone else's bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			// A legacy root holding data, or a custom active root at or
			// below proxieddb: either way a directory is already there.
			"directory": func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Join(path, "beads", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			"symlink": func(t *testing.T, path string) {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink: %v", err)
				}
			},
			"dangling symlink": func(t *testing.T, path string) {
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Skipf("symlink: %v", err)
				}
			},
		}
		for name, plant := range cases {
			t.Run(name, func(t *testing.T) {
				_, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
				path := legacyRootPath(beadsDir)
				plant(t, path)
				before := legacyRootSnapshot(t, path)
				armLegacyProxiedRootGuard(t.Context(), beadsDir)
				if after := legacyRootSnapshot(t, path); after != before {
					t.Fatalf("guard changed the existing %s: before %q, after %q", name, before, after)
				}
			})
		}
	})

	t.Run("ineffective ignore rules skip arming", func(t *testing.T) {
		for name, gitignore := range map[string]string{
			"no gitignore":          "",
			"directory-only rule":   "dolt/\nproxieddb/\n",
			"later negation":        "proxieddb\n!proxieddb\n",
			"commented-out pattern": "# proxieddb\n",
		} {
			t.Run(name, func(t *testing.T) {
				_, beadsDir := legacyGuardWorkspace(t, gitignore)
				armLegacyProxiedRootGuard(t.Context(), beadsDir)
				assertLegacyRootAbsent(t, beadsDir)
			})
		}
	})

	t.Run("broad ignore without the local standalone pattern skips arming", func(t *testing.T) {
		// A parent .gitignore or info/exclude can make git ignore the path
		// while .beads/.gitignore holds only the directory-only rule: arming
		// waits for doctor's repair to add the standalone pattern.
		for name, plant := range map[string]func(t *testing.T, repo string){
			"parent gitignore": func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("proxieddb\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"info/exclude": func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("proxieddb\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				repo, beadsDir := legacyGuardWorkspace(t, "dolt/\nproxieddb/\n")
				if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o700); err != nil {
					t.Fatal(err)
				}
				plant(t, repo)
				if out, err := runGitIn(t, repo, "check-ignore", "-q", "--", ".beads/"+legacyProxiedRootName); err != nil {
					t.Fatalf("fixture: git does not ignore the legacy root: %v\n%s", err, out)
				}
				armLegacyProxiedRootGuard(t.Context(), beadsDir)
				assertLegacyRootAbsent(t, beadsDir)
			})
		}
	})

	t.Run("inherited git routing cannot redirect the check", func(t *testing.T) {
		// The workspace is in no Git worktree; inherited GIT_DIR and
		// GIT_WORK_TREE would point check-ignore at another repository whose
		// work tree is this workspace, where the template ignores the path.
		workspace := t.TempDir()
		beadsDir := filepath.Join(workspace, ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := runGitIn(t, beadsDir, "rev-parse", "--is-inside-work-tree"); err == nil {
			t.Skipf("temp dir is inside a Git worktree: %s", out)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte(doctor.GitignoreTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
		other := t.TempDir()
		initGitRepoAt(t, other)
		routed := gitCommand("check-ignore", "-q", "--", legacyProxiedRootName)
		routed.Dir = beadsDir
		routed.Env = append(testutil.WithoutGitRepoLocationEnv(os.Environ()),
			"GIT_DIR="+filepath.Join(other, ".git"), "GIT_WORK_TREE="+workspace, "GIT_INDEX_FILE="+filepath.Join(other, ".git", "index"))
		if out, err := routed.CombinedOutput(); err != nil {
			t.Fatalf("fixture: routed check-ignore does not report the path ignored: %v\n%s", err, out)
		}
		t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
		t.Setenv("GIT_WORK_TREE", workspace)
		t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootAbsent(t, beadsDir)
	})

	t.Run("tracked path skips arming", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, "proxieddb\n")
		path := legacyRootPath(beadsDir)
		if err := os.WriteFile(path, []byte("tracked"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "-f", "--", ".beads/" + legacyProxiedRootName}, {"commit", "-q", "--no-verify", "-m", "track proxieddb"}} {
			if out, err := runGitIn(t, repo, args...); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootAbsent(t, beadsDir)
	})

	t.Run("outside a Git worktree skips arming", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := runGitIn(t, beadsDir, "rev-parse", "--is-inside-work-tree"); err == nil {
			t.Skipf("temp dir is inside a Git worktree: %s", out)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte(doctor.GitignoreTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootAbsent(t, beadsDir)
	})

	t.Run("absent git skips arming", func(t *testing.T) {
		_, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
		t.Setenv("PATH", t.TempDir())
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootAbsent(t, beadsDir)
	})

	t.Run("unwritable beads dir skips arming", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permissions enforced for this user")
		}
		_, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
		if err := os.Chmod(beadsDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(beadsDir, 0o700) })
		armLegacyProxiedRootGuard(t.Context(), beadsDir)
		assertLegacyRootAbsent(t, beadsDir)
	})
}

// TestLegacyProxiedRootGuardLosesToConcurrentCreation pins the exclusive
// create: when another writer creates the legacy root between the guard's git
// probe and its create, the guard yields and the other writer's bytes stay
// exactly as written. The probe seam lands that writer deterministically.
func TestLegacyProxiedRootGuardLosesToConcurrentCreation(t *testing.T) {
	_, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
	path := legacyRootPath(beadsDir)
	const winner = "written by the winner"
	prev := runLegacyProxiedRootGitProbe
	runLegacyProxiedRootGitProbe = func(ctx context.Context, dir string, args ...string) error {
		if err := prev(ctx, dir, args...); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(winner), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	t.Cleanup(func() { runLegacyProxiedRootGitProbe = prev })

	armLegacyProxiedRootGuard(t.Context(), beadsDir)
	if got, err := os.ReadFile(path); err != nil || string(got) != winner {
		t.Fatalf("legacy root = %q, %v; want the concurrent writer's bytes %q", got, err, winner)
	}
}

// legacyRootSnapshot describes the entry at path (type, link target, bytes)
// without following a symlink.
func legacyRootSnapshot(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return "lstat error: " + err.Error()
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return "symlink:" + target + ":" + errString(err)
	case info.IsDir():
		entries, err := os.ReadDir(path)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return "dir:" + strings.Join(names, ",") + ":" + errString(err)
	default:
		body, err := os.ReadFile(path)
		return "file:" + info.Mode().String() + ":" + string(body) + ":" + errString(err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestLegacyProxiedRootGuardAfterOpen pins the wiring: the guard arms only
// after a successful managed-local proxied open that is not a preview; an
// external topology, a failed open or a --dry-run/--inspect preview leaves the
// legacy root alone, and the next ordinary open arms it.
func TestLegacyProxiedRootGuardAfterOpen(t *testing.T) {
	managed := sqlServerUOWTopology{database: "beads"}
	for name, tc := range map[string]struct {
		topology sqlServerUOWTopology
		opts     []uow.ProviderOption
		openErr  error
		armed    bool
	}{
		"managed-local success":           {topology: managed, armed: true},
		"managed-local read-only success": {topology: managed, opts: rootProviderOptions(false, true, false), armed: true},
		"managed-local preview success":   {topology: managed, opts: rootProviderOptions(true, true, false)},
		"managed-local failure":           {topology: managed, openErr: errors.New("open failed")},
		"external success":                {topology: sqlServerUOWTopology{database: "beads", external: &configfile.ExternalDoltConfig{}}},
	} {
		t.Run(name, func(t *testing.T) {
			repo, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
			armLegacyProxiedRootGuardAfterOpen(t.Context(), beadsDir, tc.topology, tc.opts, tc.openErr)
			if tc.armed {
				assertLegacyRootArmed(t, repo, beadsDir)
			} else {
				assertLegacyRootAbsent(t, beadsDir)
			}
		})
	}

	t.Run("preview, then an ordinary open", func(t *testing.T) {
		repo, beadsDir := legacyGuardWorkspace(t, doctor.GitignoreTemplate)
		armLegacyProxiedRootGuardAfterOpen(t.Context(), beadsDir, managed, rootProviderOptions(true, false, false), nil)
		assertLegacyRootAbsent(t, beadsDir)
		armLegacyProxiedRootGuardAfterOpen(t.Context(), beadsDir, managed, rootProviderOptions(false, false, false), nil)
		assertLegacyRootArmed(t, repo, beadsDir)
	})
}
