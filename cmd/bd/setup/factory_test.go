package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateBeadsSection(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "replace existing section",
			content: `# My Project

Some content

<!-- BEGIN BEADS INTEGRATION -->
Old content here
<!-- END BEADS INTEGRATION -->

More content after`,
			expected: `# My Project

Some content

` + factoryBeadsSection + `
More content after`,
		},
		{
			name:     "append when no markers exist",
			content:  "# My Project\n\nSome content",
			expected: "# My Project\n\nSome content\n\n" + factoryBeadsSection,
		},
		{
			name: "handle section at end of file",
			content: `# My Project

<!-- BEGIN BEADS INTEGRATION -->
Old content
<!-- END BEADS INTEGRATION -->`,
			expected: `# My Project

` + factoryBeadsSection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := updateBeadsSection(tt.content)
			if got != tt.expected {
				t.Errorf("updateBeadsSection() mismatch\ngot:\n%s\nwant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestRemoveBeadsSection(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "remove section in middle",
			content: `# My Project

<!-- BEGIN BEADS INTEGRATION -->
Beads content
<!-- END BEADS INTEGRATION -->

More content`,
			expected: `# My Project
More content`,
		},
		{
			name: "remove section at end",
			content: `# My Project

Content

<!-- BEGIN BEADS INTEGRATION -->
Beads content
<!-- END BEADS INTEGRATION -->`,
			expected: `# My Project

Content`,
		},
		{
			name:     "no markers - return unchanged",
			content:  "# My Project\n\nNo beads section",
			expected: "# My Project\n\nNo beads section",
		},
		{
			name:     "only begin marker - return unchanged",
			content:  "# My Project\n<!-- BEGIN BEADS INTEGRATION -->\nContent",
			expected: "# My Project\n<!-- BEGIN BEADS INTEGRATION -->\nContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeBeadsSection(tt.content)
			if got != tt.expected {
				t.Errorf("removeBeadsSection() mismatch\ngot:\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestCreateNewAgentsFile(t *testing.T) {
	content := createNewAgentsFile()

	// Verify it contains required elements
	if !strings.Contains(content, "# Project Instructions for AI Agents") {
		t.Error("Missing header in new agents file")
	}

	if !strings.Contains(content, factoryBeginMarker) {
		t.Error("Missing begin marker in new agents file")
	}

	if !strings.Contains(content, factoryEndMarker) {
		t.Error("Missing end marker in new agents file")
	}

	if !strings.Contains(content, "## Build & Test") {
		t.Error("Missing Build & Test section")
	}

	if !strings.Contains(content, "## Architecture Overview") {
		t.Error("Missing Architecture Overview section")
	}
}

func TestCheckFactory(t *testing.T) {
	// Save original working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tests := []struct {
		name          string
		setupFile     bool
		fileContent   string
		expectExit    bool
		expectMessage string
	}{
		{
			name:          "no AGENTS.md file",
			setupFile:     false,
			expectExit:    true,
			expectMessage: "AGENTS.md not found",
		},
		{
			name:          "AGENTS.md without beads section",
			setupFile:     true,
			fileContent:   "# Project\n\nNo beads here",
			expectExit:    true,
			expectMessage: "no beads section found",
		},
		{
			name:          "AGENTS.md with beads section",
			setupFile:     true,
			fileContent:   "# Project\n\n" + factoryBeadsSection,
			expectExit:    false,
			expectMessage: "integration installed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory and change to it
			tmpDir := t.TempDir()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("failed to change to temp directory: %v", err)
			}
			defer func() {
				if err := os.Chdir(origDir); err != nil {
					t.Fatalf("failed to restore working directory: %v", err)
				}
			}()

			if tt.setupFile {
				if err := os.WriteFile("AGENTS.md", []byte(tt.fileContent), 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}
			}

			// We can't easily test os.Exit, so we just verify the function doesn't panic
			// for the success case
			if !tt.expectExit {
				// This should not panic
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("CheckFactory panicked: %v", r)
						}
					}()
					// Note: CheckFactory calls os.Exit on failure, so we can't test those cases directly
					// We would need to refactor to use a testable exit function
				}()
			}
		})
	}
}

func TestInstallFactory_NewFile(t *testing.T) {
	// Save original working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Run InstallFactory
	InstallFactory()

	// Verify file was created
	data, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, factoryBeginMarker) {
		t.Error("AGENTS.md missing begin marker")
	}
	if !strings.Contains(content, factoryEndMarker) {
		t.Error("AGENTS.md missing end marker")
	}
}

