package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/utils"
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

// TestVerifyPrimeOutputDoesNotRunPathBd pins GH#8130: the "bd prime" check
// must verify the running binary against repoPath, never execute whichever
// `bd` comes first on PATH (a different version could migrate the store).
func TestVerifyPrimeOutputDoesNotRunPathBd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd is a shell script")
	}
	fakeDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "fake-bd.log")
	script := "#!/bin/sh\n{ echo \"argv: $*\"; echo \"cwd: $(pwd)\"; } >> " + logPath + "\necho 'bd ready'\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repoPath := t.TempDir()
	t.Chdir(t.TempDir()) // caller's cwd differs from the diagnosed workspace

	_ = VerifyPrimeOutput(repoPath)

	if data, err := os.ReadFile(logPath); err == nil {
		t.Fatalf("VerifyPrimeOutput executed the PATH bd instead of the running binary:\n%s", data)
	}
}

// TestPrimeChecksDoNotExecPathBd covers both prime checks from GH#8130: with
// a decoy `bd` first on PATH, and with no PATH at all, neither check may
// execute the PATH binary, and their results must not depend on PATH
// availability. What the checks report under a test binary is pinned
// separately by TestPrimeChecksTestBinaryNA.
func TestPrimeChecksDoNotExecPathBd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd is a shell script")
	}

	run := func(t *testing.T, decoyOnPath bool) (prime DoctorCheck, docs DoctorCheck) {
		t.Helper()
		repoPath := t.TempDir()
		if err := os.WriteFile(filepath.Join(repoPath, "CLAUDE.md"), []byte("Run `bd prime` for workflow context.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		fakeDir := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "path-bd.log")
		t.Setenv("PRIME_CHECK_LOG", logPath)
		if err := os.WriteFile(filepath.Join(fakeDir, "bd"), []byte(fakeBdRecorderScript()), 0o755); err != nil {
			t.Fatal(err)
		}
		if decoyOnPath {
			t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		} else {
			t.Setenv("PATH", "")
		}
		t.Setenv("HOME", t.TempDir())
		t.Setenv("USERPROFILE", t.TempDir())
		t.Chdir(t.TempDir()) // caller's cwd differs from the diagnosed workspace

		prime = VerifyPrimeOutput(repoPath)
		docs = CheckDocumentationBdPrimeReference(repoPath)
		if data, err := os.ReadFile(logPath); err == nil {
			t.Fatalf("prime checks executed the PATH bd instead of the running binary:\n%s", data)
		}
		return prime, docs
	}

	outcomes := map[string][2]DoctorCheck{}
	for _, tc := range []struct {
		name        string
		decoyOnPath bool
	}{
		{"decoy bd first on PATH", true},
		{"no bd on PATH", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prime, docs := run(t, tc.decoyOnPath)
			outcomes[tc.name] = [2]DoctorCheck{prime, docs}
		})
	}

	decoy := outcomes["decoy bd first on PATH"]
	empty := outcomes["no bd on PATH"]
	if decoy[0] != empty[0] {
		t.Errorf("VerifyPrimeOutput result depends on PATH:\ndecoy: %+v\nempty: %+v", decoy[0], empty[0])
	}
	if decoy[1] != empty[1] {
		t.Errorf("CheckDocumentationBdPrimeReference result depends on PATH:\ndecoy: %+v\nempty: %+v", decoy[1], empty[1])
	}
}

// fakeBdRecorderScript returns a shell script that records how it was called
// (argv, physical cwd, effective BEADS_DIR) into the file named by
// PRIME_CHECK_LOG and prints the output VerifyPrimeOutput classifies as CLI
// mode. Routing the log through the environment lets every subtest get its
// own log file from one script.
func fakeBdRecorderScript() string {
	return "#!/bin/sh\n{ echo \"argv: $*\"; echo \"cwd: $(pwd -P)\"; echo \"beads_dir: ${BEADS_DIR:-}\"; } >> \"$PRIME_CHECK_LOG\"\necho 'bd ready'\n"
}

// stubPrimeCheckBinary points the prime checks' executable resolver at fn for
// the duration of the test and restores it afterwards. Tests using it must
// not run in parallel.
func stubPrimeCheckBinary(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := primeCheckBinary
	primeCheckBinary = fn
	t.Cleanup(func() { primeCheckBinary = orig })
}

// effectiveEnvValue returns the value the child would see for key: os/exec
// dedups environment entries in favor of later values, so the last matching
// entry wins.
func effectiveEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	value := ""
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

