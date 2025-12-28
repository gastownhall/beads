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
			name:           "single issues.jsonl",
			files:          []string{"issues.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "single beads.jsonl is ok",
			files:          []string{"beads.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "custom name is ok",
			files:          []string{"my-issues.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "multiple JSONL files warning",
			files:          []string{"beads.jsonl", "issues.jsonl"},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name:           "backup files ignored",
			files:          []string{"issues.jsonl", "issues.jsonl.backup", "BACKUP_issues.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "multiple real files with backups",
			files:          []string{"issues.jsonl", "beads.jsonl", "issues.jsonl.backup"},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name:           "deletions.jsonl ignored as system file",
			files:          []string{"beads.jsonl", "deletions.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "merge artifacts ignored",
			files:          []string{"issues.jsonl", "issues.base.jsonl", "issues.left.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "merge artifacts with right variant ignored",
			files:          []string{"issues.jsonl", "issues.right.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "beads merge artifacts ignored (bd-ov1)",
			files:          []string{"issues.jsonl", "beads.base.jsonl", "beads.left.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "interactions.jsonl ignored as system file (GH#709)",
			files:          []string{"issues.jsonl", "interactions.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "molecules.jsonl ignored as system file",
			files:          []string{"issues.jsonl", "molecules.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "all system files ignored together",
			files:          []string{"issues.jsonl", "deletions.jsonl", "interactions.jsonl", "molecules.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
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

func TestCheckLegacyJSONLConfig(t *testing.T) {
	tests := []struct {
		name           string
		configJSONL    string   // what metadata.json says
		existingFiles  []string // which files actually exist
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "no config (defaults)",
			configJSONL:    "",
			existingFiles:  []string{},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "using canonical issues.jsonl",
			configJSONL:    "issues.jsonl",
			existingFiles:  []string{"issues.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "using custom name",
			configJSONL:    "my-project.jsonl",
			existingFiles:  []string{"my-project.jsonl"},
			expectedStatus: "ok",
			expectWarning:  false,
		},
		{
			name:           "using legacy beads.jsonl",
			configJSONL:    "beads.jsonl",
			existingFiles:  []string{"beads.jsonl"},
			expectedStatus: "warning",
			expectWarning:  true,
		},
		{
			name:           "config says beads.jsonl but issues.jsonl exists",
			configJSONL:    "beads.jsonl",
			existingFiles:  []string{"issues.jsonl"},
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
			for _, file := range tt.existingFiles {
				filePath := filepath.Join(beadsDir, file)
				if err := os.WriteFile(filePath, []byte(`{"id":"test"}`), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Create metadata.json if configJSONL is set
			if tt.configJSONL != "" {
				metadataPath := filepath.Join(beadsDir, "metadata.json")
				content := `{"database":"beads.db","jsonl_export":"` + tt.configJSONL + `"}`
				if err := os.WriteFile(metadataPath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := CheckLegacyJSONLConfig(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if tt.expectWarning && check.Fix == "" {
				t.Error("Expected fix message for warning, got empty string")
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
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db","jsonl_export":"issues.jsonl"}`), 0644); err != nil {
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

func TestCheckDatabaseConfig_SystemJSONLExportIsError(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db","jsonl_export":"interactions.jsonl"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckDatabaseConfig(tmpDir)
	if check.Status != "error" {
		t.Fatalf("expected error, got %s: %s", check.Status, check.Message)
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

			// Create database if needed
			if tt.hasDatabase {
				dbPath := filepath.Join(beadsDir, "beads.db")
				if err := os.WriteFile(dbPath, []byte("fake db"), 0644); err != nil {
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