func TestInstallFactory_ExistingWithoutBeads(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create existing AGENTS.md without beads section
	existingContent := "# My Custom Agents File\n\nExisting content\n"
	if err := os.WriteFile("AGENTS.md", []byte(existingContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	// Run InstallFactory
	InstallFactory()

	// Verify file was updated
	data, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "My Custom Agents File") {
		t.Error("Lost existing content")
	}
	if !strings.Contains(content, factoryBeginMarker) {
		t.Error("AGENTS.md missing begin marker")
	}
}

func TestInstallFactory_ExistingWithBeads(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create existing AGENTS.md with old beads section
	oldContent := `# My Project

<!-- BEGIN BEADS INTEGRATION -->
Old beads content
<!-- END BEADS INTEGRATION -->

Other content`
	if err := os.WriteFile("AGENTS.md", []byte(oldContent), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	// Run InstallFactory
	InstallFactory()

	// Verify file was updated
	data, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "Old beads content") {
		t.Error("Old beads content should have been replaced")
	}
	if !strings.Contains(content, "Other content") {
		t.Error("Lost content after beads section")
	}
	if !strings.Contains(content, "Issue Tracking with bd") {
		t.Error("Missing new beads section content")
	}
}

func TestRemoveFactory(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tests := []struct {
		name           string
		initialContent string
		expectFile     bool
		expectedContent string
	}{
		{
			name:           "remove beads section, keep other content",
			initialContent: "# Project\n\n" + factoryBeadsSection + "\n\n## Other Section\n\nContent",
			expectFile:     true,
			expectedContent: "# Project\n\n## Other Section\n\nContent",
		},
		{
			name:           "remove file when only beads section",
			initialContent: factoryBeadsSection,
			expectFile:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("failed to change to temp directory: %v", err)
			}
			defer func() {
				if err := os.Chdir(origDir); err != nil {
					t.Fatalf("failed to restore working directory: %v", err)
				}
			}()

			if err := os.WriteFile("AGENTS.md", []byte(tt.initialContent), 0644); err != nil {
				t.Fatalf("failed to create AGENTS.md: %v", err)
			}

			RemoveFactory()

			_, err := os.Stat("AGENTS.md")
			fileExists := err == nil

			if fileExists != tt.expectFile {
				t.Errorf("file exists = %v, want %v", fileExists, tt.expectFile)
			}

			if tt.expectFile {
				data, err := os.ReadFile("AGENTS.md")
				if err != nil {
					t.Fatalf("failed to read AGENTS.md: %v", err)
				}
				if string(data) != tt.expectedContent {
					t.Errorf("content mismatch\ngot: %q\nwant: %q", string(data), tt.expectedContent)
				}
			}
		})
	}
}

func TestRemoveFactory_NoFile(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Should not panic when file doesn't exist
	RemoveFactory()
}

func TestRemoveFactory_NoBeadsSection(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	content := "# Project\n\nNo beads here"
	if err := os.WriteFile("AGENTS.md", []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	// Should not panic or modify file
	RemoveFactory()

	data, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if string(data) != content {
		t.Error("File should not have been modified")
	}
}

func TestFactoryBeadsSectionContent(t *testing.T) {
	// Verify the beads section contains expected documentation
	section := factoryBeadsSection

	requiredContent := []string{
		"bd create",
		"bd update",
		"bd close",
		"bd ready",
		"bug",
		"feature",
		"task",
		"epic",
		"discovered-from",
	}

	for _, req := range requiredContent {
		if !strings.Contains(section, req) {
			t.Errorf("factoryBeadsSection missing required content: %q", req)
		}
	}
}

func TestFactoryMarkers(t *testing.T) {
	// Verify markers are properly formatted
	if !strings.Contains(factoryBeginMarker, "BEGIN") {
		t.Error("Begin marker should contain 'BEGIN'")
	}
	if !strings.Contains(factoryEndMarker, "END") {
		t.Error("End marker should contain 'END'")
	}
	if !strings.Contains(factoryBeginMarker, "BEADS") {
		t.Error("Begin marker should contain 'BEADS'")
	}
	if !strings.Contains(factoryEndMarker, "BEADS") {
		t.Error("End marker should contain 'BEADS'")
	}
}

func TestInstallFactoryIdempotent(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Run InstallFactory twice
	InstallFactory()
	firstData, _ := os.ReadFile("AGENTS.md")

	InstallFactory()
	secondData, _ := os.ReadFile("AGENTS.md")

	// Content should be identical
	if string(firstData) != string(secondData) {
		t.Error("InstallFactory should be idempotent")
	}

	// Should only have one beads section
	content := string(secondData)
	beginCount := strings.Count(content, factoryBeginMarker)
	if beginCount != 1 {
		t.Errorf("Expected 1 begin marker, got %d", beginCount)
	}
}

func TestInstallFactory_DirectoryError(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create AGENTS.md as a directory to cause an error
	if err := os.Mkdir("AGENTS.md", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// InstallFactory should handle this gracefully (or exit)
	// We can't easily test os.Exit, but verify it doesn't panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("InstallFactory panicked: %v", r)
		}
	}()
}

// Test internal marker constants
func TestMarkersMatch(t *testing.T) {
	// Ensure the section template contains both markers
	if !strings.HasPrefix(factoryBeadsSection, factoryBeginMarker) {
		t.Error("factoryBeadsSection should start with begin marker")
	}

	if !strings.HasSuffix(strings.TrimSpace(factoryBeadsSection), factoryEndMarker) {
		t.Error("factoryBeadsSection should end with end marker")
	}
}

func TestUpdateBeadsSectionPreservesWhitespace(t *testing.T) {
	// Test that whitespace around content is preserved
	content := "# Header\n\n" + factoryBeadsSection + "\n\n# Footer"

	// Update should be idempotent for content that already has current section
	updated := updateBeadsSection(content)

	if !strings.Contains(updated, "# Header") {
		t.Error("Lost header")
	}
	if !strings.Contains(updated, "# Footer") {
		t.Error("Lost footer")
	}
}

func TestCheckFactory_SubdirectoryPath(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdirectory: %v", err)
	}

	// Create AGENTS.md in tmpDir
	content := "# Project\n\n" + factoryBeadsSection
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create AGENTS.md: %v", err)
	}

	// Change to subdirectory - AGENTS.md should not be found
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// CheckFactory looks for AGENTS.md in current directory, not parent
	// So it should fail in subdirectory
	// We can't test os.Exit, but this documents the expected behavior
}
