package doctor

import (
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

func TestCheckLegacyJSONLFilename(t *testing.T) {
	tests := []struct {
		name           string
		files          []string
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "no JSONL files",
			files:          []string{},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "canonical beads.jsonl",
			files:          []string{"beads.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "legacy issues.jsonl",
			files:          []string{"issues.jsonl"},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name:           "both files present",
			files:          []string{"beads.jsonl", "issues.jsonl"},
			expectedStatus: "warning",
			expectWarning:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			// Create test files
			for _, file := range tt.files {
				filePath := filepath.Join(beadsDir, file)
				if err := os.WriteFile(filePath, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckLegacyJSONLFilename(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, check.Status)
			}

			if tt.expectWarning && check.Fix == "" {
				t.Error("Expected fix message for warning, got empty string")
			}
		})
	}
}
