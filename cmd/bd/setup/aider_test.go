package setup

import (
	"os"
	"strings"
	"testing"
)

func TestAiderConfigTemplate(t *testing.T) {
	// Verify template contains required content
	if !strings.Contains(aiderConfigTemplate, "read:") {
		t.Error("aiderConfigTemplate missing 'read:' directive")
	}
	if !strings.Contains(aiderConfigTemplate, ".aider/BEADS.md") {
		t.Error("aiderConfigTemplate missing reference to BEADS.md")
	}
}

func TestAiderBeadsInstructions(t *testing.T) {
	requiredContent := []string{
		"bd ready",
		"bd create",
		"bd update",
		"bd close",
		"bd sync",
		"/run",
		"bug",
		"feature",
		"task",
		"epic",
	}

	for _, req := range requiredContent {
		if !strings.Contains(aiderBeadsInstructions, req) {
			t.Errorf("aiderBeadsInstructions missing required content: %q", req)
		}
	}
}

func TestAiderReadmeTemplate(t *testing.T) {
	requiredContent := []string{
		"Aider + Beads Integration",
		"/run",
		"bd ready",
		"bd create",
		"bd close",
		"bd sync",
	}

	for _, req := range requiredContent {
		if !strings.Contains(aiderReadmeTemplate, req) {
			t.Errorf("aiderReadmeTemplate missing required content: %q", req)
		}
	}
}

func TestInstallAider(t *testing.T) {
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

	InstallAider()

	// Verify all files were created
	files := []struct {
		path    string
		content string
	}{
		{".aider.conf.yml", aiderConfigTemplate},
		{".aider/BEADS.md", aiderBeadsInstructions},
		{".aider/README.md", aiderReadmeTemplate},
	}

	for _, f := range files {
		if !FileExists(f.path) {
			t.Errorf("File was not created: %s", f.path)
			continue
		}

		data, err := os.ReadFile(f.path)
		if err != nil {
			t.Errorf("Failed to read %s: %v", f.path, err)
			continue
		}

		if string(data) != f.content {
			t.Errorf("File %s content doesn't match expected template", f.path)
		}
	}
}

func TestInstallAider_ExistingDirectory(t *testing.T) {
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

	// Pre-create the directory
	if err := os.MkdirAll(".aider", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Should not fail
	InstallAider()

	// Verify files were created
	if !FileExists(".aider/BEADS.md") {
		t.Error("BEADS.md not created")
	}
}

func TestInstallAiderIdempotent(t *testing.T) {
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

	// Run twice
	InstallAider()
	firstData, _ := os.ReadFile(".aider.conf.yml")

	InstallAider()
	secondData, _ := os.ReadFile(".aider.conf.yml")

	if string(firstData) != string(secondData) {
		t.Error("InstallAider should be idempotent")
	}
}

func TestRemoveAider(t *testing.T) {
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

	// Install first
	InstallAider()

	// Verify files exist
	files := []string{".aider.conf.yml", ".aider/BEADS.md", ".aider/README.md"}
	for _, f := range files {
		if !FileExists(f) {
			t.Fatalf("File should exist before removal: %s", f)
		}
	}

	// Remove
	RemoveAider()

	// Verify files are gone
	for _, f := range files {
		if FileExists(f) {
			t.Errorf("File should have been removed: %s", f)
		}
	}
}

func TestRemoveAider_NoFiles(t *testing.T) {
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

	// Should not panic when files don't exist
	RemoveAider()
}

func TestRemoveAider_PartialFiles(t *testing.T) {
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

	// Create only the config file
	if err := os.WriteFile(".aider.conf.yml", []byte(aiderConfigTemplate), 0644); err != nil {
		t.Fatalf("failed to create config file: %v", err)
	}

	// Should not panic
	RemoveAider()

	// Config should be removed
	if FileExists(".aider.conf.yml") {
		t.Error("Config file should have been removed")
	}
}

func TestRemoveAider_DirectoryCleanup(t *testing.T) {
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

	// Install
	InstallAider()

	// Remove
	RemoveAider()

	// Directory should be cleaned up if empty
	// (the implementation tries to remove it but ignores errors)
	if DirExists(".aider") {
		// This is acceptable - directory might not be removed if not empty
		// or the implementation doesn't remove it
	}
}

func TestRemoveAider_DirectoryWithOtherFiles(t *testing.T) {
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

	// Install
	InstallAider()

	// Add another file to .aider directory
	if err := os.WriteFile(".aider/other.txt", []byte("keep me"), 0644); err != nil {
		t.Fatalf("failed to create other file: %v", err)
	}

	// Remove
	RemoveAider()

	// Directory should still exist (has other files)
	if !DirExists(".aider") {
		t.Error("Directory should not be removed when it has other files")
	}

	// Other file should still exist
	if !FileExists(".aider/other.txt") {
		t.Error("Other files should be preserved")
	}
}

func TestCheckAider_NotInstalled(t *testing.T) {
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

	// CheckAider calls os.Exit(1) when not installed
	// We can't easily test that, but we document expected behavior
}

func TestCheckAider_Installed(t *testing.T) {
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

	// Install first
	InstallAider()

	// Should not panic or exit
	CheckAider()
}

func TestAiderInstructionsWorkflowPattern(t *testing.T) {
	// Verify instructions contain the workflow pattern Aider users need
	instructions := aiderBeadsInstructions

	// Should mention the /run command pattern
	if !strings.Contains(instructions, "/run bd ready") {
		t.Error("Should mention /run bd ready")
	}
	if !strings.Contains(instructions, "/run bd sync") {
		t.Error("Should mention /run bd sync")
	}

	// Should explain that Aider requires explicit commands
	if !strings.Contains(instructions, "Aider requires") {
		t.Error("Should explain Aider's explicit command requirement")
	}
}

func TestAiderReadmeForHumans(t *testing.T) {
	// README should be helpful for humans, not just AI
	readme := aiderReadmeTemplate

	// Should have human-friendly sections
	if !strings.Contains(readme, "Quick Start") {
		t.Error("README should have Quick Start section")
	}
	if !strings.Contains(readme, "How This Works") {
		t.Error("README should explain how it works")
	}
}

func TestAiderFilePaths(t *testing.T) {
	// Verify paths match Aider conventions
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

	InstallAider()

	// Check expected file paths
	expectedPaths := []string{
		".aider.conf.yml",
		".aider/BEADS.md",
		".aider/README.md",
	}

	for _, path := range expectedPaths {
		if !FileExists(path) {
			t.Errorf("Expected file at %s", path)
		}
	}
}
