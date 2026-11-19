package main

import (
	"encoding/json"
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

	// AC-4: an older bd version already appended its shell section to a
	// python hook. The first install removes the section — byte-exact, no
	// re-injection, no stale-backup restore — and warns; a second install
	// changes nothing.
	t.Run("AC-4 install removes a complete stale bd section and warns", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
		corrupted := pythonPrePushFixture + "\n" + generateHookSection("pre-push")
		writeHookFile(t, hookPath, corrupted)

		// A stale one-time backup from the original install: restoring it
		// would discard every edit made since, so it must be left alone.
		backupPath := hookPath + ".backup"
		staleBackup := "#!/usr/bin/env python3\nprint('ancient snapshot')\n"
		if err := os.WriteFile(backupPath, []byte(staleBackup), 0755); err != nil {
			t.Fatal(err)
		}

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
			t.Fatalf("stale section not removed byte-exact:\n%s", got)
		}
		if !strings.Contains(stderr, "removed the stale section") {
			t.Fatalf("stderr must warn about the removal, got:\n%s", stderr)
		}

		backupAfter, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("stale .backup sidecar must survive the repair: %v", err)
		}
		if string(backupAfter) != staleBackup {
			t.Fatalf("stale .backup sidecar must be left untouched, got:\n%s", backupAfter)
		}

		// Second install run: the hook is clean now — nothing changes.
		var secondErr error
		stderr2 := captureStderr(t, func() {
			secondErr = installHooksWithOptions(managedHookNames, false, false, false, false)
		})
		if secondErr != nil {
			t.Fatalf("second install failed: %v", secondErr)
		}
		got2, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got2) != pythonPrePushFixture {
			t.Fatalf("second install must not rewrite the repaired hook:\n%s", got2)
		}
		if strings.Contains(stderr2, "removed the stale section") {
			t.Fatalf("second install must report no repair:\n%s", stderr2)
		}
	})

	t.Run("AC-4 install removes multiple complete stale bd sections", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
		corrupted := pythonPrePushFixture + "\n" + generateHookSection("pre-push") +
			"\nimport os\n" + generateHookSection("pre-push")
		writeHookFile(t, hookPath, corrupted)

		stderr := captureStderr(t, func() {
			if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
				t.Fatalf("install failed: %v", err)
			}
		})
		got, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		want := pythonPrePushFixture + "\nimport os\n"
		if string(got) != want {
			t.Fatalf("both stale sections must be removed, want:\n%q\ngot:\n%q", want, got)
		}
		if !strings.Contains(stderr, "removed the stale section") {
			t.Fatalf("stderr must warn about the removal, got:\n%s", stderr)
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

// AC-5: the non-shell policy must not change what happens to hooks a shell
// runs — injection, the one-time .backup sidecar, and marker refresh all
// keep working, and no skip warnings appear.
func TestInstallHooks_ShellHooksUnaffected(t *testing.T) {
	t.Run("AC-5 shell and no-shebang hooks keep injection and backup behavior", func(t *testing.T) {
		for _, tc := range []struct{ name, content string }{
			{"sh", "#!/bin/sh\necho custom-hook\n"},
			{"bash", "#!/usr/bin/env bash\necho custom-hook\n"},
			{"zsh", "#!/usr/bin/env zsh\necho custom-hook\n"},
			{"dash", "#!/usr/bin/env dash\necho custom-hook\n"},
			{"busybox sh", "#!/bin/busybox sh\necho custom-hook\n"},
			{"no shebang plain text", "echo custom-hook\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				repoDir := setupGuardTestRepo(t)
				hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
				writeHookFile(t, hookPath, tc.content)

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
				if !strings.Contains(string(got), "echo custom-hook") {
					t.Fatalf("user content not preserved:\n%s", got)
				}
				if !strings.Contains(string(got), hookSectionBeginPrefix) {
					t.Fatalf("bd section not injected:\n%s", got)
				}

				// The one-time .backup sidecar still snapshots the user content.
				backup, err := os.ReadFile(hookPath + ".backup")
				if err != nil {
					t.Fatalf(".backup sidecar missing: %v", err)
				}
				if string(backup) != tc.content {
					t.Fatalf(".backup must snapshot the original content, got:\n%s", backup)
				}

				if strings.Contains(stderr, "cannot execute") ||
					strings.Contains(stderr, "left untouched") ||
					strings.Contains(stderr, "manual inspection") {
					t.Fatalf("shell hook must not produce skip warnings, got:\n%s", stderr)
				}
			})
		}
	})

	t.Run("AC-5 shell hook with existing markers is refreshed, not skipped", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
		// A shell hook carrying an old-version bd section plus user content on
		// both sides of it.
		stale := "#!/bin/sh\necho before\n" +
			"# --- BEGIN BEADS INTEGRATION v0.0.1 ---\nold section body\n" +
			"# --- END BEADS INTEGRATION v0.0.1 ---\necho after\n"
		writeHookFile(t, hookPath, stale)

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
		content := string(got)
		if !strings.Contains(content, "echo before") || !strings.Contains(content, "echo after") {
			t.Fatalf("user content outside markers not preserved:\n%s", content)
		}
		if !strings.Contains(content, hookSectionBeginLine()) {
			t.Fatalf("section not refreshed to the current version:\n%s", content)
		}
		if strings.Contains(content, "old section body") {
			t.Fatalf("stale section body not replaced:\n%s", content)
		}
		if strings.Contains(stderr, "cannot execute") || strings.Contains(stderr, "left untouched") {
			t.Fatalf("marker refresh must not warn like a skip, got:\n%s", stderr)
		}
	})

	t.Run("AC-5 standalone install reports no skipped hooks for a shell repo", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			writeHookFile(t, filepath.Join(repoDir, ".git", "hooks", "pre-push"), "#!/bin/sh\necho custom-hook\n")

			var skipped []string
			var installErr error
			stderr := captureStderr(t, func() {
				skipped, installErr = installStandaloneHooks(managedHookNames, false, false, false, false)
			})
			if installErr != nil {
				t.Fatalf("install failed: %v", installErr)
			}
			if len(skipped) != 0 {
				t.Fatalf("skipped = %v, want empty", skipped)
			}
			if strings.Contains(stderr, "cannot execute") {
				t.Fatalf("shell repo must not produce skip diagnostics, got:\n%s", stderr)
			}
		})
	})
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

