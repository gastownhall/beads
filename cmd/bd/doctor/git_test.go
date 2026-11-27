package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// setupGitRepo creates a temporary git repository for testing
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Use cached git template instead of spawning git init per test
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	return dir
}

func TestCheckGitHooks(t *testing.T) {
	// This test needs to run in a git repository
	// We test the basic case where hooks are not installed
	t.Run("not in git repo returns N/A", func(t *testing.T) {
		tmpDir := t.TempDir()
		runInDir(t, tmpDir, func() {
			check := CheckGitHooks("0.49.6")

			if check.Status != StatusOK {
				t.Errorf("expected status %q, got %q", StatusOK, check.Status)
			}
			if check.Message != "N/A (not a git repository)" {
				t.Errorf("unexpected message: %s", check.Message)
			}
		})
	})

	// The Fix links name live pages, not the pre-move repository
	// or its never-existing examples/git-hooks directory (GH#3059).
	assertHooksFixLink := func(t *testing.T, check DoctorCheck, want string) {
		t.Helper()
		if check.Status != StatusWarning {
			t.Fatalf("expected status %q, got %q (%s)", StatusWarning, check.Status, check.Message)
		}
		if !strings.Contains(check.Fix, want) {
			t.Errorf("Fix = %q, want it to link %s", check.Fix, want)
		}
		for _, stale := range []string{legacyRepoOrg, "examples/git-hooks"} {
			if strings.Contains(check.Fix, stale) {
				t.Errorf("Fix = %q contains stale %q", check.Fix, stale)
			}
		}
	}

	t.Run("missing hooks link the hooks reference", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		runInDir(t, tmpDir, func() {
			assertHooksFixLink(t, CheckGitHooks("0.49.6"), "https://beads.gascity.com/cli-reference/hooks")
		})
	})

	t.Run("hooks not calling bd link the upgrade issue", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		lefthook := "pre-commit:\n  commands:\n    lint:\n      run: echo lint\n"
		if err := os.WriteFile(filepath.Join(tmpDir, "lefthook.yml"), []byte(lefthook), 0o600); err != nil {
			t.Fatal(err)
		}
		runInDir(t, tmpDir, func() {
			assertHooksFixLink(t, CheckGitHooks("0.49.6"), "https://github.com/gastownhall/beads/issues/615")
		})
	})
}

// setupGitRepoInDir initializes a git repo in the given directory with .beads
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		// cmd.Dir = "" inherits the test process cwd — this checkout's own
		// package dir — so the config write below would hit the host .git
		// (the r880 clobber class, wy-qlvlmf).
		t.Fatal("setupGitRepoInDir: empty dir would run git against the host checkout")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("setupGitRepoInDir: %q is not an existing directory (err=%v)", dir, err)
	}

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Use cached git template instead of spawning git init per test
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Force repo-local hooks path for test isolation. This prevents global
	// core.hooksPath from affecting hook detection behavior in tests. The env
	// scrub keeps a leaked GIT_DIR/GIT_WORK_TREE from routing the write at
	// some other repository.
	cmd := gitCommand("config", "core.hooksPath", ".git/hooks")
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to set core.hooksPath for test repo: %v\n%s", err, out)
	}
}

// Edge case tests for CheckGitHooks

func TestCheckGitHooks_CorruptedHookFiles(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "pre-commit hook is directory instead of file",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create pre-commit as directory instead of file
				os.MkdirAll(filepath.Join(hooksDir, "pre-commit"), 0755)
				// create valid post-merge and pre-push hooks
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\nbd sync\n"), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\nbd sync\n"), 0755)
			},
			// os.Stat reports directories as existing, so CheckGitHooks sees it as installed
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "hook file with no execute permissions",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create hooks but with no execute permissions
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nbd sync\n"), 0644)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\nbd sync\n"), 0644)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\nbd sync\n"), 0644)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "empty hook file",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create empty hook files
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(""), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(""), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(""), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "hook file with binary content",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create hooks with binary content
				binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), binaryContent, 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), binaryContent, 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), binaryContent, 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "outdated bd hook versions are flagged",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				oldHook := "#!/bin/sh\n# bd-hooks-version: 0.49.1\nbd hooks run pre-push\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(oldHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(oldHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(oldHook), 0755)
			},
			expectedStatus: "warning",
			expectInMsg:    "outdated",
		},
		{
			name: "current bd hook versions are accepted",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				currentHook := "#!/bin/sh\n# bd-hooks-version: 0.49.6\nbd hooks run pre-push\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(currentHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(currentHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(currentHook), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "marker-managed hooks at current version are accepted (GH#2244)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				markerHook := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n" +
					"# This section is managed by beads. Do not remove these markers.\n" +
					"if command -v bd >/dev/null 2>&1; then\n" +
					"  export BD_GIT_HOOK=1\n" +
					"  bd hooks run pre-commit \"$@\"\n" +
					"  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
					"fi\n" +
					"# --- END BEADS INTEGRATION ---\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(markerHook), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "marker-managed hooks at older version are flagged outdated",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				markerHook := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.49.1 ---\n" +
					"if command -v bd >/dev/null 2>&1; then\n" +
					"  bd hooks run pre-commit \"$@\"\n" +
					"fi\n" +
					"# --- END BEADS INTEGRATION ---\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(markerHook), 0755)
			},
			expectedStatus: "warning",
			expectInMsg:    "outdated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			runInDir(t, tmpDir, func() {
				check := CheckGitHooks("0.49.6")

				if check.Status != tt.expectedStatus {
					t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
				}
				if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
					t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
				}
			})
		})
	}
}

