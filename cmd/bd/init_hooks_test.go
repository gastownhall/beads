package main

import (
	"os"
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
	if !strings.Contains(section, hookSectionEnd) {
		t.Error("section missing end marker")
	}
	if !strings.Contains(section, "bd hooks run pre-commit") {
		t.Error("section missing hook invocation")
	}
	if !strings.Contains(section, Version) {
		t.Errorf("section missing version %s", Version)
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
			wantHas:  []string{"#!/bin/sh\n", hookSectionBeginPrefix, hookSectionEnd},
		},
		{
			name:     "inject preserving user content",
			existing: "#!/bin/sh\necho before\n",
			wantHas:  []string{"echo before", hookSectionBeginPrefix, hookSectionEnd},
		},
		{
			name:     "update existing section",
			existing: "#!/bin/sh\necho before\n# --- BEGIN BEADS INTEGRATION v0.40.0 ---\nold content\n# --- END BEADS INTEGRATION ---\necho after\n",
			wantHas:  []string{"echo before", "echo after", "bd hooks run pre-commit", hookSectionEnd},
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
			wantNot:   []string{hookSectionBeginPrefix, hookSectionEnd},
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
		embeddedHooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}
		if err := installHooksWithOptions(embeddedHooks, false, false, false, false); err != nil {
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
		if err := installHooksWithOptions(embeddedHooks, false, false, false, false); err != nil {
			t.Fatalf("second installHooksWithOptions() failed: %v", err)
		}

		content2, _ := os.ReadFile(preCommitPath)
		if string(content2) != contentStr {
			t.Errorf("second install changed content:\nbefore:\n%s\nafter:\n%s", contentStr, string(content2))
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
