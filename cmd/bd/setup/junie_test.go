package setup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestJunieGuidelinesTemplate(t *testing.T) {
	requiredContent := []string{
		"bd ready",
		"bd create",
		"bd update",
		"bd close",
		"bd sync",
		"mcp_beads_ready",
		"mcp_beads_list",
		"mcp_beads_create",
		"bug",
		"feature",
		"task",
		"epic",
	}

	for _, req := range requiredContent {
		if !strings.Contains(junieGuidelinesTemplate, req) {
			t.Errorf("junieGuidelinesTemplate missing required content: %q", req)
		}
	}
}

func TestJunieMCPConfig(t *testing.T) {
	config := junieMCPConfig()

	// Verify structure
	mcpServers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing or wrong type")
	}

	beads, ok := mcpServers["beads"].(map[string]interface{})
	if !ok {
		t.Fatal("beads server config missing or wrong type")
	}

	command, ok := beads["command"].(string)
	if !ok || command != "bd" {
		t.Errorf("Expected command 'bd', got %v", beads["command"])
	}

	args, ok := beads["args"].([]string)
	if !ok || len(args) != 1 || args[0] != "mcp" {
		t.Errorf("Expected args ['mcp'], got %v", beads["args"])
	}

	// Verify it's valid JSON
	data, err := json.Marshal(config)
	if err != nil {
		t.Errorf("MCP config should be valid JSON: %v", err)
	}

	// Verify it can be unmarshaled back
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("MCP config JSON should be parseable: %v", err)
	}
}

func TestInstallJunie(t *testing.T) {
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

	InstallJunie()

	// Verify guidelines file was created
	guidelinesPath := ".junie/guidelines.md"
	if !FileExists(guidelinesPath) {
		t.Errorf("File was not created: %s", guidelinesPath)
	} else {
		data, err := os.ReadFile(guidelinesPath)
		if err != nil {
			t.Errorf("Failed to read %s: %v", guidelinesPath, err)
		} else if string(data) != junieGuidelinesTemplate {
			t.Errorf("File %s content doesn't match expected template", guidelinesPath)
		}
	}

	// Verify MCP config file was created
	mcpPath := ".junie/mcp/mcp.json"
	if !FileExists(mcpPath) {
		t.Errorf("File was not created: %s", mcpPath)
	} else {
		data, err := os.ReadFile(mcpPath)
		if err != nil {
			t.Errorf("Failed to read %s: %v", mcpPath, err)
		} else {
			// Verify it's valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Errorf("MCP config should be valid JSON: %v", err)
			}

			// Verify structure
			mcpServers, ok := parsed["mcpServers"].(map[string]interface{})
			if !ok {
				t.Error("mcpServers key missing or wrong type")
			} else if _, ok := mcpServers["beads"]; !ok {
				t.Error("beads server config missing")
			}
		}
	}
}

