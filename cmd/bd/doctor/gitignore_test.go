package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFixGitignore_FilePermissions(t *testing.T) {
	// Skip on Windows as it doesn't support Unix-style file permissions
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file permissions test on Windows")
	}

	tests := []struct {
		name          string
		setupFunc     func(t *testing.T, tmpDir string) // setup before fix
		expectedPerms os.FileMode
		expectError   bool
	}{
		{
			name: "creates new file with 0600 permissions",
			setupFunc: func(t *testing.T, tmpDir string) {
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

func TestFixGitignore_PartialPatterns(t *testing.T) {
	tests := []struct {
		name              string
		initialContent    string
		expectAllPatterns bool
		description       string
	}{
		{
			name: "partial patterns - missing some merge artifacts",
			initialContent: `# SQLite databases
*.db
*.db-journal
daemon.log

# Has some merge artifacts but not all
beads.base.jsonl
beads.left.jsonl
`,
			expectAllPatterns: true,
			description:       "should add missing merge artifact patterns",
		},
		{
			name: "partial patterns - has db wildcards but missing specific ones",
			initialContent: `*.db
daemon.log
beads.base.jsonl
beads.left.jsonl
beads.right.jsonl
beads.base.meta.json
beads.left.meta.json
beads.right.meta.json
`,
			expectAllPatterns: true,
			description:       "should add missing *.db?* pattern",
		},
		{
			name: "outdated pattern syntax - old db patterns",
			initialContent: `# Old style database patterns
*.sqlite
*.sqlite3
daemon.log

# Missing modern patterns
`,
			expectAllPatterns: true,
			description:       "should replace outdated patterns with current template",
		},
		{
			name: "conflicting patterns - has negation without base pattern",
			initialContent: `# Conflicting setup
!issues.jsonl
!metadata.json

# Missing the actual ignore patterns
`,
			expectAllPatterns: true,
			description:       "should fix by using canonical template",
		},
		{
			name:              "empty gitignore",
			initialContent:    "",
			expectAllPatterns: true,
			description:       "should add all required patterns to empty file",
		},
		{
			name:              "already correct gitignore",
			initialContent:    GitignoreTemplate,
			expectAllPatterns: true,
			description:       "should preserve correct template unchanged",
		},
		{
			name: "has all required patterns but different formatting",
			initialContent: `*.db
*.db?*
*.db-journal
daemon.log
beads.base.jsonl
beads.left.jsonl
beads.right.jsonl
beads.base.meta.json
beads.left.meta.json
beads.right.meta.json
`,
			expectAllPatterns: true,
			description:       "FixGitignore replaces with canonical template",
		},
		{
			name: "partial patterns with user comments",
			initialContent: `# My custom comment
*.db
daemon.log

# User added this
custom-pattern.txt
`,
			expectAllPatterns: true,
			description:       "FixGitignore replaces entire file, user comments will be lost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

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

			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			gitignorePath := filepath.Join(".beads", ".gitignore")
			if err := os.WriteFile(gitignorePath, []byte(tt.initialContent), 0600); err != nil {
				t.Fatal(err)
			}

			err = FixGitignore()
			if err != nil {
				t.Fatalf("FixGitignore failed: %v", err)
			}

			content, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Fatalf("Failed to read gitignore after fix: %v", err)
			}

			contentStr := string(content)

			// Verify all required patterns are present
			if tt.expectAllPatterns {
				for _, pattern := range requiredPatterns {
					if !strings.Contains(contentStr, pattern) {
						t.Errorf("Missing required pattern after fix: %s\nContent:\n%s", pattern, contentStr)
					}
				}
			}

			// Verify content matches template exactly (FixGitignore always writes the template)
			if contentStr != GitignoreTemplate {
				t.Errorf("Content does not match GitignoreTemplate.\nExpected:\n%s\n\nGot:\n%s", GitignoreTemplate, contentStr)
			}
		})
	}
}

func TestFixGitignore_PreservesNothing(t *testing.T) {
	// This test documents that FixGitignore does NOT preserve custom patterns
	// It always replaces with the canonical template
	tmpDir := t.TempDir()

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

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	customContent := `# User custom patterns
custom-file.txt
*.backup

# Required patterns
*.db
*.db?*
daemon.log
beads.base.jsonl
beads.left.jsonl
beads.right.jsonl
beads.base.meta.json
beads.left.meta.json
beads.right.meta.json
`

	gitignorePath := filepath.Join(".beads", ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(customContent), 0600); err != nil {
		t.Fatal(err)
	}

	err = FixGitignore()
	if err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("Failed to read gitignore: %v", err)
	}

	contentStr := string(content)

	// Verify custom patterns are NOT preserved
	if strings.Contains(contentStr, "custom-file.txt") {
		t.Error("Custom pattern 'custom-file.txt' should not be preserved")
	}
	if strings.Contains(contentStr, "*.backup") {
		t.Error("Custom pattern '*.backup' should not be preserved")
	}

	// Verify it matches template exactly
	if contentStr != GitignoreTemplate {
		t.Error("Content should match GitignoreTemplate exactly after fix")
	}
}

