package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
)

// TestUninstallHooksUnsetsBeadsRole verifies AC2: bd hooks uninstall clears
// beads.role in addition to core.hooksPath, so a manual `rm -rf .beads/`
// followed by a proper uninstall doesn't leave stale beads-managed git
// config behind (GH#4440).
func TestUninstallHooksUnsetsBeadsRole(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		cmd := exec.Command("git", "config", "beads.role", "primary")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to set beads.role: %v", err)
		}

		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed: %v", err)
		}

		getCmd := exec.Command("git", "config", "--get", "beads.role")
		getCmd.Dir = tmpDir
		out, err := getCmd.Output()
		if err == nil {
			t.Errorf("expected beads.role to be unset after uninstallHooks(), got %q", strings.TrimSpace(string(out)))
		}
	})
}

// TestUninstallHooksNoBeadsRoleIsNotAnError verifies that an already-absent
// beads.role is treated as success, not surfaced as a failure.
func TestUninstallHooksNoBeadsRoleIsNotAnError(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed when beads.role was never set: %v", err)
		}
	})
}

// A duplicated beads.role makes git refuse the unset as ambiguous — and it
// exits 5 to say so, the same code it uses for "key not set". Reading the key
// before unsetting is what keeps the two apart; treating exit 5 as success
// would report a clean uninstall with the key still set, which is precisely
// the failure AC2 exists to prevent.
func TestUninstallHooksReportsAmbiguousBeadsRole(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		for _, value := range []string{"primary", "secondary"} {
			cmd := exec.Command("git", "config", "--add", "beads.role", value)
			cmd.Dir = tmpDir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to add beads.role=%s: %v", value, err)
			}
		}

		err := uninstallHooks()
		if err == nil {
			t.Fatal("uninstallHooks() = nil, want an error: git cannot unset a multi-valued beads.role")
		}
		if !strings.Contains(err.Error(), "beads.role") {
			t.Errorf("error %q does not name beads.role", err)
		}

		// And the key really is still set — the error was not spurious.
		getCmd := exec.Command("git", "config", "--get-all", "beads.role")
		getCmd.Dir = tmpDir
		out, getErr := getCmd.Output()
		if getErr != nil {
			t.Fatalf("expected beads.role to still be set, --get-all failed: %v", getErr)
		}
		if len(strings.Fields(string(out))) != 2 {
			t.Errorf("beads.role values = %q, want both still present", strings.TrimSpace(string(out)))
		}
	})
}

// TestResetHooksPathIfBeadsManagedReportsFailureLoudly verifies AC2: when
// the underlying git config command genuinely fails, resetHooksPathIfBeadsManaged
// returns a non-nil error (and thus uninstallHooks propagates it) instead of
// silently printing a scrolling stderr warning and reporting success.
func TestResetHooksPathIfBeadsManagedReportsFailureLoudly(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDir := filepath.Join(tmpDir, ".git")
		info, err := os.Stat(gitDir)
		if err != nil {
			t.Fatalf("failed to stat .git: %v", err)
		}
		orig := info.Mode()

		// There has to be something to unset, or there is no git invocation to
		// fail: the reset reads beads.role first and only unsets it when it is
		// actually present.
		setCmd := exec.Command("git", "config", "beads.role", "primary")
		setCmd.Dir = tmpDir
		if err := setCmd.Run(); err != nil {
			t.Fatalf("failed to set beads.role: %v", err)
		}

		// Make .git/ read+execute only (no write), so `git rev-parse` (used to
		// resolve repoRoot) and `git config --get` still succeed but
		// `git config --unset` cannot create its lockfile — a genuine failure,
		// which resetHooksPathIfBeadsManaged must not swallow.
		if err := os.Chmod(gitDir, 0555); err != nil {
			t.Fatalf("failed to chmod .git: %v", err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(gitDir, orig)
		})

		err = resetHooksPathIfBeadsManaged()
		if err == nil {
			t.Fatal("expected resetHooksPathIfBeadsManaged to return an error when the config lockfile cannot be created")
		}
		if !strings.Contains(err.Error(), "beads.role") {
			t.Errorf("error %q does not name the key it failed to unset", err)
		}
	})
}

