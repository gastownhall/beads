package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/git"
)

func TestDetectExistingHooks(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")

		tests := []struct {
			name                     string
			setupHook                string
			hookContent              string
			wantExists               bool
			wantIsBdHook             bool
			wantIsPreCommitFramework bool
		}{
			{
				name:       "no hook",
				setupHook:  "",
				wantExists: false,
			},
			{
				name:         "bd hook",
				setupHook:    "pre-commit",
				hookContent:  "#!/bin/sh\n# bd (beads) pre-commit hook\necho test",
				wantExists:   true,
				wantIsBdHook: true,
			},
			{
				name:                     "pre-commit framework hook",
				setupHook:                "pre-commit",
				hookContent:              "#!/bin/sh\n# pre-commit framework\npre-commit run",
				wantExists:               true,
				wantIsPreCommitFramework: true,
			},
			{
				name:        "custom hook",
				setupHook:   "pre-commit",
				hookContent: "#!/bin/sh\necho custom",
				wantExists:  true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				os.RemoveAll(hooksDir)
				os.MkdirAll(hooksDir, 0750)

				if tt.setupHook != "" {
					hookPath := filepath.Join(hooksDir, tt.setupHook)
					if err := os.WriteFile(hookPath, []byte(tt.hookContent), 0700); err != nil {
						t.Fatal(err)
					}
				}

				hooks := detectExistingHooks()

				var found *hookInfo
				for i := range hooks {
					if hooks[i].name == "pre-commit" {
						found = &hooks[i]
						break
					}
				}

				if found == nil {
					t.Fatal("pre-commit hook not found in results")
				}

				if found.exists != tt.wantExists {
					t.Errorf("exists = %v, want %v", found.exists, tt.wantExists)
				}
				if found.isBdHook != tt.wantIsBdHook {
					t.Errorf("isBdHook = %v, want %v", found.isBdHook, tt.wantIsBdHook)
				}
				if found.isPreCommitFramework != tt.wantIsPreCommitFramework {
					t.Errorf("isPreCommitFramework = %v, want %v", found.isPreCommitFramework, tt.wantIsPreCommitFramework)
				}
			})
		}
	})
}

func TestInstallGitHooks_NoExistingHooks(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")

		// Note: Can't fully test interactive prompt in automated tests
		// This test verifies the logic works when no existing hooks present
		// For full testing, we'd need to mock user input

		// Check hooks were created
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		postMergePath := filepath.Join(hooksDir, "post-merge")

		if _, err := os.Stat(preCommitPath); err == nil {
			content, _ := os.ReadFile(preCommitPath)
			if !strings.Contains(string(content), "bd (beads)") {
				t.Error("pre-commit hook doesn't contain bd marker")
			}
			if strings.Contains(string(content), "chained") {
				t.Error("pre-commit hook shouldn't be chained when no existing hooks")
			}
		}

		if _, err := os.Stat(postMergePath); err == nil {
			content, _ := os.ReadFile(postMergePath)
			if !strings.Contains(string(content), "bd (beads)") {
				t.Error("post-merge hook doesn't contain bd marker")
			}
		}
	})
}

func TestInstallGitHooks_ExistingHookBackup(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")

		// Ensure hooks directory exists
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create an existing pre-commit hook
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		existingContent := "#!/bin/sh\necho existing hook"
		if err := os.WriteFile(preCommitPath, []byte(existingContent), 0700); err != nil {
			t.Fatal(err)
		}

		// Detect that hook exists
		hooks := detectExistingHooks()

		hasExisting := false
		for _, hook := range hooks {
			if hook.exists && !hook.isBdHook && hook.name == "pre-commit" {
				hasExisting = true
				break
			}
		}

		if !hasExisting {
			t.Error("should detect existing non-bd hook")
		}
	})
}

func TestGenerateHookSection(t *testing.T) {
	section := generateHookSection("pre-commit")

	if !strings.Contains(section, hookSectionBeginPrefix) {
		t.Error("section missing begin marker")
	}
	if !strings.Contains(section, hookSectionEndPrefix) {
		t.Error("section missing end marker prefix")
	}
	if !strings.Contains(section, "bd hooks run pre-commit") {
		t.Error("section missing hook invocation")
	}
	if !strings.Contains(section, Version) {
		t.Errorf("section missing version %s", Version)
	}

	// Verify versioned END marker format
	expectedEnd := hookSectionEndLine()
	if !strings.Contains(section, expectedEnd) {
		t.Errorf("section missing versioned end marker %q\ngot:\n%s", expectedEnd, section)
	}
}

// TestGenerateHookSection_Timeout verifies the timeout wrapper around bd hooks run (GH#2453).
func TestGenerateHookSection_Timeout(t *testing.T) {
	section := generateHookSection("pre-push")

	// The duration is configurable but must be validated before it reaches a
	// helper command line.
	if !strings.Contains(section, "BEADS_HOOK_TIMEOUT") {
		t.Error("section missing BEADS_HOOK_TIMEOUT env var")
	}
	if !strings.Contains(section, fmt.Sprintf("%d", hookTimeoutSeconds)) {
		t.Errorf("section missing default timeout %d", hookTimeoutSeconds)
	}
	if !strings.Contains(section, `*[^0-9]*`) && !strings.Contains(section, `*[!0-9]*`) {
		t.Error("section missing positive-integer timeout validation")
	}
	if !strings.Contains(section, "invalid BEADS_HOOK_TIMEOUT") {
		t.Error("section missing invalid-timeout warning")
	}

	// A name match is insufficient: native Windows has an incompatible
	// timeout.exe. Require a successful GNU identity probe for timeout or
	// gtimeout before invoking it.
	if !strings.Contains(section, "for _bd_timeout_candidate in timeout gtimeout") {
		t.Error("section missing ordered timeout/gtimeout capability probes")
	}
	if !strings.Contains(section, `if _bd_timeout_version="$("$_bd_timeout_candidate" --version 2>/dev/null)"; then`) {
		t.Error("section does not require a successful version probe")
	}
	if !strings.Contains(section, `"timeout (GNU coreutils) "*`) {
		t.Error("section missing GNU coreutils identity check")
	}
	if !strings.Contains(section, `"timeout (uutils coreutils) "*`) {
		t.Error("section missing uutils coreutils identity check")
	}
	if !strings.Contains(section, `"$_bd_timeout_command" -- "$_bd_timeout"`) {
		t.Error("section missing GNU timeout argv separator")
	}
	// The Perl fallback must supervise bd, not alarm+exec it: a pending alarm
	// survives exec and the Go runtime ignores SIGALRM (GH#7753). It signals
	// bd's own process group, because bd catches the first SIGTERM and keeps
	// waiting on its children. TestGeneratedHookTimeoutProcessBoundary drives
	// this program on a real Perl.
	if strings.Contains(section, "alarm shift; exec @ARGV") {
		t.Error("section still execs bd under a pending perl alarm")
	}
	for _, want := range []string{
		"my $pid = fork;",
		"setpgrp(0, 0); exec { $ARGV[0] } @ARGV",
		"kill $sig, -$pid",
		"for qw(HUP INT TERM)",
		"exit 142 if $sent;",
		`' -- "$_bd_timeout" bd hooks run pre-push "$@"; then`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing perl supervisor fragment %q", want)
		}
	}
	// The supervisor is one single-quoted shell word inside the managed
	// section, so it must not end an orphaned section early (a blank line) or
	// look like the shim's exec line.
	for _, hookName := range managedHookNames {
		for i, line := range strings.Split(strings.TrimSuffix(generateHookSection(hookName), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s section line %d is blank", hookName, i+1)
			}
			if strings.HasPrefix(strings.TrimSpace(line), "exec") {
				t.Errorf("%s section line %d starts with exec: %q", hookName, i+1, line)
			}
		}
	}
	if !strings.Contains(section, "_bd_timeout_backend=perl") {
		t.Error("section missing scoped perl backend marker")
	}

	// GNU deadline statuses and the Perl supervisor's 142 are scoped to the
	// backend that can synthesize them. The direct fallback must not swallow a natural 124/142.
	if !strings.Contains(section, `"$_bd_exit" -eq 124`) {
		t.Error("section missing timeout exit code handling")
	}
	if !strings.Contains(section, `"$_bd_timeout_backend" = perl`) || !strings.Contains(section, `"$_bd_exit" -eq 142`) {
		t.Error("section missing perl-scoped supervisor timeout exit code handling")
	}
	if !strings.Contains(section, "timed out") {
		t.Error("section missing timeout warning message")
	}

	// Last-resort path is explicit when no timeout implementation is available.
	if !strings.Contains(section, "running without timeout") {
		t.Error("section missing clear fallback warning for systems without timeout support")
	}
}