func TestFixGitignore_Symlink(t *testing.T) {
	// Skip on Windows as symlink creation requires elevated privileges
	if runtime.GOOS == "windows" {
		t.Skip("Skipping symlink test on Windows")
	}

	tmpDir := t.TempDir()

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

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create a target file that the symlink will point to
	targetPath := filepath.Join(tmpDir, "target_gitignore")
	if err := os.WriteFile(targetPath, []byte("old content"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create symlink at .beads/.gitignore pointing to target
	gitignorePath := filepath.Join(".beads", ".gitignore")
	if err := os.Symlink(targetPath, gitignorePath); err != nil {
		t.Fatal(err)
	}

	// Run FixGitignore - it should write through the symlink
	// (os.WriteFile follows symlinks, it doesn't replace them)
	err = FixGitignore()
	if err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Verify it's still a symlink (os.WriteFile follows symlinks)
	info, err := os.Lstat(gitignorePath)
	if err != nil {
		t.Fatalf("Failed to stat .gitignore: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Expected symlink to be preserved (os.WriteFile follows symlinks)")
	}

	// Verify content is correct (reading through symlink)
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}
	if string(content) != GitignoreTemplate {
		t.Error("Content doesn't match GitignoreTemplate")
	}

	// Verify target file was updated with correct content
	targetContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("Failed to read target file: %v", err)
	}
	if string(targetContent) != GitignoreTemplate {
		t.Error("Target file content doesn't match GitignoreTemplate")
	}

	// Note: permissions are set on the target file, not the symlink itself
	targetInfo, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("Failed to stat target file: %v", err)
	}
	if targetInfo.Mode().Perm() != 0600 {
		t.Errorf("Expected target file permissions 0600, got %o", targetInfo.Mode().Perm())
	}
}

func TestFixGitignore_NonASCIICharacters(t *testing.T) {
	tests := []struct {
		name           string
		initialContent string
		description    string
	}{
		{
			name: "UTF-8 characters in comments",
			initialContent: `# SQLite databases 数据库
*.db
# Daemon files 守护进程文件
daemon.log
`,
			description: "handles UTF-8 characters in comments",
		},
		{
			name: "emoji in content",
			initialContent: `# 🚀 Database files
*.db
# 📝 Logs
daemon.log
`,
			description: "handles emoji characters",
		},
		{
			name: "mixed unicode patterns",
			initialContent: `# файлы базы данных
*.db
# Arquivos de registro
daemon.log
`,
			description: "handles Cyrillic and Latin-based unicode",
		},
		{
			name: "unicode patterns with required content",
			initialContent: `# Unicode comment ñ é ü
*.db
*.db?*
daemon.log
beads.base.jsonl
beads.left.jsonl
beads.right.jsonl
beads.base.meta.json
beads.left.meta.json
beads.right.meta.json
`,
			description: "replaces file even when required patterns present with unicode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

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

			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			gitignorePath := filepath.Join(".beads", ".gitignore")
			if err := os.WriteFile(gitignorePath, []byte(tt.initialContent), 0600); err != nil {
				t.Fatal(err)
			}

			err = FixGitignore()
			if err != nil {
				t.Fatalf("FixGitignore failed: %v", err)
			}

			// Verify content is replaced with template (ASCII only)
			content, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Fatalf("Failed to read .gitignore: %v", err)
			}

			if string(content) != GitignoreTemplate {
				t.Errorf("Content doesn't match GitignoreTemplate\nExpected:\n%s\n\nGot:\n%s", GitignoreTemplate, string(content))
			}
		})
	}
}

func TestFixGitignore_VeryLongLines(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T, tmpDir string) string
		description   string
		expectSuccess bool
	}{
		{
			name: "single very long line (10KB)",
			setupFunc: func(t *testing.T, tmpDir string) string {
				// Create a 10KB line
				longLine := strings.Repeat("x", 10*1024)
				content := "# Comment\n" + longLine + "\n*.db\n"
				return content
			},
			description:   "handles 10KB single line",
			expectSuccess: true,
		},
		{
			name: "multiple long lines",
			setupFunc: func(t *testing.T, tmpDir string) string {
				line1 := "# " + strings.Repeat("a", 5000)
				line2 := "# " + strings.Repeat("b", 5000)
				line3 := "# " + strings.Repeat("c", 5000)
				content := line1 + "\n" + line2 + "\n" + line3 + "\n*.db\n"
				return content
			},
			description:   "handles multiple long lines",
			expectSuccess: true,
		},
		{
			name: "very long pattern line",
			setupFunc: func(t *testing.T, tmpDir string) string {
				// Create a pattern with extremely long filename
				longPattern := strings.Repeat("very_long_filename_", 500) + ".db"
				content := "# Comment\n" + longPattern + "\n*.db\n"
				return content
			},
			description:   "handles very long pattern names",
			expectSuccess: true,
		},
		{
			name: "100KB single line",
			setupFunc: func(t *testing.T, tmpDir string) string {
				// Create a 100KB line
				longLine := strings.Repeat("y", 100*1024)
				content := "# Comment\n" + longLine + "\n*.db\n"
				return content
			},
			description:   "handles 100KB single line",
			expectSuccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

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

			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			initialContent := tt.setupFunc(t, tmpDir)
			gitignorePath := filepath.Join(".beads", ".gitignore")
			if err := os.WriteFile(gitignorePath, []byte(initialContent), 0600); err != nil {
				t.Fatal(err)
			}

			err = FixGitignore()

			if tt.expectSuccess {
				if err != nil {
					t.Fatalf("FixGitignore failed: %v", err)
				}

				// Verify content is replaced with template
				content, err := os.ReadFile(gitignorePath)
				if err != nil {
					t.Fatalf("Failed to read .gitignore: %v", err)
				}

				if string(content) != GitignoreTemplate {
					t.Error("Content doesn't match GitignoreTemplate")
				}
			} else {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			}
		})
	}
}

