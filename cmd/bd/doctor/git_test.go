package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()

	return dir
}

func TestCheckGitHooks(t *testing.T) {
	// This test needs to run in a git repository
	// We test the basic case where hooks are not installed
	t.Run("not in git repo returns N/A", func(t *testing.T) {
		// Save current directory
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(origDir); err != nil {
				t.Fatalf("failed to restore directory: %v", err)
			}
		}()

		// Change to a non-git directory
		tmpDir := t.TempDir()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		check := CheckGitHooks()

		if check.Status != StatusOK {
			t.Errorf("expected status %q, got %q", StatusOK, check.Status)
		}
		if check.Message != "N/A (not a git repository)" {
			t.Errorf("unexpected message: %s", check.Message)
		}
	})
}

func TestCheckMergeDriver(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "not a git repo",
			setup: func(t *testing.T, dir string) {
				// Just create .beads directory, no git
				// CheckMergeDriver uses global git detection
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(beadsDir, 0755); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "warning", // Uses global git detection, so still checks
			expectMessage:  "",        // Message varies
		},
		{
			name: "merge driver not configured",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
			},
			expectedStatus: "warning",
			expectMessage:  "Git merge driver not configured",
		},
		{
			name: "correct config",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O %A %B")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			expectedStatus: "ok",
			expectMessage:  "Correctly configured",
		},
		{
			name: "incorrect config with old placeholders",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %L %O %A %R")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			expectedStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckMergeDriver(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestCheckSyncBranchConfig(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no .beads directory",
			setup: func(t *testing.T, dir string) {
				// Empty directory, no .beads
			},
			expectedStatus: "ok",
		},
		{
			name: "not a git repo",
			setup: func(t *testing.T, dir string) {
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(beadsDir, 0755); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "ok",
		},
		{
			name: "no remote configured",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
			},
			expectedStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchConfig(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckSyncBranchHealth(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "no sync branch configured (uses global git detection)",
			setup: func(t *testing.T, dir string) {
				// CheckSyncBranchHealth uses global git.GetGitDir() detection
				// which checks from current working directory, not the path parameter
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(beadsDir, 0755); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no sync branch configured)",
		},
		{
			name: "no sync branch configured with git",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no sync branch configured)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchHealth(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestCheckSyncBranchHookCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "sync-branch not configured",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
			},
			expectedStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchHookCompatibility(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q", tt.expectedStatus, check.Status)
			}
		})
	}
}

// setupGitRepoInDir initializes a git repo in the given directory with .beads
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			origDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(origDir)

			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}

			check := CheckGitHooks()

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
				t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
			}
		})
	}
}

// Edge case tests for CheckMergeDriver

func TestCheckMergeDriver_PartiallyConfigured(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "only merge.beads.name configured",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.name", "Beads merge driver")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			expectedStatus: "warning",
			expectInMsg:    "not configured",
		},
		{
			name: "empty merge driver config",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			// git config trims to empty string, which is non-standard
			expectedStatus: "warning",
			expectInMsg:    "Non-standard",
		},
		{
			name: "merge driver with extra spaces",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "  bd merge %A %O %A %B  ")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			// git config stores the value with spaces, but the code trims it
			expectedStatus: "ok",
			expectInMsg:    "Correctly configured",
		},
		{
			name: "merge driver with wrong bd path",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "/usr/local/bin/bd merge %A %O %A %B")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			expectedStatus: "warning",
			expectInMsg:    "Non-standard",
		},
		{
			name: "merge driver with only two placeholders",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O")
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			},
			expectedStatus: "warning",
			expectInMsg:    "Non-standard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckMergeDriver(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
				t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
			}
		})
	}
}

// Edge case tests for CheckSyncBranchConfig

func TestCheckSyncBranchConfig_MultipleRemotes(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "multiple remotes without sync-branch",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// add multiple remotes
				cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/user/repo.git")
				cmd.Dir = dir
				_ = cmd.Run()
				cmd = exec.Command("git", "remote", "add", "upstream", "https://github.com/upstream/repo.git")
				cmd.Dir = dir
				_ = cmd.Run()
			},
			expectedStatus: "warning",
			expectInMsg:    "not configured",
		},
		{
			name: "multiple remotes with sync-branch configured via env",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// add multiple remotes
				cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/user/repo.git")
				cmd.Dir = dir
				_ = cmd.Run()
				cmd = exec.Command("git", "remote", "add", "upstream", "https://github.com/upstream/repo.git")
				cmd.Dir = dir
				_ = cmd.Run()
				// use env var to configure sync-branch since config package reads from cwd
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "ok",
			expectInMsg:    "Configured",
		},
		{
			name: "no remotes at all",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
			},
			expectedStatus: "ok",
			expectInMsg:    "no remote configured",
		},
		{
			name: "on sync branch itself via env (error case)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create and checkout sync branch
				cmd := exec.Command("git", "checkout", "-b", "beads-sync")
				cmd.Dir = dir
				_ = cmd.Run()
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "error",
			expectInMsg:    "On sync branch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchConfig(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
				t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
			}
		})
	}
}