func TestTrackedManagedHookSectionsMatchGenerator(t *testing.T) {
	for _, hookName := range managedHookNames {
		hookName := hookName
		t.Run(hookName, func(t *testing.T) {
			path := filepath.Join("..", "..", ".githooks", hookName)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read tracked hook %s: %v", path, err)
			}

			tracked := string(content)
			if strings.Count(tracked, hookSectionBeginPrefix) != 1 || strings.Count(tracked, hookSectionEndPrefix) != 1 {
				t.Fatal("tracked hook must contain exactly one managed section")
			}
			// Locate the section by marker PREFIX, not by the versioned marker
			// line. Tracked hooks drift for two reasons — generator content and
			// the release version stamp — and a prefix scan makes the second one
			// ordinary drift that the byte compare below reports and the regen
			// arm repairs. Matching hookSectionBeginLine() here would instead
			// fail with "missing marker" after a Version bump, before the regen
			// arm is reachable. The versioned markers are still verified: they
			// are part of `want`.
			begin := strings.Index(tracked, hookSectionBeginPrefix)
			if begin < 0 {
				t.Fatalf("tracked hook missing %q", hookSectionBeginPrefix)
			}
			relativeEnd := strings.Index(tracked[begin:], hookSectionEndPrefix)
			if relativeEnd < 0 {
				t.Fatalf("tracked hook missing %q", hookSectionEndPrefix)
			}
			end := begin + relativeEnd
			newline := strings.IndexByte(tracked[end:], '\n')
			if newline < 0 {
				t.Fatalf("tracked hook %q line is not newline-terminated", hookSectionEndPrefix)
			}
			end += newline + 1
			got, want := tracked[begin:end], generateHookSection(hookName)
			// A dedicated variable, not the package's shared BD_UPDATE_GOLDEN:
			// this arm rewrites tracked SOURCE files, so an unscoped
			// `BD_UPDATE_GOLDEN=1 go test ./cmd/bd/` must not silently convert
			// real hook drift into a working-tree mutation that reports PASS.
			if os.Getenv("BD_UPDATE_HOOKS_GOLDEN") == "1" {
				if got == want {
					return
				}
				if err := os.WriteFile(path, []byte(tracked[:begin]+want+tracked[end:]), 0o755); err != nil {
					t.Fatalf("write tracked hook: %v", err)
				}
				t.Logf("regenerated managed section in %s", path)
				return
			}
			if got != want {
				t.Fatalf("tracked managed section drifted from generator (regenerate with `make githooks-regen`)\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

// TestGenerateHookSection_DBNotInitialized verifies exit code 3 handling (GH#2449).
func TestGenerateHookSection_DBNotInitialized(t *testing.T) {
	section := generateHookSection("pre-commit")

	// Exit code 3 = beads database not initialized; hook must continue gracefully
	if !strings.Contains(section, `"$_bd_exit" -eq 3`) {
		t.Error("section missing exit code 3 (DB not initialized) handling")
	}
	if !strings.Contains(section, "database not initialized") {
		t.Error("section missing DB-not-initialized warning message")
	}

	// After handling exit code 3, the effective exit must be 0 (success)
	// Verify the pattern: set _bd_exit=0 after detecting code 3
	if !strings.Contains(section, `if [ "$_bd_exit" -eq 3 ]; then`) {
		t.Error("section missing exit code 3 conditional")
	}
}

// TestGenerateHookSection_HookNameInMessages verifies hook name appears in warning messages.
func TestGenerateHookSection_HookNameInMessages(t *testing.T) {
	for _, hook := range managedHookNames {
		section := generateHookSection(hook)
		// Each hook's timeout and DB-missing messages should include the hook name
		if !strings.Contains(section, "hook '"+hook+"'") {
			t.Errorf("section for %q missing hook name in warning messages", hook)
		}
	}
}

func TestInjectHookSection(t *testing.T) {
	section := generateHookSection("pre-commit")

	tests := []struct {
		name     string
		existing string
		wantHas  []string // substrings the result must contain
	}{
		{
			name:     "inject into empty file",
			existing: "#!/bin/sh\n",
			wantHas:  []string{"#!/bin/sh\n", hookSectionBeginPrefix, hookSectionEndPrefix},
		},
		{
			name:     "inject preserving user content",
			existing: "#!/bin/sh\necho before\n",
			wantHas:  []string{"echo before", hookSectionBeginPrefix, hookSectionEndPrefix},
		},
		{
			name:     "update existing section",
			existing: "#!/bin/sh\necho before\n# --- BEGIN BEADS INTEGRATION v0.40.0 ---\nold content\n# --- END BEADS INTEGRATION ---\necho after\n",
			wantHas:  []string{"echo before", "echo after", "bd hooks run pre-commit", hookSectionEndPrefix},
		},
		{
			name:     "orphaned BEGIN without END",
			existing: "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n",
			wantHas:  []string{"#!/bin/sh\n", hookSectionBeginPrefix, "bd hooks run pre-commit"},
		},
		{
			name: "orphaned BEGIN followed by valid block",
			existing: "#!/bin/sh\n" +
				"# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n" +
				"bd hook pre-commit \"$@\"\n" +
				"\n" +
				"# --- BEGIN BEADS INTEGRATION v0.58.0 ---\n" +
				"# This section is managed by beads. Do not remove these markers.\n" +
				"if command -v bd >/dev/null 2>&1; then\n" +
				"  export BD_GIT_HOOK=1\n" +
				"  bd hooks run pre-commit \"$@\"\n" +
				"  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
				"fi\n" +
				"# --- END BEADS INTEGRATION ---\n",
			wantHas: []string{"#!/bin/sh\n", hookSectionBeginPrefix, "bd hooks run pre-commit"},
		},
		{
			name: "reversed markers (END before BEGIN)",
			existing: "#!/bin/sh\necho user-linter\n" +
				"# --- END BEADS INTEGRATION ---\n" +
				"# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n" +
				"bd hook pre-commit \"$@\"\n",
			wantHas: []string{"#!/bin/sh\n", "echo user-linter", hookSectionBeginPrefix, "bd hooks run pre-commit"},
		},
		{
			name:     "update existing section with versioned END marker",
			existing: "#!/bin/sh\necho before\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nold content\n# --- END BEADS INTEGRATION v0.57.0 ---\necho after\n",
			wantHas:  []string{"echo before", "echo after", "bd hooks run pre-commit", hookSectionEndPrefix},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectHookSection(tt.existing, section)
			for _, want := range tt.wantHas {
				if !strings.Contains(result, want) {
					t.Errorf("result missing %q\ngot:\n%s", want, result)
				}
			}
			// Verify old content is not present when updating
			if tt.name == "update existing section" {
				if strings.Contains(result, "old content") {
					t.Error("old section content should have been replaced")
				}
				if strings.Contains(result, "v0.40.0") {
					t.Error("old version should have been replaced")
				}
			}
			// Verify broken marker scenarios leave exactly one clean section
			brokenCases := map[string]bool{
				"orphaned BEGIN without END":             true,
				"orphaned BEGIN followed by valid block": true,
				"reversed markers (END before BEGIN)":    true,
			}
			if brokenCases[tt.name] {
				beginCount := strings.Count(result, hookSectionBeginPrefix)
				if beginCount != 1 {
					t.Errorf("expected exactly 1 BEGIN marker, got %d\ngot:\n%s", beginCount, result)
				}
				endCount := strings.Count(result, hookSectionEndPrefix)
				if endCount != 1 {
					t.Errorf("expected exactly 1 END marker, got %d\ngot:\n%s", endCount, result)
				}
				if strings.Contains(result, "bd hook pre-commit") && !strings.Contains(result, "bd hooks run pre-commit") {
					t.Error("stale 'bd hook' command should have been removed")
				}
				if strings.Contains(result, "v0.57.0") {
					t.Error("stale v0.57.0 marker should have been removed")
				}
			}
		})
	}
}

func TestRemoveHookSection(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantFound bool
		wantHas   []string
		wantNot   []string
	}{
		{
			name:      "remove section preserving user content",
			content:   "#!/bin/sh\necho before\n\n" + generateHookSection("pre-commit") + "echo after\n",
			wantFound: true,
			wantHas:   []string{"echo before", "echo after"},
			wantNot:   []string{hookSectionBeginPrefix, hookSectionEndPrefix},
		},
		{
			name:      "no section to remove",
			content:   "#!/bin/sh\necho custom\n",
			wantFound: false,
			wantHas:   []string{"echo custom"},
		},
		{
			name:      "only section — leaves shebang",
			content:   "#!/bin/sh\n" + generateHookSection("pre-commit"),
			wantFound: true,
			wantNot:   []string{hookSectionBeginPrefix},
		},
		{
			name:      "orphaned BEGIN without END",
			content:   "#!/bin/sh\necho before\n\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n",
			wantFound: true,
			wantHas:   []string{"echo before"},
			wantNot:   []string{hookSectionBeginPrefix, "bd hook pre-commit"},
		},
		{
			name: "reversed markers (END before BEGIN)",
			content: "#!/bin/sh\necho user-linter\n" +
				"# --- END BEADS INTEGRATION ---\n" +
				"# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n" +
				"bd hook pre-commit \"$@\"\n",
			wantFound: true,
			wantHas:   []string{"echo user-linter"},
			wantNot:   []string{hookSectionBeginPrefix, hookSectionEndPrefix, "bd hook pre-commit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := removeHookSection(tt.content)
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			for _, want := range tt.wantHas {
				if !strings.Contains(result, want) {
					t.Errorf("result missing %q\ngot:\n%s", want, result)
				}
			}
			for _, notWant := range tt.wantNot {
				if strings.Contains(result, notWant) {
					t.Errorf("result should not contain %q\ngot:\n%s", notWant, result)
				}
			}
		})
	}
}

func TestInstallHooksWithSectionMarkers(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create an existing non-bd hook
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(preCommitPath, []byte("#!/bin/sh\necho my-linter\n"), 0700); err != nil {
			t.Fatal(err)
		}

		// Install hooks — should inject section, not replace file
		if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
			t.Fatalf("installHooksWithOptions() failed: %v", err)
		}

		// Verify pre-commit has both user content and section
		content, err := os.ReadFile(preCommitPath)
		if err != nil {
			t.Fatal(err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "echo my-linter") {
			t.Error("user content should be preserved")
		}
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Error("section marker should be present")
		}
		if !strings.Contains(contentStr, "bd hooks run pre-commit") {
			t.Error("hook invocation should be present")
		}

		// Run install again — should be idempotent (update section only)
		if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
			t.Fatalf("second installHooksWithOptions() failed: %v", err)
		}

		content2, _ := os.ReadFile(preCommitPath)
		if string(content2) != contentStr {
			t.Errorf("second install changed content:\nbefore:\n%s\nafter:\n%s", contentStr, string(content2))
		}
	})
}