// TestPrimeCheckCommandPinsWorkspace pins the GH#8130 workspace contract of
// the prime-check command builder: whatever the caller's cwd and BEADS_DIR
// are, the child runs with cwd pinned to the absolute diagnosed repo path
// and BEADS_DIR pinned to doctor's resolution for that path.
func TestPrimeCheckCommandPinsWorkspace(t *testing.T) {
	// A sentinel standing in for the resolved running binary.
	sentinel := filepath.Join(t.TempDir(), "sentinel-bd")
	if err := os.WriteFile(sentinel, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubPrimeCheckBinary(t, func() (string, error) { return sentinel, nil })

	tests := []struct {
		name      string
		setBEADSDIR bool
		prep      func(t *testing.T) (repoPath, wantDir, wantBeadsDir string)
	}{
		{
			name: "absolute repo path",
			prep: func(t *testing.T) (string, string, string) {
				repo := t.TempDir()
				if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
					t.Fatal(err)
				}
				return repo, repo, utils.CanonicalizePath(filepath.Join(repo, ".beads"))
			},
		},
		{
			name: "relative repo path",
			prep: func(t *testing.T) (string, string, string) {
				parent := t.TempDir()
				repo := filepath.Join(parent, "ws")
				if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
					t.Fatal(err)
				}
				t.Chdir(parent)
				return "ws", repo, utils.CanonicalizePath(filepath.Join(repo, ".beads"))
			},
		},
		{
			name:        "conflicting BEADS_DIR in environment",
			setBEADSDIR: true,
			prep: func(t *testing.T) (string, string, string) {
				repo := t.TempDir()
				if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
					t.Fatal(err)
				}
				return repo, repo, utils.CanonicalizePath(filepath.Join(repo, ".beads"))
			},
		},
		{
			name: "redirected .beads directory",
			prep: func(t *testing.T) (string, string, string) {
				repo := t.TempDir()
				if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
					t.Fatal(err)
				}
				target := t.TempDir()
				// A bare metadata.json makes the redirect target a valid
				// beads directory for FollowRedirect.
				if err := os.WriteFile(filepath.Join(target, "metadata.json"), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, ".beads", "redirect"), []byte(target+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, repo, utils.CanonicalizePath(target)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, wantDir, wantBeadsDir := tt.prep(t)
			if tt.setBEADSDIR {
				t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "caller-workspace"))
			}

			for _, args := range [][]string{{"prime"}, {"prime", "--help"}} {
				cmd, err := primeCheckCommand(repoPath, args...)
				if err != nil {
					t.Fatalf("primeCheckCommand(%q, %v): %v", repoPath, args, err)
				}
				if cmd.Path != sentinel {
					t.Errorf("args %v: child Path = %q, want the resolved executable %q", args, cmd.Path, sentinel)
				}
				wantArgs := append([]string{sentinel}, args...)
				if !reflect.DeepEqual(cmd.Args, wantArgs) {
					t.Errorf("args %v: child Args = %q, want %q", args, cmd.Args, wantArgs)
				}
				if cmd.Dir != wantDir {
					t.Errorf("args %v: child Dir = %q, want the absolute diagnosed repo path %q", args, cmd.Dir, wantDir)
				}
				if got := effectiveEnvValue(t, cmd.Env, "BEADS_DIR"); got != wantBeadsDir {
					t.Errorf("args %v: child BEADS_DIR = %q, want doctor's resolution for the repo %q", args, got, wantBeadsDir)
				}
			}
		})
	}
}

