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
				if tt.expectPrefix != "" && strings.Contains(check.Fix, "bd init") {
					t.Errorf("did not expect legacy fresh-clone recovery to keep init-based guidance, got: %s", check.Fix)
				}
				if !strings.Contains(check.Fix, "bd bootstrap") {
					t.Error("Expected fix to mention 'bd bootstrap'")
				}
			}
		})
	}
}

func TestStripBeadsIntegrationSection(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no markers — content unchanged",
			in:   "# Header\n\nUser content.\n",
			want: "# Header\n\nUser content.\n",
		},
		{
			name: "legacy markers stripped with trailing newline",
			in:   "# Header\n<!-- BEGIN BEADS INTEGRATION -->\nManaged body\n<!-- END BEADS INTEGRATION -->\nFooter\n",
			want: "# Header\nFooter\n",
		},
		{
			name: "v1 markers stripped",
			in:   "Pre\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:abcd1234 -->\nbody\n<!-- END BEADS INTEGRATION -->\nPost\n",
			want: "Pre\nPost\n",
		},
		{
			name: "missing END marker — left untouched",
			in:   "<!-- BEGIN BEADS INTEGRATION -->\nbody without end\n",
			want: "<!-- BEGIN BEADS INTEGRATION -->\nbody without end\n",
		},
		{
			name: "END before BEGIN — left untouched",
			in:   "<!-- END BEADS INTEGRATION -->\nstuff\n<!-- BEGIN BEADS INTEGRATION -->\n",
			want: "<!-- END BEADS INTEGRATION -->\nstuff\n<!-- BEGIN BEADS INTEGRATION -->\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripBeadsIntegrationSection(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeUserAuthored(t *testing.T) {
	a := "# Header\n\nUser content.\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:aaaa1111 -->\nbody A\n<!-- END BEADS INTEGRATION -->\n"
	b := "# Header\r\n\r\nUser content.   \r\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:bbbb2222 -->\nbody B is different\n<!-- END BEADS INTEGRATION -->\n\n"
	if normalizeUserAuthored(a) != normalizeUserAuthored(b) {
		t.Errorf("expected normalized equality despite CRLF, trailing space, and managed-section differences\nA: %q\nB: %q", normalizeUserAuthored(a), normalizeUserAuthored(b))
	}

	c := "# Header\n\nUser content.\n"
	d := "# Header\n\nDIFFERENT user content.\n"
	if normalizeUserAuthored(c) == normalizeUserAuthored(d) {
		t.Error("expected divergent user-authored content to differ after normalization")
	}
}

func TestCheckAgentDocDivergence(t *testing.T) {
	const managed = "<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:abcd1234 -->\nManaged body\n<!-- END BEADS INTEGRATION -->\n"

	t.Run("missing files — N/A ok", func(t *testing.T) {
		tmp := t.TempDir()
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok when files missing, got %s", check.Status)
		}
	})

	t.Run("matching user-authored content — ok", func(t *testing.T) {
		tmp := t.TempDir()
		body := "# Project\n\nShared instructions.\n\n" + managed
		writeFile(t, tmp, "AGENTS.md", body)
		writeFile(t, tmp, "CLAUDE.md", body)
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok for matching content, got %s (msg=%s)", check.Status, check.Message)
		}
	})

	t.Run("matching content with different managed sections — ok", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "AGENTS.md", "# Project\n\nShared instructions.\n\n"+managed)
		writeFile(t, tmp, "CLAUDE.md", "# Project\n\nShared instructions.\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:99999999 -->\nDifferent managed body\n<!-- END BEADS INTEGRATION -->\n")
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok when only managed sections differ, got %s", check.Status)
		}
	})

	t.Run("diverged user-authored content — warning with fix", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "AGENTS.md", "# Project\n\nOriginal instructions.\n\n"+managed)
		writeFile(t, tmp, "CLAUDE.md", "# Project\n\nHand-edited divergent instructions.\n\n"+managed)
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusWarning {
			t.Fatalf("expected warning, got %s (msg=%s)", check.Status, check.Message)
		}
		if check.Fix == "" {
			t.Error("expected fix message")
		}
		if !strings.Contains(check.Fix, "ln -sf") {
			t.Error("expected fix to suggest symlink option")
		}
		if !strings.Contains(check.Fix, "bd setup claude") {
			t.Error("expected fix to suggest bd setup claude")
		}
	})

	t.Run("opt-out marker silences divergence — ok", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "AGENTS.md", "# Agents\n\n<!-- bd-doctor-divergence: ok -->\n\nFor agents.\n\n"+managed)
		writeFile(t, tmp, "CLAUDE.md", "# Claude\n\nFor Claude with totally different content.\n\n"+managed)
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok with opt-out marker, got %s (msg=%s)", check.Status, check.Message)
		}
	})

	t.Run("symlinked pair — skipped as ok", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "AGENTS.md", "# Project\n\nInstructions.\n\n"+managed)
		if err := os.Symlink("AGENTS.md", filepath.Join(tmp, "CLAUDE.md")); err != nil {
			t.Skipf("symlink not supported in this environment: %v", err)
		}
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok for symlinked pair, got %s (msg=%s)", check.Status, check.Message)
		}
	})

	t.Run("hardlinked pair — skipped as ok", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, tmp, "AGENTS.md", "# Project\n\nInstructions.\n\n"+managed)
		if err := os.Link(filepath.Join(tmp, "AGENTS.md"), filepath.Join(tmp, "CLAUDE.md")); err != nil {
			t.Skipf("hardlink not supported in this environment: %v", err)
		}
		check := CheckAgentDocDivergence(tmp)
		if check.Status != StatusOK {
			t.Errorf("expected ok for hardlinked pair, got %s (msg=%s)", check.Status, check.Message)
		}
	})
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if d := filepath.Dir(path); d != dir {
		if err := os.MkdirAll(d, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