func TestInstallHooksWithOptions_MockHookWithoutCurrentHook(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		mockHookName := "pre-commit-mock"
		mockHookPath := filepath.Join(hooksDir, mockHookName)

		if err := installHooksWithOptions([]string{mockHookName}, false, false, false, false); err != nil {
			t.Fatalf("installHooksWithOptions() failed: %v", err)
		}

		content, err := os.ReadFile(mockHookPath)
		if err != nil {
			t.Fatalf("failed to read mock hook: %v", err)
		}

		contentStr := string(content)
		if !strings.HasPrefix(contentStr, "#!/usr/bin/env sh\n") {
			t.Errorf("mock hook should start with shebang, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Errorf("mock hook should include managed section begin marker, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "bd hooks run "+mockHookName) {
			t.Errorf("mock hook should invoke bd hooks run %s, got:\n%s", mockHookName, contentStr)
		}
	})
}

func TestInstallHooksWithOptions_MockHookWithCurrentHook(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("failed to create hooks dir: %v", err)
		}

		mockHookName := "pre-commit-mock"
		mockHookPath := filepath.Join(hooksDir, mockHookName)
		existing := "#!/bin/sh\necho current-hook\n"
		if err := os.WriteFile(mockHookPath, []byte(existing), 0700); err != nil {
			t.Fatalf("failed to seed mock hook: %v", err)
		}

		if err := installHooksWithOptions([]string{mockHookName}, false, false, false, false); err != nil {
			t.Fatalf("installHooksWithOptions() failed: %v", err)
		}

		content, err := os.ReadFile(mockHookPath)
		if err != nil {
			t.Fatalf("failed to read mock hook: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "echo current-hook") {
			t.Errorf("existing hook content should be preserved, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Errorf("mock hook should include managed section begin marker, got:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "bd hooks run "+mockHookName) {
			t.Errorf("mock hook should invoke bd hooks run %s, got:\n%s", mockHookName, contentStr)
		}
	})
}

func TestInstallJJHooks_PreservesContentWithoutOldSidecars(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		postMergePath := filepath.Join(hooksDir, "post-merge")
		if err := os.WriteFile(preCommitPath, []byte("#!/bin/sh\necho jj-pre\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(postMergePath, []byte("#!/bin/sh\necho jj-post\n"), 0700); err != nil {
			t.Fatal(err)
		}

		if err := installJJHooks(); err != nil {
			t.Fatalf("installJJHooks() failed: %v", err)
		}

		preCommitContent, err := os.ReadFile(preCommitPath)
		if err != nil {
			t.Fatal(err)
		}
		postMergeContent, err := os.ReadFile(postMergePath)
		if err != nil {
			t.Fatal(err)
		}

		preCommitStr := string(preCommitContent)
		postMergeStr := string(postMergeContent)
		if !strings.Contains(preCommitStr, "echo jj-pre") {
			t.Error("pre-commit user content should be preserved")
		}
		if !strings.Contains(postMergeStr, "echo jj-post") {
			t.Error("post-merge user content should be preserved")
		}
		if !strings.Contains(preCommitStr, hookSectionBeginPrefix) {
			t.Error("pre-commit section marker should be present")
		}
		if !strings.Contains(postMergeStr, hookSectionBeginPrefix) {
			t.Error("post-merge section marker should be present")
		}

		if _, err := os.Stat(preCommitPath + ".old"); !os.IsNotExist(err) {
			t.Error("pre-commit .old sidecar should not be created in jj install path")
		}
		if _, err := os.Stat(postMergePath + ".old"); !os.IsNotExist(err) {
			t.Error("post-merge .old sidecar should not be created in jj install path")
		}

		preCommitOnce := preCommitStr
		postMergeOnce := postMergeStr
		if err := installJJHooks(); err != nil {
			t.Fatalf("second installJJHooks() failed: %v", err)
		}

		preCommitTwice, _ := os.ReadFile(preCommitPath)
		postMergeTwice, _ := os.ReadFile(postMergePath)
		if string(preCommitTwice) != preCommitOnce {
			t.Errorf("pre-commit changed on second jj install:\nbefore:\n%s\nafter:\n%s", preCommitOnce, string(preCommitTwice))
		}
		if string(postMergeTwice) != postMergeOnce {
			t.Errorf("post-merge changed on second jj install:\nbefore:\n%s\nafter:\n%s", postMergeOnce, string(postMergeTwice))
		}
	})
}

func TestUninstallHooksWithSectionMarkers(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create a hook with user content + beads section
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		hookContent := "#!/bin/sh\necho my-linter\n\n" + generateHookSection("pre-commit")
		if err := os.WriteFile(preCommitPath, []byte(hookContent), 0700); err != nil {
			t.Fatal(err)
		}

		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed: %v", err)
		}

		// File should still exist with user content, but no beads section
		content, err := os.ReadFile(preCommitPath)
		if err != nil {
			t.Fatal("hook file should still exist after uninstall")
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "echo my-linter") {
			t.Error("user content should be preserved after uninstall")
		}
		if strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Error("beads section should be removed after uninstall")
		}
	})
}