// TestPrimeChecksRunResolvedBinaryInDiagnosedWorkspace runs both prime checks
// against a recording executable resolved by the stub (standing in for the
// running binary), with a decoy `bd` first on PATH and with an empty PATH:
// the checks must execute the resolved binary in the diagnosed workspace and
// report its result, never the decoy's.
func TestPrimeChecksRunResolvedBinaryInDiagnosedWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording executables are shell scripts")
	}

	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "CLAUDE.md"), []byte("Run `bd prime` for workflow context.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantBeadsDir := utils.CanonicalizePath(filepath.Join(repoPath, ".beads"))

	// The recording "running binary", kept off PATH.
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "bd")
	if err := os.WriteFile(exe, []byte(fakeBdRecorderScript()), 0o755); err != nil {
		t.Fatal(err)
	}
	stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })

	// A decoy that must never run.
	decoyLog := filepath.Join(t.TempDir(), "path-bd.log")
	decoyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoyDir, "bd"), []byte(fakeBdRecorderScript()), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		pathEnv string
	}{
		{"decoy bd first on PATH", decoyDir + string(os.PathListSeparator) + os.Getenv("PATH")},
		{"empty PATH", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			childLog := filepath.Join(t.TempDir(), "child-bd.log")
			t.Setenv("PRIME_CHECK_LOG", childLog)
			t.Setenv("PATH", tc.pathEnv)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("USERPROFILE", t.TempDir())
			t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "caller-workspace"))
			t.Chdir(t.TempDir()) // caller's cwd is a different workspace

			prime := VerifyPrimeOutput(repoPath)
			docs := CheckDocumentationBdPrimeReference(repoPath)

			if data, err := os.ReadFile(decoyLog); err == nil {
				t.Fatalf("prime checks executed the PATH bd instead of the running binary:\n%s", data)
			}

			data, err := os.ReadFile(childLog)
			if err != nil {
				t.Fatalf("the resolved binary was not executed: %v", err)
			}
			wantCwd := utils.CanonicalizePath(repoPath)
			for _, wantArgv := range []string{"prime", "prime --help"} {
				if !strings.Contains(string(data), "argv: "+wantArgv+"\n") {
					t.Errorf("resolved binary was not called with %q; log:\n%s", wantArgv, data)
				}
			}
			if cwds := grepLines(string(data), "cwd: "); len(cwds) != 2 || cwds[0] != wantCwd || cwds[1] != wantCwd {
				t.Errorf("child cwd = %q, want the diagnosed workspace %q for both calls", cwds, wantCwd)
			}
			if dirs := grepLines(string(data), "beads_dir: "); len(dirs) != 2 || dirs[0] != wantBeadsDir || dirs[1] != wantBeadsDir {
				t.Errorf("child BEADS_DIR = %q, want doctor's resolution %q for both calls", dirs, wantBeadsDir)
			}

			if prime.Status != "ok" || prime.Message != "CLI mode detected" {
				t.Errorf("VerifyPrimeOutput = %q/%q, want ok/\"CLI mode detected\"", prime.Status, prime.Message)
			}
			if docs.Status != "ok" || !strings.Contains(docs.Detail, "CLAUDE.md") {
				t.Errorf("CheckDocumentationBdPrimeReference = %q/%q, want ok with the referencing file in Detail", docs.Status, docs.Detail)
			}
		})
	}

	// With no bd on PATH at all, the separate availability check still warns;
	// the prime checks above must not depend on it.
	t.Run("availability check warns on empty PATH", func(t *testing.T) {
		t.Setenv("PATH", "")
		if check := CheckBdInPath(); check.Status != "warning" {
			t.Errorf("CheckBdInPath = %q/%q, want warning when no bd is on PATH", check.Status, check.Message)
		}
	})
}

// grepLines returns the remainder of every line in log starting with prefix.
func grepLines(log, prefix string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, prefix) {
			out = append(out, strings.TrimPrefix(line, prefix))
		}
	}
	return out
}