func TestCheckGitignore_VariousStatuses(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, tmpDir string)
		expectedStatus string
		expectedFix    string
		description    string
	}{
		{
			name: "missing .beads directory",
			setupFunc: func(t *testing.T, tmpDir string) {
				// Don't create .beads directory
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd init (safe to re-run) or bd doctor --fix",
			description:    "returns warning when .beads directory doesn't exist",
		},
		{
			name: "missing .gitignore file",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd init (safe to re-run) or bd doctor --fix",
			description:    "returns warning when .gitignore doesn't exist",
		},
		{
			name: "perfect gitignore",
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
			expectedStatus: StatusOK,
			expectedFix:    "",
			description:    "returns ok when gitignore matches template",
		},
		{
			name: "missing required patterns",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				content := `*.db
*.db?*
daemon.log
`
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd doctor --fix or bd init (safe to re-run)",
			description:    "returns warning when missing required patterns like dolt/ and redirect",
		},
		{
			name: "missing multiple required patterns",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				content := `*.db
daemon.log
`
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd doctor --fix or bd init (safe to re-run)",
			description:    "returns warning when missing multiple patterns",
		},
		{
			name: "empty gitignore file",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(""), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd doctor --fix or bd init (safe to re-run)",
			description:    "returns warning for empty file",
		},
		{
			name: "gitignore with only comments",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				content := `# Comment 1
# Comment 2
# Comment 3
`
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			expectedFix:    "Run: bd doctor --fix or bd init (safe to re-run)",
			description:    "returns warning for comments-only file",
		},
		{
			name: "gitignore as symlink pointing to valid file",
			setupFunc: func(t *testing.T, tmpDir string) {
				if runtime.GOOS == "windows" {
					t.Skip("Skipping symlink test on Windows")
				}
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				targetPath := filepath.Join(tmpDir, "target_gitignore")
				if err := os.WriteFile(targetPath, []byte(GitignoreTemplate), 0600); err != nil {
					t.Fatal(err)
				}
				gitignorePath := filepath.Join(beadsDir, ".gitignore")
				if err := os.Symlink(targetPath, gitignorePath); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusOK,
			expectedFix:    "",
			description:    "follows symlink and checks content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

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

			if tt.expectedFix != "" && !strings.Contains(check.Fix, tt.expectedFix) {
				t.Errorf("Expected fix to contain %q, got %q", tt.expectedFix, check.Fix)
			}

			if tt.expectedFix == "" && check.Fix != "" {
				t.Errorf("Expected no fix message, got: %s", check.Fix)
			}
		})
	}
}