func TestUninstallHooksRemovesEmptyFile(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		hooksDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create a hook with only beads section (no user content)
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		hookContent := "#!/usr/bin/env sh\n" + generateHookSection("pre-commit")
		if err := os.WriteFile(preCommitPath, []byte(hookContent), 0700); err != nil {
			t.Fatal(err)
		}

		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed: %v", err)
		}

		// File should be removed entirely (only shebang left)
		if _, err := os.Stat(preCommitPath); !os.IsNotExist(err) {
			t.Error("hook file with only shebang should be removed entirely")
		}
	})
}

// TestConfigureBeadsHooksPath_AbsolutePath verifies that core.hooksPath is set to
// an absolute path so that git worktrees can find the hooks directory (GH#2414).
func TestConfigureBeadsHooksPath_AbsolutePath(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		// Create .beads/hooks/ directory
		beadsHooksDir := filepath.Join(tmpDir, ".beads", "hooks")
		if err := os.MkdirAll(beadsHooksDir, 0750); err != nil {
			t.Fatalf("Failed to create .beads/hooks/: %v", err)
		}

		if err := configureBeadsHooksPath(); err != nil {
			t.Fatalf("configureBeadsHooksPath() failed: %v", err)
		}

		// Read back core.hooksPath
		out, err := gitCommand("config", "--get", "core.hooksPath").Output()
		if err != nil {
			t.Fatalf("git config --get core.hooksPath failed: %v", err)
		}
		hooksPath := strings.TrimSpace(string(out))

		// Must be absolute
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("core.hooksPath should be absolute, got %q", hooksPath)
		}

		// Must point to .beads/hooks
		if !strings.HasSuffix(hooksPath, filepath.Join(".beads", "hooks")) {
			t.Errorf("core.hooksPath should end with .beads/hooks, got %q", hooksPath)
		}
	})
}

// TestInstallHooksBeads_WorktreeAccess verifies that hooks installed with --beads
// are accessible from a git worktree (GH#2414).
func TestInstallHooksBeads_WorktreeAccess(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		// Create .beads/ directory with metadata.json (needed for FindBeadsDir)
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("Failed to create .beads/: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0644); err != nil {
			t.Fatalf("Failed to create metadata.json: %v", err)
		}

		cmd := gitCommand("commit", "--allow-empty", "--no-verify", "-m", "init")
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v\n%s", err, string(output))
		}

		// Install hooks with --beads
		if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil {
			t.Fatalf("installHooksWithOptions(beads=true) failed: %v", err)
		}

		// Verify hooks exist in .beads/hooks/
		for _, hookName := range managedHookNames {
			hookPath := filepath.Join(beadsDir, "hooks", hookName)
			if _, err := os.Stat(hookPath); err != nil {
				t.Errorf("hook %s not found at %s", hookName, hookPath)
			}
		}

		// Read core.hooksPath and verify it's absolute
		out, err := gitCommand("config", "--get", "core.hooksPath").Output()
		if err != nil {
			t.Fatalf("core.hooksPath not set after --beads install: %v", err)
		}
		hooksPath := strings.TrimSpace(string(out))
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("core.hooksPath should be absolute for worktree compatibility, got %q", hooksPath)
		}

		// Create a worktree and verify hooks are accessible from it
		worktreeDir := filepath.Join(t.TempDir(), "worktree")
		cmd = gitCommand("worktree", "add", worktreeDir, "-b", "test-worktree")
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, string(output))
		}
		defer func() {
			gitCommand("worktree", "remove", worktreeDir).Run()
		}()

		// From the worktree, core.hooksPath should resolve to the same hooks
		cmd = gitCommand("config", "--get", "core.hooksPath")
		cmd.Dir = worktreeDir
		wtOut, err := cmd.Output()
		if err != nil {
			t.Fatalf("core.hooksPath not visible from worktree: %v", err)
		}
		wtHooksPath := strings.TrimSpace(string(wtOut))

		if wtHooksPath != hooksPath {
			t.Errorf("worktree core.hooksPath = %q, want %q", wtHooksPath, hooksPath)
		}

		// The hooks directory must actually exist at the resolved path
		if _, err := os.Stat(wtHooksPath); err != nil {
			t.Errorf("hooks directory not accessible from worktree at %q: %v", wtHooksPath, err)
		}

		// Verify a specific hook file exists
		preCommitPath := filepath.Join(wtHooksPath, "pre-commit")
		if _, err := os.Stat(preCommitPath); err != nil {
			t.Errorf("pre-commit hook not accessible from worktree: %v", err)
		}
	})
}

// setupBeadsDir creates .beads/ with a minimal metadata.json so FindBeadsDir works.
func setupBeadsDir(t *testing.T, repoDir string) string {
	t.Helper()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads/: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("failed to create metadata.json: %v", err)
	}
	return beadsDir
}

// TestInstallHooksBeads_PreservesGlobalHooks is a regression test: bd init sets
// a local core.hooksPath that shadows the global one, silently killing global
// hooks. The fix copies hooks from the effective directory before overriding.
func TestInstallHooksBeads_PreservesGlobalHooks(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(fakeHome, ".gitconfig"))

	globalHooksDir := filepath.Join(fakeHome, "global-hooks")
	if err := os.MkdirAll(globalHooksDir, 0755); err != nil {
		t.Fatalf("failed to create global hooks dir: %v", err)
	}
	globalHookContent := "#!/bin/sh\necho global-hook-marker\n"
	if err := os.WriteFile(filepath.Join(globalHooksDir, "pre-commit"), []byte(globalHookContent), 0755); err != nil {
		t.Fatalf("failed to write global pre-commit hook: %v", err)
	}

	setGlobal := gitCommand("config", "--global", "core.hooksPath", globalHooksDir)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("failed to set global core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Manual repo init (can't use newGitRepo which sets a local core.hooksPath).
	repoDir := t.TempDir()
	initCmd := gitCommand("init", "--initial-branch=main")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git config %v failed: %v", args, err)
		}
	}

	runInDir(t, repoDir, func() {
		beadsDir := setupBeadsDir(t, repoDir)

		if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil {
			t.Fatalf("installHooksWithOptions(beads=true) failed: %v", err)
		}

		content, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
		if err != nil {
			t.Fatalf("failed to read .beads/hooks/pre-commit: %v", err)
		}
		contentStr := string(content)

		if !strings.Contains(contentStr, "echo global-hook-marker") {
			t.Errorf("global hook content not preserved in .beads/hooks/pre-commit.\nGot:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Errorf("beads section marker missing.\nGot:\n%s", contentStr)
		}
	})
}

// TestInstallHooksBeads_PreservesDefaultGitHooks verifies that hooks in the
// default .git/hooks/ directory (both managed and non-managed) are preserved
// when beads redirects core.hooksPath to .beads/hooks/.
func TestInstallHooksBeads_PreservesDefaultGitHooks(t *testing.T) {
	repoDir := newGitRepo(t)
	runInDir(t, repoDir, func() {
		hooksDir := filepath.Join(repoDir, ".git", "hooks")
		if err := os.MkdirAll(hooksDir, 0755); err != nil {
			t.Fatalf("failed to create .git/hooks: %v", err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\necho custom-default-hook\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "commit-msg"), []byte("#!/bin/sh\necho commit-msg-hook\n"), 0755); err != nil {
			t.Fatal(err)
		}

		// Unset the local core.hooksPath that newGitRepo sets so git falls back to .git/hooks/.
		gitCommand("config", "--unset", "core.hooksPath").Run()

		beadsDir := setupBeadsDir(t, repoDir)

		if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil {
			t.Fatalf("installHooksWithOptions(beads=true) failed: %v", err)
		}

		// Managed hook: should be preserved with beads section injected.
		content, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
		if err != nil {
			t.Fatalf("failed to read .beads/hooks/pre-commit: %v", err)
		}
		contentStr := string(content)
		if !strings.Contains(contentStr, "echo custom-default-hook") {
			t.Errorf(".git/hooks/pre-commit content not preserved.\nGot:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Errorf("beads section marker missing.\nGot:\n%s", contentStr)
		}

		// Non-managed hook: should be copied as-is.
		cmContent, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "commit-msg"))
		if err != nil {
			t.Fatalf("non-managed hook commit-msg not copied to .beads/hooks/: %v", err)
		}
		if !strings.Contains(string(cmContent), "echo commit-msg-hook") {
			t.Errorf("Unmanaged hook content not preserved.\nGot:\n%s", string(cmContent))
		}
	})
}

