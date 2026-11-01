//go:build cgo

package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// TestInitInUntrackedHomeWithCallerBeadsDir_SetsUpOnlyTheWorkspace runs the
// opt-out the home refusal offers (`mkdir -p <dir> && BEADS_DIR=<dir>/.beads
// bd init`) from an untracked home, end to end. The refusal promises a
// different init, one that sets up the workspace and leaves the home alone:
// no git init, no agent files, no .gitignore. So the home is seeded with the
// files a real one has, and every one of them must come out unchanged, with
// nothing added beside them.
//
// Two shapes, because they reach different writes. A workspace outside the
// home is not "local" to the working directory, so the direct route skips the
// project .gitignore on its own; one under the home is local, and only
// keepHomeClean keeps init from appending to ~/.gitignore.
//
// A third puts the home inside another repository, as a dotfiles or monorepo
// checkout above it does. That repository is not the project's either (see
// guardInitInHomeDir), so it must come out as it went in: no commit and
// nothing staged, though the home holds a legacy ~/.beads for a bootstrap
// commit to pick up; no core.hooksPath or beads.role; and its origin not
// taken as the workspace's Dolt remote. It has no upstream remote: fork
// routing would create ~/.beads-planning under the suite's real HOME.
//
// The last two give that repository an origin git cannot reach, and run init
// with --reinit-local and with --force. A failed probe counts as Dolt history,
// so an early remote-safety check that still consulted the origin would
// refuse both with exit 10 (ExitRemoteDivergenceRefused).
//
// The home is stubbed rather than set through $HOME: the guard resolves the OS
// account home (resolveUserHomeDir), and HOME stays the suite's, which is
// where Dolt keeps its own global config. That is tool state, not the project
// setup this checks for.
func TestInitInUntrackedHomeWithCallerBeadsDir_SetsUpOnlyTheWorkspace(t *testing.T) {
	skipIfNoDolt(t)

	cases := []struct {
		name string
		// workspace returns the directory that will hold .beads.
		workspace func(t *testing.T, home string) string
		// enclosed puts the home inside a git repository.
		enclosed bool
		// originGone leaves nothing at that repository's origin.
		originGone bool
		// flags are added to init's arguments.
		flags []string
	}{
		{name: "workspace_outside_home", workspace: func(t *testing.T, _ string) string { return t.TempDir() }},
		{name: "workspace_under_home", workspace: func(_ *testing.T, home string) string { return filepath.Join(home, "notes") }},
		{name: "home_inside_a_repository", workspace: func(t *testing.T, _ string) string { return t.TempDir() }, enclosed: true},
		{name: "unreachable_origin_reinit_local", workspace: func(t *testing.T, _ string) string { return t.TempDir() }, enclosed: true, originGone: true, flags: []string{"--reinit-local"}},
		{name: "unreachable_origin_force", workspace: func(t *testing.T, _ string) string { return t.TempDir() }, enclosed: true, originGone: true, flags: []string{"--force"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetInitFlagsForServerInit(t)

			home := t.TempDir()
			enclosing := ""
			if tc.enclosed {
				enclosing, home = home, filepath.Join(home, "home")
				if err := os.Mkdir(home, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			stubHomeDir(t, home)
			workspace := tc.workspace(t, home)
			if err := os.MkdirAll(workspace, 0o750); err != nil {
				t.Fatal(err)
			}
			beadsDir := filepath.Join(workspace, ".beads")
			stubCallerBeadsDir(t, beadsDir)
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("BD_NON_INTERACTIVE", "1")

			// What a home already has, and what init would otherwise write or
			// append to there.
			if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o750); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(home, "AGENTS.md"), "# my own agent notes\n")
			writeTestFile(t, filepath.Join(home, ".claude", "settings.json"), "{\"model\": \"mine\"}\n")
			writeTestFile(t, filepath.Join(home, ".gitignore"), "*.swp\n")
			var enclosingConfig []byte
			if tc.enclosed {
				enclosingConfig = initEnclosingRepo(t, enclosing, home, !tc.originGone)
			}
			before := snapshotHomeTree(t, home, workspace)

			t.Chdir(home)
			beads.ResetCaches()
			git.ResetCaches()
			origStore, origDBPath := store, dbPath
			t.Cleanup(func() {
				if store != nil && store != origStore {
					_ = store.Close()
				}
				store, dbPath = origStore, origDBPath
				rootCmd.SetArgs(nil)
				beads.ResetCaches()
				git.ResetCaches()
			})
			store, dbPath = nil, ""

			// The enclosed home runs without --quiet: its repository has an
			// origin and no upstream, which is when init advises setting one,
			// and the opt-out's note has to show.
			args := []string{"init", "--server", "--database", uniqueTestDBName(t), "--prefix", "hg"}
			if !tc.enclosed {
				args = append(args, "--quiet")
			}
			args = append(args, tc.flags...)
			rootCmd.SetArgs(args)
			var execErr error
			stderr := captureStderr(t, func() { execErr = rootCmd.Execute() })
			if execErr != nil {
				t.Fatalf("BEADS_DIR=%s bd init from an untracked home failed: %v\n%s", beadsDir, execErr, stderr)
			}

			if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err != nil {
				t.Fatalf("init did not set up the workspace it was named: %v", err)
			}
			after := snapshotHomeTree(t, home, workspace)
			for path, was := range before {
				if now, ok := after[path]; !ok {
					t.Errorf("init removed ~/%s", path)
				} else if now != was {
					t.Errorf("init changed ~/%s:\nbefore: %q\nafter:  %q", path, was, now)
				}
			}
			for path := range after {
				if _, ok := before[path]; !ok {
					t.Errorf("init created ~/%s in the home directory", path)
				}
			}
			if tc.enclosed {
				if !strings.Contains(stderr, "this is your home directory") {
					t.Errorf("init did not note what the opt-out sets up:\n%s", stderr)
				}
				if strings.Contains(stderr, "Git upstream not configured") {
					t.Errorf("init advised an upstream for the repository around the home:\n%s", stderr)
				}
				assertEnclosingRepoUntouched(t, enclosing, enclosingConfig, beadsDir)
			}
		})
	}
}