// The CLI report must tell the truth about a skipped hook (GH#6348): the
// human "Installed hooks" list excludes it, the skip is identified, and the
// JSON gains skipped_hooks only when something was skipped. Per-hook
// diagnostics stay on stderr.
func TestHooksInstallCmdReportsSkippedHooks(t *testing.T) {
	t.Run("AC-3 human report excludes the skipped hook and identifies the skip", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			writeHookFile(t, filepath.Join(repoDir, ".git", "hooks", "pre-push"), pythonPrePushFixture)

			stdout := captureStdout(t, func() error {
				return hooksInstallCmd.RunE(hooksInstallCmd, nil)
			})

			skipIdx := strings.Index(stdout, "Skipped hooks")
			if skipIdx < 0 {
				t.Fatalf("skip not identified in the report:\n%s", stdout)
			}
			beforeSkip := stdout[:skipIdx]
			if !strings.Contains(beforeSkip, "Installed hooks:") {
				t.Fatalf("installed list missing:\n%s", stdout)
			}
			if strings.Contains(beforeSkip, "pre-push") {
				t.Fatalf("skipped hook listed under Installed hooks:\n%s", stdout)
			}
			for _, hookName := range []string{"pre-commit", "post-merge", "post-checkout", "prepare-commit-msg"} {
				if !strings.Contains(beforeSkip, hookName) {
					t.Fatalf("%s missing from installed list:\n%s", hookName, stdout)
				}
			}
			if !strings.Contains(stdout[skipIdx:], "pre-push") {
				t.Fatalf("skipped section does not name pre-push:\n%s", stdout)
			}
		})
	})

	t.Run("AC-3 JSON gains skipped_hooks when a hook was skipped", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			writeHookFile(t, filepath.Join(repoDir, ".git", "hooks", "pre-push"), pythonPrePushFixture)

			oldJSON := jsonOutput
			jsonOutput = true
			defer func() { jsonOutput = oldJSON }()

			stdout := captureStdout(t, func() error {
				return hooksInstallCmd.RunE(hooksInstallCmd, nil)
			})

			var payload map[string]any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatalf("install output is not JSON:\n%s", stdout)
			}
			if payload["success"] != true {
				t.Fatalf("success missing from JSON:\n%s", stdout)
			}
			skipped, ok := payload["skipped_hooks"].([]any)
			if !ok || len(skipped) != 1 || skipped[0] != "pre-push" {
				t.Fatalf("skipped_hooks = %v, want [pre-push]:\n%s", payload["skipped_hooks"], stdout)
			}
			// Diagnostics stay on stderr; the JSON carries no interpreter text.
			if strings.Contains(stdout, "cannot execute") || strings.Contains(stdout, "bd hooks run pre-push") {
				t.Fatalf("diagnostics leaked into JSON:\n%s", stdout)
			}
		})
	})

	t.Run("AC-3 JSON has no skipped_hooks key on a clean install", func(t *testing.T) {
		repoDir := newGitRepo(t)
		runInDir(t, repoDir, func() {
			oldJSON := jsonOutput
			jsonOutput = true
			defer func() { jsonOutput = oldJSON }()

			stdout := captureStdout(t, func() error {
				return hooksInstallCmd.RunE(hooksInstallCmd, nil)
			})

			var payload map[string]any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatalf("install output is not JSON:\n%s", stdout)
			}
			if _, present := payload["skipped_hooks"]; present {
				t.Fatalf("skipped_hooks must be absent when nothing was skipped:\n%s", stdout)
			}
		})
	})
}
