package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/setup"
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
	result := isMCPServerInstalled(".")

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

			if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmpDir, ".claude", filename), []byte(mcpContent), 0o644); err != nil {
				t.Fatal(err)
			}

			if !isMCPServerInstalled(tmpDir) {
				t.Errorf("expected to detect MCP server in .claude/%s", filename)
			}
		})
	}

	// Test negative cases
	t.Run("no mcpServers section", func(t *testing.T) {
		tmpDir := t.TempDir()

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"hooks":{}}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isMCPServerInstalled(tmpDir) {
			t.Error("expected NOT to detect MCP server when mcpServers section missing")
		}
	})

	t.Run("mcpServers but not beads", func(t *testing.T) {
		tmpDir := t.TempDir()

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"mcpServers":{"other-server":{"command":"other"}}}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isMCPServerInstalled(tmpDir) {
			t.Error("expected NOT to detect MCP server when beads not present")
		}
	})
}

func TestIsBeadsPluginInstalled(t *testing.T) {
	// Similar sanity check for plugin detection
	result := isBeadsPluginInstalled(".")

	// Just verify it returns a boolean without panicking
	if result != true && result != false {
		t.Error("Expected boolean result from isBeadsPluginInstalled")
	}
}

func TestIsBeadsPluginInstalledProjectLevel(t *testing.T) {
	// Keep the host's managed settings out of these rows.
	t.Cleanup(setup.SetClaudeManagedSettingsDirForTest(""))

	// Test that plugin is detected in each project-level settings file
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		t.Run(filename, func(t *testing.T) {
			tmpDir := t.TempDir()

			if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			content := `{"enabledPlugins":{"beads@beads-marketplace":true}}`
			if err := os.WriteFile(filepath.Join(tmpDir, ".claude", filename), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			if !isBeadsPluginInstalled(tmpDir) {
				t.Errorf("expected to detect plugin in .claude/%s", filename)
			}
		})
	}

	// Test negative cases - plugin should NOT be detected
	t.Run("plugin disabled", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Set temp home to avoid detecting plugin from real ~/.claude/settings.json
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"enabledPlugins":{"beads@beads-marketplace":false}}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isBeadsPluginInstalled(tmpDir) {
			t.Error("expected NOT to detect plugin when explicitly disabled")
		}
	})

	// bd doctor resolves the plugin as bd setup claude --check does: the
	// first settings file that names it decides, so a project's false turns
	// off a plugin the user settings enable, and only a plugin named beads
	// counts (#7774's review).
	for _, tc := range []struct {
		name, user, local string
	}{
		{"a local false turns off a user plugin", `{"enabledPlugins":{"beads@beads-marketplace":true}}`, `{"enabledPlugins":{"beads@beads-marketplace":false}}`},
		{"design-to-beads is not the beads plugin", `{"enabledPlugins":{"design-to-beads@xexr-marketplace":true}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			home := filepath.Join(tmpDir, "home")
			project := filepath.Join(tmpDir, "project")
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for path, content := range map[string]string{
				filepath.Join(home, ".claude", "settings.json"):          tc.user,
				filepath.Join(project, ".claude", "settings.local.json"): tc.local,
			} {
				if content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if isBeadsPluginInstalled(project) {
				t.Error("expected the beads plugin to be off in the project")
			}
		})
	}

	t.Run("no plugin section", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Set temp home to avoid detecting plugin from real ~/.claude/settings.json
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"hooks":{}}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if isBeadsPluginInstalled(tmpDir) {
			t.Error("expected NOT to detect plugin when enabledPlugins section missing")
		}
	})
}

func TestHasClaudeHooks(t *testing.T) {
	// Sanity check for hooks detection
	result := hasClaudeHooks(".")

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
			setTempHome(t, t.TempDir())

			if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmpDir, ".claude", filename), []byte(hooksContent), 0o644); err != nil {
				t.Fatal(err)
			}

			if !hasClaudeHooks(tmpDir) {
				t.Errorf("expected to detect hooks in .claude/%s", filename)
			}
		})
	}

	// Test negative cases
	t.Run("no hooks section", func(t *testing.T) {
		tmpDir := t.TempDir()
		setTempHome(t, t.TempDir())

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"enabledPlugins":{}}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if hasClaudeHooks(tmpDir) {
			t.Error("expected NOT to detect hooks when hooks section missing")
		}
	})

	t.Run("hooks but not bd prime", func(t *testing.T) {
		tmpDir := t.TempDir()
		setTempHome(t, t.TempDir())

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"hooks": {
				"SessionStart": [
					{"matcher": "", "hooks": [{"type": "command", "command": "echo hello"}]}
				]
			}
		}`
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		if hasClaudeHooks(tmpDir) {
			t.Error("expected NOT to detect hooks when bd prime not present")
		}
	})
}

