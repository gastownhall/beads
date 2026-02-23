package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAgentDocumentation(t *testing.T) {
	tests := []struct {
		name           string
		files          []string
		expectedStatus string
		expectFix      bool
	}{
		{
			name:           "no documentation",
			files:          []string{},
			expectedStatus: "warning",
			expectFix:      true,
		},
		{
			name:           "AGENTS.md exists",
			files:          []string{"AGENTS.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name:           "CLAUDE.md exists",
			files:          []string{"CLAUDE.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name:           ".claude/CLAUDE.md exists",
			files:          []string{".claude/CLAUDE.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name:           "claude.local.md exists (local-only)",
			files:          []string{"claude.local.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name:           ".claude/claude.local.md exists (local-only)",
			files:          []string{".claude/claude.local.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
		{
			name:           "multiple docs",
			files:          []string{"AGENTS.md", "CLAUDE.md"},
			expectedStatus: "ok",
			expectFix:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Create test files
			for _, file := range tt.files {
				filePath := filepath.Join(tmpDir, file)
				dir := filepath.Dir(filePath)
				if dir != tmpDir {
					if err := os.MkdirAll(dir, 0750); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filePath, []byte("# Test documentation"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckAgentDocumentation(tmpDir)

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

func TestCheckLegacyBeadsSlashCommands(t *testing.T) {
	tests := []struct {
		name           string
		fileContent    map[string]string // filename -> content
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "no documentation files",
			fileContent:    map[string]string{},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name: "clean documentation",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse bd ready to see ready issues.",
			},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name: "legacy slash command in AGENTS.md",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse /beads:ready to see ready issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "legacy slash command in CLAUDE.md",
			fileContent: map[string]string{
				"CLAUDE.md": "# Claude\n\nRun /beads:quickstart to get started.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "legacy slash command in .claude/CLAUDE.md",
			fileContent: map[string]string{
				".claude/CLAUDE.md": "Use /beads:show to see an issue.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "legacy slash command in claude.local.md",
			fileContent: map[string]string{
				"claude.local.md": "Use /beads:show to see an issue.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "legacy slash command in .claude/claude.local.md",
			fileContent: map[string]string{
				".claude/claude.local.md": "Use /beads:ready to see ready issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "multiple files with legacy commands",
			fileContent: map[string]string{
				"AGENTS.md": "Use /beads:ready",
				"CLAUDE.md": "Use /beads:show",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Create test files
			for filename, content := range tt.fileContent {
				filePath := filepath.Join(tmpDir, filename)
				dir := filepath.Dir(filePath)
				if dir != tmpDir {
					if err := os.MkdirAll(dir, 0750); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckLegacyBeadsSlashCommands(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, check.Status)
			}

			if tt.expectWarning {
				if check.Fix == "" {
					t.Error("Expected fix message for warning, got empty string")
				}
				if !strings.Contains(check.Fix, "bd setup claude") {
					t.Error("Expected fix message to mention 'bd setup claude'")
				}
				if !strings.Contains(check.Fix, "token") {
					t.Error("Expected fix message to mention token savings")
				}
			}
		})
	}
}

func TestCheckLegacyMCPToolReferences(t *testing.T) {
	tests := []struct {
		name           string
		fileContent    map[string]string // filename -> content
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "no documentation files",
			fileContent:    map[string]string{},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name: "clean documentation without MCP references",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse bd ready to see ready issues.",
			},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name: "old MCP tool reference in AGENTS.md",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse mcp__beads_beads__list to list issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "plugin MCP tool reference in CLAUDE.md",
			fileContent: map[string]string{
				"CLAUDE.md": "# Claude\n\nCall mcp__plugin_beads_beads__show to see an issue.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "Junie-style MCP tool reference",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse mcp_beads_ready to see ready issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "MCP reference in .claude/CLAUDE.md",
			fileContent: map[string]string{
				".claude/CLAUDE.md": "Call mcp__beads_beads__create to create issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "MCP reference in claude.local.md",
			fileContent: map[string]string{
				"claude.local.md": "Use mcp__beads_beads__ready to find work.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "MCP reference in .claude/claude.local.md",
			fileContent: map[string]string{
				".claude/claude.local.md": "Call mcp__plugin_beads_beads__list for issues.",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name: "multiple files with MCP references",
			fileContent: map[string]string{
				"AGENTS.md": "Use mcp__beads_beads__list",
				"CLAUDE.md": "Call mcp__plugin_beads_beads__show",
			},
			expectedStatus: "warning",
			expectWarning:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Create test files
			for filename, content := range tt.fileContent {
				filePath := filepath.Join(tmpDir, filename)
				dir := filepath.Dir(filePath)
				if dir != tmpDir {
					if err := os.MkdirAll(dir, 0750); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckLegacyMCPToolReferences(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, check.Status)
			}

			if tt.expectWarning {
				if check.Fix == "" {
					t.Error("Expected fix message for warning, got empty string")
				}
				if !strings.Contains(check.Fix, "bd setup claude") {
					t.Error("Expected fix message to mention 'bd setup claude'")
				}
				if !strings.Contains(check.Fix, "token") {
					t.Error("Expected fix message to mention token savings")
				}
				if !strings.Contains(check.Fix, "bd list") {
					t.Error("Expected fix message to show CLI command equivalents")
				}
			}
		})
	}
}

func TestCheckDatabaseConfig_IgnoresSystemJSONLs(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Configure issues.jsonl, but only create interactions.jsonl.
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckDatabaseConfig(tmpDir)
	if check.Status != "ok" {
		t.Fatalf("expected ok, got %s: %s\n%s", check.Status, check.Message, check.Detail)
	}
}

func TestCheckFreshClone(t *testing.T) {
	tests := []struct {
		name           string
		hasBeadsDir    bool
		jsonlFile      string   // name of JSONL file to create
		jsonlIssues    []string // issue IDs to put in JSONL
		hasDatabase    bool
		expectedStatus string
		expectPrefix   string // expected prefix in fix message
	}{
		{
			name:           "no beads directory",
			hasBeadsDir:    false,
			expectedStatus: "ok",
		},
		{
			name:           "no JSONL file",
			hasBeadsDir:    true,
			jsonlFile:      "",
			expectedStatus: "ok",
		},
		{
			name:           "database exists",
			hasBeadsDir:    true,
			jsonlFile:      "issues.jsonl",
			jsonlIssues:    []string{"bd-abc", "bd-def"},
			hasDatabase:    true,
			expectedStatus: "ok",
		},
		{
			name:           "empty JSONL",
			hasBeadsDir:    true,
			jsonlFile:      "issues.jsonl",
			jsonlIssues:    []string{},
			hasDatabase:    false,
			expectedStatus: "ok",
		},
		{
			name:           "fresh clone with issues.jsonl (bd-4ew)",
			hasBeadsDir:    true,
			jsonlFile:      "issues.jsonl",
			jsonlIssues:    []string{"bd-abc", "bd-def", "bd-ghi"},
			hasDatabase:    false,
			expectedStatus: "warning",
			expectPrefix:   "bd",
		},
		{
			name:           "fresh clone with beads.jsonl",
			hasBeadsDir:    true,
			jsonlFile:      "beads.jsonl",
			jsonlIssues:    []string{"proj-1", "proj-2"},
			hasDatabase:    false,
			expectedStatus: "warning",
			expectPrefix:   "proj",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")

			if tt.hasBeadsDir {
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
			}

			// Create JSONL file with issues
			if tt.jsonlFile != "" {
				jsonlPath := filepath.Join(beadsDir, tt.jsonlFile)
				file, err := os.Create(jsonlPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, issueID := range tt.jsonlIssues {
					issue := map[string]string{"id": issueID, "title": "Test issue"}
					data, _ := json.Marshal(issue)
					file.Write(data)
					file.WriteString("\n")
				}
				file.Close()
			}

			// Create database if needed (Dolt backend uses .beads/dolt/ directory)
			if tt.hasDatabase {
				doltDir := filepath.Join(beadsDir, "dolt")
				if err := os.MkdirAll(doltDir, 0755); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckFreshClone(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if tt.expectedStatus == "warning" {
				if check.Fix == "" {
					t.Error("Expected fix message for warning, got empty string")
				}
				if tt.expectPrefix != "" && !strings.Contains(check.Fix, tt.expectPrefix) {
					t.Errorf("Expected fix to contain prefix %q, got: %s", tt.expectPrefix, check.Fix)
				}
				if !strings.Contains(check.Fix, "bd init") {
					t.Error("Expected fix to mention 'bd init'")
				}
			}
		})
	}
}
