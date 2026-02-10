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

func TestIsMCPServerInstalledProjectLevel(t *testing.T) {
	mcpContent := `{"mcpServers":{"beads":{"command":"beads-mcp"}}}`

	// Test that MCP server is detected in each project-level settings file
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		t.Run(filename, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Chdir(tmpDir)

			if err := os.MkdirAll(".claude", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(".claude", filename), []byte(mcpContent), 0o644); err != nil {
				t.Fatal(err)
			}

			if !isMCPServerInstalled() {
				t.Errorf("expected to detect MCP server in .claude/%s", filename)
			}
		})
	}

	// Test negative cases
	t.Run("no mcpServers section", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"hooks":{}}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isMCPServerInstalled() {
			t.Error("expected NOT to detect MCP server when mcpServers section missing")
		}
	})

	t.Run("mcpServers but not beads", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"mcpServers":{"other-server":{"command":"other"}}}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isMCPServerInstalled() {
			t.Error("expected NOT to detect MCP server when beads not present")
		}
	})
}

func TestIsBeadsPluginInstalled(t *testing.T) {
	// Similar sanity check for plugin detection
	result := isBeadsPluginInstalled()

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from isBeadsPluginInstalled")
	}
}

func TestIsBeadsPluginInstalledProjectLevel(t *testing.T) {
	// Test that plugin is detected in each project-level settings file
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		t.Run(filename, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Chdir(tmpDir)

			if err := os.MkdirAll(".claude", 0o755); err != nil {
				t.Fatal(err)
			}
			content := `{"enabledPlugins":{"beads@beads-marketplace":true}}`
			if err := os.WriteFile(filepath.Join(".claude", filename), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			if !isBeadsPluginInstalled() {
				t.Errorf("expected to detect plugin in .claude/%s", filename)
			}
		})
	}

	// Test negative cases - plugin should NOT be detected
	t.Run("plugin disabled", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		// Set temp home to avoid detecting plugin from real ~/.claude/settings.json
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"enabledPlugins":{"beads@beads-marketplace":false}}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isBeadsPluginInstalled() {
			t.Error("expected NOT to detect plugin when explicitly disabled")
		}
	})

	t.Run("no plugin section", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		// Set temp home to avoid detecting plugin from real ~/.claude/settings.json
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"hooks":{}}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isBeadsPluginInstalled() {
			t.Error("expected NOT to detect plugin when enabledPlugins section missing")
		}
	})
}

func TestHasClaudeHooks(t *testing.T) {
	// Sanity check for hooks detection
	result := hasClaudeHooks()

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from hasClaudeHooks")
	}
}