func TestHooksNeedUpdate(t *testing.T) {
	tests := []struct {
		name           string
		setupHooks     bool // whether to create .git/hooks/ with hook files
		preCommitBody  string
		postMergeBody  string
		skipPostMerge  bool        // skip writing post-merge hook file
		fileMode       os.FileMode // file mode for hook files (0 = default 0700)
		wantNeedUpdate bool
	}{
		{
			name:           "no hooks directory",
			setupHooks:     false,
			wantNeedUpdate: false,
		},
		{
			name:           "current version hooks",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: false,
		},
		{
			name:           "outdated version hooks",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:           "inline hooks without version",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n#\n# bd (beads) pre-commit hook\n#\nbd sync --flush-only\n",
			postMergeBody:  "#!/bin/sh\n#\n# bd (beads) post-merge hook\n#\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:           "shim hooks",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-shim 0.40.0\nexec bd hooks run pre-commit \"$@\"\n",
			postMergeBody:  "#!/bin/sh\n# bd-shim 0.40.0\nexec bd hooks run post-merge \"$@\"\n",
			wantNeedUpdate: false,
		},
		{
			name:           "non-bd hooks",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\necho 'custom pre-commit'\n",
			postMergeBody:  "#!/bin/sh\necho 'custom post-merge'\n",
			wantNeedUpdate: false,
		},
		{
			name:           "empty hook files",
			setupHooks:     true,
			preCommitBody:  "",
			postMergeBody:  "",
			wantNeedUpdate: false,
		},
		{
			name:           "version prefix with empty version",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: \n# bd (beads) pre-commit hook\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: \n# bd (beads) post-merge hook\n",
			wantNeedUpdate: true,
		},
		{
			name:           "mixed state: one outdated one current",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:           "mixed state: shim and outdated template",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-shim 0.49.6\nexec bd hooks run pre-commit \"$@\"\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) post-merge hook\n",
			wantNeedUpdate: true,
		},
		{
			name:           "only pre-commit exists",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			skipPostMerge:  true,
			wantNeedUpdate: true,
		},
		{
			name:           "non-executable current version hooks",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			fileMode:       0644,
			wantNeedUpdate: false, // hooksNeedUpdate checks version, not permissions
		},
		{
			name:           "section marker hooks current version",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n" + generateHookSection("pre-commit"),
			postMergeBody:  "#!/bin/sh\n" + generateHookSection("post-merge"),
			wantNeedUpdate: false,
		},
		{
			name:           "section marker hooks older version (shim-like, not outdated)",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.40.0 ---\nbd hooks run pre-commit \"$@\"\n# --- END BEADS INTEGRATION ---\n",
			postMergeBody:  "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.40.0 ---\nbd hooks run post-merge \"$@\"\n# --- END BEADS INTEGRATION ---\n",
			wantNeedUpdate: false, // section-marker hooks delegate to bd hooks run, like shims
		},
		{
			name:           "section marker with user content preserved",
			setupHooks:     true,
			preCommitBody:  "#!/bin/sh\necho user-before\n\n" + generateHookSection("pre-commit") + "\necho user-after\n",
			postMergeBody:  "#!/bin/sh\n" + generateHookSection("post-merge"),
			wantNeedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := newGitRepo(t)
			runInDir(t, tmpDir, func() {
				if tt.setupHooks {
					gitDirPath, err := git.GetGitDir()
					if err != nil {
						t.Fatalf("git.GetGitDir() failed: %v", err)
					}
					hooksDir := filepath.Join(gitDirPath, "hooks")
					if err := os.MkdirAll(hooksDir, 0750); err != nil {
						t.Fatalf("Failed to create hooks directory: %v", err)
					}

					mode := tt.fileMode
					if mode == 0 {
						mode = 0700
					}

					preCommitPath := filepath.Join(hooksDir, "pre-commit")
					if err := os.WriteFile(preCommitPath, []byte(tt.preCommitBody), mode); err != nil {
						t.Fatalf("Failed to write pre-commit hook: %v", err)
					}

					if !tt.skipPostMerge {
						postMergePath := filepath.Join(hooksDir, "post-merge")
						if err := os.WriteFile(postMergePath, []byte(tt.postMergeBody), mode); err != nil {
							t.Fatalf("Failed to write post-merge hook: %v", err)
						}
					}
				}

				got := hooksNeedUpdate()
				if got != tt.wantNeedUpdate {
					t.Errorf("hooksNeedUpdate() = %v, want %v", got, tt.wantNeedUpdate)
				}
			})
		})
	}
}

// TestInstallHooksBeads_HuskyV8Helper verifies that the husky v8 _/ helper
// directory is symlinked when hooks are preserved from a husky-managed directory.
// GH#3132 Bug 1: without this, hooks that source $(dirname "$0")/_/husky.sh fail.
func TestInstallHooksBeads_HuskyV8Helper(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(fakeHome, ".gitconfig"))

	// Create a husky v8-style hooks directory
	huskyDir := filepath.Join(fakeHome, "husky-hooks")
	huskyHelperDir := filepath.Join(huskyDir, "_")
	if err := os.MkdirAll(huskyHelperDir, 0755); err != nil {
		t.Fatalf("mkdir husky helper: %v", err)
	}
	huskyShContent := "#!/usr/bin/env sh\n# husky v8 helper\n"
	if err := os.WriteFile(filepath.Join(huskyHelperDir, "husky.sh"), []byte(huskyShContent), 0755); err != nil {
		t.Fatalf("write husky.sh: %v", err)
	}
	// Hook that sources the helper via relative path
	hookContent := "#!/usr/bin/env sh\n. \"$(dirname -- \"$0\")/_/husky.sh\"\nnpx lint-staged\n"
	if err := os.WriteFile(filepath.Join(huskyDir, "pre-commit"), []byte(hookContent), 0755); err != nil {
		t.Fatalf("write pre-commit: %v", err)
	}

	// Set as global hooks path (simulating husky v8)
	setGlobal := gitCommand("config", "--global", "core.hooksPath", huskyDir)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("set global core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	repoDir := t.TempDir()
	initCmd := gitCommand("init", "--initial-branch=main")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git config %v: %v", args, err)
		}
	}

	runInDir(t, repoDir, func() {
		beadsDir := setupBeadsDir(t, repoDir)

		if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil {
			t.Fatalf("installHooksWithOptions: %v", err)
		}

		// Verify the _/ symlink was created
		tgtHelper := filepath.Join(beadsDir, "hooks", "_")
		info, err := os.Lstat(tgtHelper)
		if err != nil {
			t.Fatalf("expected _/ symlink in .beads/hooks/: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected _/ to be a symlink, got mode %v", info.Mode())
		}

		// Verify the symlink target resolves to the original helper
		target, err := os.Readlink(tgtHelper)
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		resolved := filepath.Join(filepath.Dir(tgtHelper), target, "husky.sh")
		if _, err := os.Stat(resolved); err != nil {
			t.Errorf("symlink does not resolve to husky.sh: %v (target=%s)", err, target)
		}

		// Verify the hook content was preserved
		content, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
		if err != nil {
			t.Fatalf("read pre-commit: %v", err)
		}
		if !strings.Contains(string(content), "npx lint-staged") {
			t.Errorf("hook content not preserved.\nGot:\n%s", string(content))
		}
	})
}

