package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCursorEnv(t *testing.T) cursorEnv {
	t.Helper()
	return cursorEnv{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
}

func TestCursorRulesTemplate(t *testing.T) {
	requiredContent := []string{
		"alwaysApply: true",
		"@beads-managed",
		"NEVER use `bd edit`",
		"TodoWrite",
		"bd claim",
		"bd prime",
		"bd remember",
		"bd ready",
		"bd update <id> --claim",
		"bd close",
		"git push",
		"discovered-from",
		"--json",
	}

	for _, req := range requiredContent {
		if !strings.Contains(cursorRulesTemplate, req) {
			t.Errorf("cursorRulesTemplate missing required content: %q", req)
		}
	}

	if strings.Contains(cursorRulesTemplate, "BEGIN BEADS INTEGRATION") {
		t.Error("cursorRulesTemplate should not use legacy BEGIN/END markers")
	}
	if strings.Contains(cursorRulesTemplate, "## Context Loading") {
		t.Error("cursorRulesTemplate should defer reference material to bd prime")
	}
}

func rawServer(command string, args ...string) json.RawMessage {
	s := cursorMCPServerCore{Command: command, Args: args}
	raw, _ := json.Marshal(s)
	return raw
}

func TestCursorHasBeadsMCP(t *testing.T) {
	tests := []struct {
		name   string
		config cursorMCPConfig
		want   bool
	}{
		{
			name: "beads key exists",
			config: cursorMCPConfig{
				MCPServers: map[string]json.RawMessage{
					"beads": rawServer("jawnt", "mcp"),
				},
			},
			want: true,
		},
		{
			name: "bd mcp under custom key",
			config: cursorMCPConfig{
				MCPServers: map[string]json.RawMessage{
					"custom": rawServer("bd", "mcp"),
				},
			},
			want: true,
		},
		{
			name: "unrelated server",
			config: cursorMCPConfig{
				MCPServers: map[string]json.RawMessage{
					"jawnt": rawServer("jawnt", "mcp"),
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cursorHasBeadsMCP(tt.config); got != tt.want {
				t.Fatalf("cursorHasBeadsMCP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInstallCursor_FreshWorkspace(t *testing.T) {
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

	env := testCursorEnv(t)
	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor: %v", err)
	}

	if !FileExists(cursorRulesPath) {
		t.Fatal("rules file was not created")
	}
	data, err := os.ReadFile(cursorRulesPath)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if string(data) != cursorRulesTemplate {
		t.Fatal("rules content doesn't match template")
	}

	if !FileExists(cursorMCPPath) {
		t.Fatal("mcp.json was not created")
	}
	mcpData, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var config cursorMCPConfig
	if err := json.Unmarshal(mcpData, &config); err != nil {
		t.Fatalf("unmarshal mcp.json: %v", err)
	}
	raw, ok := config.MCPServers["beads"]
	if !ok {
		t.Fatal("beads MCP entry missing")
	}
	core, valid := decodeMCPServerCore(raw)
	if !valid {
		t.Fatal("beads MCP entry could not be decoded")
	}
	if core.Command != "bd" || len(core.Args) != 1 || core.Args[0] != "mcp" {
		t.Fatalf("unexpected beads MCP entry: %+v", core)
	}
}

func TestMergeCursorMCP_PreservesExistingServers(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
  "mcpServers": {
    "other": {
      "command": "node",
      "args": ["server.js"]
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write existing mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	written, err := mergeCursorMCP(env)
	if err != nil {
		t.Fatalf("mergeCursorMCP: %v", err)
	}
	if !written {
		t.Fatal("expected MCP merge to write beads entry")
	}

	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var config cursorMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal mcp.json: %v", err)
	}
	if _, ok := config.MCPServers["other"]; !ok {
		t.Fatal("existing server was removed")
	}
	if _, ok := config.MCPServers["beads"]; !ok {
		t.Fatal("beads server was not added")
	}
}

func TestMergeCursorMCP_SkipsWhenBeadsKeyExists(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
  "mcpServers": {
    "beads": {
      "command": "jawnt",
      "args": ["mcp"]
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write existing mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	written, err := mergeCursorMCP(env)
	if err != nil {
		t.Fatalf("mergeCursorMCP: %v", err)
	}
	if written {
		t.Fatal("expected MCP merge to skip existing beads key")
	}

	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if !strings.Contains(string(data), `"command": "jawnt"`) {
		t.Fatal("existing beads MCP entry was modified")
	}
}

func TestMergeCursorMCP_SkipsWhenBDMCPUnderCustomKey(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
  "mcpServers": {
    "custom-beads": {
      "command": "bd",
      "args": ["mcp"]
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write existing mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	written, err := mergeCursorMCP(env)
	if err != nil {
		t.Fatalf("mergeCursorMCP: %v", err)
	}
	if written {
		t.Fatal("expected MCP merge to skip when bd mcp already configured")
	}
}

func TestMergeCursorMCP_InvalidJSON(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	invalid := `{not json`
	if err := os.WriteFile(cursorMCPPath, []byte(invalid), 0644); err != nil {
		t.Fatalf("write invalid mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	written, err := mergeCursorMCP(env)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if written {
		t.Fatal("expected invalid JSON to not be written")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error should mention invalid JSON, got: %v", err)
	}

	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if string(data) != invalid {
		t.Fatal("invalid mcp.json was modified")
	}
}

func TestMergeCursorMCP_PreservesUnknownFields(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
  "mcpServers": {
    "other": {
      "command": "node",
      "args": ["server.js"],
      "env": {"API_KEY": "secret123"},
      "cwd": "/some/path"
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write existing mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	written, err := mergeCursorMCP(env)
	if err != nil {
		t.Fatalf("mergeCursorMCP: %v", err)
	}
	if !written {
		t.Fatal("expected MCP merge to write beads entry")
	}

	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if !strings.Contains(string(data), `"API_KEY"`) {
		t.Fatal("env field was lost during round-trip")
	}
	if !strings.Contains(string(data), `"/some/path"`) {
		t.Fatal("cwd field was lost during round-trip")
	}
}

func TestRemoveCursor_SkipsThirdPartyBeadsKey(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Dir(cursorMCPPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cursorRulesPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cursorRulesPath, []byte(cursorRulesTemplate), 0644); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	thirdParty := `{
  "mcpServers": {
    "beads": {
      "command": "jawnt",
      "args": ["mcp", "--beads"]
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(thirdParty), 0644); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}

	env := testCursorEnv(t)
	if err := removeCursor(env); err != nil {
		t.Fatalf("removeCursor: %v", err)
	}

	if FileExists(cursorRulesPath) {
		t.Fatal("rules file should be removed")
	}
	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("mcp.json should still exist: %v", err)
	}
	if !strings.Contains(string(data), "jawnt") {
		t.Fatal("third-party beads entry was incorrectly removed")
	}
}

func TestRemoveCursor_PreservesOtherServers(t *testing.T) {
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

	env := testCursorEnv(t)
	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor: %v", err)
	}

	existing := `{
  "mcpServers": {
    "beads": {
      "command": "bd",
      "args": ["mcp"]
    },
    "other": {
      "command": "node",
      "args": ["server.js"]
    }
  }
}`
	if err := os.WriteFile(cursorMCPPath, []byte(existing), 0644); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}

	if err := removeCursor(env); err != nil {
		t.Fatalf("removeCursor: %v", err)
	}

	if FileExists(cursorRulesPath) {
		t.Fatal("rules file should be removed")
	}
	if !FileExists(cursorMCPPath) {
		t.Fatal("mcp.json should remain when other servers exist")
	}

	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var config cursorMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal mcp.json: %v", err)
	}
	if _, ok := config.MCPServers["beads"]; ok {
		t.Fatal("beads entry should be removed")
	}
	if _, ok := config.MCPServers["other"]; !ok {
		t.Fatal("other server should be preserved")
	}
}

func TestRemoveCursor_DeletesMCPFileWhenOnlyBeads(t *testing.T) {
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

	env := testCursorEnv(t)
	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor: %v", err)
	}
	if err := removeCursor(env); err != nil {
		t.Fatalf("removeCursor: %v", err)
	}

	if FileExists(cursorMCPPath) {
		t.Fatal("mcp.json should be removed when only beads entry existed")
	}
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

	env := testCursorEnv(t)
	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor: %v", err)
	}
	if err := checkCursor(env); err != nil {
		t.Fatalf("checkCursor: %v", err)
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

	env := testCursorEnv(t)
	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor first: %v", err)
	}
	firstRules, _ := os.ReadFile(cursorRulesPath)
	firstMCP, _ := os.ReadFile(cursorMCPPath)

	if err := installCursor(env); err != nil {
		t.Fatalf("installCursor second: %v", err)
	}
	secondRules, _ := os.ReadFile(cursorRulesPath)
	secondMCP, _ := os.ReadFile(cursorMCPPath)

	if string(firstRules) != string(secondRules) {
		t.Error("rules content changed on second install")

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

	if err := CheckCursor(); err == nil {
		t.Fatal("CheckCursor should return error when not installed")
	}
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
