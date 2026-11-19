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
