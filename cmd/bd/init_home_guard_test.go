package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsUserHomeDir covers the guard decision used to refuse `bd init` directly
// in the user's home directory (GH#4635). The resolver is stubbed so the test
// does not depend on (or touch) the real home directory.
func TestIsUserHomeDir(t *testing.T) {
	home := t.TempDir()
	stubHomeDir(t, home)

	if !isUserHomeDir(home) {
		t.Errorf("isUserHomeDir(home) = false, want true for %q", home)
	}
	if sub := filepath.Join(home, "project"); isUserHomeDir(sub) {
		t.Errorf("isUserHomeDir(%q) = true, want false (subdir of home)", sub)
	}
	if parent := filepath.Dir(home); isUserHomeDir(parent) {
		t.Errorf("isUserHomeDir(%q) = true, want false (parent of home)", parent)
	}
}

// TestIsUserHomeDir_UnresolvableHome verifies the guard never fires when the
// home directory cannot be resolved (fail-open: better to allow init than to
// wrongly refuse every directory).
func TestIsUserHomeDir_UnresolvableHome(t *testing.T) {
	stubHomeDir(t, "")

	if isUserHomeDir("/some/dir") {
		t.Error("isUserHomeDir should be false when home is unresolvable")
	}
}

// TestGuardInitInHomeDir covers the conditions under which the guard declines
// to fire: an explicit BEADS_DIR, an already-tracked home, and any directory
// that simply is not home.
func TestGuardInitInHomeDir(t *testing.T) {
	t.Run("refuses_in_untracked_home", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		t.Setenv("BEADS_DIR", "")

		err := guardInitInHomeDir()
		if err == nil {
			t.Fatal("guardInitInHomeDir() = nil, want a refusal in an untracked home")
		}
		if !strings.Contains(err.Error(), "refusing to 'bd init' directly in your home directory") {
			t.Errorf("unexpected error text: %v", err)
		}
	})

	t.Run("allows_with_explicit_beads_dir", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		t.Setenv("BEADS_DIR", filepath.Join(home, "elsewhere", ".beads"))

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil when BEADS_DIR is explicit", err)
		}
	})

	t.Run("allows_when_home_is_already_a_git_repo", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		chdir(t, home)
		t.Setenv("BEADS_DIR", "")
		gitInitAt(t, home)

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil when home is already tracked", err)
		}
	})

	t.Run("allows_in_an_ordinary_project_dir", func(t *testing.T) {
		home := t.TempDir()
		stubHomeDir(t, home)
		project := filepath.Join(home, "project")
		if err := os.MkdirAll(project, 0o750); err != nil {
			t.Fatal(err)
		}
		chdir(t, project)
		t.Setenv("BEADS_DIR", "")

		if err := guardInitInHomeDir(); err != nil {
			t.Errorf("guardInitInHomeDir() = %v, want nil for a project dir under home", err)
		}
	})
}

// TestInitCommand_RefusesInHomeDirBeforeAnySideEffect runs the actual init
// command, which is where the guard's *position* is under test rather than its
// decision. Both flag shapes below reach a side effect before the guard's
// original location did:
//
//   - --reinit-local / --force run countExistingIssues, which opens a real
//     store (creating the data directory, and able to run migrations);
//   - --proxied-server dispatches to runInitProxiedServer, which calls
//     EnsureGitRepo — a `git init` in the current directory.
//
// So the assertion is not merely "it errors": nothing may exist afterwards.
func TestInitCommand_RefusesInHomeDirBeforeAnySideEffect(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
	}{
		{name: "plain", flags: nil},
		{name: "reinit_local", flags: map[string]string{"reinit-local": "true"}},
		{name: "force", flags: map[string]string{"force": "true"}},
		{name: "proxied_server", flags: map[string]string{"proxied-server": "true"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stubHomeDir(t, home)
			chdir(t, home)
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BD_NON_INTERACTIVE", "1")
			for name, value := range tc.flags {
				setInitFlag(t, name, value)
			}

			err := initCmd.RunE(initCmd, nil)
			if err == nil {
				t.Fatal("bd init in an untracked home returned nil, want a refusal")
			}
			if !strings.Contains(err.Error(), "refusing to 'bd init' directly in your home directory") {
				t.Fatalf("unexpected error: %v", err)
			}

			// A refusal that still left something behind is not a refusal.
			for _, name := range []string{".git", ".beads", "CLAUDE.md", "AGENTS.md", ".gitignore", ".agents", ".codex", ".claude"} {
				if _, statErr := os.Stat(filepath.Join(home, name)); statErr == nil {
					t.Errorf("init created %s in the home directory despite refusing", name)
				}
			}
		})
	}
}

// stubHomeDir points the guard's home resolver at dir for the duration of the
// test. Stubbing rather than setting $HOME is deliberate: the resolver prefers
// the OS account database, so $HOME alone would not move it.
func stubHomeDir(t *testing.T, dir string) {
	t.Helper()
	orig := resolveUserHomeDir
	resolveUserHomeDir = func() string { return dir }
	t.Cleanup(func() { resolveUserHomeDir = orig })
}

// gitInitAt makes dir a git repository.
func gitInitAt(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// setInitFlag sets a flag on the shared initCmd and restores it afterwards —
// cobra commands are package-level singletons, so a leaked flag would bleed
// into every later test in this package.
func setInitFlag(t *testing.T, name, value string) {
	t.Helper()
	flag := initCmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("init has no --%s flag", name)
	}
	orig := flag.Value.String()
	origChanged := flag.Changed
	if err := initCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("setting --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = initCmd.Flags().Set(name, orig)
		flag.Changed = origChanged
	})
}
