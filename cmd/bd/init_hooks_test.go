package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	// Must use shell timeout command with configurable duration
	if !strings.Contains(section, "BEADS_HOOK_TIMEOUT") {
		t.Error("section missing BEADS_HOOK_TIMEOUT env var")
	}
	if !strings.Contains(section, fmt.Sprintf("%d", hookTimeoutSeconds)) {
		t.Errorf("section missing default timeout %d", hookTimeoutSeconds)
	}
	if !strings.Contains(section, "command -v timeout") {
		t.Error("section missing timeout availability check")
	}

	// Timeout exit code (124) must be handled gracefully — continue, don't block git
	if !strings.Contains(section, "_bd_exit -eq 124") {
		t.Error("section missing timeout exit code handling")
	}
	if !strings.Contains(section, "timed out") {
		t.Error("section missing timeout warning message")
	}

	// Fallback path when timeout command is not available (e.g. macOS without coreutils)
	if !strings.Contains(section, "else") {
		t.Error("section missing fallback for systems without timeout command")
	}
}

// TestGenerateHookSection_DBNotInitialized verifies exit code 3 handling (GH#2449).
func TestGenerateHookSection_DBNotInitialized(t *testing.T) {
	section := generateHookSection("pre-commit")

	// Exit code 3 = beads database not initialized; hook must continue gracefully
	if !strings.Contains(section, "_bd_exit -eq 3") {
		t.Error("section missing exit code 3 (DB not initialized) handling")
	}
	if !strings.Contains(section, "database not initialized") {
		t.Error("section missing DB-not-initialized warning message")
	}

	// After handling exit code 3, the effective exit must be 0 (success)
	// Verify the pattern: set _bd_exit=0 after detecting code 3
	if !strings.Contains(section, "if [ $_bd_exit -eq 3 ]; then") {
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
		out, err := exec.Command("git", "config", "--get", "core.hooksPath").Output()
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
		out, err := exec.Command("git", "config", "--get", "core.hooksPath").Output()
		if err != nil {
			t.Fatalf("core.hooksPath not set after --beads install: %v", err)
		}
		hooksPath := strings.TrimSpace(string(out))
		if !filepath.IsAbs(hooksPath) {
			t.Errorf("core.hooksPath should be absolute for worktree compatibility, got %q", hooksPath)
		}

		// Create a worktree and verify hooks are accessible from it
		worktreeDir := filepath.Join(t.TempDir(), "worktree")
		cmd := exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-worktree")
		cmd.Dir = tmpDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, string(output))
		}
		defer func() {
			exec.Command("git", "worktree", "remove", worktreeDir).Run()
		}()

		// From the worktree, core.hooksPath should resolve to the same hooks
		cmd = exec.Command("git", "config", "--get", "core.hooksPath")
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

	setGlobal := exec.Command("git", "config", "--global", "core.hooksPath", globalHooksDir)
	if out, err := setGlobal.CombinedOutput(); err != nil {
		t.Fatalf("failed to set global core.hooksPath: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Manual repo init (can't use newGitRepo which sets a local core.hooksPath).
	repoDir := t.TempDir()
	initCmd := exec.Command("git", "init", "--initial-branch=main")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
	} {
		cmd := exec.Command("git", args...)
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
		exec.Command("git", "config", "--unset", "core.hooksPath").Run()

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
