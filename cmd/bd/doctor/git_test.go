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

// setupGitRepoInDir initializes a git repo in the given directory with .beads
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()

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

// Tests for CheckOrphanedIssues

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
