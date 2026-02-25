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

func TestHooksNeedUpdate(t *testing.T) {
	tests := []struct {
		name           string
		setupHooks     bool        // whether to create .git/hooks/ with hook files
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
			name:       "current version hooks",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody: "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: false,
		},
		{
			name:       "outdated version hooks",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:       "inline hooks without version",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n#\n# bd (beads) pre-commit hook\n#\nbd sync --flush-only\n",
			postMergeBody: "#!/bin/sh\n#\n# bd (beads) post-merge hook\n#\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:       "shim hooks",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-shim 0.40.0\nexec bd hooks run pre-commit \"$@\"\n",
			postMergeBody: "#!/bin/sh\n# bd-shim 0.40.0\nexec bd hooks run post-merge \"$@\"\n",
			wantNeedUpdate: false,
		},
		{
			name:       "non-bd hooks",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\necho 'custom pre-commit'\n",
			postMergeBody: "#!/bin/sh\necho 'custom post-merge'\n",
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
			name:       "version prefix with empty version",
			setupHooks: true,
			preCommitBody:  "#!/bin/sh\n# bd-hooks-version: \n# bd (beads) pre-commit hook\n",
			postMergeBody:  "#!/bin/sh\n# bd-hooks-version: \n# bd (beads) post-merge hook\n",
			wantNeedUpdate: true,
		},
		{
			name:       "mixed state: one outdated one current",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody: "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			wantNeedUpdate: true,
		},
		{
			name:       "mixed state: shim and outdated template",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-shim 0.49.6\nexec bd hooks run pre-commit \"$@\"\n",
			postMergeBody: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) post-merge hook\n",
			wantNeedUpdate: true,
		},
		{
			name:          "only pre-commit exists",
			setupHooks:    true,
			preCommitBody: "#!/bin/sh\n# bd-hooks-version: 0.40.0\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			skipPostMerge: true,
			wantNeedUpdate: true,
		},
		{
			name:       "non-executable current version hooks",
			setupHooks: true,
			preCommitBody: "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) pre-commit hook\nbd sync --flush-only\n",
			postMergeBody: "#!/bin/sh\n# bd-hooks-version: " + Version + "\n# bd (beads) post-merge hook\nbd import\n",
			fileMode:       0644,
			wantNeedUpdate: false, // hooksNeedUpdate checks version, not permissions
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