func TestCheckClaude(t *testing.T) {
	// Verify CheckClaude returns a valid DoctorCheck
	check := CheckClaude(".")

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

		check := CheckClaudeSettingsHealth(tmpDir)
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

		check := CheckClaudeSettingsHealth(tmpDir)
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

		check := CheckClaudeSettingsHealth(tmpDir)
		if check.Status != StatusError {
			t.Errorf("Expected error for malformed settings, got %s", check.Status)
		}
		if check.Fix == "" {
			t.Error("Expected fix message for malformed settings")
		}
	})

	t.Run("project-level malformed settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		setTempHome(t, tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(`not json`), 0o644); err != nil {
			t.Fatal(err)
		}

		check := CheckClaudeSettingsHealth(tmpDir)
		if check.Status != StatusError {
			t.Errorf("Expected error for malformed project settings, got %s", check.Status)
		}
	})

	t.Run("malformed repository root local settings, from a subdirectory", func(t *testing.T) {
		home, repo, sub := rootLocalSettingsFixture(t)
		rootLocal := filepath.Join(repo, ".claude", "settings.local.json")
		for _, path := range []string{filepath.Join(sub, ".claude", "settings.json"), rootLocal, filepath.Join(home, ".claude", "settings.json")} {
			writeClaudeSettingsFile(t, path, `not json`)
		}

		check := CheckClaudeSettingsHealth(sub)
		if check.Status != StatusError || check.Message != "3 malformed settings file(s)" {
			t.Fatalf("Expected 3 malformed settings files, got %s: %s\n%s", check.Status, check.Message, check.Detail)
		}
		// The project's file is named relative to the directory doctor
		// checks, the user's from ~, and the repository root's in full.
		lines := strings.Split(check.Detail, "\n")
		for i, prefix := range []string{filepath.Join(".claude", "settings.json") + ": ", rootLocal + ": ", "~/.claude/settings.json: "} {
			if i >= len(lines) || !strings.HasPrefix(lines[i], prefix) {
				t.Errorf("Expected detail line %d to start with %q, got:\n%s", i, prefix, check.Detail)
			}
		}
	})
}

// rootLocalSettingsFixture makes a git repository with a subdirectory sub
// and a home directory outside it, and points HOME there. Claude Code reads
// the repository root's .claude/settings.local.json in a session started in
// sub, as bd setup claude --check does there.
func rootLocalSettingsFixture(t *testing.T) (home, repo, sub string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Claude Code keeps settings.local.json beside settings.json on Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, repo = filepath.Join(root, "home"), filepath.Join(root, "repo")
	sub = filepath.Join(repo, "sub")
	for _, dir := range []string{home, sub} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home, repo, sub
}