// TestInstallHooksBeads_HuskyV9Shims verifies that husky v9 shims are replaced
// with actual user hook content when preserved.
// GH#3132 Bug 2: husky v9's h dispatcher uses dirname(dirname($0)) which breaks
// when hooks are relocated from .husky/_/ to .beads/hooks/.
func TestInstallHooksBeads_HuskyV9Shims(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(fakeHome, ".gitconfig"))

	// Create husky v9 directory structure:
	// .husky/
	//   pre-commit   <- user's actual commands
	//   _/
	//     h           <- dispatcher
	//     pre-commit  <- shim that sources h
	huskyBase := filepath.Join(fakeHome, "project", ".husky")
	huskyInner := filepath.Join(huskyBase, "_")
	if err := os.MkdirAll(huskyInner, 0755); err != nil {
		t.Fatalf("mkdir .husky/_: %v", err)
	}

	// User's actual hook commands (in .husky/)
	userHookContent := "npm run minify-templates\nnpx lint-staged --allow-empty\n"
	if err := os.WriteFile(filepath.Join(huskyBase, "pre-commit"), []byte(userHookContent), 0644); err != nil {
		t.Fatalf("write user hook: %v", err)
	}

	// Husky v9 dispatcher (in .husky/_/)
	hDispatcher := `#!/usr/bin/env sh
n=$(basename "$0")
s=$(dirname "$(dirname "$0")")/$n
[ ! -f "$s" ] && exit 0
. "$s"
`
	if err := os.WriteFile(filepath.Join(huskyInner, "h"), []byte(hDispatcher), 0755); err != nil {
		t.Fatalf("write h dispatcher: %v", err)
	}

	// Husky v9 shim (in .husky/_/)
	shimContent := "#!/usr/bin/env sh\n. \"$(dirname \"$0\")/h\"\n"
	if err := os.WriteFile(filepath.Join(huskyInner, "pre-commit"), []byte(shimContent), 0755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	// Set core.hooksPath to .husky/_/ (husky v9 style)
	setGlobal := gitCommand("config", "--global", "core.hooksPath", huskyInner)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("set global core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	repoDir := t.TempDir()
	initCmd := gitCommand("init", "--initial-branch=main")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git config %v: %v", args, err)
		}
	}

	runInDir(t, repoDir, func() {
		beadsDir := setupBeadsDir(t, repoDir)

		if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil {
			t.Fatalf("installHooksWithOptions: %v", err)
		}

		// Verify the h dispatcher was removed
		hTarget := filepath.Join(beadsDir, "hooks", "h")
		if _, err := os.Stat(hTarget); !os.IsNotExist(err) {
			t.Error("h dispatcher should have been removed from .beads/hooks/")
		}

		// Verify the shim was replaced with actual user hook content
		content, err := os.ReadFile(filepath.Join(beadsDir, "hooks", "pre-commit"))
		if err != nil {
			t.Fatalf("read pre-commit: %v", err)
		}
		contentStr := string(content)

		// Should contain the user's actual commands, not the shim
		if strings.Contains(contentStr, `. "$(dirname "$0")/h"`) {
			t.Error("shim content should have been replaced with user hook content")
		}
		if !strings.Contains(contentStr, "npx lint-staged --allow-empty") {
			t.Errorf("user hook content not found.\nGot:\n%s", contentStr)
		}
		if !strings.Contains(contentStr, "npm run minify-templates") {
			t.Errorf("user hook content not found.\nGot:\n%s", contentStr)
		}

		// Should have a shebang (added since user hooks in .husky/ often omit it)
		if !strings.HasPrefix(contentStr, "#!") {
			t.Error("preserved hook should have a shebang")
		}

		// Beads section should also be present (injected by installHooksWithOptions)
		if !strings.Contains(contentStr, hookSectionBeginPrefix) {
			t.Errorf("beads section marker missing.\nGot:\n%s", contentStr)
		}
	})
}

// TestFixHuskyHookLayout_NoHusky verifies the fix is a no-op for non-husky directories.
func TestFixHuskyHookLayout_NoHusky(t *testing.T) {
	sourceDir := t.TempDir()
	targetDir := t.TempDir()

	// Write a normal hook (no husky)
	if err := os.WriteFile(filepath.Join(sourceDir, "pre-commit"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "pre-commit"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	fixHuskyHookLayout(sourceDir, targetDir)

	// No _/ symlink should be created
	if _, err := os.Lstat(filepath.Join(targetDir, "_")); !os.IsNotExist(err) {
		t.Error("_/ should not exist for non-husky directories")
	}
	// No h file to remove
	if _, err := os.Stat(filepath.Join(targetDir, "h")); !os.IsNotExist(err) {
		t.Error("h should not exist")
	}
}

// TestCheckGitHooks_ForeignHookNotInstalled reproduces GH#6084.
// A hook file that beads never wrote must not be reported as installed by
// bd hooks list. Previously CheckGitHooks set Installed=true whenever
// getHookVersion returned without error, but getHookVersion returns a zero-
// value hookVersionInfo (IsBdHook=false) with a nil error when the file is
// readable but contains no beads markers.
func TestCheckGitHooks_ForeignHookNotInstalled(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		hooksDir := filepath.Join(tmpDir, ".git", "hooks")
		if err := os.MkdirAll(hooksDir, 0750); err != nil {
			t.Fatalf("failed to create hooks dir: %v", err)
		}

		// Write a hook that contains no beads markers whatsoever — exactly
		// the script from the issue report.
		foreignHook := "#!/bin/sh\necho hello\nexit 0\n"
		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte(foreignHook), 0755); err != nil {
			t.Fatalf("failed to write foreign hook: %v", err)
		}

		statuses := CheckGitHooks()

		for _, s := range statuses {
			if s.Name != "pre-commit" {
				continue
			}
			if s.Installed {
				t.Errorf("pre-commit: got Installed=true for a foreign hook file that beads never wrote; want Installed=false (GH#6084)")
			}
			return
		}
		t.Error("pre-commit hook status not found in CheckGitHooks result")
	})
}

// TestCheckGitHooks_BeadsHookIsInstalled verifies that the IsBdHook gate added
// in GH#6084 does not regress recognition of genuine beads-managed hooks.
// Each variant that getHookVersion can recognise must produce Installed=true.
func TestCheckGitHooks_BeadsHookIsInstalled(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantInstalled bool
		wantOutdated  bool
	}{
		{
			name: "section-marker",
			// Minimal hook with a BEGIN BEADS INTEGRATION section marker.
			body:          "#!/bin/sh\n" + hookSectionBeginPrefix + " v" + Version + " ---\nbd hooks run pre-commit \"$@\"\n" + hookSectionEndPrefix + " v" + Version + " ---\n",
			wantInstalled: true,
		},
		{
			name:          "legacy-version-marker",
			body:          "#!/bin/sh\n" + hookVersionPrefix + Version + "\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			wantInstalled: true,
		},
		{
			name:          "shim-marker",
			body:          "#!/bin/sh\n" + shimVersionPrefix + Version + "\nexec bd hooks run pre-commit \"$@\"\n",
			wantInstalled: true,
		},
		{
			name: "inline-marker",
			// Old bd init style: no version line, just the inline comment.
			// bd wrote this template, so a missing version still means
			// "re-install me": Outdated stays true (GH#1120).
			body:          "#!/bin/sh\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			wantInstalled: true,
			wantOutdated:  true,
		},
		{
			name: "bd-hooks-run-no-marker",
			// The GH#946 integration: an external hook manager's own hook that
			// calls bd hooks run and carries no beads marker. bd doctor counts
			// it as a bd hook (doctor.IsBdHookContent), so bd hooks list must
			// too, or bd config drift exits 1 on a supported setup. It is not
			// outdated: bd itself supplies the behavior, and "run bd hooks
			// install" would overwrite the manager's hook.
			body:          "#!/bin/sh\nlefthook run pre-commit\nbd hooks run pre-commit \"$@\"\n",
			wantInstalled: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := newGitRepo(t)
			runInDir(t, tmpDir, func() {
				hooksDir := filepath.Join(tmpDir, ".git", "hooks")
				if err := os.MkdirAll(hooksDir, 0750); err != nil {
					t.Fatalf("failed to create hooks dir: %v", err)
				}
				hookPath := filepath.Join(hooksDir, "pre-commit")
				if err := os.WriteFile(hookPath, []byte(tc.body), 0755); err != nil {
					t.Fatalf("failed to write hook: %v", err)
				}

				statuses := CheckGitHooks()

				for _, s := range statuses {
					if s.Name != "pre-commit" {
						continue
					}
					if s.Installed != tc.wantInstalled {
						t.Errorf("pre-commit (%s): got Installed=%v, want %v", tc.name, s.Installed, tc.wantInstalled)
					}
					if s.Outdated != tc.wantOutdated {
						t.Errorf("pre-commit (%s): got Outdated=%v, want %v", tc.name, s.Outdated, tc.wantOutdated)
					}
					return
				}
				t.Error("pre-commit hook status not found in CheckGitHooks result")
			})
		})
	}
}

