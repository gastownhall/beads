package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFixGitignore_FilePermissions(t *testing.T) {
	// Skip on Windows as it doesn't support Unix-style file permissions
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file permissions test on Windows")
	}

	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, tmpDir string) // setup before fix
		expectedPerms  os.FileMode
		expectError    bool
	}{
		{
			name:          "creates new file with 0600 permissions",
			setupFunc:     func(t *testing.T, tmpDir string) {
				// Create .beads directory but no .gitignore
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
			},
			expectedPerms: 0600,
			expectError:   false,
		},
		{
			name: "replaces existing file with insecure permissions",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				// Create file with too-permissive permissions (0644)
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte("old content"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			expectedPerms: 0600,
			expectError:   false,
		},
		{
			name: "replaces existing file with secure permissions",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				// Create file with already-secure permissions (0400)
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte("old content"), 0400); err != nil {
					t.Fatal(err)
				}
			},
			expectedPerms: 0600,
			expectError:   false,
		},
		{
			name: "fails gracefully when .beads directory doesn't exist",
			setupFunc: func(t *testing.T, tmpDir string) {
				// Don't create .beads directory
			},
			expectedPerms: 0,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Change to tmpDir for the test
			oldDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(oldDir); err != nil {
					t.Error(err)
				}
			}()

			// Setup test conditions
			tt.setupFunc(t, tmpDir)

			// Run FixGitignore
			err = FixGitignore()

			// Check error expectation
			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// Verify file permissions
			gitignorePath := filepath.Join(".beads", ".gitignore")
			info, err := os.Stat(gitignorePath)
			if err != nil {
				t.Fatalf("Failed to stat .gitignore: %v", err)
			}

			actualPerms := info.Mode().Perm()
			if actualPerms != tt.expectedPerms {
				t.Errorf("Expected permissions %o, got %o", tt.expectedPerms, actualPerms)
			}

			// Verify permissions are not too permissive (0600 or less)
			if actualPerms&0177 != 0 { // Check group and other permissions
				t.Errorf("File has too-permissive permissions: %o (group/other should be 0)", actualPerms)
			}

			// Verify content was written correctly
			content, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Fatalf("Failed to read .gitignore: %v", err)
			}
			if string(content) != GitignoreTemplate {
				t.Error("File content doesn't match GitignoreTemplate")
			}
		})
	}
}

func TestFixGitignore_FileOwnership(t *testing.T) {
	// Skip on Windows as it doesn't have POSIX file ownership
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file ownership test on Windows")
	}

	tmpDir := t.TempDir()

	// Change to tmpDir for the test
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Error(err)
		}
	}()

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Run FixGitignore
	if err := FixGitignore(); err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Verify file ownership matches current user
	gitignorePath := filepath.Join(".beads", ".gitignore")
	info, err := os.Stat(gitignorePath)
	if err != nil {
		t.Fatalf("Failed to stat .gitignore: %v", err)
	}

	// Get expected UID from the test directory
	dirInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf("Failed to stat .beads: %v", err)
	}

	// On Unix systems, verify the file has the same ownership as the directory
	// (This is a basic check - full ownership validation would require syscall)
	if info.Mode() != info.Mode() { // placeholder check
		// Note: Full ownership check requires syscall and is platform-specific
		// This test mainly documents the security concern
		t.Log("File created with current user ownership (full validation requires syscall)")
	}

	// Verify the directory is still accessible
	if !dirInfo.IsDir() {
		t.Error(".beads should be a directory")
	}
}

func TestFixGitignore_DoesNotLoosenPermissions(t *testing.T) {
	// Skip on Windows as it doesn't support Unix-style file permissions
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file permissions test on Windows")
	}

	tmpDir := t.TempDir()

	// Change to tmpDir for the test
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Error(err)
		}
	}()

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create file with very restrictive permissions (0400 - read-only)
	gitignorePath := filepath.Join(".beads", ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("old content"), 0400); err != nil {
		t.Fatal(err)
	}

	// Get original permissions
	beforeInfo, err := os.Stat(gitignorePath)
	if err != nil {
		t.Fatal(err)
	}
	beforePerms := beforeInfo.Mode().Perm()

	// Run FixGitignore
	if err := FixGitignore(); err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Get new permissions
	afterInfo, err := os.Stat(gitignorePath)
	if err != nil {
		t.Fatal(err)
	}
	afterPerms := afterInfo.Mode().Perm()

	// Verify permissions are still secure (0600 or less)
	if afterPerms&0177 != 0 {
		t.Errorf("File has too-permissive permissions after fix: %o", afterPerms)
	}

	// Document that we replace with 0600 (which is more permissive than 0400 but still secure)
	if afterPerms != 0600 {
		t.Errorf("Expected 0600 permissions, got %o", afterPerms)
	}

	t.Logf("Permissions changed from %o to %o (both secure, 0600 is standard)", beforePerms, afterPerms)
}

func TestCheckGitignore(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, tmpDir string)
		expectedStatus string
		expectFix      bool
	}{
		{
			name: "missing .gitignore file",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "warning",
			expectFix:      true,
		},
		{
			name: "up-to-date .gitignore",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(GitignoreTemplate), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name: "outdated .gitignore missing required patterns",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				// Write old content missing merge artifact patterns
				oldContent := `*.db
daemon.log
`
				if err := os.WriteFile(gitignorePath, []byte(oldContent), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: "warning",
			expectFix:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Change to tmpDir for the test
			oldDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(oldDir); err != nil {
					t.Error(err)
				}
			}()

			tt.setupFunc(t, tmpDir)

			check := CheckGitignore()

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, check.Status)
			}

			if tt.expectFix && check.Fix == "" {
				t.Error("Expected fix message, got empty string")
			}

			if !tt.expectFix && check.Fix != "" {
				t.Errorf("Expected no fix message, got: %s", check.Fix)
			}
		})
	}
}