func TestInstallJunie_ExistingDirectory(t *testing.T) {
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

	// Pre-create the directories
	if err := os.MkdirAll(".junie/mcp", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Should not fail
	InstallJunie()

	// Verify files were created
	if !FileExists(".junie/guidelines.md") {
		t.Error("guidelines.md not created")
	}
	if !FileExists(".junie/mcp/mcp.json") {
		t.Error("mcp.json not created")
	}
}

func TestInstallJunieIdempotent(t *testing.T) {
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
	InstallJunie()
	firstGuidelines, _ := os.ReadFile(".junie/guidelines.md")
	firstMCP, _ := os.ReadFile(".junie/mcp/mcp.json")

	InstallJunie()
	secondGuidelines, _ := os.ReadFile(".junie/guidelines.md")
	secondMCP, _ := os.ReadFile(".junie/mcp/mcp.json")

	if string(firstGuidelines) != string(secondGuidelines) {
		t.Error("InstallJunie should be idempotent for guidelines")
	}
	if string(firstMCP) != string(secondMCP) {
		t.Error("InstallJunie should be idempotent for MCP config")
	}
}

func TestRemoveJunie(t *testing.T) {
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
	InstallJunie()

	// Verify files exist
	files := []string{".junie/guidelines.md", ".junie/mcp/mcp.json"}
	for _, f := range files {
		if !FileExists(f) {
			t.Fatalf("File should exist before removal: %s", f)
		}
	}

	// Remove
	RemoveJunie()

	// Verify files are gone
	for _, f := range files {
		if FileExists(f) {
			t.Errorf("File should have been removed: %s", f)
		}
	}
}

func TestRemoveJunie_NoFiles(t *testing.T) {
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
	RemoveJunie()
}

func TestRemoveJunie_PartialFiles(t *testing.T) {
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

	// Create only the guidelines file
	if err := os.MkdirAll(".junie", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(".junie/guidelines.md", []byte(junieGuidelinesTemplate), 0644); err != nil {
		t.Fatalf("failed to create guidelines file: %v", err)
	}

	// Should not panic
	RemoveJunie()

	// Guidelines should be removed
	if FileExists(".junie/guidelines.md") {
		t.Error("Guidelines file should have been removed")
	}
}

func TestRemoveJunie_DirectoryCleanup(t *testing.T) {
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
	InstallJunie()

	// Remove
	RemoveJunie()

	// Directories should be cleaned up if empty
	if DirExists(".junie/mcp") {
		t.Error(".junie/mcp directory should be removed when empty")
	}
	if DirExists(".junie") {
		t.Error(".junie directory should be removed when empty")
	}
}

func TestRemoveJunie_DirectoryWithOtherFiles(t *testing.T) {
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
	InstallJunie()

	// Add another file to .junie directory
	if err := os.WriteFile(".junie/other.txt", []byte("keep me"), 0644); err != nil {
		t.Fatalf("failed to create other file: %v", err)
	}

	// Remove
	RemoveJunie()

	// Directory should still exist (has other files)
	if !DirExists(".junie") {
		t.Error("Directory should not be removed when it has other files")
	}

	// Other file should still exist
	if !FileExists(".junie/other.txt") {
		t.Error("Other files should be preserved")
	}
}

func TestCheckJunie_NotInstalled(t *testing.T) {
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

	// CheckJunie calls os.Exit(1) when not installed
	// We can't easily test that, but we document expected behavior
}

func TestCheckJunie_Installed(t *testing.T) {
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
	InstallJunie()

	// Should not panic or exit
	CheckJunie()
}

func TestCheckJunie_PartialInstall_GuidelinesOnly(t *testing.T) {
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

	// Create only guidelines
	if err := os.MkdirAll(".junie", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(".junie/guidelines.md", []byte(junieGuidelinesTemplate), 0644); err != nil {
		t.Fatalf("failed to create guidelines file: %v", err)
	}

	// CheckJunie calls os.Exit(1) for partial installation
	// We can't easily test that, but we document expected behavior
}

func TestCheckJunie_PartialInstall_MCPOnly(t *testing.T) {
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

	// Create only MCP config
	if err := os.MkdirAll(".junie/mcp", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	mcpConfig := junieMCPConfig()
	mcpData, _ := json.MarshalIndent(mcpConfig, "", "  ")
	if err := os.WriteFile(".junie/mcp/mcp.json", mcpData, 0644); err != nil {
		t.Fatalf("failed to create MCP config file: %v", err)
	}

	// CheckJunie calls os.Exit(1) for partial installation
	// We can't easily test that, but we document expected behavior
}

func TestJunieFilePaths(t *testing.T) {
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

	InstallJunie()

	// Check expected file paths
	expectedPaths := []string{
		".junie/guidelines.md",
		".junie/mcp/mcp.json",
	}

	for _, path := range expectedPaths {
		if !FileExists(path) {
			t.Errorf("Expected file at %s", path)
		}
	}
}

func TestJunieGuidelinesWorkflowPattern(t *testing.T) {
	// Verify guidelines contain the workflow patterns Junie users need
	guidelines := junieGuidelinesTemplate

	// Should mention core workflow commands
	if !strings.Contains(guidelines, "bd ready") {
		t.Error("Should mention bd ready")
	}
	if !strings.Contains(guidelines, "bd sync") {
		t.Error("Should mention bd sync")
	}

	// Should explain MCP tools
	if !strings.Contains(guidelines, "MCP Tools Available") {
		t.Error("Should have MCP Tools section")
	}
}
