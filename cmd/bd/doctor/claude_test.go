package doctor

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
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
// run appends its argv and physical cwd to the file named by the environment
// variable logEnv, then prints CLI-mode prime output. It uses only an absolute
// shebang and shell builtins, so it runs with an empty PATH.
func writeRecordingBd(t *testing.T, dir, name, logEnv string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"{ echo \"argv=$*\"; echo \"cwd=$(pwd -P)\"; echo ---; } >> \"$" + logEnv + "\"\n" +
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

	// The exported wrapper must preserve Go and Bazel test detection.
	if _, err := fix.BdBinary(); !errors.Is(err, fix.ErrTestBinary) {
		t.Errorf("fix.BdBinary() under a test binary = %v, want ErrTestBinary", err)
	}

	prime := VerifyPrimeOutput(repo)
	doc := CheckDocumentationBdPrimeReference(repo)

	if records := readPrimeRecords(t, decoyLog); len(records) != 0 {
		t.Fatalf("prime checks executed the PATH bd %d time(s): %v", len(records), records)
	}
	const na = "N/A (running as a test binary)"
	if prime.Status != "ok" || prime.Message != na {
		t.Errorf("VerifyPrimeOutput = %+v, want ok %q", prime, na)
	}
	if doc.Status != "ok" || doc.Message != na || doc.Detail != "Files: CLAUDE.md" {
		t.Errorf("CheckDocumentationBdPrimeReference = %+v, want ok %q with Files: CLAUDE.md", doc, na)
	}
}