// initEnclosingRepo makes dir, the directory around home, a repository with
// one commit and an origin, and gives home a legacy ~/.beads for a bootstrap
// commit to pick up. Unless originReachable, nothing exists at the origin's
// path, so git cannot reach it. It returns the repository's config as it
// stands.
func initEnclosingRepo(t *testing.T, dir, home string, originReachable bool) []byte {
	t.Helper()
	gitInitAt(t, dir)
	origin := filepath.Join(t.TempDir(), "dotfiles.git")
	steps := [][]string{
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=", "commit", "--allow-empty", "--quiet", "-m", "dotfiles"},
		{"remote", "add", "origin", origin},
	}
	if originReachable {
		steps = append(steps, []string{"init", "--quiet", "--bare", origin})
	}
	for _, args := range steps {
		if out, err := initArtifactGitCommand(dir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Mkdir(filepath.Join(home, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".beads", "config.yaml"), "# legacy user-level config\n")
	gitConfig, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	return gitConfig
}

// assertEnclosingRepoUntouched checks that init left the repository around the
// home as initEnclosingRepo made it: one commit, nothing staged, the same
// config (so no core.hooksPath and no beads.role), and its origin not taken as
// the workspace's Dolt remote.
func assertEnclosingRepoUntouched(t *testing.T, dir string, gitConfig []byte, beadsDir string) {
	t.Helper()
	run := func(args ...string) (string, error) {
		out, err := initArtifactGitCommand(dir, args...).Output()
		return strings.TrimSpace(string(out)), err
	}
	if count, err := run("rev-list", "--count", "HEAD"); err != nil || count != "1" {
		t.Errorf("init committed to the repository around the home: %s commits (%v)", count, err)
	}
	if staged, err := run("diff", "--cached", "--name-only"); err != nil || staged != "" {
		t.Errorf("init staged into the repository around the home: %q (%v)", staged, err)
	}
	for _, key := range []string{"core.hooksPath", "beads.role"} {
		if value, err := run("config", "--local", "--get", key); err == nil {
			t.Errorf("init set %s = %q in the repository around the home", key, value)
		}
	}
	if now, err := os.ReadFile(filepath.Join(dir, ".git", "config")); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(now, gitConfig) {
		t.Errorf("init changed the config of the repository around the home:\nbefore: %q\nafter:  %q", gitConfig, now)
	}
	if data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml")); err == nil && strings.Contains(string(data), "dotfiles.git") {
		t.Errorf("init took the origin of the repository around the home as the workspace's Dolt remote:\n%s", data)
	}
}

// snapshotHomeTree maps every path under home, relative to it, to its mode and,
// for a regular file, its content. It skips the workspace when that lives
// under home: the workspace is init's to write.
func snapshotHomeTree(t *testing.T, home, workspace string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == home {
			return nil
		}
		if path == workspace {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		tree[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