// TestHooksList_StrandedHooksPathNamesTheConfig reproduces GH#5853 (bd-m9es2y).
// The .beads/hooks files are present and current, but core.hooksPath still
// names the absolute path of the repo's old location, so git runs no hooks.
// bd hooks list must name the stranded core.hooksPath instead of reporting every
// hook as "not installed", which sends the user to reinstall.
func TestHooksList_StrandedHooksPathNamesTheConfig(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		beadsHooks := filepath.Join(tmpDir, ".beads", "hooks")
		if err := os.MkdirAll(beadsHooks, 0750); err != nil {
			t.Fatalf("mkdir .beads/hooks: %v", err)
		}
		for _, name := range managedHookNames {
			body := "#!/bin/sh\n" + shimVersionPrefix + Version + "\nexec bd hooks run " + name + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(beadsHooks, name), []byte(body), 0755); err != nil {
				t.Fatalf("write hook %s: %v", name, err)
			}
		}

		stale := filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
		if out, err := exec.Command("git", "config", "core.hooksPath", stale).CombinedOutput(); err != nil {
			t.Fatalf("git config core.hooksPath: %v: %s", err, out)
		}
		resetRepoCaches()

		out := captureStdout(t, func() error {
			return hooksListCmd.RunE(hooksListCmd, nil)
		})

		if !strings.Contains(out, stale) {
			t.Errorf("bd hooks list does not name the stranded core.hooksPath %q (GH#5853); output:\n%s", stale, out)
		}
		if strings.Count(out, "not installed") == len(managedHookNames) {
			t.Errorf("bd hooks list reports every hook \"not installed\" although .beads/hooks holds all of them and the fault is core.hooksPath (GH#5853); output:\n%s", out)
		}
	})
}

// TestHooksList_RelocatedHooksPath covers the full GH#5853 diagnosis on
// `bd hooks list`: text names the stale path, explains that git runs no
// hooks, names the current target and the explicit repair; JSON keeps the
// hooks array and adds the hooks_path diagnostics; ineligible paths get the
// fault named without claiming hooks exist elsewhere.
func TestHooksList_RelocatedHooksPath(t *testing.T) {
	t.Run("eligible relocation, text", func(t *testing.T) {
		tmpDir, stale := strandedHooksRepo(t)
		runInDir(t, tmpDir, func() {
			out := captureStdout(t, func() error {
				return hooksListCmd.RunE(hooksListCmd, nil)
			})
			current := filepath.Join(tmpDir, ".beads", "hooks")
			for _, want := range []string{stale, current, "runs no hooks", "bd doctor --fix"} {
				if !strings.Contains(out, want) {
					t.Errorf("bd hooks list output missing %q (GH#5853); output:\n%s", want, out)
				}
			}
		})
	})

	t.Run("eligible relocation, json", func(t *testing.T) {
		tmpDir, stale := strandedHooksRepo(t)
		runInDir(t, tmpDir, func() {
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = false })
			out := captureStdout(t, func() error {
				return hooksListCmd.RunE(hooksListCmd, nil)
			})
			var payload struct {
				Hooks     []HookStatus `json:"hooks"`
				HooksPath *struct {
					Configured  string `json:"configured"`
					Resolved    string `json:"resolved"`
					Exists      bool   `json:"exists"`
					RelocatedTo string `json:"relocated_to"`
				} `json:"hooks_path"`
			}
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatalf("hooks list --json output is not valid JSON: %v\n%s", err, out)
			}
			if len(payload.Hooks) != len(managedHookNames) {
				t.Errorf("hooks array lost its semantics: got %d hooks, want %d; output:\n%s", len(payload.Hooks), len(managedHookNames), out)
			}
			if payload.HooksPath == nil {
				t.Fatalf("hooks_path diagnostics missing from JSON output:\n%s", out)
			}
			if payload.HooksPath.Configured != stale || payload.HooksPath.Exists {
				t.Errorf("hooks_path = %+v, want configured=%q exists=false", payload.HooksPath, stale)
			}
			if want := filepath.Join(tmpDir, ".beads", "hooks"); payload.HooksPath.RelocatedTo != want {
				t.Errorf("hooks_path.relocated_to = %q, want %q", payload.HooksPath.RelocatedTo, want)
			}
		})
	})

	t.Run("ineligible third-party path, text", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		stale := filepath.Join(t.TempDir(), "team-hooks")
		runInDir(t, tmpDir, func() {
			if out, err := exec.Command("git", "config", "core.hooksPath", stale).CombinedOutput(); err != nil {
				t.Fatalf("git config core.hooksPath: %v: %s", err, out)
			}
			resetRepoCaches()
			out := captureStdout(t, func() error {
				return hooksListCmd.RunE(hooksListCmd, nil)
			})
			if !strings.Contains(out, stale) {
				t.Errorf("output must name the missing configured path %q (GH#5853); output:\n%s", stale, out)
			}
			if strings.Contains(out, "repoint") {
				t.Errorf("no automatic relocation must be offered for a path bd does not own; output:\n%s", out)
			}
		})
	})
}

// strandedHooksRepo builds the GH#5853 fixture: a repository whose
// .beads/hooks holds recognizable bd hooks while a repository-local
// core.hooksPath still points at the absolute .beads/hooks of a former
// location that no longer exists. It returns the repo root and the stale
// path.
func strandedHooksRepo(t *testing.T) (repoDir, stale string) {
	t.Helper()
	repoDir, stale, _ = strandedHooksRepoLayout(t, ".beads/hooks")
	return repoDir, stale
}

// strandedHooksRepoLayout is strandedHooksRepo for either beads layout
// (".beads/hooks" or ".beads-hooks"). It returns the repo root, the stale
// configured value, and the current hooks directory.
func strandedHooksRepoLayout(t *testing.T, layout string) (repoDir, stale, current string) {
	t.Helper()
	tmpDir := newGitRepo(t)
	switch layout {
	case ".beads-hooks":
		current = filepath.Join(tmpDir, ".beads-hooks")
	default:
		current = filepath.Join(tmpDir, ".beads", "hooks")
	}
	if err := os.MkdirAll(current, 0750); err != nil {
		t.Fatalf("mkdir %s: %v", current, err)
	}
	for _, name := range managedHookNames {
		body := "#!/bin/sh\n" + shimVersionPrefix + Version + "\nexec bd hooks run " + name + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(current, name), []byte(body), 0755); err != nil {
			t.Fatalf("write hook %s: %v", name, err)
		}
	}
	// The old location: the same layout under a directory that no longer
	// exists anywhere on disk.
	stale = filepath.Join(t.TempDir(), "moved-away", layout)
	cmd := exec.Command("git", "config", "core.hooksPath", stale)
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config core.hooksPath: %v: %s", err, out)
	}
	resetRepoCachesForTest(t)
	return tmpDir, stale, current
}