func TestHasClaudeHooksProjectLevel(t *testing.T) {
	hooksContent := `{
		"hooks": {
			"SessionStart": [
				{"matcher": "", "hooks": [{"type": "command", "command": "bd prime"}]}
			]
		}
	}`

	setTempHome := func(t *testing.T, dir string) {
		t.Helper()
		t.Setenv("HOME", dir)
		t.Setenv("USERPROFILE", dir)
	}

	// Test that hooks are detected in each project-level settings file
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		t.Run(filename, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Chdir(tmpDir)
			setTempHome(t, t.TempDir())

			if err := os.MkdirAll(".claude", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(".claude", filename), []byte(hooksContent), 0o644); err != nil {
				t.Fatal(err)
			}

			if !hasClaudeHooks() {
				t.Errorf("expected to detect hooks in .claude/%s", filename)
			}
		})
	}

	// Test negative cases
	t.Run("no hooks section", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, t.TempDir())

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"enabledPlugins":{}}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if hasClaudeHooks() {
			t.Error("expected NOT to detect hooks when hooks section missing")
		}
	})

	t.Run("hooks but not bd prime", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, t.TempDir())

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"SessionStart": [
					{"matcher": "", "hooks": [{"type": "command", "command": "echo hello"}]}
				]
			}
		}`
		if err := os.WriteFile(filepath.Join(".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if hasClaudeHooks() {
			t.Error("expected NOT to detect hooks when bd prime not present")
		}
	})
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

func TestHasBeadsHooksWithStealthMode(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	settingsContent := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": "",
					"hooks": [
						{
							"type": "command",
							"command": "bd prime --stealth"
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
		t.Error("Expected true for settings file with bd prime --stealth hook")
	}
}

func TestCheckClaudeSettingsHealth(t *testing.T) {
	setTempHome := func(t *testing.T, dir string) {
		t.Helper()
		t.Setenv("HOME", dir)
		t.Setenv("USERPROFILE", dir)
	}

	t.Run("no settings files", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		check := CheckClaudeSettingsHealth()
		if check.Status != StatusOK {
			t.Errorf("Expected OK for no settings files, got %s", check.Status)
		}
		if check.Message != "No Claude Code settings files found" {
			t.Errorf("Expected 'No Claude Code settings files found', got %s", check.Message)
		}
	})

	t.Run("valid settings file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeSettingsHealth()
		if check.Status != StatusOK {
			t.Errorf("Expected OK for valid settings, got %s: %s", check.Status, check.Message)
		}
	})

	t.Run("malformed settings file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(`{bad json`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeSettingsHealth()
		if check.Status != StatusError {
			t.Errorf("Expected error for malformed settings, got %s", check.Status)
		}
		if check.Fix == "" {
			t.Error("Expected fix message for malformed settings")
		}
	})

	t.Run("project-level malformed settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(".claude", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(".claude", "settings.local.json"), []byte(`not json`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeSettingsHealth()
		if check.Status != StatusError {
			t.Errorf("Expected error for malformed project settings, got %s", check.Status)
		}
	})
}

func TestCheckClaudeHookCompleteness(t *testing.T) {
	setTempHome := func(t *testing.T, dir string) {
		t.Helper()
		t.Setenv("HOME", dir)
		t.Setenv("USERPROFILE", dir)
	}

	t.Run("no hooks at all", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		check := CheckClaudeHookCompleteness()
		if check.Status != StatusOK {
			t.Errorf("Expected OK for no hooks, got %s", check.Status)
		}
	})

	t.Run("both hooks present", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}],
				"PreCompact": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]
			}
		}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeHookCompleteness()
		if check.Status != StatusOK {
			t.Errorf("Expected OK for both hooks, got %s: %s", check.Status, check.Message)
		}
	})

	t.Run("only SessionStart hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]
			}
		}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeHookCompleteness()
		if check.Status != StatusWarning {
			t.Errorf("Expected warning for missing PreCompact, got %s", check.Status)
		}
	})

	t.Run("only PreCompact hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"PreCompact": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]
			}
		}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeHookCompleteness()
		if check.Status != StatusWarning {
			t.Errorf("Expected warning for missing SessionStart, got %s", check.Status)
		}
	})

	t.Run("stealth mode hooks detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"bd prime --stealth"}]}],
				"PreCompact": [{"matcher":"","hooks":[{"type":"command","command":"bd prime --stealth"}]}]
			}
		}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeHookCompleteness()
		if check.Status != StatusOK {
			t.Errorf("Expected OK for stealth mode hooks, got %s: %s", check.Status, check.Message)
		}
	})
}

func TestCheckHookEvents(t *testing.T) {
	t.Run("nonexistent file", func(t *testing.T) {
		ss, pc := checkHookEvents("/nonexistent/settings.json")
		if ss || pc {
			t.Error("Expected false for nonexistent file")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "settings.json")
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		ss, pc := checkHookEvents(path)
		if ss || pc {
			t.Error("Expected false for invalid JSON")
		}
	})

	t.Run("both events present", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "settings.json")
		content := `{
			"hooks": {
				"SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}],
				"PreCompact": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]
			}
		}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		ss, pc := checkHookEvents(path)
		if !ss || !pc {
			t.Errorf("Expected both true, got SessionStart=%v PreCompact=%v", ss, pc)
		}
	})

	t.Run("only SessionStart", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "settings.json")
		content := `{
			"hooks": {
				"SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]
			}
		}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		ss, pc := checkHookEvents(path)
		if !ss {
			t.Error("Expected SessionStart=true")
		}
		if pc {
			t.Error("Expected PreCompact=false")
		}
	})
}