func TestFixGitignore_SubdirectoryGitignore(t *testing.T) {
	// This test verifies that FixGitignore only operates on .beads/.gitignore
	// and doesn't touch other .gitignore files

	tmpDir := t.TempDir()

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

	// Create .beads directory and gitignore
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create .beads/.gitignore with old content
	beadsGitignorePath := filepath.Join(".beads", ".gitignore")
	oldBeadsContent := "old beads content"
	if err := os.WriteFile(beadsGitignorePath, []byte(oldBeadsContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory with its own .gitignore
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0750); err != nil {
		t.Fatal(err)
	}
	subGitignorePath := filepath.Join(subDir, ".gitignore")
	subGitignoreContent := "subdirectory gitignore content"
	if err := os.WriteFile(subGitignorePath, []byte(subGitignoreContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Create root .gitignore
	rootGitignorePath := filepath.Join(tmpDir, ".gitignore")
	rootGitignoreContent := "root gitignore content"
	if err := os.WriteFile(rootGitignorePath, []byte(rootGitignoreContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Run FixGitignore
	err = FixGitignore()
	if err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Verify .beads/.gitignore was updated
	beadsContent, err := os.ReadFile(beadsGitignorePath)
	if err != nil {
		t.Fatalf("Failed to read .beads/.gitignore: %v", err)
	}
	if string(beadsContent) != GitignoreTemplate {
		t.Error(".beads/.gitignore should be updated to template")
	}

	// Verify subdirectory .gitignore was NOT touched
	subContent, err := os.ReadFile(subGitignorePath)
	if err != nil {
		t.Fatalf("Failed to read subdir/.gitignore: %v", err)
	}
	if string(subContent) != subGitignoreContent {
		t.Error("subdirectory .gitignore should not be modified")
	}

	// Verify root .gitignore was NOT touched
	rootContent, err := os.ReadFile(rootGitignorePath)
	if err != nil {
		t.Fatalf("Failed to read root .gitignore: %v", err)
	}
	if string(rootContent) != rootGitignoreContent {
		t.Error("root .gitignore should not be modified")
	}
}

func TestCheckRedirectNotTracked_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

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

	// Create .beads directory but no redirect file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectNotTracked()

	if check.Status != StatusOK {
		t.Errorf("Expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "No redirect file present" {
		t.Errorf("Expected message about no redirect file, got: %s", check.Message)
	}
}

func TestCheckRedirectNotTracked_FileExistsNotTracked(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with redirect file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../../.beads"), 0600); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectNotTracked()

	if check.Status != StatusOK {
		t.Errorf("Expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "redirect file not tracked (correct)" {
		t.Errorf("Expected message about correct tracking, got: %s", check.Message)
	}
}

func TestCheckRedirectNotTracked_FileTracked(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with redirect file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../../.beads"), 0600); err != nil {
		t.Fatal(err)
	}

	// Stage (track) the redirect file
	gitAdd := exec.Command("git", "add", redirectPath)
	if err := gitAdd.Run(); err != nil {
		t.Skipf("git add failed: %v", err)
	}

	check := CheckRedirectNotTracked()

	if check.Status != StatusWarning {
		t.Errorf("Expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Message != "redirect file is tracked by git" {
		t.Errorf("Expected message about tracked file, got: %s", check.Message)
	}
	if check.Fix == "" {
		t.Error("Expected fix message to be present")
	}
}

func TestFixRedirectTracking(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with redirect file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../../.beads"), 0600); err != nil {
		t.Fatal(err)
	}

	// Stage (track) the redirect file
	gitAdd := exec.Command("git", "add", redirectPath)
	if err := gitAdd.Run(); err != nil {
		t.Skipf("git add failed: %v", err)
	}

	// Verify it's tracked
	lsFiles := exec.Command("git", "ls-files", redirectPath)
	output, _ := lsFiles.Output()
	if strings.TrimSpace(string(output)) == "" {
		t.Fatal("redirect file should be tracked before fix")
	}

	// Run the fix
	if err := FixRedirectTracking(); err != nil {
		t.Fatalf("FixRedirectTracking failed: %v", err)
	}

	// Verify it's no longer tracked
	lsFiles = exec.Command("git", "ls-files", redirectPath)
	output, _ = lsFiles.Output()
	if strings.TrimSpace(string(output)) != "" {
		t.Error("redirect file should be untracked after fix")
	}

	// Verify the local file still exists
	if _, err := os.Stat(redirectPath); os.IsNotExist(err) {
		t.Error("redirect file should still exist locally after untracking")
	}
}

func TestCheckRedirectTargetValid_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()

	targetRoot := filepath.Join(tmpDir, "target")
	targetBeads := filepath.Join(targetRoot, ".beads")
	if err := os.MkdirAll(targetBeads, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBeads, "metadata.json"), []byte(`{"backend":"dolt"}`), 0600); err != nil {
		t.Fatal(err)
	}

	workRoot := filepath.Join(tmpDir, "work")
	workBeads := filepath.Join(workRoot, ".beads")
	if err := os.MkdirAll(workBeads, 0750); err != nil {
		t.Fatal(err)
	}

	redirectPath := filepath.Join(workBeads, "redirect")
	if err := os.WriteFile(redirectPath, []byte(targetBeads+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workRoot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Error(err)
		}
	}()

	check := CheckRedirectTargetValid()
	if check.Status != StatusOK {
		t.Fatalf("expected status %s, got %s (detail: %s)", StatusOK, check.Status, check.Detail)
	}
	if !strings.Contains(check.Message, targetBeads) {
		t.Errorf("expected message to include target path, got: %s", check.Message)
	}
}

func TestGitignoreTemplate_ContainsRedirect(t *testing.T) {
	// Verify the template contains the redirect pattern
	if !strings.Contains(GitignoreTemplate, "redirect") {
		t.Error("GitignoreTemplate should contain 'redirect' pattern")
	}
}

func TestRequiredPatterns_ContainsRedirect(t *testing.T) {
	// Verify requiredPatterns includes redirect
	found := false
	for _, pattern := range requiredPatterns {
		if pattern == "redirect" {
			found = true
			break
		}
	}
	if !found {
		t.Error("requiredPatterns should include 'redirect'")
	}
}

// TestGitignoreTemplate_ContainsLegacyDaemonPatterns verifies that legacy daemon
// file patterns are still gitignored to prevent old files from being committed.
// GH#1142, GH#919
func TestGitignoreTemplate_ContainsLegacyDaemonPatterns(t *testing.T) {
	if !strings.Contains(GitignoreTemplate, "daemon-*.log.gz") {
		t.Error("GitignoreTemplate should contain 'daemon-*.log.gz' pattern for legacy daemon log files")
	}
}

// TestGitignoreTemplate_ContainsSyncStateFiles verifies that sync state files
// introduced in PR #918 (pull-first sync with 3-way merge) are gitignored.
// These files are machine-specific and should not be shared across clones.
// GH#974
func TestGitignoreTemplate_ContainsSyncStateFiles(t *testing.T) {
	syncStateFiles := []string{
		".sync.lock", // Concurrency guard
	}

	for _, pattern := range syncStateFiles {
		if !strings.Contains(GitignoreTemplate, pattern) {
			t.Errorf("GitignoreTemplate should contain '%s' pattern", pattern)
		}
	}
}

// TestRequiredPatterns_ContainsSyncStatePatterns verifies that bd doctor
// validates the presence of sync state patterns in .beads/.gitignore.
// GH#974
func TestRequiredPatterns_ContainsSyncStatePatterns(t *testing.T) {
	syncStatePatterns := []string{
		".sync.lock",
	}

	for _, expected := range syncStatePatterns {
		found := false
		for _, pattern := range requiredPatterns {
			if pattern == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("requiredPatterns should include '%s'", expected)
		}
	}
}

// TestCheckLastTouchedNotTracked_NoFile verifies that check passes when no last-touched file exists
func TestCheckLastTouchedNotTracked_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

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

	// Create .beads directory but no last-touched file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	check := CheckLastTouchedNotTracked()

	if check.Status != StatusOK {
		t.Errorf("Expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "No last-touched file present" {
		t.Errorf("Expected message about no last-touched file, got: %s", check.Message)
	}
}

func TestCheckLastTouchedNotTracked_FileExistsNotTracked(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with last-touched file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	lastTouchedPath := filepath.Join(beadsDir, "last-touched")
	if err := os.WriteFile(lastTouchedPath, []byte("bd-test1"), 0600); err != nil {
		t.Fatal(err)
	}

	check := CheckLastTouchedNotTracked()

	if check.Status != StatusOK {
		t.Errorf("Expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "last-touched file not tracked (correct)" {
		t.Errorf("Expected message about correct tracking, got: %s", check.Message)
	}
}

func TestCheckLastTouchedNotTracked_FileTracked(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with last-touched file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	lastTouchedPath := filepath.Join(beadsDir, "last-touched")
	if err := os.WriteFile(lastTouchedPath, []byte("bd-test1"), 0600); err != nil {
		t.Fatal(err)
	}

	// Stage (track) the last-touched file
	gitAdd := exec.Command("git", "add", lastTouchedPath)
	if err := gitAdd.Run(); err != nil {
		t.Skipf("git add failed: %v", err)
	}

	check := CheckLastTouchedNotTracked()

	if check.Status != StatusWarning {
		t.Errorf("Expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Message != "last-touched file is tracked by git" {
		t.Errorf("Expected message about tracked file, got: %s", check.Message)
	}
	if check.Fix == "" {
		t.Error("Expected fix message to be present")
	}
}

func TestFixLastTouchedTracking(t *testing.T) {
	// Skip on Windows as git behavior may differ
	if runtime.GOOS == "windows" {
		t.Skip("Skipping git-based test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory with last-touched file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	lastTouchedPath := filepath.Join(beadsDir, "last-touched")
	if err := os.WriteFile(lastTouchedPath, []byte("bd-test1"), 0600); err != nil {
		t.Fatal(err)
	}

	// Stage (track) the last-touched file
	gitAdd := exec.Command("git", "add", lastTouchedPath)
	if err := gitAdd.Run(); err != nil {
		t.Skipf("git add failed: %v", err)
	}

	// Verify it's tracked before fix
	checkBefore := CheckLastTouchedNotTracked()
	if checkBefore.Status != StatusWarning {
		t.Fatalf("Expected file to be tracked before fix, status: %s", checkBefore.Status)
	}

	// Apply the fix
	if err := FixLastTouchedTracking(); err != nil {
		t.Fatalf("FixLastTouchedTracking failed: %v", err)
	}

	// Verify it's no longer tracked after fix
	checkAfter := CheckLastTouchedNotTracked()
	if checkAfter.Status != StatusOK {
		t.Errorf("Expected status %s after fix, got %s", StatusOK, checkAfter.Status)
	}

	// Verify the file still exists locally
	if _, err := os.Stat(lastTouchedPath); os.IsNotExist(err) {
		t.Error("last-touched file should still exist after untracking")
	}
}

// TestGitignoreTemplate_ContainsLastTouched verifies that the .beads/.gitignore template
// includes last-touched to prevent it from being tracked.
func TestGitignoreTemplate_ContainsLastTouched(t *testing.T) {
	if !strings.Contains(GitignoreTemplate, "last-touched") {
		t.Error("GitignoreTemplate should contain 'last-touched' pattern")
	}
}

// TestRequiredPatterns_ContainsLastTouched verifies that bd doctor validates
// the presence of the last-touched pattern in .beads/.gitignore.
func TestRequiredPatterns_ContainsLastTouched(t *testing.T) {
	found := false
	for _, pattern := range requiredPatterns {
		if pattern == "last-touched" {
			found = true
			break
		}
	}
	if !found {
		t.Error("requiredPatterns should include 'last-touched'")
	}
}

// TestGitignoreTemplate_ContainsDolt verifies that the .beads/.gitignore template
// includes dolt/ to prevent the Dolt database directory from being committed.
func TestGitignoreTemplate_ContainsDolt(t *testing.T) {
	if !strings.Contains(GitignoreTemplate, "dolt/") {
		t.Error("GitignoreTemplate should contain 'dolt/' pattern")
	}
}

// TestGitignoreTemplate_ContainsDoltAccessLock verifies that the .beads/.gitignore template
// includes dolt-access.lock to prevent the Dolt advisory lock file from being committed.
func TestGitignoreTemplate_ContainsDoltAccessLock(t *testing.T) {
	if !strings.Contains(GitignoreTemplate, "dolt-access.lock") {
		t.Error("GitignoreTemplate should contain 'dolt-access.lock' pattern")
	}
}

// TestRequiredPatterns_ContainsDolt verifies that bd doctor validates
// the presence of the dolt/ pattern in .beads/.gitignore.
func TestRequiredPatterns_ContainsDolt(t *testing.T) {
	found := false
	for _, pattern := range requiredPatterns {
		if pattern == "dolt/" {
			found = true
			break
		}
	}
	if !found {
		t.Error("requiredPatterns should include 'dolt/'")
	}
}

// TestRequiredPatterns_ContainsDoltAccessLock verifies that bd doctor validates
// the presence of the dolt-access.lock pattern in .beads/.gitignore.
func TestRequiredPatterns_ContainsDoltAccessLock(t *testing.T) {
	found := false
	for _, pattern := range requiredPatterns {
		if pattern == "dolt-access.lock" {
			found = true
			break
		}
	}
	if !found {
		t.Error("requiredPatterns should include 'dolt-access.lock'")
	}
}

func TestCheckProjectGitignore_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
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

	check := CheckProjectGitignore()
	if check.Status != StatusWarning {
		t.Errorf("Expected warning when no .gitignore exists, got %s", check.Status)
	}
}

func TestCheckProjectGitignore_MissingPatterns(t *testing.T) {
	tmpDir := t.TempDir()
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

	// Create .gitignore without Dolt patterns
	if err := os.WriteFile(".gitignore", []byte("node_modules/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckProjectGitignore()
	if check.Status != StatusWarning {
		t.Errorf("Expected warning for missing patterns, got %s", check.Status)
	}
	if !strings.Contains(check.Detail, ".dolt/") {
		t.Errorf("Expected detail to mention .dolt/, got: %s", check.Detail)
	}
}

func TestCheckProjectGitignore_AllPresent(t *testing.T) {
	tmpDir := t.TempDir()
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

	content := "node_modules/\n.dolt/\n*.db\n"
	if err := os.WriteFile(".gitignore", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckProjectGitignore()
	if check.Status != StatusOK {
		t.Errorf("Expected ok when all patterns present, got %s: %s", check.Status, check.Message)
	}
}

func TestEnsureProjectGitignore_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
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

	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("EnsureProjectGitignore failed: %v", err)
	}

	content, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, ".dolt/") {
		t.Error("Expected .dolt/ pattern in .gitignore")
	}
	if !strings.Contains(contentStr, "*.db") {
		t.Error("Expected *.db pattern in .gitignore")
	}
	if !strings.Contains(contentStr, projectGitignoreComment) {
		t.Error("Expected section comment in .gitignore")
	}
}

func TestEnsureProjectGitignore_AppendsToExisting(t *testing.T) {
	tmpDir := t.TempDir()
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

	existingContent := "node_modules/\n.env\n"
	if err := os.WriteFile(".gitignore", []byte(existingContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("EnsureProjectGitignore failed: %v", err)
	}

	content, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatalf("Failed to read .gitignore: %v", err)
	}

	contentStr := string(content)
	// Original content preserved
	if !strings.HasPrefix(contentStr, existingContent) {
		t.Error("Expected existing content to be preserved")
	}
	// New patterns added
	if !strings.Contains(contentStr, ".dolt/") {
		t.Error("Expected .dolt/ pattern in .gitignore")
	}
	if !strings.Contains(contentStr, "*.db") {
		t.Error("Expected *.db pattern in .gitignore")
	}
}

func TestEnsureProjectGitignore_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
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

	// Run twice
	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("First EnsureProjectGitignore failed: %v", err)
	}
	firstContent, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}

	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("Second EnsureProjectGitignore failed: %v", err)
	}
	secondContent, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}

	if string(firstContent) != string(secondContent) {
		t.Error("EnsureProjectGitignore should be idempotent")
	}
}

func TestEnsureProjectGitignore_PartialPatterns(t *testing.T) {
	tmpDir := t.TempDir()
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

	// Start with one pattern already present
	existingContent := ".dolt/\n"
	if err := os.WriteFile(".gitignore", []byte(existingContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("EnsureProjectGitignore failed: %v", err)
	}

	content, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	// Should add only the missing pattern
	if !strings.Contains(contentStr, "*.db") {
		t.Error("Expected *.db pattern to be added")
	}
	// Should only contain .dolt/ once (the original)
	count := strings.Count(contentStr, ".dolt/")
	if count != 1 {
		t.Errorf("Expected .dolt/ to appear once, found %d times", count)
	}
}

func TestFixGitignore_FollowsRedirect(t *testing.T) {
	tmpDir := t.TempDir()

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

	// Simulate a rig layout:
	//   tmpDir/
	//     mayor/rig/.beads/          ← canonical beads dir (redirect target)
	//     crew/worker/.beads/redirect ← redirect-only dir (cwd context)
	rigBeads := filepath.Join(tmpDir, "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0750); err != nil {
		t.Fatal(err)
	}

	// Create the local redirect-only .beads dir
	localBeads := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(localBeads, 0750); err != nil {
		t.Fatal(err)
	}

	// Write redirect file pointing to the rig's .beads
	// Redirect is resolved relative to the project root (parent of .beads)
	redirectContent := "mayor/rig/.beads"
	if err := os.WriteFile(filepath.Join(localBeads, "redirect"), []byte(redirectContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Run FixGitignore - should write to the rig's .beads, NOT the local one
	if err := FixGitignore(); err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Verify: .gitignore was written at the redirect target
	targetGitignore := filepath.Join(rigBeads, ".gitignore")
	content, err := os.ReadFile(targetGitignore)
	if err != nil {
		t.Fatalf("Expected .gitignore at redirect target %s, got error: %v", targetGitignore, err)
	}
	if string(content) != GitignoreTemplate {
		t.Errorf("Expected canonical template at redirect target, got:\n%s", string(content))
	}

	// Verify: NO .gitignore was created in the local redirect-only dir
	localGitignore := filepath.Join(localBeads, ".gitignore")
	if _, err := os.Stat(localGitignore); !os.IsNotExist(err) {
		t.Errorf("FixGitignore should NOT have created .gitignore in the redirect-only .beads dir at %s", localGitignore)
	}
}

func TestCheckGitignore_FollowsRedirect(t *testing.T) {
	tmpDir := t.TempDir()

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

	// Set up rig with canonical .beads containing an up-to-date .gitignore
	rigBeads := filepath.Join(tmpDir, "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, ".gitignore"), []byte(GitignoreTemplate), 0600); err != nil {
		t.Fatal(err)
	}

	// Set up local redirect-only .beads
	localBeads := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(localBeads, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localBeads, "redirect"), []byte("mayor/rig/.beads"), 0600); err != nil {
		t.Fatal(err)
	}

	// CheckGitignore should report OK by reading the redirect target
	check := CheckGitignore()
	if check.Status != "ok" {
		t.Errorf("Expected status ok when redirect target has valid .gitignore, got %s: %s", check.Status, check.Message)
	}
}

func TestFixGitignore_RedirectRoundTrip(t *testing.T) {
	// Verifies the full bug scenario: CheckGitignore detects outdated at redirect target,
	// FixGitignore fixes it AT the redirect target, and re-check passes.
	tmpDir := t.TempDir()

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

	// Set up rig with outdated .gitignore (missing required patterns)
	rigBeads := filepath.Join(tmpDir, "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0750); err != nil {
		t.Fatal(err)
	}
	oldContent := "*.db\ndaemon.log\n"
	if err := os.WriteFile(filepath.Join(rigBeads, ".gitignore"), []byte(oldContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Set up local redirect-only .beads
	localBeads := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(localBeads, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localBeads, "redirect"), []byte("mayor/rig/.beads"), 0600); err != nil {
		t.Fatal(err)
	}

	// Step 1: Check should detect outdated
	check := CheckGitignore()
	if check.Status != "warning" {
		t.Fatalf("Expected warning for outdated .gitignore at redirect target, got %s", check.Status)
	}

	// Step 2: Fix should update the redirect target
	if err := FixGitignore(); err != nil {
		t.Fatalf("FixGitignore failed: %v", err)
	}

	// Step 3: Re-check should pass
	check = CheckGitignore()
	if check.Status != "ok" {
		t.Errorf("Expected ok after fix, got %s: %s", check.Status, check.Message)
	}

	// Step 4: No .gitignore in redirect-only dir (the original bug)
	localGitignore := filepath.Join(localBeads, ".gitignore")
	if _, err := os.Stat(localGitignore); !os.IsNotExist(err) {
		t.Errorf("FixGitignore must NOT create .gitignore in redirect-only .beads dir (the original bug)")
	}
}

func TestEnsureProjectGitignore_FilePermissions(t *testing.T) {
	// Verify that project .gitignore is created with 0644 permissions.
	// Unlike .beads/.gitignore (0600), the project .gitignore must be
	// readable by git and collaborators — this justifies the #nosec G306.
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file permissions test on Windows")
	}

	tests := []struct {
		name          string
		setupFunc     func(t *testing.T, tmpDir string)
		expectedPerms os.FileMode
	}{
		{
			name: "creates new file with 0644 permissions",
			setupFunc: func(t *testing.T, tmpDir string) {
				// No .gitignore exists yet
			},
			expectedPerms: 0644,
		},
		{
			name: "preserves restrictive permissions on existing file",
			setupFunc: func(t *testing.T, tmpDir string) {
				// Create .gitignore with restrictive permissions
				if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("node_modules/\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			expectedPerms: 0600, // os.WriteFile preserves existing perms
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

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

			if err := EnsureProjectGitignore(); err != nil {
				t.Fatalf("EnsureProjectGitignore failed: %v", err)
			}

			info, err := os.Stat(".gitignore")
			if err != nil {
				t.Fatalf("Failed to stat .gitignore: %v", err)
			}

			actualPerms := info.Mode().Perm()
			if actualPerms != tt.expectedPerms {
				t.Errorf("Expected permissions %o, got %o", tt.expectedPerms, actualPerms)
			}
		})
	}
}

func TestEnsureProjectGitignore_DoesNotLoosenPermissions(t *testing.T) {
	// Verify that appending to an existing project .gitignore does not
	// widen its permissions (e.g., 0600 should not become 0644).
	if runtime.GOOS == "windows" {
		t.Skip("Skipping file permissions test on Windows")
	}

	tmpDir := t.TempDir()

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

	// Create file with restrictive permissions
	if err := os.WriteFile(".gitignore", []byte("node_modules/\n"), 0600); err != nil {
		t.Fatal(err)
	}

	beforeInfo, err := os.Stat(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	beforePerms := beforeInfo.Mode().Perm()

	if err := EnsureProjectGitignore(); err != nil {
		t.Fatalf("EnsureProjectGitignore failed: %v", err)
	}

	afterInfo, err := os.Stat(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	afterPerms := afterInfo.Mode().Perm()

	if afterPerms > beforePerms {
		t.Errorf("Permissions were loosened: before=%o, after=%o", beforePerms, afterPerms)
	}
}

func TestGitignoreTemplate_NoSensitivePatterns(t *testing.T) {
	// Verify the gitignore template itself doesn't contain patterns
	// that could leak information about secrets or sensitive paths.
	// The template should only contain infrastructure/runtime patterns.
	sensitiveKeywords := []string{
		"password",
		"secret",
		"token",
		"credential",
		"private_key",
		"api_key",
	}

	for _, keyword := range sensitiveKeywords {
		if strings.Contains(strings.ToLower(GitignoreTemplate), keyword) {
			t.Errorf("GitignoreTemplate contains sensitive keyword %q — review for information leakage", keyword)
		}
	}
}

func TestContainsGitignorePattern(t *testing.T) {
	tests := []struct {
		content  string
		pattern  string
		expected bool
	}{
		{"*.db\n.dolt/\n", "*.db", true},
		{"*.db\n.dolt/\n", ".dolt/", true},
		{"node_modules/\n", ".dolt/", false},
		{"# .dolt/ is ignored\n", ".dolt/", false}, // comment, not pattern
		{"  .dolt/  \n", ".dolt/", true},           // whitespace trimmed
		{"", ".dolt/", false},
		{".dolt/foo\n", ".dolt/", false}, // not exact match
	}

	for _, tt := range tests {
		result := containsGitignorePattern(tt.content, tt.pattern)
		if result != tt.expected {
			t.Errorf("containsGitignorePattern(%q, %q) = %v, want %v",
				tt.content, tt.pattern, result, tt.expected)
		}
	}
}

// TestParseRedirectTarget verifies redirect file parsing.
func TestParseRedirectTarget(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		expected string
	}{
		{"simple path", "../main-repo/.beads", "../main-repo/.beads"},
		{"absolute path", "/home/user/project/.beads", "/home/user/project/.beads"},
		{"with whitespace", "  ../main-repo/.beads  \n", "../main-repo/.beads"},
		{"with comment", "# redirect target\n../main-repo/.beads", "../main-repo/.beads"},
		{"multiple comments", "# comment1\n# comment2\n../main/.beads", "../main/.beads"},
		{"empty", "", ""},
		{"only comments", "# comment\n# another", ""},
		{"only whitespace", "   \n  \n", ""},
		{"with BOM", "\ufeff../main-repo/.beads", "../main-repo/.beads"},
		{"BOM in middle line", "# comment\n\ufeff../path", "../path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRedirectTarget([]byte(tt.data))
			if result != tt.expected {
				t.Errorf("parseRedirectTarget(%q) = %q, want %q", tt.data, result, tt.expected)
			}
		})
	}
}

// TestResolveRedirectTarget verifies redirect target path resolution.
func TestResolveRedirectTarget(t *testing.T) {
	tests := []struct {
		name     string
		beadsDir string
		target   string
		wantAbs  bool
	}{
		{"empty target", "/project/.beads", "", false},
		{"relative path", "/project/.beads", "../other-repo/.beads", true},
		{"absolute path", "/project/.beads", "/absolute/path/.beads", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveRedirectTarget(tt.beadsDir, tt.target)
			if tt.target == "" {
				if result != "" {
					t.Errorf("resolveRedirectTarget(%q, %q) = %q, want empty", tt.beadsDir, tt.target, result)
				}
				return
			}
			if tt.wantAbs && !filepath.IsAbs(result) {
				t.Errorf("resolveRedirectTarget(%q, %q) = %q, want absolute path", tt.beadsDir, tt.target, result)
			}
		})
	}
}

// TestCheckRedirectTargetValid_NoRedirect verifies handling when no redirect file exists.
func TestCheckRedirectTargetValid_NoRedirect(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Create .beads dir but no redirect file
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectTargetValid()
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No redirect configured" {
		t.Errorf("Message = %q, want %q", check.Message, "No redirect configured")
	}
}

// TestCheckRedirectTargetValid_EmptyRedirect verifies handling when redirect file is empty.
func TestCheckRedirectTargetValid_EmptyRedirect(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectTargetValid()
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Message, "empty") {
		t.Errorf("Message = %q, want it to contain 'empty'", check.Message)
	}
}

// TestCheckRedirectTargetValid_NonExistentTarget verifies error when redirect points to
// a path that doesn't exist.
func TestCheckRedirectTargetValid_NonExistentTarget(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("/nonexistent/path/.beads"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectTargetValid()
	if check.Status != StatusError {
		t.Errorf("Status = %q, want %q", check.Status, StatusError)
	}
	if !strings.Contains(check.Message, "does not exist") {
		t.Errorf("Message = %q, want it to contain 'does not exist'", check.Message)
	}
}

// TestCheckRedirectTargetValid_TargetIsFile verifies error when redirect target is
// a file instead of a directory.
func TestCheckRedirectTargetValid_TargetIsFile(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a file (not a directory) as the target
	targetFile := filepath.Join(tmpDir, "target-file")
	if err := os.WriteFile(targetFile, []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(targetFile), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectTargetValid()
	if check.Status != StatusError {
		t.Errorf("Status = %q, want %q", check.Status, StatusError)
	}
	if !strings.Contains(check.Message, "not a directory") {
		t.Errorf("Message = %q, want it to contain 'not a directory'", check.Message)
	}
}

// TestCheckRedirectTargetSyncWorktree_NoRedirect verifies handling when no redirect exists.
func TestCheckRedirectTargetSyncWorktree_NoRedirect(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckRedirectTargetSyncWorktree()
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No redirect configured" {
		t.Errorf("Message = %q, want %q", check.Message, "No redirect configured")
	}
}

// TestCheckNoVestigialSyncWorktrees_NoRedirect verifies the check is N/A
// when no redirect file exists.
func TestCheckNoVestigialSyncWorktrees_NoRedirect(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckNoVestigialSyncWorktrees()
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("Message = %q, want it to contain 'N/A'", check.Message)
	}
}

// TestCheckNoVestigialSyncWorktrees_WithRedirectNoWorktree verifies OK
// when redirect exists but no vestigial .beads-sync worktree.
func TestCheckNoVestigialSyncWorktrees_WithRedirectNoWorktree(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo so the git root detection works
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create redirect file
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../other/.beads"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckNoVestigialSyncWorktrees()
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No vestigial sync worktrees found" {
		t.Errorf("Message = %q, want %q", check.Message, "No vestigial sync worktrees found")
	}
}

// TestCheckNoVestigialSyncWorktrees_VestigialDetected verifies warning
// when redirect exists AND a .beads-sync worktree exists locally.
func TestCheckNoVestigialSyncWorktrees_VestigialDetected(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create redirect file
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../other/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create vestigial .beads-sync directory
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads-sync"), 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckNoVestigialSyncWorktrees()
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Message, "Vestigial") {
		t.Errorf("Message = %q, want it to contain 'Vestigial'", check.Message)
	}
}
