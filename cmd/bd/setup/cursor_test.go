package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorRulesTemplate(t *testing.T) {
	// Verify template contains required content
	requiredContent := []string{
		"bd prime",
		"bd ready",
		"bd create",
		"bd update",
		"bd close",
		"bd sync",
		"BEADS INTEGRATION",
	}

	for _, req := range requiredContent {
		if !strings.Contains(cursorRulesTemplate, req) {
			t.Errorf("cursorRulesTemplate missing required content: %q", req)
		}
	}
}

func TestInstallCursor(t *testing.T) {
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

	InstallCursor()

	// Verify file was created
	rulesPath := ".cursor/rules/beads.mdc"
	if !FileExists(rulesPath) {
		t.Fatal("Cursor rules file was not created")
	}

	// Verify content
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("failed to read rules file: %v", err)
	}

	if string(data) != cursorRulesTemplate {
		t.Error("Rules file content doesn't match template")
	}
}

func TestInstallCursor_ExistingDirectory(t *testing.T) {
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
	if err := os.MkdirAll(".cursor/rules", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Should not fail
	InstallCursor()

	// Verify file was created
	if !FileExists(".cursor/rules/beads.mdc") {
		t.Fatal("Cursor rules file was not created")
	}
}

func TestInstallCursor_OverwriteExisting(t *testing.T) {
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

	// Create existing file with different content
	rulesPath := ".cursor/rules/beads.mdc"
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(rulesPath, []byte("old content"), 0644); err != nil {
		t.Fatalf("failed to create old file: %v", err)
	}

	InstallCursor()

	// Verify content was overwritten
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("failed to read rules file: %v", err)
	}

	if string(data) == "old content" {
		t.Error("Old content was not overwritten")
	}
	if string(data) != cursorRulesTemplate {
		t.Error("Content doesn't match template")
	}
}

func TestInstallCursorIdempotent(t *testing.T) {
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
	InstallCursor()
	firstData, _ := os.ReadFile(".cursor/rules/beads.mdc")

	InstallCursor()
	secondData, _ := os.ReadFile(".cursor/rules/beads.mdc")

	if string(firstData) != string(secondData) {
		t.Error("InstallCursor should be idempotent")
	}
}

func TestRemoveCursor(t *testing.T) {
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
	InstallCursor()

	// Verify file exists
	rulesPath := ".cursor/rules/beads.mdc"
	if !FileExists(rulesPath) {
		t.Fatal("File should exist before removal")
	}

	// Remove
	RemoveCursor()

	// Verify file is gone
	if FileExists(rulesPath) {
		t.Error("File should have been removed")
	}
}

func TestRemoveCursor_NoFile(t *testing.T) {
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
	RemoveCursor()
}

func TestCheckCursor_NotInstalled(t *testing.T) {
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

	// CheckCursor calls os.Exit(1) when not installed
	// We can't easily test that, but we document expected behavior
}

func TestCheckCursor_Installed(t *testing.T) {
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
	InstallCursor()

	// Should not panic or exit
	CheckCursor()
}

func TestCursorRulesPath(t *testing.T) {
	// Verify the path is correct for Cursor IDE
	expectedPath := ".cursor/rules/beads.mdc"

	// These are the paths used in the implementation
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

	InstallCursor()

	// Verify the file was created at the expected path
	if !FileExists(expectedPath) {
		t.Errorf("Expected file at %s", expectedPath)
	}
}

func TestCursorTemplateFormatting(t *testing.T) {
	// Verify template is well-formed
	template := cursorRulesTemplate

	// Should have both markers
	if !strings.Contains(template, "BEGIN BEADS INTEGRATION") {
		t.Error("Missing BEGIN marker")
	}
	if !strings.Contains(template, "END BEADS INTEGRATION") {
		t.Error("Missing END marker")
	}

	// Should have workflow section
	if !strings.Contains(template, "## Workflow") {
		t.Error("Missing Workflow section")
	}

	// Should have context loading section
	if !strings.Contains(template, "## Context Loading") {
		t.Error("Missing Context Loading section")
	}
}