// Tests for CheckOrphanedIssues

// TestParseBDHookVersion verifies version extraction from all hook formats (GH#2244).
func TestParseBDHookVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "legacy bd-hooks-version comment",
			content:     "#!/bin/sh\n# bd-hooks-version: 0.55.0\nbd hooks run pre-commit\n",
			wantVersion: "0.55.0",
			wantOK:      true,
		},
		{
			name:        "section marker format",
			content:     "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n",
			wantVersion: "0.57.0",
			wantOK:      true,
		},
		{
			name:        "section marker with user content before",
			content:     "#!/bin/sh\n# my custom stuff\necho hello\n# --- BEGIN BEADS INTEGRATION v0.56.1 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n",
			wantVersion: "0.56.1",
			wantOK:      true,
		},
		{
			name:    "no version markers at all",
			content: "#!/bin/sh\necho hello\n",
			wantOK:  false,
		},
		{
			name:    "empty content",
			content: "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, ok := parseBDHookVersion(tt.content)
			if ok != tt.wantOK {
				t.Errorf("parseBDHookVersion() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && version != tt.wantVersion {
				t.Errorf("parseBDHookVersion() version = %q, want %q", version, tt.wantVersion)
			}
		})
	}
}

// TestIsBdHookContent verifies detection of all bd hook formats (GH#2244).
func TestIsBdHookContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"shim marker", "#!/bin/sh\n# bd-shim 0.55.0\nbd hooks run pre-commit\n", true},
		{"inline marker", "#!/bin/sh\n# bd (beads)\nbd sync\n", true},
		{"section marker", "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n", true},
		{"bd hooks run call", "#!/bin/sh\nbd hooks run pre-commit\n", true},
		{"not a bd hook", "#!/bin/sh\necho hello\n", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBdHookContent(tt.content)
			if got != tt.want {
				t.Errorf("IsBdHookContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckOrphanedIssues_DoltBackend verifies that CheckOrphanedIssues returns
// N/A for the Dolt backend (orphan detection not yet reimplemented for Dolt).
func TestCheckOrphanedIssues_DoltBackend(t *testing.T) {
	check := CheckOrphanedIssues(t.TempDir())

	if check.Status != StatusOK {
		t.Errorf("expected status %q, got %q", StatusOK, check.Status)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("expected N/A message, got %q", check.Message)
	}
}

func TestCheckStaleLegacyHooks(t *testing.T) {
	t.Run("no legacy files", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q: %s", check.Status, check.Message)
			}
		})
	})

	t.Run("stale legacy hook with removed bd hook command", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.legacy"),
			"#!/bin/sh\nexec bd hook pre-commit \"$@\"\n")
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusWarning {
				t.Errorf("expected warning, got %q: %s", check.Status, check.Message)
			}
			if !strings.Contains(check.Detail, "pre-commit.legacy") {
				t.Errorf("expected detail to mention pre-commit.legacy, got %q", check.Detail)
			}
		})
	})

	t.Run("legacy hook with current bd hooks run command is OK", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.legacy"),
			"#!/bin/sh\nbd hooks run pre-commit \"$@\"\n")
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q: %s", check.Status, check.Message)
			}
		})
	})

	t.Run("not in git repo", func(t *testing.T) {
		dir := t.TempDir()
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q", check.Status)
			}
		})
	})
}

