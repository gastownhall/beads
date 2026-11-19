package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// GH#6348: bd's generated integration section is POSIX sh. Injecting it into
// a hook whose shebang names another interpreter (python3, node, ruby, ...)
// leaves a syntax error that breaks the whole file. These tests pin the
// non-shell policy across installation, preservation and migration.

// pythonPrePushFixture is the shape pre-commit and similar frameworks
// install: a Python script that ends in sys.exit(main()).
const pythonPrePushFixture = "#!/usr/bin/env python3\nimport sys\n\n\ndef main():\n    return 0\n\n\nif __name__ == \"__main__\":\n    sys.exit(main())\n"

// writeHookFile writes hook content (creating parent directories).
func writeHookFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallHooks_NonShell(t *testing.T) {
	t.Run("AC-1 python pre-push stays byte-identical, no backup, install succeeds", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
		writeHookFile(t, hookPath, pythonPrePushFixture)

		var installErr error
		stderr := captureStderr(t, func() {
			installErr = installHooksWithOptions(managedHookNames, false, false, false, false)
		})
		if installErr != nil {
			t.Fatalf("install failed: %v", installErr)
		}

		got, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != pythonPrePushFixture {
			t.Fatalf("python hook not byte-identical after install:\n%s", got)
		}
		if _, statErr := os.Lstat(hookPath + ".backup"); !os.IsNotExist(statErr) {
			t.Fatalf("skip must not create a .backup sidecar, stat err = %v", statErr)
		}
		if !strings.Contains(stderr, "pre-push") || !strings.Contains(stderr, "python3") {
			t.Fatalf("stderr warning must name the skipped hook and interpreter, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "bd hooks run pre-push") {
			t.Fatalf("stderr warning must include integration advice, got:\n%s", stderr)
		}

		// The other managed hooks install normally.
		for _, hookName := range []string{"pre-commit", "post-merge", "post-checkout", "prepare-commit-msg"} {
			content, err := os.ReadFile(filepath.Join(repoDir, ".git", "hooks", hookName))
			if err != nil {
				t.Fatalf("%s not installed: %v", hookName, err)
			}
			if !strings.Contains(string(content), hookSectionBeginPrefix) {
				t.Fatalf("%s missing bd section", hookName)
			}
		}
	})

	t.Run("AC-1 external integration non-shell hook is skipped silently", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
		delegating := "#!/usr/bin/env python3\nimport subprocess\nsubprocess.call(\"bd hooks run pre-push\", shell=True)\n"
		writeHookFile(t, hookPath, delegating)

		var installErr error
		stderr := captureStderr(t, func() {
			installErr = installHooksWithOptions(managedHookNames, false, false, false, false)
		})
		if installErr != nil {
			t.Fatalf("install failed: %v", installErr)
		}
		got, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != delegating {
			t.Fatalf("delegating python hook modified:\n%s", got)
		}
		if strings.Contains(stderr, "pre-push") {
			t.Fatalf("recognized external integration must stay silent, got:\n%s", stderr)
		}
	})

	t.Run("AC-1 node and ruby hooks are skipped too", func(t *testing.T) {
		for _, tc := range []struct{ name, content, interp string }{
			{"node", "#!/usr/bin/env node\nconsole.log('hi');\n", "node"},
			{"ruby", "#!/usr/bin/env ruby\nputs 'hi'\n", "ruby"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				repoDir := setupGuardTestRepo(t)
				hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
				writeHookFile(t, hookPath, tc.content)

				stderr := captureStderr(t, func() {
					if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
						t.Fatalf("install failed: %v", err)
					}
				})
				got, err := os.ReadFile(hookPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.content {
					t.Fatalf("hook modified:\n%s", got)
				}
				if !strings.Contains(stderr, tc.interp) {
					t.Fatalf("warning must name interpreter %q, got:\n%s", tc.interp, stderr)
				}
			})
		}
	})
}

// The classifier must treat the shells bd has always injected into as shell,
// so the guard never skips them (AC-5's classification half).
func TestInstallHooks_NonShellClassifierUsedByInstall(t *testing.T) {
	if doctor.ClassifyHookLanguage([]byte(pythonPrePushFixture)).Kind != doctor.HookLanguageNonShell {
		t.Fatal("python fixture must classify as non-shell")
	}
}