// writeClaudeSettingsFile writes a Claude Code settings file, creating its
// .claude directory.
func writeClaudeSettingsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
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

		check := CheckClaudeHookCompleteness(tmpDir)
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

		check := CheckClaudeHookCompleteness(tmpDir)
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

		check := CheckClaudeHookCompleteness(tmpDir)
		if check.Status != StatusOK {
			t.Errorf("Expected OK for SessionStart hook, got %s", check.Status)
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

		check := CheckClaudeHookCompleteness(tmpDir)
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

		check := CheckClaudeHookCompleteness(tmpDir)
		if check.Status != StatusOK {
			t.Errorf("Expected OK for stealth mode hooks, got %s: %s", check.Status, check.Message)
		}
	})

	// From a subdirectory Claude Code loads the hooks in the repository
	// root's .claude/settings.local.json, as bd setup claude --check finds.
	t.Run("PreCompact hook only in the repository root's local settings, from a subdirectory", func(t *testing.T) {
		_, repo, sub := rootLocalSettingsFixture(t)
		writeClaudeSettingsFile(t, filepath.Join(repo, ".claude", "settings.local.json"), `{
			"hooks": {
				"PreCompact": [{"matcher":"","hooks":[{"type":"command","command":"bd prime --hook-json"}]}]
			}
		}`)

		check := CheckClaudeHookCompleteness(sub)
		if check.Status != StatusWarning {
			t.Errorf("Expected warning for missing SessionStart, got %s: %s", check.Status, check.Message)
		}
		if !hasClaudeHooks(sub) {
			t.Error("Expected the repository root's local hook to count as installed")
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

// TestIsClaudePresent tests the isClaudePresent helper across different scenarios.
// Tests that require claude to be absent from PATH are skipped when claude is
// installed locally; they run in CI where claude is not available.
func TestIsClaudePresent(t *testing.T) {
	t.Run("claude_binary_in_path", func(t *testing.T) {
		// Create a fake "claude" script in a temp bin dir so we can test
		// the exec.LookPath success path without depending on a real installation.
		tmpBin := t.TempDir()
		claudePath := filepath.Join(tmpBin, "claude")
		if err := os.WriteFile(claudePath, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", tmpBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())

		if !isClaudePresent() {
			t.Error("Expected isClaudePresent() to return true when claude binary is in PATH")
		}
	})

	t.Run("dot_claude_dir_exists", func(t *testing.T) {
		// Only meaningful when claude binary is absent; skip otherwise so we actually
		// exercise the ~/.claude stat path rather than returning early via LookPath.
		if _, lookErr := exec.LookPath("claude"); lookErr == nil {
			t.Skip("claude binary found in PATH; ~/.claude directory check is unreachable")
		}
		tmpDir := t.TempDir()
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)
		if err := os.Mkdir(filepath.Join(tmpDir, ".claude"), 0o750); err != nil {
			t.Fatal(err)
		}
		if !isClaudePresent() {
			t.Error("Expected isClaudePresent() to return true when ~/.claude directory exists")
		}
	})

	t.Run("dot_claude_is_file_not_dir", func(t *testing.T) {
		if _, lookErr := exec.LookPath("claude"); lookErr == nil {
			t.Skip("claude binary found in PATH; IsDir() check is unreachable")
		}
		tmpDir := t.TempDir()
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)
		if err := os.WriteFile(filepath.Join(tmpDir, ".claude"), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}
		// .claude exists but is a file, not a directory — should return false.
		if isClaudePresent() {
			t.Error("Expected isClaudePresent() to return false when ~/.claude is a file, not a directory")
		}
	})

	t.Run("no_binary_no_dot_claude", func(t *testing.T) {
		if _, lookErr := exec.LookPath("claude"); lookErr == nil {
			t.Skip("claude binary found in PATH; skipping no-claude absence test")
		}
		tmpDir := t.TempDir()
		t.Setenv("HOME", tmpDir)
		t.Setenv("USERPROFILE", tmpDir)
		if isClaudePresent() {
			t.Error("Expected isClaudePresent() to return false when claude not in PATH and ~/.claude absent")
		}
	})
}

// TestCheckClaudePlugin_ClaudeCodeWithoutClaude verifies that CheckClaudePlugin
// returns StatusOK early when CLAUDECODE=1 but the claude CLI/dir is absent.
func TestCheckClaudePlugin_ClaudeCodeWithoutClaude(t *testing.T) {
	if _, lookErr := exec.LookPath("claude"); lookErr == nil {
		t.Skip("claude binary found in PATH; cannot exercise !isClaudePresent() short-circuit")
	}
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("CLAUDECODE", "1")

	check := CheckClaudePlugin()

	if check.Name != "Claude Plugin" {
		t.Errorf("Expected name %q, got %q", "Claude Plugin", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("Expected %s when CLAUDECODE=1 but claude absent, got %s: %s",
			StatusOK, check.Status, check.Message)
	}
	if check.Message != "N/A (not running in Claude Code)" {
		t.Errorf("Expected message %q, got %q", "N/A (not running in Claude Code)", check.Message)
	}
}

// TestCheckClaude_ClaudeCodeWithoutClaude verifies that CheckClaude returns
// "CLI-only mode" (ok) when CLAUDECODE=1 but the claude CLI/dir is absent.
func TestCheckClaude_ClaudeCodeWithoutClaude(t *testing.T) {
	if _, lookErr := exec.LookPath("claude"); lookErr == nil {
		t.Skip("claude binary found in PATH; cannot exercise !isClaudePresent() short-circuit in CheckClaude")
	}
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("CLAUDECODE", "1")
	// Keep a host managed policy that enables beads out of "CLI-only mode".
	t.Cleanup(setup.SetClaudeManagedSettingsDirForTest(""))

	check := CheckClaude(tmpDir)

	if check.Name != "Claude Integration" {
		t.Errorf("Expected name %q, got %q", "Claude Integration", check.Name)
	}
	if check.Status != "ok" {
		t.Errorf("Expected %q status when CLAUDECODE=1 but claude absent, got %q: %s",
			"ok", check.Status, check.Message)
	}
	if check.Message != "CLI-only mode" {
		t.Errorf("Expected message %q, got %q", "CLI-only mode", check.Message)
	}
}

// isolatePrimeCheckHome points HOME at an empty directory so a developer's
// Claude MCP settings cannot change how the prime output is classified.
func isolatePrimeCheckHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// writeRecordingBd writes an executable shell script named name into dir. Each
// run appends its argv, physical cwd and BEADS_DIR to the file named by the
// environment variable logEnv, then prints CLI-mode prime output. It uses only
// an absolute shebang and shell builtins, so it runs with an empty PATH.
func writeRecordingBd(t *testing.T, dir, name, logEnv string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"{ echo \"argv=$*\"; echo \"cwd=$(pwd -P)\"; echo \"beads_dir=${BEADS_DIR-<unset>}\"; echo ---; } >> \"$" + logEnv + "\"\n" +
		"echo 'Run bd ready to find work'\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// readPrimeRecords parses the invocations a recording bd appended to logPath.
func readPrimeRecords(t *testing.T, logPath string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]string
	rec := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "---" {
			records = append(records, rec)
			rec = map[string]string{}
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			rec[key] = value
		}
	}
	return records
}

// writePrimeReferencingRepo creates a workspace whose CLAUDE.md mentions bd prime.
func writePrimeReferencingRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("Run `bd prime` at session start.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func stubPrimeCheckBinary(t *testing.T, resolve func() (string, error)) {
	t.Helper()
	orig := primeCheckBinary
	primeCheckBinary = resolve
	t.Cleanup(func() { primeCheckBinary = orig })
}

// TestPrimeChecksDoNotExecPathBd pins GH#8130: the prime checks must never run
// whichever `bd` comes first on PATH (a different version could open and
// migrate the diagnosed store). Under a test binary they run nothing at all.
func TestPrimeChecksDoNotExecPathBd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoy bd is a shell script")
	}
	isolatePrimeCheckHome(t)
	decoyDir := t.TempDir()
	decoyLog := filepath.Join(t.TempDir(), "decoy.log")
	writeRecordingBd(t, decoyDir, "bd", "PRIME_DECOY_LOG")
	t.Setenv("PRIME_DECOY_LOG", decoyLog)
	t.Setenv("PATH", decoyDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := writePrimeReferencingRepo(t)
	t.Chdir(t.TempDir()) // the caller's cwd differs from the diagnosed workspace

	_ = VerifyPrimeOutput(repo)
	_ = CheckDocumentationBdPrimeReference(repo)

	if records := readPrimeRecords(t, decoyLog); len(records) != 0 {
		t.Fatalf("prime checks executed the PATH bd %d time(s): %v", len(records), records)
	}
}

// TestPrimeChecksRunResolvedBinaryInDiagnosedWorkspace runs both prime checks
// against a real recording child selected by the resolver, with a decoy bd on
// PATH and with an empty PATH: the resolved binary runs with each check's
// arguments in the diagnosed workspace, the decoy never runs, and the result
// does not depend on PATH or the caller's cwd and BEADS_DIR.
func TestPrimeChecksRunResolvedBinaryInDiagnosedWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording bd is a shell script")
	}
	isolatePrimeCheckHome(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	decoyLog := filepath.Join(t.TempDir(), "decoy.log")
	t.Setenv("PRIME_CHILD_LOG", childLog)
	t.Setenv("PRIME_DECOY_LOG", decoyLog)
	exe := writeRecordingBd(t, t.TempDir(), "bd-under-test", "PRIME_CHILD_LOG")
	stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })
	decoyDir := t.TempDir()
	writeRecordingBd(t, decoyDir, "bd", "PRIME_DECOY_LOG")

	callerBeadsDir := filepath.Join(t.TempDir(), ".beads")
	mustMkdir(t, callerBeadsDir)

	for _, tc := range []struct {
		name     string
		path     string
		beadsEnv string // the caller's BEADS_DIR ("" = unset)
	}{
		{name: "decoy bd on PATH", path: decoyDir, beadsEnv: callerBeadsDir},
		{name: "empty PATH", path: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.RemoveAll(childLog); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tc.path)
			t.Setenv("BEADS_DIR", tc.beadsEnv)
			if tc.beadsEnv == "" {
				if err := os.Unsetenv("BEADS_DIR"); err != nil {
					t.Fatal(err)
				}
			}
			repo := writePrimeReferencingRepo(t)
			mustMkdir(t, filepath.Join(repo, ".beads"))
			t.Chdir(t.TempDir())

			prime := VerifyPrimeOutput(repo)
			if prime.Status != "ok" || prime.Message != "CLI mode detected" {
				t.Errorf("VerifyPrimeOutput = %+v, want ok CLI mode detected", prime)
			}
			doc := CheckDocumentationBdPrimeReference(repo)
			if doc.Status != "ok" || doc.Detail != "Files: CLAUDE.md" {
				t.Errorf("CheckDocumentationBdPrimeReference = %+v, want ok with Files: CLAUDE.md", doc)
			}

			records := readPrimeRecords(t, childLog)
			if len(records) != 2 {
				t.Fatalf("resolved binary ran %d time(s), want 2: %v", len(records), records)
			}
			if got := records[0]["argv"]; got != "prime" {
				t.Errorf("VerifyPrimeOutput argv = %q, want %q", got, "prime")
			}
			if got := records[1]["argv"]; got != "prime --help" {
				t.Errorf("CheckDocumentationBdPrimeReference argv = %q, want %q", got, "prime --help")
			}
			for _, rec := range records {
				if rec["cwd"] != resolve(t, repo) {
					t.Errorf("%q ran in %q, want the diagnosed repo %q", rec["argv"], rec["cwd"], resolve(t, repo))
				}
				if want := filepath.Join(repo, ".beads"); rec["beads_dir"] == "<unset>" || resolve(t, rec["beads_dir"]) != resolve(t, want) {
					t.Errorf("%q saw BEADS_DIR=%q, want %q", rec["argv"], rec["beads_dir"], want)
				}
			}
			if decoy := readPrimeRecords(t, decoyLog); len(decoy) != 0 {
				t.Errorf("PATH decoy ran: %v", decoy)
			}
			if tc.path == "" {
				if avail := CheckBdInPath(); avail.Status != "warning" {
					t.Errorf("CheckBdInPath with empty PATH = %+v, want warning", avail)
				}
			}
		})
	}
}