// TestNonShellHookInterpreter pins which hook files bd treats as run by a
// non-shell interpreter, so it never splices its POSIX-sh section into them
// (GH#6348). The first rows are adapted from cyb3rd0g01's table in PR #6625;
// an `env` with no command is ambiguous here and so counts as non-shell.
func TestNonShellHookInterpreter(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantInterp   string
		wantNonShell bool
	}{
		{"python3 via env", "#!/usr/bin/env python3\nimport sys\n", "python3", true},
		{"python direct path", "#!/usr/bin/python3\nimport sys\n", "python3", true},
		{"node via env", "#!/usr/bin/env node\nconsole.log('hi')\n", "node", true},
		{"ruby via env", "#!/usr/bin/env ruby\nputs 'hi'\n", "ruby", true},
		{"sh via env is fine", "#!/usr/bin/env sh\necho hi\n", "", false},
		{"bash direct path is fine", "#!/bin/bash\necho hi\n", "", false},
		{"zsh is fine", "#!/usr/bin/env zsh\necho hi\n", "", false},
		{"no shebang at all", "echo hi\n", "", false},
		{"empty file", "", "", false},
		{"env with no interpreter argument", "#!/usr/bin/env\n", "/usr/bin/env", true},

		{"fish", "#!/usr/bin/env fish\nexit 0\n", "fish", true},
		{"perl with flags", "#!/usr/bin/perl -w\n", "perl", true},
		{"dash", "#!/bin/dash\n", "", false},
		{"ksh93", "#!/usr/bin/ksh93\n", "", false},
		{"space after bang", "#! /bin/sh\n", "", false},
		{"crlf sh", "#!/bin/sh\r\necho hi\r\n", "", false},
		{"crlf python", "#!/usr/bin/env python3\r\nimport sys\r\n", "python3", true},
		{"env -S bash -e", "#!/usr/bin/env -S bash -e\n", "", false},
		{"env -Sbash", "#!/usr/bin/env -Sbash -e\n", "", false},
		{"env -S python3 -u", "#!/usr/bin/env -S python3 -u\n", "python3", true},
		{"env assignment then sh", "#!/usr/bin/env LC_ALL=C sh\n", "", false},
		{"env -i assignment python", "#!/usr/bin/env -i PATH=/usr/bin python3\n", "python3", true},
		{"env -u NAME bash", "#!/usr/bin/env -u FOO bash\n", "", false},
		{"env -- sh", "#!/usr/bin/env -- sh\n", "", false},
		{"env unknown option", "#!/usr/bin/env --chdir=/tmp sh\n", "/usr/bin/env --chdir=/tmp sh", true},
		{"busybox sh", "#!/bin/busybox sh\n", "", false},
		{"busybox ash", "#!/bin/busybox ash\n", "", false},
		{"busybox without applet", "#!/bin/busybox\n", "/bin/busybox", true},
		{"windows bash.exe", "#!C:\\Git\\bin\\bash.exe\n", "", false},
		{"windows python.exe", "#!C:/Python311/python.exe\n", "python", true},
		{"empty shebang", "#!\necho hi\n", "empty shebang", true},
		{"binary content", "\x7fELF\x02\x01\x01\x00", "binary content", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			interp, nonShell := NonShellHookInterpreter(tc.content)
			if nonShell != tc.wantNonShell {
				t.Fatalf("nonShell = %v, want %v (interp=%q)", nonShell, tc.wantNonShell, interp)
			}
			if interp != tc.wantInterp {
				t.Fatalf("interp = %q, want %q", interp, tc.wantInterp)
			}
		})
	}
}

