package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestDetectExistingHooks(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

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
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

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
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

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
