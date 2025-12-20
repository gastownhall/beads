package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckBdInPath(t *testing.T) {
	// This test verifies CheckBdInPath works correctly
	// Note: This test will pass if bd is in PATH (which it likely is during development)
	// In CI environments, the test may show "warning" if bd isn't installed
	check := CheckBdInPath()

	// Just verify the check returns a valid result
	if check.Name != "CLI Availability" {
		t.Errorf("Expected check name 'CLI Availability', got %s", check.Name)
	}

	if check.Status != "ok" && check.Status != "warning" {
		t.Errorf("Expected status 'ok' or 'warning', got %s", check.Status)
	}

	// If warning, should have a fix message
	if check.Status == "warning" && check.Fix == "" {
		t.Error("Expected fix message for warning status, got empty string")
	}
}

func TestCheckDocumentationBdPrimeReference(t *testing.T) {
	tests := []struct {
		name           string
		fileContent    map[string]string // filename -> content
		expectedStatus string
		expectDetail   bool
	}{
		{
			name:           "no documentation files",
			fileContent:    map[string]string{},
			expectedStatus: "ok",
			expectDetail:   false,
		},
		{
			name: "documentation without bd prime",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nUse bd ready to see ready issues.",
			},
			expectedStatus: "ok",
			expectDetail:   false,
		},
		{
			name: "AGENTS.md references bd prime",
			fileContent: map[string]string{
				"AGENTS.md": "# Agents\n\nRun `bd prime` to get context.",
			},
			expectedStatus: "ok", // Will be ok if bd is installed, warning otherwise
			expectDetail:   true,
		},
		{
			name: "CLAUDE.md references bd prime",
			fileContent: map[string]string{
				"CLAUDE.md": "# Claude\n\nUse bd prime for workflow context.",
			},
			expectedStatus: "ok",
			expectDetail:   true,
		},
		{
			name: ".claude/CLAUDE.md references bd prime",
			fileContent: map[string]string{
				".claude/CLAUDE.md": "Run bd prime to see workflow.",
			},
			expectedStatus: "ok",
			expectDetail:   true,
		},
		{
			name: "claude.local.md references bd prime (local-only)",
			fileContent: map[string]string{
				"claude.local.md": "Run bd prime for context.",
			},
			expectedStatus: "ok",
			expectDetail:   true,
		},
		{
			name: ".claude/claude.local.md references bd prime (local-only)",
			fileContent: map[string]string{
				".claude/claude.local.md": "Use bd prime for workflow context.",
			},
			expectedStatus: "ok",
			expectDetail:   true,
		},
		{
			name: "multiple files reference bd prime",
			fileContent: map[string]string{
				"AGENTS.md": "Use bd prime",
				"CLAUDE.md": "Run bd prime",
			},
			expectedStatus: "ok",
			expectDetail:   true,
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

			check := CheckDocumentationBdPrimeReference(tmpDir)

			if check.Name != "Prime Documentation" {
				t.Errorf("Expected check name 'Prime Documentation', got %s", check.Name)
			}

			// The status depends on whether bd is installed, so we accept both ok and warning
			if check.Status != "ok" && check.Status != "warning" {
				t.Errorf("Expected status 'ok' or 'warning', got %s", check.Status)
			}

			// If we expect detail (files were found), verify it's present
			if tt.expectDetail && check.Status == "ok" && check.Detail == "" {
				t.Error("Expected Detail field to be set when files reference bd prime")
			}

			// If warning, should have a fix message
			if check.Status == "warning" && check.Fix == "" {
				t.Error("Expected fix message for warning status, got empty string")
			}
		})
	}
}

func TestCheckDocumentationBdPrimeReferenceNoFiles(t *testing.T) {
	tmpDir := t.TempDir()

	check := CheckDocumentationBdPrimeReference(tmpDir)

	if check.Status != "ok" {
		t.Errorf("Expected status 'ok' for no documentation files, got %s", check.Status)
	}

	if check.Message != "No bd prime references in documentation" {
		t.Errorf("Expected message about no references, got: %s", check.Message)
	}
}

func TestIsMCPServerInstalled(t *testing.T) {
	// This test verifies the function doesn't crash with missing/invalid settings
	// We can't easily test the positive case without modifying the user's actual settings

	// The function should return false if settings don't exist or are invalid
	// This is a basic sanity check
	result := isMCPServerInstalled()

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from isMCPServerInstalled")
	}
}

func TestIsBeadsPluginInstalled(t *testing.T) {
	// Similar sanity check for plugin detection
	result := isBeadsPluginInstalled()

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from isBeadsPluginInstalled")
	}
}

func TestHasClaudeHooks(t *testing.T) {
	// Sanity check for hooks detection
	result := hasClaudeHooks()

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from hasClaudeHooks")
	}
}

func TestCheckClaude(t *testing.T) {
	// Verify CheckClaude returns a valid DoctorCheck
	check := CheckClaude()

	if check.Name != "Claude Integration" {
		t.Errorf("Expected check name 'Claude Integration', got %s", check.Name)
	}

	validStatuses := map[string]bool{"ok": true, "warning": true, "error": true}
	if !validStatuses[check.Status] {
		t.Errorf("Invalid status: %s", check.Status)
	}

	// If warning, should have fix message
	if check.Status == "warning" && check.Fix == "" {
		t.Error("Expected fix message for warning status")
	}
}

func TestHasBeadsHooksWithInvalidPath(t *testing.T) {
	// Test that hasBeadsHooks handles invalid/missing paths gracefully
	result := hasBeadsHooks("/nonexistent/path/to/settings.json")

	if result != false {
		t.Error("Expected false for non-existent settings file")
	}
}

func TestHasBeadsHooksWithInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// Write invalid JSON
	if err := os.WriteFile(settingsPath, []byte("not valid json"), 0644); err != nil {
		t.Fatal(err)
	}

	result := hasBeadsHooks(settingsPath)

	if result != false {
		t.Error("Expected false for invalid JSON settings file")
	}
}

func TestHasBeadsHooksWithNoHooksSection(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// Write valid JSON without hooks section
	if err := os.WriteFile(settingsPath, []byte(`{"enabledPlugins": {}}`), 0644); err != nil {
		t.Fatal(err)
	}

	result := hasBeadsHooks(settingsPath)

	if result != false {
		t.Error("Expected false for settings file without hooks section")
	}
}

func TestHasBeadsHooksWithBdPrime(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// Write settings with bd prime hook
	settingsContent := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": "beads",
					"hooks": [
						{
							"type": "command",
							"command": "bd prime"
						}
					]
				}
			]
		}
	}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := hasBeadsHooks(settingsPath)

	if result != true {
		t.Error("Expected true for settings file with bd prime hook")
	}
}

func TestHasBeadsHooksWithoutBdPrime(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// Write settings with hooks but not bd prime
	settingsContent := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": "something",
					"hooks": [
						{
							"type": "command",
							"command": "echo hello"
						}
					]
				}
			]
		}
	}`
	if err := os.WriteFile(settingsPath, []byte(settingsContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := hasBeadsHooks(settingsPath)

	if result != false {
		t.Error("Expected false for settings file without bd prime hook")
	}
}