// TestNonShellHookSectionPairs pins which bd sections in a non-shell hook bd
// may remove (GH#6348): whole-line, ordered pairs whose body is still the
// shell code bd writes. A body someone replaced, such as the issue's own port
// of the section to Python, is reported as edited so that bd never deletes it.
func TestNonShellHookSectionPairs(t *testing.T) {
	const (
		python  = "#!/usr/bin/env python3\nimport sys\nsys.exit(0)\n" // lines 1-3
		begin   = "# --- BEGIN BEADS INTEGRATION v1.2.2 ---\n"
		end     = "# --- END BEADS INTEGRATION v1.2.2 ---\n"
		managed = "# This section is managed by beads. Do not remove these markers.\n"
	)
	// The section bd wrote from GH#1380 until the timeout arrived: six lines.
	const guard = "if command -v bd >/dev/null 2>&1; then\n"
	shellBody := managed + guard +
		"  export BD_GIT_HOOK=1\n" +
		"  bd hooks run pre-push \"$@\"\n" +
		"  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
		"fi\n"
	// The same code reindented with tabs and trailing blanks.
	reindented := managed + guard +
		"\texport BD_GIT_HOOK=1 \n" +
		"\tbd  hooks run pre-push \"$@\"\n" +
		"\t_bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\t\n" +
		"fi\n"
	// The GH#6348 workaround: the shell code ported to Python in place, five
	// lines.
	pythonPort := managed +
		"import subprocess as _sp\n" +
		"_bd_exit = _sp.call([\"bd\", \"hooks\", \"run\", \"pre-push\"] + sys.argv[1:])\n" +
		"if _bd_exit != 0:\n" +
		"    sys.exit(_bd_exit)\n"

	cases := []struct {
		name        string
		content     string
		wantPairs   int
		wantProblem bool
		wantEdited  string
	}{
		{"no markers", python, 0, false, ""},
		{"bd shell section", python + begin + shellBody + end, 1, false, ""},
		{"crlf bd shell section", python + strings.ReplaceAll(begin+shellBody+end, "\n", "\r\n"), 1, false, ""},
		{"comment after fi", python + begin + shellBody + "# end of bd\n" + end, 1, false, ""},
		{"reindented", python + begin + reindented + end, 1, false, ""},
		{"section for another hook", python + begin + strings.Replace(shellBody, "pre-push", "pre-commit", 1) + end, 1, false, ""},
		{"code deleted, markers kept", python + begin + managed + "\n" + end, 1, false, ""},
		{"empty section", python + begin + end, 1, false, ""},
		{"python port", python + begin + pythonPort + end, 1, false, "lines 4-10"},
		{"code before the guard", python + begin + "print('mine')\n" + shellBody + end, 1, false, "lines 4-12"},
		{"code after the guard", python + begin + strings.Replace(shellBody, guard, guard+"print('mine')\n", 1) + end, 1, false, "lines 4-12"},
		{"code before fi", python + begin + strings.TrimSuffix(shellBody, "fi\n") + "print('mine')\nfi\n" + end, 1, false, "lines 4-12"},
		{"code after fi", python + begin + shellBody + "print('mine')\n" + end, 1, false, "lines 4-12"},
		{"statement changed", python + begin + strings.Replace(shellBody, "BD_GIT_HOOK=1", "BD_GIT_HOOK=0", 1) + end, 1, false, "lines 4-11"},
		{"statement removed", python + begin + strings.Replace(shellBody, "  export BD_GIT_HOOK=1\n", "", 1) + end, 1, false, "lines 4-10"},
		{"guard never closed by fi", python + begin + strings.TrimSuffix(shellBody, "fi\n") + end, 1, false, "lines 4-10"},
		{"no bd hooks run call", python + begin + managed + "if command -v bd >/dev/null 2>&1; then\n  :\nfi\n" + end, 1, false, "lines 4-9"},
		{"second of two pairs edited", python + begin + shellBody + end + begin + pythonPort + end, 2, false, "lines 12-18"},
		{"orphan begin", python + begin + pythonPort, 0, true, ""},
		{"nested", python + begin + begin + shellBody + end + end, 0, true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pairs, problem, edited := NonShellHookSectionPairs(tc.content)
			if pairs != tc.wantPairs || (problem != "") != tc.wantProblem || edited != tc.wantEdited {
				t.Fatalf("got pairs=%d problem=%q edited=%q, want pairs=%d problem=%v edited=%q",
					pairs, problem, edited, tc.wantPairs, tc.wantProblem, tc.wantEdited)
			}
		})
	}
}

// TestNonShellHookSectionPairsKnowsEveryShape runs each section bd has written
// (bdShellSectionShapes) through the classifier for every managed hook: the
// section is bd's, and one line added anywhere in its code, or a default
// timeout those versions never wrote, makes it edited (GH#6348).
func TestNonShellHookSectionPairsKnowsEveryShape(t *testing.T) {
	const python = "#!/usr/bin/env python3\nimport sys\n"
	section := func(code string) string {
		return python + "# --- BEGIN BEADS INTEGRATION v1.3.2 ---\n" +
			"# This section is managed by beads. Do not remove these markers.\n" +
			code + "# --- END BEADS INTEGRATION v1.3.2 ---\n"
	}
	for i, shape := range bdShellSectionShapes {
		for _, hook := range managedHookNames {
			code := strings.NewReplacer("@HOOK@", hook, "@TIMEOUT@", shape.timeout).Replace(strings.TrimPrefix(shape.code, "\n"))
			t.Run(fmt.Sprintf("shape_%d_timeout_%s_%s", i, shape.timeout, hook), func(t *testing.T) {
				if pairs, problem, edited := NonShellHookSectionPairs(section(code)); pairs != 1 || problem != "" || edited != "" {
					t.Fatalf("bd's own section must be removable, got pairs=%d problem=%q edited=%q", pairs, problem, edited)
				}
				lines := strings.SplitAfter(code, "\n")
				for at := range lines { // before the guard, between any two lines, after fi
					added := strings.Join(lines[:at], "") + "print('mine')\n" + strings.Join(lines[at:], "")
					if _, _, edited := NonShellHookSectionPairs(section(added)); edited == "" {
						t.Fatalf("a line added before code line %d must make the section edited", at+1)
					}
				}
				if shape.timeout != "" {
					other := strings.Replace(code, ":-"+shape.timeout+"}", ":-600}", 1)
					if other == code {
						t.Fatal("the shape's default timeout was not found")
					}
					if _, _, edited := NonShellHookSectionPairs(section(other)); edited == "" {
						t.Fatal("a default timeout bd never wrote must make the section edited")
					}
				}
			})
		}
	}
}