func TestResetRolePreservesSelectedGitContext(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"ordinary", "selected_repository", "bare_external", "invalid", "inline_absent", "selected_config", "global_role", "config_lock"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
				t.Setenv(key, home)
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			global := filepath.Join(home, ".gitconfig")
			globalData := "[user]\n\tname = fixture global\n"
			if name == "global_role" {
				globalData += "[beads]\n\trole = global-default\n"
			}
			if err := os.WriteFile(global, []byte(globalData), 0o600); err != nil {
				t.Fatal(err)
			}
			query := func(dir string, args ...string) ([]byte, error) {
				cmd := exec.Command("git", args...)
				cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
				return cmd.CombinedOutput()
			}
			must := func(dir string, args ...string) string {
				out, err := query(dir, args...)
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			cwd, decoy := newGitRepo(t), newGitRepo(t)
			selected := filepath.Join(cwd, ".git")
			for _, repo := range []string{cwd, decoy} {
				must(repo, "config", "beads.role", "primary")
				must(repo, "read-tree", "--empty")
				if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			must(decoy, "config", "beads.role", "decoy")
			switch name {
			case "selected_repository":
				selected = filepath.Join(decoy, ".git")
				t.Setenv("GIT_DIR", selected)
				t.Setenv("GIT_WORK_TREE", decoy)
			case "bare_external":
				root := t.TempDir()
				selected, cwd = filepath.Join(root, "selected bare.git"), filepath.Join(root, "external tree")
				must(root, "init", "--bare", selected)
				if err := os.Mkdir(cwd, 0o750); err != nil {
					t.Fatal(err)
				}
				must(root, "--git-dir", selected, "config", "beads.role", "primary")
				t.Setenv("GIT_DIR", filepath.Join("..", "selected bare.git"))
				t.Setenv("GIT_WORK_TREE", cwd)
			case "invalid":
				t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing.git"))
			case "inline_absent":
				must(cwd, "config", "--unset", "beads.role")
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
				t.Setenv("GIT_CONFIG_VALUE_0", "forged")
			case "selected_config":
				t.Setenv("GIT_CONFIG", filepath.Join(decoy, ".git", "config"))
				probe := exec.Command("git", "--git-dir", selected, "config", "beads.routing-test", "selected-file")
				probe.Dir = cwd // This fixture precondition deliberately inherits GIT_CONFIG.
				if out, err := probe.CombinedOutput(); err != nil {
					t.Fatalf("selected-file probe: %v\n%s", err, out)
				}
				if got := must(decoy, "config", "--get", "beads.routing-test"); got != "selected-file" {
					t.Fatalf("GIT_CONFIG did not select decoy: %q", got)
				}
				must(decoy, "config", "--unset", "beads.routing-test")
			case "config_lock":
				if err := os.WriteFile(filepath.Join(selected, "config.lock"), []byte("owned lock"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			saved := map[string][]byte{global: []byte(globalData)}
			for _, repo := range []string{cwd, decoy} {
				for _, path := range []string{filepath.Join(repo, ".git", "config"), filepath.Join(repo, ".git", "index"), filepath.Join(repo, ".git", "hooks", "pre-commit")} {
					if path == filepath.Join(selected, "config") && name != "invalid" && name != "config_lock" {
						continue
					}
					if data, err := os.ReadFile(path); err == nil {
						saved[path] = data
					} else if !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			}
			t.Chdir(cwd)
			before := strings.Join(os.Environ(), "\x00")
			git.ResetCaches() // Poison is present before either actual entrypoint resolves its context.
			t.Cleanup(git.ResetCaches)
			var err error
			if name == "invalid" || name == "inline_absent" {
				err = resetHooksPathIfBeadsManaged()
			} else {
				err = uninstallHooks()
			}
			if name == "config_lock" {
				if err == nil || !strings.Contains(err.Error(), "beads.role") {
					t.Errorf("locked uninstall = %v, want beads.role failure", err)
				}
			} else if err != nil {
				t.Errorf("reset/uninstall failed: %v", err)
			}
			out, readErr := query(cwd, "--git-dir", selected, "config", "--local", "--get", "beads.role")
			if name == "invalid" || name == "config_lock" {
				if readErr != nil || strings.TrimSpace(string(out)) != "primary" {
					t.Errorf("retained selected role = %q, %v", out, readErr)
				}
			} else if exit, ok := readErr.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Errorf("selected local role remains: %q, %v", out, readErr)
			}
			for path, before := range saved {
				if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
					t.Errorf("unrelated file changed: %s (%v)", path, err)
				}
			}
			if strings.Join(os.Environ(), "\x00") != before {
				t.Error("reset changed inherited environment")
			}
		})
	}
}