// effectiveEnv returns the value the child sees for key: the last entry wins.
func effectiveEnv(env []string, key string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value, found = v, true
		}
	}
	return value, found
}

// TestPrimeCheckCommandPinsWorkspace pins GH#8130's workspace half: the child
// runs in the absolute diagnosed repo path with BEADS_DIR set to doctor's
// resolution for it, whatever the caller's cwd and BEADS_DIR are.
func TestPrimeCheckCommandPinsWorkspace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "bd-sentinel")
	stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })
	callerBeadsDir := filepath.Join(t.TempDir(), ".beads")

	type fixture struct {
		repoArg   string // the repoPath passed to the check
		repoDir   string // the directory it names
		beadsDir  string // where the workspace's .beads really lives
		beadsEnv  string // the caller's BEADS_DIR ("" = unset)
		chdirInto string // the caller's cwd
	}
	cases := map[string]func(t *testing.T) fixture{
		"absolute path, BEADS_DIR unset": func(t *testing.T) fixture {
			repo := t.TempDir()
			mustMkdir(t, filepath.Join(repo, ".beads"))
			return fixture{repoArg: repo, repoDir: repo, beadsDir: filepath.Join(repo, ".beads"), chdirInto: t.TempDir()}
		},
		"relative path, BEADS_DIR elsewhere": func(t *testing.T) fixture {
			parent := t.TempDir()
			repo := filepath.Join(parent, "ws")
			mustMkdir(t, filepath.Join(repo, ".beads"))
			return fixture{repoArg: "ws", repoDir: repo, beadsDir: filepath.Join(repo, ".beads"), beadsEnv: callerBeadsDir, chdirInto: parent}
		},
		"redirected .beads": func(t *testing.T) fixture {
			repo := t.TempDir()
			target := filepath.Join(t.TempDir(), ".beads")
			mustMkdir(t, target)
			// A redirect is followed only to a directory holding a database.
			if err := os.WriteFile(filepath.Join(target, "metadata.json"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mustMkdir(t, filepath.Join(repo, ".beads"))
			if err := os.WriteFile(filepath.Join(repo, ".beads", "redirect"), []byte(target+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return fixture{repoArg: repo, repoDir: repo, beadsDir: target, beadsEnv: callerBeadsDir, chdirInto: t.TempDir()}
		},
		"worktree shares the main repo's .beads": func(t *testing.T) fixture {
			mainRepo, worktree := setupWorktreeRepo(t)
			mustMkdir(t, filepath.Join(mainRepo, ".beads"))
			return fixture{repoArg: worktree, repoDir: worktree, beadsDir: filepath.Join(mainRepo, ".beads"), beadsEnv: callerBeadsDir, chdirInto: t.TempDir()}
		},
	}
	argLists := [][]string{{"prime"}, {"prime", "--help"}}

	for name, setup := range cases {
		for _, args := range argLists {
			t.Run(name+"/"+strings.Join(args, " "), func(t *testing.T) {
				fx := setup(t)
				if fx.beadsEnv == "" {
					t.Setenv("BEADS_DIR", "")
					if err := os.Unsetenv("BEADS_DIR"); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv("BEADS_DIR", fx.beadsEnv)
				}
				t.Chdir(fx.chdirInto)

				cmd, err := primeCheckCommand(fx.repoArg, args...)
				if err != nil {
					t.Fatalf("primeCheckCommand: %v", err)
				}
				if cmd.Path != exe {
					t.Errorf("Path = %q, want %q", cmd.Path, exe)
				}
				if got, want := strings.Join(cmd.Args[1:], " "), strings.Join(args, " "); got != want {
					t.Errorf("Args = %q, want %q", got, want)
				}
				if !filepath.IsAbs(cmd.Dir) {
					t.Errorf("Dir = %q, want an absolute path", cmd.Dir)
				} else if resolve(t, cmd.Dir) != resolve(t, fx.repoDir) {
					t.Errorf("Dir = %q, want the diagnosed repo %q", cmd.Dir, fx.repoDir)
				}
				got, ok := effectiveEnv(cmd.Env, "BEADS_DIR")
				if !ok {
					t.Fatalf("child env has no BEADS_DIR; want %q", fx.beadsDir)
				}
				if resolve(t, got) != resolve(t, fx.beadsDir) {
					t.Errorf("effective BEADS_DIR = %q, want %q", got, fx.beadsDir)
				}
			})
		}
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPrimeChecksReportResolvedBinaryFailure pins how the prime checks report
// a binary that cannot be resolved or fails: the command check stays an error,
// the documentation check stays a warning with its file list, both say what
// failed, and neither tells the user to install, upgrade or PATH-fix bd (the
// binary that failed is the one running doctor, not PATH's).
func TestPrimeChecksReportResolvedBinaryFailure(t *testing.T) {
	isolatePrimeCheckHome(t)
	assertAdvice := func(t *testing.T, check DoctorCheck) {
		t.Helper()
		if check.Fix == "" {
			t.Errorf("%s: no Fix advice", check.Name)
		}
		lower := strings.ToLower(check.Fix)
		for _, banned := range []string{"install", "upgrade", "remove"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s: Fix %q mentions %q", check.Name, check.Fix, banned)
			}
		}
		if strings.Contains(check.Fix, "PATH") {
			t.Errorf("%s: Fix %q mentions PATH", check.Name, check.Fix)
		}
	}
	assertFailure := func(t *testing.T, repo, wantContext string) {
		t.Helper()
		prime := VerifyPrimeOutput(repo)
		if prime.Status != "error" {
			t.Errorf("VerifyPrimeOutput status = %q, want error (%+v)", prime.Status, prime)
		}
		if !strings.Contains(prime.Detail, wantContext) {
			t.Errorf("VerifyPrimeOutput Detail = %q, want it to contain %q", prime.Detail, wantContext)
		}
		assertAdvice(t, prime)

		doc := CheckDocumentationBdPrimeReference(repo)
		if doc.Status != "warning" {
			t.Errorf("CheckDocumentationBdPrimeReference status = %q, want warning (%+v)", doc.Status, doc)
		}
		if !strings.Contains(doc.Detail, "Files: CLAUDE.md") || !strings.Contains(doc.Detail, wantContext) {
			t.Errorf("CheckDocumentationBdPrimeReference Detail = %q, want Files: CLAUDE.md and %q", doc.Detail, wantContext)
		}
		assertAdvice(t, doc)
	}

	t.Run("resolver fails", func(t *testing.T) {
		stubPrimeCheckBinary(t, func() (string, error) {
			return "", errors.New("os.Executable: no such file")
		})
		assertFailure(t, writePrimeReferencingRepo(t), "os.Executable: no such file")
	})

	t.Run("resolved binary exits nonzero", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("failing bd is a shell script")
		}
		exe := filepath.Join(t.TempDir(), "bd-failing")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'prime exploded' >&2\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })
		repo := writePrimeReferencingRepo(t)
		assertFailure(t, repo, "exit status 3")
		if prime := VerifyPrimeOutput(repo); !strings.Contains(prime.Detail, exe) {
			t.Errorf("VerifyPrimeOutput Detail = %q, want the resolved binary %q", prime.Detail, exe)
		}
	})

	t.Run("resolved binary prints nothing", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("silent bd is a shell script")
		}
		exe := filepath.Join(t.TempDir(), "bd-silent")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })
		prime := VerifyPrimeOutput(writePrimeReferencingRepo(t))
		if prime.Status != "error" || prime.Message != "No output produced" {
			t.Errorf("VerifyPrimeOutput = %+v, want error No output produced", prime)
		}
	})
}