func TestInstallHooksBeads_NonShell(t *testing.T) {
	t.Run("AC-2 python pre-push preserved byte-identically into .beads/hooks", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			hooksDir := filepath.Join(repoDir, ".git", "hooks")
			writeHookFile(t, filepath.Join(hooksDir, "pre-push"), pythonPrePushFixture)

			// Unset the local core.hooksPath that newGitRepo sets so git falls
			// back to .git/hooks/ — the previously effective directory.
			gitCommand("config", "--unset", "core.hooksPath").Run()

			beadsDir := setupBeadsDir(t, repoDir)

			var installErr error
			_ = captureStderr(t, func() {
				installErr = installHooksWithOptions(managedHookNames, false, false, false, true)
			})
			if installErr != nil {
				t.Fatalf("beads-mode install failed: %v", installErr)
			}

			target := filepath.Join(beadsDir, "hooks", "pre-push")
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("python pre-push not preserved into %s: %v", target, err)
			}
			if string(got) != pythonPrePushFixture {
				t.Fatalf("preserved python hook not byte-identical:\n%s", got)
			}

			// Remaining managed hooks are installed normally.
			for _, hookName := range []string{"pre-commit", "post-merge", "post-checkout", "prepare-commit-msg"} {
				content, err := os.ReadFile(filepath.Join(beadsDir, "hooks", hookName))
				if err != nil {
					t.Fatalf("%s not installed: %v", hookName, err)
				}
				if !strings.Contains(string(content), hookSectionBeginPrefix) {
					t.Fatalf("%s missing bd section", hookName)
				}
			}

			// core.hooksPath selects the target directory.
			out, err := gitCommand("config", "--get", "core.hooksPath").CombinedOutput()
			if err != nil {
				t.Fatalf("core.hooksPath not set: %v", err)
			}
			if got, want := filepath.Clean(strings.TrimSpace(string(out))), filepath.Clean(filepath.Join(repoDir, ".beads", "hooks")); got != want {
				t.Fatalf("core.hooksPath = %q, want %q", got, want)
			}
		})
	})

	t.Run("AC-2 husky-source python hook is preserved without husky sanitization", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			huskyDir := filepath.Join(repoDir, ".husky")
			pyHook := "#!/usr/bin/env python3\n. \"$(dirname -- \"$0\")/_/husky.sh\"\nimport sys\nprint('hi')\n"
			writeHookFile(t, filepath.Join(huskyDir, "pre-commit"), pyHook)
			if out, err := gitCommand("config", "core.hooksPath", ".husky").CombinedOutput(); err != nil {
				t.Fatalf("set core.hooksPath: %v (%s)", err, out)
			}

			beadsDir := setupBeadsDir(t, repoDir)

			var installErr error
			_ = captureStderr(t, func() {
				installErr = installHooksWithOptions(managedHookNames, false, false, false, true)
			})
			if installErr != nil {
				t.Fatalf("beads-mode install failed: %v", installErr)
			}

			got, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
			if err != nil {
				t.Fatalf("python husky hook not preserved: %v", err)
			}
			if string(got) != pyHook {
				t.Fatalf("non-shell husky hook was rewritten instead of preserved byte-identically:\n%s", got)
			}
		})
	})

	t.Run("AC-2 python hook with stale bd section preserved with the section repaired", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			hooksDir := filepath.Join(repoDir, ".git", "hooks")
			// The corruption an older bd version left: a sh section appended
			// after sys.exit(main()).
			corrupted := pythonPrePushFixture + "\n" + generateHookSection("pre-push")
			writeHookFile(t, filepath.Join(hooksDir, "pre-push"), corrupted)

			gitCommand("config", "--unset", "core.hooksPath").Run()
			beadsDir := setupBeadsDir(t, repoDir)

			var installErr error
			stderr := captureStderr(t, func() {
				installErr = installHooksWithOptions(managedHookNames, false, false, false, true)
			})
			if installErr != nil {
				t.Fatalf("beads-mode install failed: %v", installErr)
			}

			got, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-push"))
			if err != nil {
				t.Fatalf("python pre-push not preserved: %v", err)
			}
			if string(got) != pythonPrePushFixture {
				t.Fatalf("preserved python hook must have the stale bd section removed and nothing else:\n%s", got)
			}
			if !strings.Contains(stderr, "removed a stale bd shell section") {
				t.Fatalf("stderr must report the repair, got:\n%s", stderr)
			}
		})
	})
}