// TestPrimeChecksTestBinaryNA pins the guard that keeps the prime checks from
// re-executing a Go or Bazel test binary: when the executable resolver
// reports a test binary, both checks start no subprocess and report ok with
// an explicit N/A message, and the documentation check keeps naming the
// referencing files.
func TestPrimeChecksTestBinaryNA(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd is a shell script")
	}

	// The exported wrapper must preserve Bazel test detection.
	if _, err := fix.BdBinary(); !errors.Is(err, fix.ErrTestBinary) {
		t.Errorf("fix.BdBinary() under a test binary = %v, want ErrTestBinary", err)
	}

	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "CLAUDE.md"), []byte("Run `bd prime` for workflow context.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "path-bd.log")
	t.Setenv("PRIME_CHECK_LOG", logPath)
	if err := os.WriteFile(filepath.Join(fakeDir, "bd"), []byte(fakeBdRecorderScript()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Chdir(t.TempDir()) // caller's cwd differs from the diagnosed workspace

	prime := VerifyPrimeOutput(repoPath)
	if prime.Status != "ok" || prime.Message != "N/A (running as a test binary)" {
		t.Errorf("VerifyPrimeOutput under a test binary = %q/%q, want ok/%q",
			prime.Status, prime.Message, "N/A (running as a test binary)")
	}

	docs := CheckDocumentationBdPrimeReference(repoPath)
	if docs.Status != "ok" || docs.Message != "N/A (running as a test binary)" {
		t.Errorf("CheckDocumentationBdPrimeReference under a test binary = %q/%q, want ok/%q",
			docs.Status, docs.Message, "N/A (running as a test binary)")
	}
	if !strings.Contains(docs.Detail, "CLAUDE.md") {
		t.Errorf("documentation check lost its referencing filenames: Detail = %q", docs.Detail)
	}

	if data, err := os.ReadFile(logPath); err == nil {
		t.Fatalf("prime checks started a subprocess under a test binary:\n%s", data)
	}
}

// TestPrimeChecksReportResolvedBinaryFailure pins how the prime checks report
// a resolver failure or a failing child: the command check stays an error and
// the documentation check stays a warning, both carry the underlying error
// and the resolved executable when one was resolved, the documentation check
// keeps naming the referencing files, and neither recommends installing,
// upgrading or adding bd to PATH — the failure is about the binary being
// diagnosed, not about PATH.
func TestPrimeChecksReportResolvedBinaryFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("failing child is a shell script")
	}

	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "CLAUDE.md"), []byte("Run `bd prime` for workflow context.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	assertNoInstallAdvice := func(t *testing.T, check DoctorCheck) {
		t.Helper()
		fix := strings.ToLower(check.Fix)
		for _, bad := range []string{"path", "install", "upgrade", "brew"} {
			if strings.Contains(fix, bad) {
				t.Errorf("%s recommends %q (%q); the failure is about the diagnosed binary, not PATH or installation",
					check.Name, bad, check.Fix)
			}
		}
	}

	t.Run("resolver failure", func(t *testing.T) {
		stubPrimeCheckBinary(t, func() (string, error) { return "", errors.New("bd binary not found: boom") })

		prime := VerifyPrimeOutput(repoPath)
		if prime.Status != "error" {
			t.Errorf("VerifyPrimeOutput resolver failure status = %q, want error", prime.Status)
		}
		if !strings.Contains(prime.Message+" "+prime.Detail, "boom") {
			t.Errorf("VerifyPrimeOutput resolver failure lost the underlying error: %q / %q", prime.Message, prime.Detail)
		}
		assertNoInstallAdvice(t, prime)

		docs := CheckDocumentationBdPrimeReference(repoPath)
		if docs.Status != "warning" {
			t.Errorf("documentation check resolver failure status = %q, want warning", docs.Status)
		}
		if !strings.Contains(docs.Message+" "+docs.Detail, "boom") {
			t.Errorf("documentation check resolver failure lost the underlying error: %q / %q", docs.Message, docs.Detail)
		}
		if !strings.Contains(docs.Detail, "CLAUDE.md") {
			t.Errorf("documentation check lost its referencing filenames: Detail = %q", docs.Detail)
		}
		assertNoInstallAdvice(t, docs)
	})

	t.Run("child exits nonzero", func(t *testing.T) {
		exeDir := t.TempDir()
		exe := filepath.Join(exeDir, "bd")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'kaboom' >&2\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })

		prime := VerifyPrimeOutput(repoPath)
		if prime.Status != "error" {
			t.Errorf("VerifyPrimeOutput exit failure status = %q, want error", prime.Status)
		}
		if !strings.Contains(prime.Message+" "+prime.Detail, "exit status 3") {
			t.Errorf("VerifyPrimeOutput exit failure lost the exit error: %q / %q", prime.Message, prime.Detail)
		}
		if !strings.Contains(prime.Message+" "+prime.Detail+" "+prime.Fix, exe) {
			t.Errorf("VerifyPrimeOutput exit failure does not name the resolved executable %q: %+v", exe, prime)
		}
		assertNoInstallAdvice(t, prime)

		docs := CheckDocumentationBdPrimeReference(repoPath)
		if docs.Status != "warning" {
			t.Errorf("documentation check exit failure status = %q, want warning", docs.Status)
		}
		if !strings.Contains(docs.Message+" "+docs.Detail, "exit status 3") {
			t.Errorf("documentation check exit failure lost the exit error: %q / %q", docs.Message, docs.Detail)
		}
		if !strings.Contains(docs.Detail, "CLAUDE.md") {
			t.Errorf("documentation check lost its referencing filenames: Detail = %q", docs.Detail)
		}
		assertNoInstallAdvice(t, docs)
	})

	t.Run("child produces no output", func(t *testing.T) {
		exeDir := t.TempDir()
		exe := filepath.Join(exeDir, "bd")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })

		prime := VerifyPrimeOutput(repoPath)
		if prime.Status != "error" || prime.Message != "No output produced" {
			t.Errorf("VerifyPrimeOutput empty output = %q/%q, want error/%q", prime.Status, prime.Message, "No output produced")
		}
	})
}