// TestInspectHooksPath_Classification pins the shared read-only inspection
// behind the GH#5853 diagnostics: the states it must distinguish and the
// fail-closed eligibility rules.
func TestInspectHooksPath_Classification(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		runInDir(t, tmpDir, func() {
			// newGitRepo pins core.hooksPath=.git/hooks for test isolation;
			// unset it to exercise the genuinely-unset case.
			if out, err := exec.Command("git", "config", "--unset", "core.hooksPath").CombinedOutput(); err != nil {
				t.Fatalf("git config --unset: %v: %s", err, out)
			}
			resetRepoCaches()
			if st := doctor.InspectHooksPath(); st.State != doctor.HooksPathUnset {
				t.Errorf("State = %v, want HooksPathUnset; status: %+v", st.State, st)
			}
		})
	})

	t.Run("existing configured path", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		runInDir(t, tmpDir, func() {
			hooks := filepath.Join(tmpDir, ".git", "hooks")
			cmd := exec.Command("git", "config", "core.hooksPath", hooks)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git config: %v: %s", err, out)
			}
			resetRepoCaches()
			if st := doctor.InspectHooksPath(); st.State != doctor.HooksPathExists {
				t.Errorf("State = %v, want HooksPathExists; status: %+v", st.State, st)
			}
		})
	})

	t.Run("eligible relocated path", func(t *testing.T) {
		tmpDir, stale := strandedHooksRepo(t)
		runInDir(t, tmpDir, func() {
			st := doctor.InspectHooksPath()
			if st.State != doctor.HooksPathRelocated {
				t.Fatalf("State = %v, want HooksPathRelocated; status: %+v", st.State, st)
			}
			if want := filepath.Join(tmpDir, ".beads", "hooks"); st.Target != want {
				t.Errorf("Target = %q, want %q", st.Target, want)
			}
			if st.Configured != stale {
				t.Errorf("Configured = %q, want %q", st.Configured, stale)
			}
		})
	})

	t.Run("third-party missing path", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		stale := filepath.Join(t.TempDir(), "team-hooks")
		runInDir(t, tmpDir, func() {
			cmd := exec.Command("git", "config", "core.hooksPath", stale)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git config: %v: %s", err, out)
			}
			resetRepoCaches()
			st := doctor.InspectHooksPath()
			if st.State != doctor.HooksPathMissing {
				t.Fatalf("State = %v, want HooksPathMissing; status: %+v", st.State, st)
			}
			if !strings.Contains(st.Reason, "does not infer ownership") {
				t.Errorf("Reason should name the unrecognized layout, got: %s", st.Reason)
			}
		})
	})
}

// TestInstallHooks_RelocatedHooksPath pins AC-4 of the GH#5853 design: a
// plain hook installation (and the plain reinstall bd config apply uses)
// against an eligible stranded core.hooksPath refreshes hooks at the
// validated current target and repoints the configuration — without
// recreating the stale directory and without discarding user content outside
// the beads-managed section.
func TestInstallHooks_RelocatedHooksPath(t *testing.T) {
	t.Run("plain installer preserves user content and repoints", func(t *testing.T) {
		tmpDir, stale, current := strandedHooksRepoLayout(t, ".beads/hooks")
		// User-owned content in the hook at the current target, outside any
		// beads section: the install must preserve it byte-for-byte while
		// injecting the managed section.
		userContent := "#!/bin/sh\necho user pre-commit content\nexec ./pre-commit-framework\n"
		if err := os.WriteFile(filepath.Join(current, "pre-commit"), []byte(userContent), 0755); err != nil {
			t.Fatalf("write user pre-commit: %v", err)
		}
		runInDir(t, tmpDir, func() {
			if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
				t.Fatalf("installHooksWithOptions failed: %v", err)
			}
		})
		if got, err := execGitConfig(tmpDir, "core.hooksPath"); err != nil || got != current {
			t.Errorf("core.hooksPath = %q (err=%v) after install, want repointed to %q", got, err, current)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("the stale directory %s must not be recreated (stat err=%v)", stale, err)
		}
		body, err := os.ReadFile(filepath.Join(current, "pre-commit"))
		if err != nil {
			t.Fatalf("read pre-commit: %v", err)
		}
		if !strings.Contains(string(body), "user pre-commit content") {
			t.Errorf("user content outside the beads section was discarded; pre-commit:\n%s", body)
		}
		if !strings.Contains(string(body), hookSectionBeginPrefix) {
			t.Errorf("beads section missing from pre-commit:\n%s", body)
		}
	})

	t.Run("bd config apply reinstall path", func(t *testing.T) {
		tmpDir, stale, current := strandedHooksRepoLayout(t, ".beads/hooks")
		runInDir(t, tmpDir, func() {
			result := applyHooks(true, false)
			if result.Status != applyStatusApplied {
				t.Fatalf("applyHooks status = %q (%s), want %q", result.Status, result.Message, applyStatusApplied)
			}
		})
		if got, err := execGitConfig(tmpDir, "core.hooksPath"); err != nil || got != current {
			t.Errorf("core.hooksPath = %q (err=%v) after config apply, want %q", got, err, current)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("the stale directory %s must not be recreated (stat err=%v)", stale, err)
		}
	})
}

// execGitConfig reads a git config value for the repository at dir.
func execGitConfig(dir, key string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TestInstallHooks_RefusesIneligibleDestination pins AC-5 of the GH#5853
// design: a plain hook installation against an ineligible missing configured
// destination explains why it cannot proceed, never repoints the value, and
// never creates the stale directory.
func TestInstallHooks_RefusesIneligibleDestination(t *testing.T) {
	t.Run("third-party path", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		stale := filepath.Join(t.TempDir(), "team-hooks")
		runInDir(t, tmpDir, func() {
			if out, err := exec.Command("git", "config", "core.hooksPath", stale).CombinedOutput(); err != nil {
				t.Fatalf("git config core.hooksPath: %v: %s", err, out)
			}
			resetRepoCaches()
			err := installHooksWithOptions(managedHookNames, false, false, false, false)
			if err == nil {
				t.Fatal("installHooksWithOptions must refuse a missing third-party hooks path")
			}
			if !strings.Contains(err.Error(), "core.hooksPath") {
				t.Errorf("refusal must name core.hooksPath, got: %v", err)
			}
		})
		if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
			t.Errorf("the stale directory %s must not be created (stat err=%v)", stale, statErr)
		}
		if got, err := execGitConfig(tmpDir, "core.hooksPath"); err != nil || got != stale {
			t.Errorf("core.hooksPath = %q (err=%v), want unchanged %q", got, err, stale)
		}
	})

	t.Run("beads layout without a validated replacement", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		// The repository has no .beads/hooks at all: nothing validates the
		// replacement, so the stale destination must not be recreated.
		stale := filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
		runInDir(t, tmpDir, func() {
			if out, err := exec.Command("git", "config", "core.hooksPath", stale).CombinedOutput(); err != nil {
				t.Fatalf("git config core.hooksPath: %v: %s", err, out)
			}
			resetRepoCaches()
			err := installHooksWithOptions(managedHookNames, false, false, false, false)
			if err == nil {
				t.Fatal("installHooksWithOptions must refuse when no replacement target validates")
			}
			if !strings.Contains(err.Error(), "bd hooks install --beads") {
				t.Errorf("refusal must offer the actionable alternative, got: %v", err)
			}
		})
		if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
			t.Errorf("the stale directory %s must not be created (stat err=%v)", stale, statErr)
		}
	})

	t.Run("global-only value", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		stale := filepath.Join(t.TempDir(), "moved-away", ".beads", "hooks")
		globalConfig := filepath.Join(t.TempDir(), "gitconfig")
		t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
		gc := exec.Command("git", "config", "--global", "core.hooksPath", stale)
		gc.Dir = tmpDir
		if out, err := gc.CombinedOutput(); err != nil {
			t.Fatalf("git config --global: %v: %s", err, out)
		}
		uc := exec.Command("git", "config", "--unset", "core.hooksPath")
		uc.Dir = tmpDir
		_ = uc.Run()
		runInDir(t, tmpDir, func() {
			resetRepoCaches()
			err := installHooksWithOptions(managedHookNames, false, false, false, false)
			if err == nil {
				t.Fatal("installHooksWithOptions must refuse a global-only missing hooks path")
			}
			if !strings.Contains(err.Error(), "global") {
				t.Errorf("refusal must name the non-local scope, got: %v", err)
			}
		})
		if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
			t.Errorf("the stale directory %s must not be created (stat err=%v)", stale, statErr)
		}
		globalGot, gerr := exec.Command("git", "-C", tmpDir, "config", "--global", "--get", "core.hooksPath").Output()
		if gerr != nil || strings.TrimSpace(string(globalGot)) != stale {
			t.Errorf("global core.hooksPath = %q (err=%v), want unchanged %q", strings.TrimSpace(string(globalGot)), gerr, stale)
		}
	})
}