// TestPrimeChecksRunResolvedBinaryInDiagnosedWorkspace runs both prime checks
// against a real recording child selected by the resolver, with a decoy bd on
// PATH and with an empty PATH: the resolved binary runs in the diagnosed
// workspace and selects it with -C, the decoy never runs, and the result does
// not depend on PATH or the caller's cwd and BEADS_DIR.
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
			if got, want := records[0]["argv"], "-C "+repo+" prime --no-memories"; got != want {
				t.Errorf("VerifyPrimeOutput argv = %q, want %q", got, want)
			}
			if got, want := records[1]["argv"], "-C "+repo+" prime --no-memories --help"; got != want {
				t.Errorf("CheckDocumentationBdPrimeReference argv = %q, want %q", got, want)
			}
			for _, rec := range records {
				if rec["cwd"] != resolve(t, repo) {
					t.Errorf("%q ran in %q, want the diagnosed repo %q", rec["argv"], rec["cwd"], resolve(t, repo))
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

// TestPrimeCheckCommandPinsWorkspace pins GH#8130's workspace half: the child
// is `bd -C <abs repo> prime --no-memories`, run in the absolute diagnosed repo
// path, whatever the caller's cwd and BEADS_DIR are. -C overrides an inherited
// BEADS_DIR and resolves through beads.FindBeadsDirFrom, so each case also pins
// where that lands. A linked worktree's .beads that holds only files git
// checked out must resolve to the main checkout's database: pinning BEADS_DIR
// to it would make the child create an empty database there.
func TestPrimeCheckCommandPinsWorkspace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "bd-sentinel")
	stubPrimeCheckBinary(t, func() (string, error) { return exe, nil })
	callerBeadsDir := writeBeadsWorkspace(t, t.TempDir())

	type fixture struct {
		repoArg   string // the repoPath passed to the check
		repoDir   string // the directory it names
		beadsDir  string // the .beads bd resolves for it
		beadsEnv  string // the caller's BEADS_DIR ("" = unset)
		chdirInto string // the caller's cwd
	}
	// The main checkout's .beads holds the database, which is gitignored and
	// so never checked out into the worktree.
	worktreeOfMainWithDatabase := func(t *testing.T) (string, string) {
		mainRepo, worktree := setupWorktreeRepo(t)
		mainBeads := writeBeadsWorkspace(t, mainRepo)
		mustMkdir(t, filepath.Join(mainBeads, "dolt"))
		return mainBeads, worktree
	}
	cases := map[string]func(t *testing.T) fixture{
		"absolute path, BEADS_DIR unset": func(t *testing.T) fixture {
			repo := t.TempDir()
			return fixture{repoArg: repo, repoDir: repo, beadsDir: writeBeadsWorkspace(t, repo), chdirInto: t.TempDir()}
		},
		"relative path, BEADS_DIR elsewhere": func(t *testing.T) fixture {
			parent := t.TempDir()
			repo := filepath.Join(parent, "ws")
			return fixture{repoArg: "ws", repoDir: repo, beadsDir: writeBeadsWorkspace(t, repo), beadsEnv: callerBeadsDir, chdirInto: parent}
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
		"worktree without .beads uses the main checkout's": func(t *testing.T) fixture {
			mainBeads, worktree := worktreeOfMainWithDatabase(t)
			return fixture{repoArg: worktree, repoDir: worktree, beadsDir: mainBeads, beadsEnv: callerBeadsDir, chdirInto: t.TempDir()}
		},
		"worktree with tracked .beads files uses the main checkout's database": func(t *testing.T) fixture {
			mainBeads, worktree := worktreeOfMainWithDatabase(t)
			writeBeadsWorkspace(t, worktree)
			return fixture{repoArg: worktree, repoDir: worktree, beadsDir: mainBeads, beadsEnv: callerBeadsDir, chdirInto: t.TempDir()}
		},
		"worktree tracking only .beads/formulas uses the main checkout's": func(t *testing.T) fixture {
			mainBeads, worktree := worktreeOfMainWithDatabase(t)
			writeFile(t, filepath.Join(worktree, ".beads"), filepath.Join("formulas", "release.formula.json"), "{}\n")
			return fixture{repoArg: worktree, repoDir: worktree, beadsDir: mainBeads, chdirInto: t.TempDir()}
		},
	}
	extraArgs := [][]string{nil, {"--help"}}

	for name, setup := range cases {
		for _, extra := range extraArgs {
			wantTail := append([]string{"prime", "--no-memories"}, extra...)
			t.Run(name+"/"+strings.Join(append([]string{"prime"}, extra...), " "), func(t *testing.T) {
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

				cmd, err := primeCheckCommand(fx.repoArg, extra...)
				if err != nil {
					t.Fatalf("primeCheckCommand: %v", err)
				}
				if cmd.Path != exe {
					t.Errorf("Path = %q, want %q", cmd.Path, exe)
				}
				if len(cmd.Args) < 3 || cmd.Args[1] != "-C" {
					t.Fatalf("Args = %q, want the workspace selected with -C", cmd.Args)
				}
				if got, want := strings.Join(cmd.Args[3:], " "), strings.Join(wantTail, " "); got != want {
					t.Errorf("Args after -C <dir> = %q, want %q", got, want)
				}
				for label, dir := range map[string]string{"-C": cmd.Args[2], "Dir": cmd.Dir} {
					if !filepath.IsAbs(dir) {
						t.Errorf("%s = %q, want an absolute path", label, dir)
					} else if resolve(t, dir) != resolve(t, fx.repoDir) {
						t.Errorf("%s = %q, want the diagnosed repo %q", label, dir, fx.repoDir)
					}
				}
				if got := beads.FindBeadsDirFrom(cmd.Args[2]); got == "" || resolve(t, got) != resolve(t, fx.beadsDir) {
					t.Errorf("bd -C %s resolves %q, want %q", cmd.Args[2], got, fx.beadsDir)
				}
			})
		}
	}
}

// writeBeadsWorkspace makes dir/.beads a beads workspace, with the config.yaml
// a checkout carries, and returns the .beads path.
func writeBeadsWorkspace(t *testing.T, dir string) string {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	mustMkdir(t, beadsDir)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("# beads workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return beadsDir
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
		// Detail carries what the child printed, and the advice reruns the
		// exact child, so a caller's BEADS_DIR cannot pick another workspace.
		wantRerun := exe + " -C " + repo + " prime --no-memories"
		for _, check := range []DoctorCheck{VerifyPrimeOutput(repo), CheckDocumentationBdPrimeReference(repo)} {
			if !strings.Contains(check.Detail, "prime exploded") {
				t.Errorf("%s: Detail = %q, want the child's output %q", check.Name, check.Detail, "prime exploded")
			}
			if !strings.Contains(check.Fix, wantRerun) {
				t.Errorf("%s: Fix = %q, want the exact child %q", check.Name, check.Fix, wantRerun)
			}
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

// TestPrimeCheckOutputTail pins the child output a failed prime check reports:
// short output whole and trimmed, long output cut to its last detailMaxBytes at
// a rune boundary and marked as cut.
func TestPrimeCheckOutputTail(t *testing.T) {
	if got := primeCheckOutputTail(nil); got != "" {
		t.Errorf("no output = %q, want empty", got)
	}
	if got := primeCheckOutputTail([]byte("\n  Error: boom\n")); got != "Error: boom" {
		t.Errorf("short output = %q, want %q", got, "Error: boom")
	}
	// Two-byte runes and a 21-byte last line put the cut inside a rune.
	const lastLine = "Error: the last line."
	got := primeCheckOutputTail([]byte(strings.Repeat("é", detailMaxBytes) + lastLine))
	if !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, lastLine) {
		t.Errorf("long output = %q, want \"...\" and the last line", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("long output = %q, which splits a rune", got)
	}
	if kept := len(strings.TrimPrefix(got, "...")); kept > detailMaxBytes {
		t.Errorf("long output keeps %d bytes, want at most %d", kept, detailMaxBytes)
	}
}

// TestPrimeChecksRealBdOpenNoStore runs both prime checks with the real bd as
// the child, because the shell stubs above never open a store and so cannot
// catch a child that creates or migrates one. The diagnosed path is a linked
// worktree whose .beads holds only files git checked out, and the caller's
// BEADS_DIR names another workspace. The child must prime the workspace bd
// resolves from the worktree and change no .beads. A child pinned to the
// worktree's own .beads would create an empty embedded database there through
// its memories read, which every later bd command in the worktree would then
// use, and in a worktree that tracks only formulas it would prime nothing.
func TestPrimeChecksRealBdOpenNoStore(t *testing.T) {
	bd, err := bazeltest.PrebuiltBD()
	if err != nil {
		t.Fatal(err)
	}
	if bd == "" {
		t.Skip("needs a prebuilt bd: Bazel provides one, or set " + bazeltest.BDBinaryEnv)
	}
	stubPrimeCheckBinary(t, func() (string, error) { return bd, nil })
	isolateBdChildEnv(t)

	for _, tc := range []struct {
		name       string
		checkedOut string // the file git checked out into the worktree's .beads
	}{
		{name: "tracked config", checkedOut: "config.yaml"},
		{name: "tracked formulas only", checkedOut: filepath.Join("formulas", "release.formula.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainRepo, worktree := setupWorktreeRepo(t)
			mainBeads := writeBeadsWorkspace(t, mainRepo)
			mustMkdir(t, filepath.Join(mainBeads, "embeddeddolt"))
			worktreeBeads := filepath.Join(worktree, ".beads")
			mustMkdir(t, worktreeBeads)
			writeFile(t, worktreeBeads, tc.checkedOut, "# checked out\n")
			writeFile(t, worktree, "CLAUDE.md", "Run `bd prime` at session start.\n")

			// The caller's workspace. A child that primed it would print this
			// PRIME.md, which has no "bd ready".
			callerBeads := writeBeadsWorkspace(t, t.TempDir())
			writeFile(t, callerBeads, "PRIME.md", "# Caller workspace\n")
			t.Setenv("BEADS_DIR", callerBeads)

			trees := []string{mainBeads, worktreeBeads, callerBeads}
			before := listTrees(t, trees)

			if prime := VerifyPrimeOutput(worktree); prime.Status != "ok" || prime.Message != "CLI mode detected" {
				t.Errorf("VerifyPrimeOutput = %+v, want ok CLI mode detected", prime)
			}
			if doc := CheckDocumentationBdPrimeReference(worktree); doc.Status != "ok" {
				t.Errorf("CheckDocumentationBdPrimeReference = %+v, want ok", doc)
			}
			if after := listTrees(t, trees); !slices.Equal(after, before) {
				t.Errorf("the prime checks changed a .beads:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// isolateBdChildEnv gives a real bd child a private home and none of the
// caller's bd settings: an agent shell, or this suite's TestMain, exports
// BEADS_* and BD_* selectors and server settings that would aim the child at
// another store. As in cmd/bd's bdEnv, metrics, server auto-start and the
// daemon are off.
func isolateBdChildEnv(t *testing.T) {
	t.Helper()
	isolatePrimeCheckHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	t.Setenv("APPDATA", filepath.Join(os.Getenv("HOME"), "AppData"))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BD_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_NO_DAEMON", "1")
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("BD_DISABLE_EVENT_FLUSH", "1")
}

// listTrees returns every path under dirs, so a test can tell whether a child
// process created or removed anything there.
func listTrees(t *testing.T, dirs []string) []string {
	t.Helper()
	var paths []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			paths = append(paths, path)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return paths
}