// Edge case tests for CheckSyncBranchHealth

func TestCheckSyncBranchHealth_DetachedHEAD(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "detached HEAD without sync-branch",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create initial commit
				testFile := filepath.Join(dir, "test.txt")
				os.WriteFile(testFile, []byte("test"), 0644)
				cmd := exec.Command("git", "add", "test.txt")
				cmd.Dir = dir
				_ = cmd.Run()
				cmd = exec.Command("git", "commit", "-m", "initial commit")
				cmd.Dir = dir
				_ = cmd.Run()
				// detach HEAD
				cmd = exec.Command("git", "checkout", "HEAD^0")
				cmd.Dir = dir
				_ = cmd.Run()
			},
			expectedStatus: "ok",
			expectInMsg:    "no sync branch configured",
		},
		{
			name: "detached HEAD with sync-branch configured via env",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create initial commit
				testFile := filepath.Join(dir, "test.txt")
				os.WriteFile(testFile, []byte("test"), 0644)
				cmd := exec.Command("git", "add", "test.txt")
				cmd.Dir = dir
				_ = cmd.Run()
				cmd = exec.Command("git", "commit", "-m", "initial commit")
				cmd.Dir = dir
				_ = cmd.Run()
				// create sync branch
				cmd = exec.Command("git", "branch", "beads-sync")
				cmd.Dir = dir
				_ = cmd.Run()
				// detach HEAD
				cmd = exec.Command("git", "checkout", "HEAD^0")
				cmd.Dir = dir
				_ = cmd.Run()
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "ok",
			expectInMsg:    "remote", // no remote configured, so returns "N/A (remote ... not found)"
		},
		{
			name: "sync branch exists but remote doesn't",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create initial commit
				testFile := filepath.Join(dir, "test.txt")
				os.WriteFile(testFile, []byte("test"), 0644)
				cmd := exec.Command("git", "add", "test.txt")
				cmd.Dir = dir
				_ = cmd.Run()
				cmd = exec.Command("git", "commit", "-m", "initial commit")
				cmd.Dir = dir
				_ = cmd.Run()
				// create sync branch
				cmd = exec.Command("git", "branch", "beads-sync")
				cmd.Dir = dir
				_ = cmd.Run()
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "ok",
			expectInMsg:    "remote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchHealth(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
				t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
			}
		})
	}
}

// Edge case tests for CheckSyncBranchHookCompatibility

func TestCheckSyncBranchHookCompatibility_OldHookFormat(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "old hook without version marker",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create old-style pre-push hook without version
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				hookContent := "#!/bin/sh\n# Old hook without version\nbd sync\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0755)
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "warning",
			expectInMsg:    "not a bd hook",
		},
		{
			name: "hook with version 0.28.0 (old format)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				hookContent := "#!/bin/sh\n# bd-hooks-version: 0.28.0\nbd sync\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0755)
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "error",
			expectInMsg:    "incompatible",
		},
		{
			name: "hook with version 0.29.0 (compatible)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				hookContent := "#!/bin/sh\n# bd-hooks-version: 0.29.0\nbd sync\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0755)
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "ok",
			expectInMsg:    "compatible",
		},
		{
			name: "hook with malformed version",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				hookContent := "#!/bin/sh\n# bd-hooks-version: invalid\nbd sync\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0755)
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "error",
			expectInMsg:    "incompatible",
		},
		{
			name: "hook with version marker but no value",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				hookContent := "#!/bin/sh\n# bd-hooks-version:\nbd sync\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hookContent), 0755)
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "warning",
			expectInMsg:    "Could not determine",
		},
		{
			name: "hook in shared hooks directory (core.hooksPath)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				// create shared hooks directory
				sharedHooksDir := filepath.Join(dir, ".git-hooks")
				os.MkdirAll(sharedHooksDir, 0755)
				hookContent := "#!/bin/sh\n# bd-hooks-version: 0.29.0\nbd sync\n"
				os.WriteFile(filepath.Join(sharedHooksDir, "pre-push"), []byte(hookContent), 0755)
				// configure core.hooksPath
				cmd := exec.Command("git", "config", "core.hooksPath", ".git-hooks")
				cmd.Dir = dir
				_ = cmd.Run()
				// use env var to configure sync-branch
				os.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
				t.Cleanup(func() { os.Unsetenv("BEADS_SYNC_BRANCH") })
			},
			expectedStatus: "ok",
			expectInMsg:    "compatible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			check := CheckSyncBranchHookCompatibility(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
				t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
			}
		})
	}
}
