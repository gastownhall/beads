package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

func newClaudeTestEnv(t *testing.T) (claudeEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := claudeEnv{
		stdout:     stdout,
		stderr:     stderr,
		homeDir:    homeDir,
		projectDir: projectDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}
	return env, stdout, stderr
}

func stubClaudeEnvProvider(t *testing.T, env claudeEnv, err error) {
	t.Helper()
	orig := claudeEnvProvider
	claudeEnvProvider = func() (claudeEnv, error) {
		if err != nil {
			return claudeEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { claudeEnvProvider = orig })
}

func writeSettings(t *testing.T, path string, settings map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := atomicWriteFile(path, data); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestAddHookCommand(t *testing.T) {
	tests := []struct {
		name          string
		existingHooks map[string]interface{}
		event         string
		command       string
		wantAdded     bool
	}{
		{
			name:          "add hook to empty hooks",
			existingHooks: make(map[string]interface{}),
			event:         "SessionStart",
			command:       "bd prime",
			wantAdded:     true,
		},
		{
			name:          "add stealth hook to empty hooks",
			existingHooks: make(map[string]interface{}),
			event:         "SessionStart",
			command:       "bd prime --stealth",
			wantAdded:     true,
		},
		{
			name: "hook already exists",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "bd prime",
							},
						},
					},
				},
			},
			event:     "SessionStart",
			command:   "bd prime",
			wantAdded: false,
		},
		{
			name: "stealth hook already exists",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "bd prime --stealth",
							},
						},
					},
				},
			},
			event:     "SessionStart",
			command:   "bd prime --stealth",
			wantAdded: false,
		},
		{
			name: "add second hook alongside existing",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "other command",
							},
						},
					},
				},
			},
			event:     "SessionStart",
			command:   "bd prime",
			wantAdded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addHookCommand(tt.existingHooks, tt.event, tt.command)
			if got != tt.wantAdded {
				t.Errorf("addHookCommand() = %v, want %v", got, tt.wantAdded)
			}

			// Verify hook exists in structure
			eventHooks, ok := tt.existingHooks[tt.event].([]interface{})
			if !ok {
				t.Fatal("Event hooks not found")
			}

			found := false
			for _, hook := range eventHooks {
				hookMap := hook.(map[string]interface{})
				commands := hookMap["hooks"].([]interface{})
				for _, cmd := range commands {
					cmdMap := cmd.(map[string]interface{})
					if cmdMap["command"] == tt.command {
						found = true
						break
					}
				}
			}

			if !found {
				t.Errorf("Hook command %q not found in event %q", tt.command, tt.event)
			}
		})
	}
}

func TestRemoveHookCommand(t *testing.T) {
	tests := []struct {
		name          string
		existingHooks map[string]interface{}
		event         string
		command       string
		wantRemaining int
	}{
		{
			name: "remove only hook",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "bd prime",
							},
						},
					},
				},
			},
			event:         "SessionStart",
			command:       "bd prime",
			wantRemaining: 0,
		},
		{
			name: "remove stealth hook",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "bd prime --stealth",
							},
						},
					},
				},
			},
			event:         "SessionStart",
			command:       "bd prime --stealth",
			wantRemaining: 0,
		},
		{
			name: "remove one of multiple hooks",
			existingHooks: map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "other command",
							},
						},
					},
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{
								"type":    "command",
								"command": "bd prime",
							},
						},
					},
				},
			},
			event:         "SessionStart",
			command:       "bd prime",
			wantRemaining: 1,
		},
	}

	// Separate test: sibling commands within the same hook entry must be
	// preserved when only the matching command is removed. The bug that
	// prompted this test dropped the entire hook entry on first match,
	// silently deleting any sibling commands in the same hooks array.
	t.Run("preserves sibling commands in same hook entry", func(t *testing.T) {
		hooks := map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
						map[string]interface{}{"type": "command", "command": "other-tool"},
					},
				},
			},
		}
		removeHookCommand(hooks, "SessionStart", "bd prime")

		entries, ok := hooks["SessionStart"].([]interface{})
		if !ok || len(entries) != 1 {
			t.Fatalf("expected 1 hook entry to remain, got %v", hooks["SessionStart"])
		}
		entryMap := entries[0].(map[string]interface{})
		cmds := entryMap["hooks"].([]interface{})
		if len(cmds) != 1 {
			t.Fatalf("expected 1 sibling command to remain, got %d", len(cmds))
		}
		if cmds[0].(map[string]interface{})["command"] != "other-tool" {
			t.Errorf("sibling command was lost; commands: %v", cmds)
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removeHookCommand(tt.existingHooks, tt.event, tt.command)

			eventHooks, ok := tt.existingHooks[tt.event].([]interface{})
			if !ok && tt.wantRemaining > 0 {
				t.Fatal("Event hooks not found")
			}

			if len(eventHooks) != tt.wantRemaining {
				t.Errorf("Expected %d remaining hooks, got %d", tt.wantRemaining, len(eventHooks))
			}

			// Verify target hook is actually gone
			for _, hook := range eventHooks {
				hookMap := hook.(map[string]interface{})
				commands := hookMap["hooks"].([]interface{})
				for _, cmd := range commands {
					cmdMap := cmd.(map[string]interface{})
					if cmdMap["command"] == tt.command {
						t.Errorf("Hook command %q still present after removal", tt.command)
					}
				}
			}
		})
	}
}

// TestRemoveHookCommandNoNull verifies that removing all hooks deletes the key
// instead of setting it to null. GH#955: null values in hooks cause Claude Code to fail.
func TestRemoveHookCommandNoNull(t *testing.T) {
	hooks := map[string]interface{}{
		"SessionStart": []interface{}{
			map[string]interface{}{
				"matcher": "",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "bd prime",
					},
				},
			},
		},
	}

	removeHookCommand(hooks, "SessionStart", "bd prime")

	// Key should be deleted, not set to null or empty array
	if _, exists := hooks["SessionStart"]; exists {
		t.Error("Expected SessionStart key to be deleted after removing all hooks")
	}

	// Verify JSON serialization doesn't produce null
	data, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("JSON contains null: %s", data)
	}
}

// TestInstallClaudeCleanupNullHooks verifies that install cleans up existing null values.
// GH#955: null values left by previous buggy removal cause Claude Code to fail.
func TestInstallClaudeCleanupNullHooks(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)

	// Create settings file with null hooks (simulating the bug)
	// Use project settings path (default install target)
	settingsPath := projectSettingsPath(env.projectDir)
	writeSettings(t, settingsPath, map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": nil,
			"PreCompact":   nil,
		},
	})

	// Install should clean up null values and add proper hooks (global=false → project)
	err := installClaude(env, false, false)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Verify hooks were properly added
	if !strings.Contains(stdout.String(), "Registered SessionStart hook") {
		t.Error("Expected SessionStart hook to be registered")
	}

	// Read back the file and verify no null values
	data, err := env.readFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("Settings file still contains null: %s", data)
	}

	// Verify it parses as valid Claude settings
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("hooks section missing")
	}
	for _, event := range []string{"SessionStart"} {
		eventHooks, ok := hooks[event].([]interface{})
		if !ok {
			t.Errorf("%s should be an array, not nil or missing", event)
		}
		if len(eventHooks) == 0 {
			t.Errorf("%s should have hooks", event)
		}
	}
}

func TestInstallClaudeUsesPrimeForClaudeHooks(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, err := env.readFile(projectSettingsPath(env.projectDir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	settingsJSON := string(data)

	for _, want := range []string{
		`"command": "bd prime --hook-json"`,
		`"SessionStart"`,
	} {
		if !strings.Contains(settingsJSON, want) {
			t.Fatalf("settings missing %q:\n%s", want, settingsJSON)
		}
	}

	for _, stale := range []string{"bd sync", "bd dolt push"} {
		if strings.Contains(settingsJSON, stale) {
			t.Fatalf("settings contain stale Claude hook command %q:\n%s", stale, settingsJSON)
		}
	}
}

func TestClaudeSettingsUsesRemovedSyncCommand(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", "{}", false},
		{"bd prime only", `{"hooks":{"PreCompact":[{"matcher":"","hooks":[{"type":"command","command":"bd prime"}]}]}}`, false},
		{"bd sync hook", `{"hooks":{"PreCompact":[{"matcher":"","hooks":[{"type":"command","command":"bd sync"}]}]}}`, true},
		{"bd sync with suffix", `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"bd sync --flush-only"}]}]}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeSettingsUsesRemovedSyncCommand([]byte(tt.raw)); got != tt.want {
				t.Fatalf("claudeSettingsUsesRemovedSyncCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasBeadsHooks(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name         string
		settingsData map[string]interface{}
		want         bool
	}{
		{
			name: "has bd prime hook",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has bd prime --stealth hook",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime --stealth",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has bd prime in PreCompact",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has bd prime --stealth in PreCompact",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"PreCompact": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime --stealth",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has bd prime --hook-json hook (current format)",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime --hook-json",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has bd prime --stealth --hook-json hook (current format)",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"PreCompact": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "bd prime --stealth --hook-json",
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name:         "no hooks",
			settingsData: map[string]interface{}{},
			want:         false,
		},
		{
			name: "has other hooks but not bd prime",
			settingsData: map[string]interface{}{
				"hooks": map[string]interface{}{
					"SessionStart": []interface{}{
						map[string]interface{}{
							"matcher": "",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "other command",
								},
							},
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settingsPath := filepath.Join(tmpDir, "settings.json")

			data, err := json.Marshal(tt.settingsData)
			if err != nil {
				t.Fatalf("Failed to marshal test data: %v", err)
			}

			if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			got := hasBeadsHooks(settingsPath)
			if got != tt.want {
				t.Errorf("hasBeadsHooks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdempotency(t *testing.T) {
	// Test that running addHookCommand twice doesn't duplicate hooks
	hooks := make(map[string]interface{})

	// First add
	added1 := addHookCommand(hooks, "SessionStart", "bd prime")
	if !added1 {
		t.Error("First call should have added the hook")
	}

	// Second add (should detect existing)
	added2 := addHookCommand(hooks, "SessionStart", "bd prime")
	if added2 {
		t.Error("Second call should have detected existing hook")
	}

	// Verify only one hook exists
	eventHooks := hooks["SessionStart"].([]interface{})
	if len(eventHooks) != 1 {
		t.Errorf("Expected 1 hook, got %d", len(eventHooks))
	}
}

// Test that running addHookCommand twice with stealth doesn't duplicate hooks
func TestIdempotencyWithStealth(t *testing.T) {
	hooks := make(map[string]any)

	if !addHookCommand(hooks, "SessionStart", "bd prime --stealth") {
		t.Error("First call should have added the stealth hook")
	}

	// Second add (should detect existing)
	if addHookCommand(hooks, "SessionStart", "bd prime --stealth") {
		t.Error("Second call should have detected existing stealth hook")
	}

	// Verify only one hook exists
	eventHooks := hooks["SessionStart"].([]any)
	if len(eventHooks) != 1 {
		t.Errorf("Expected 1 hook, got %d", len(eventHooks))
	}

	// and that it's the correct one
	hookMap := eventHooks[0].(map[string]any)
	commands := hookMap["hooks"].([]any)
	cmdMap := commands[0].(map[string]any)
	if cmdMap["command"] != "bd prime --stealth" {
		t.Errorf("Expected 'bd prime --stealth', got %v", cmdMap["command"])
	}
}

func TestInstallClaudeProject(t *testing.T) {
	env, stdout, stderr := newClaudeTestEnv(t)
	// global=false means project-local (the new default)
	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	data, err := os.ReadFile(projectSettingsPath(env.projectDir))
	if err != nil {
		t.Fatalf("read project settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if !hasBeadsHooks(projectSettingsPath(env.projectDir)) {
		t.Fatal("project hooks not detected")
	}
	instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read %s: %v", claudeInstructionsFile, err)
	}
	if !strings.Contains(string(instructions), "profile:minimal") {
		t.Fatalf("expected minimal profile in %s", claudeInstructionsFile)
	}
	if !strings.Contains(stdout.String(), "project") {
		t.Error("expected project installation message")
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output: %s", stderr.String())
	}
}

func TestInstallClaudeGlobalStealth(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)
	// global=true, stealth=true
	if err := installClaude(env, true, true); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	data, err := os.ReadFile(globalSettingsPath(env.homeDir))
	if err != nil {
		t.Fatalf("read global settings: %v", err)
	}
	if !strings.Contains(string(data), "bd prime --stealth") {
		t.Error("expected stealth command in settings")
	}
	instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if _, err := os.Stat(instructionsPath); err != nil {
		t.Fatalf("expected %s to be created: %v", claudeInstructionsFile, err)
	}
	if !strings.Contains(stdout.String(), "globally") {
		t.Error("expected global installation message")
	}
}

func TestInstallClaudeErrors(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		env, _, stderr := newClaudeTestEnv(t)
		path := projectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		// global=false → project-local, should hit the invalid json
		if err := installClaude(env, false, false); err == nil {
			t.Fatal("expected parse error")
		}
		if !strings.Contains(stderr.String(), "failed to parse") {
			t.Error("expected parse error output")
		}
	})

	t.Run("ensure dir error", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		env.ensureDir = func(string, os.FileMode) error { return errors.New("boom") }
		// global=false → project-local
		if err := installClaude(env, false, false); err == nil {
			t.Fatal("expected ensureDir error")
		}
	})
}

func TestCheckClaudeScenarios(t *testing.T) {
	stubDetectRenderOpts(t)

	t.Run("global hooks", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": "bd prime"},
						},
					},
				},
			},
		})
		if err := os.WriteFile(filepath.Join(env.projectDir, claudeInstructionsFile), []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
			t.Fatalf("write %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v", err)
		}
		if !strings.Contains(stdout.String(), "Global hooks installed") {
			t.Error("expected global hooks message")
		}
	})

	t.Run("project hooks", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreCompact": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": "bd prime"},
						},
					},
				},
			},
		})
		if err := os.WriteFile(filepath.Join(env.projectDir, claudeInstructionsFile), []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
			t.Fatalf("write %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v", err)
		}
		if !strings.Contains(stdout.String(), "Project hooks installed") {
			t.Error("expected project hooks message")
		}
	})

	t.Run("missing hooks", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("expected errClaudeHooksMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude") {
			t.Error("expected guidance message")
		}
	})

	t.Run("missing instructions", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": "bd prime"},
						},
					},
				},
			},
		})
		if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), claudeInstructionsFile+" not found") {
			t.Fatalf("expected missing %s message, got: %s", claudeInstructionsFile, stdout.String())
		}
	})
}

func TestRemoveClaudeScenarios(t *testing.T) {
	t.Run("remove global hooks", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		path := globalSettingsPath(env.homeDir)
		writeSettings(t, path, map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": "bd prime"},
							map[string]interface{}{"type": "command", "command": "other"},
						},
					},
				},
			},
		})
		instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
		if err := os.WriteFile(instructionsPath, []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
			t.Fatalf("seed %s: %v", claudeInstructionsFile, err)
		}
		// global=true → remove from global settings
		if err := removeClaude(env, true); err != nil {
			t.Fatalf("removeClaude: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if strings.Contains(string(data), "bd prime") {
			t.Error("expected bd prime hooks removed")
		}
		instructions, err := os.ReadFile(instructionsPath)
		if err != nil {
			t.Fatalf("read %s: %v", claudeInstructionsFile, err)
		}
		if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") {
			t.Fatalf("expected beads section removed from %s", claudeInstructionsFile)
		}
		if !strings.Contains(stdout.String(), "hooks removed") {
			t.Error("expected success message")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		// global=false → project-local (default)
		if err := removeClaude(env, false); err != nil {
			t.Fatalf("removeClaude: %v", err)
		}
		if !strings.Contains(stdout.String(), "No settings file found") {
			t.Error("expected missing file message")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		env, _, stderr := newClaudeTestEnv(t)
		path := projectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		// global=false → project-local
		if err := removeClaude(env, false); err == nil {
			t.Fatal("expected parse error")
		}
		if !strings.Contains(stderr.String(), "failed to parse") {
			t.Error("expected parse error output")
		}
	})
}

func TestClaudeWrappersReturnError(t *testing.T) {
	t.Run("install provider error", func(t *testing.T) {
		stubClaudeEnvProvider(t, claudeEnv{}, errors.New("boom"))
		if err := InstallClaude(false, false); err == nil {
			t.Fatal("InstallClaude should return error on provider error")
		}
	})

	t.Run("install internal error", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		env.ensureDir = func(string, os.FileMode) error { return errors.New("boom") }
		stubClaudeEnvProvider(t, env, nil)
		if err := InstallClaude(false, false); err == nil {
			t.Fatal("InstallClaude should return error when installClaude fails")
		}
	})

	t.Run("check missing hooks", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		stubClaudeEnvProvider(t, env, nil)
		if err := CheckClaude(); err == nil {
			t.Fatal("CheckClaude should return error when hooks missing")
		}
	})

	t.Run("remove parse error", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		path := projectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("oops"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		stubClaudeEnvProvider(t, env, nil)
		if err := RemoveClaude(false); err == nil {
			t.Fatal("RemoveClaude should return error on parse error")
		}
	})
}

// settingsWithPlugin returns settings data with the beads plugin enabled.
func settingsWithPlugin() map[string]interface{} {
	return map[string]interface{}{
		"enabledPlugins": map[string]interface{}{
			"beads@beads-marketplace": true,
		},
	}
}

func TestHasBeadsPlugin(t *testing.T) {
	t.Run("plugin in project settings", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())
		if !hasBeadsPlugin(env) {
			t.Error("expected plugin to be detected in project settings")
		}
	})

	t.Run("plugin in global settings", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		if !hasBeadsPlugin(env) {
			t.Error("expected plugin to be detected in global settings")
		}
	})

	t.Run("plugin disabled", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
			"enabledPlugins": map[string]interface{}{
				"beads@beads-marketplace": false,
			},
		})
		if hasBeadsPlugin(env) {
			t.Error("disabled plugin should not be detected")
		}
	})

	t.Run("no plugin", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		if hasBeadsPlugin(env) {
			t.Error("expected no plugin detected")
		}
	})

	t.Run("design-to-beads not mistaken for beads plugin", func(t *testing.T) {
		// GH#4244: a plugin whose name merely contains "beads" (here
		// design-to-beads) must NOT be taken for the beads hook plugin, or the
		// SessionStart hook write is wrongly skipped.
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
			"enabledPlugins": map[string]interface{}{
				"design-to-beads@xexr-marketplace": true,
			},
		})
		if hasBeadsPlugin(env) {
			t.Error("design-to-beads should not be detected as the beads plugin")
		}
	})

	t.Run("real beads plugin detected past a decoy", func(t *testing.T) {
		// The exact-name match must still find a real beads@<marketplace> even
		// when a *beads*-named decoy is enabled too (GH#3192 preserved).
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
			"enabledPlugins": map[string]interface{}{
				"design-to-beads@xexr-marketplace": true,
				"beads@beads-marketplace":          true,
			},
		})
		if !hasBeadsPlugin(env) {
			t.Error("real beads@beads-marketplace should be detected even alongside a decoy")
		}
	})
}

func TestInstallClaudeSkipsHooksWhenPluginPresent(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)

	// Pre-populate project settings with the plugin enabled
	writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "plugin-managed") {
		t.Error("expected plugin-managed message in output")
	}
	if strings.Contains(out, "Registered SessionStart hook") {
		t.Error("should NOT register hooks when plugin is present")
	}

	// Verify settings file has no hooks written
	data, err := env.readFile(projectSettingsPath(env.projectDir))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks != nil {
		if _, hasSession := hooks["SessionStart"]; hasSession {
			t.Error("SessionStart hooks should not be written when plugin is present")
		}
		if _, hasCompact := hooks["PreCompact"]; hasCompact {
			t.Error("PreCompact hooks should not be written when plugin is present")
		}
	}

	// CLAUDE.md should still be installed
	instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if _, err := os.Stat(instructionsPath); err != nil {
		t.Errorf("CLAUDE.md should still be installed even with plugin: %v", err)
	}
}

func TestInstallClaudeWritesHooksWithoutPlugin(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	out := stdout.String()
	if strings.Contains(out, "plugin-managed") {
		t.Error("should NOT show plugin-managed when no plugin")
	}
	if !strings.Contains(out, "Registered SessionStart hook") {
		t.Error("expected hooks to be registered without plugin")
	}
}

func TestInstallClaudeReportsSkippedSymlinkInstructions(t *testing.T) {
	env, stdout, stderr := newClaudeTestEnv(t)
	target := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(target, []byte("# Shared instructions\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Claude Code hooks installed") {
		t.Fatalf("expected partial hook success message, got:\n%s", out)
	}
	if strings.Contains(out, "Claude Code integration installed") {
		t.Fatalf("should not report full integration success when instructions are skipped:\n%s", out)
	}
	if !strings.Contains(out, "Agent instructions skipped: CLAUDE.md is a symlink") {
		t.Fatalf("expected skipped instructions summary, got:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "CLAUDE.md is a symlink") {
		t.Fatalf("expected symlink warning on stderr, got:\n%s", stderr.String())
	}
	if _, err := os.Stat(projectSettingsPath(env.projectDir)); err != nil {
		t.Fatalf("settings should still be installed: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(data), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("symlink target should remain untouched:\n%s", data)
	}
}

func TestCheckClaudePluginManaged(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)

	// Plugin enabled but no hooks in settings files
	writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())

	// checkClaude needs CLAUDE.md to exist for the agents check
	instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(instructionsPath, []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	if err := checkClaude(env); err != nil {
		t.Fatalf("checkClaude: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "plugin-managed") {
		t.Errorf("expected plugin-managed message, got: %s", out)
	}
}

func TestIsAgentsImportStub(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		agentsFile string
		want       bool
	}{
		{
			name:       "standalone @AGENTS.md directive",
			content:    "# Claude Code\n\n@AGENTS.md\n\nSome text.\n",
			agentsFile: "AGENTS.md",
			want:       true,
		},
		{
			name:       "directive with surrounding whitespace",
			content:    "# Claude Code\n\n  @AGENTS.md  \n",
			agentsFile: "AGENTS.md",
			want:       true,
		},
		{
			name:       "directive inline in prose",
			content:    "See @AGENTS.md for details.\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			name:       "no directive",
			content:    "# Claude Code\n\nFull instructions here.\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			name:       "custom agents file name",
			content:    "# Claude Code\n\n@INSTRUCTIONS.md\n",
			agentsFile: "INSTRUCTIONS.md",
			want:       true,
		},
		{
			name:       "relative @./AGENTS.md directive",
			content:    "# Claude Code\n\n@./AGENTS.md\n",
			agentsFile: "AGENTS.md",
			want:       true,
		},
		{
			name:       "empty content",
			content:    "",
			agentsFile: "AGENTS.md",
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAgentsImportStub(tt.content, tt.agentsFile); got != tt.want {
				t.Errorf("isAgentsImportStub() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInstallClaudeRedirectsToAgentsMD(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, _ := newClaudeTestEnv(t)

	// Create CLAUDE.md as a thin stub that imports AGENTS.md
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	stubContent := "# Claude Code\n\n@AGENTS.md\n\nShared instructions live in AGENTS.md.\n"
	if err := os.WriteFile(claudePath, []byte(stubContent), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// Create AGENTS.md with existing content (no beads block yet)
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\nSome content.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	// CLAUDE.md should NOT have a beads section
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if strings.Contains(string(claudeData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md should not contain beads section when it imports AGENTS.md:\n%s", claudeData)
	}

	// AGENTS.md SHOULD have a beads section
	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("AGENTS.md should contain beads section:\n%s", agentsData)
	}
	if !strings.Contains(string(agentsData), "profile:minimal") {
		t.Fatalf("AGENTS.md should have minimal profile:\n%s", agentsData)
	}
}

func TestCheckClaudeRedirectsToAgentsMD(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)

	// Create CLAUDE.md as a thin stub
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte("# Claude Code\n\n@AGENTS.md\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// Create AGENTS.md with a beads section
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\n"+agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Install hooks so check passes the hooks stage
	writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --hook-json"},
					},
				},
			},
		},
	})

	if err := checkClaude(env); err != nil {
		t.Fatalf("checkClaude: %v", err)
	}

	// Should report AGENTS.md as the integration file, not CLAUDE.md
	out := stdout.String()
	if !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("expected output to reference AGENTS.md, got: %s", out)
	}
}

func TestRemoveClaudeRedirectsToAgentsMD(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)

	// Create CLAUDE.md as a thin stub
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte("# Claude Code\n\n@AGENTS.md\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// Create AGENTS.md with a beads section
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\n"+agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v", err)
	}

	// AGENTS.md's beads section is project-authoritative (shared with other
	// agents), not Claude-specific — removing Claude integration must leave
	// it intact.
	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("AGENTS.md should still contain the shared beads section after removing Claude:\n%s", agentsData)
	}

	// CLAUDE.md should be untouched (still a stub)
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), "@AGENTS.md") {
		t.Fatalf("CLAUDE.md should still contain @AGENTS.md import:\n%s", claudeData)
	}

	if !strings.Contains(stdout.String(), "AGENTS.md") {
		t.Fatalf("expected output to reference AGENTS.md, got: %s", stdout.String())
	}
}

// TestRemoveClaudeRedirectPreservesFullProfileSharedBlock covers the case
// where AGENTS.md carries a full-profile shared beads block (e.g. created by
// `bd init` or `bd setup codex`). Removing Claude integration must not touch
// it — other agents still depend on it.
func TestRemoveClaudeRedirectPreservesFullProfileSharedBlock(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)

	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte("# Claude Code\n\n@AGENTS.md\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	fullBlock := "# Agent Instructions\n\n" + agents.RenderSection(agents.ProfileFull)
	if err := os.WriteFile(agentsPath, []byte(fullBlock), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v", err)
	}

	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(agentsData) != fullBlock {
		t.Fatalf("AGENTS.md full-profile shared block should be byte-for-byte unchanged:\ngot:\n%s\nwant:\n%s", agentsData, fullBlock)
	}

	if !strings.Contains(stdout.String(), "AGENTS.md") {
		t.Fatalf("expected output to reference AGENTS.md, got: %s", stdout.String())
	}
}

func TestInstallClaudeNoRedirectWhenAGENTSMDMissing(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, _ := newClaudeTestEnv(t)

	// Create CLAUDE.md as a thin stub, but do NOT create AGENTS.md
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte("# Claude Code\n\n@AGENTS.md\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	// CLAUDE.md should get the beads section (fallback: no AGENTS.md to redirect to)
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md should contain beads section when AGENTS.md is missing:\n%s", claudeData)
	}
}

// staleClaudeStubWithBlock builds a CLAUDE.md stub that carries BOTH the
// @AGENTS.md import line (redirect trigger) and a beads block written by an
// older bd, simulating a project that adopted the AGENTS.md-import pattern
// after a beads block was already installed directly into CLAUDE.md.
func staleClaudeStubWithBlock() string {
	return "# Claude Code\n\n@AGENTS.md\n\n" +
		agents.RenderSection(agents.ProfileMinimal) +
		"\nShared instructions live in AGENTS.md.\n"
}

func TestInstallClaudeRedirectCleansStaleClaudeBlock(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, _ := newClaudeTestEnv(t)

	// CLAUDE.md is a stub that imports AGENTS.md but still carries a stale
	// beads block from an older bd install.
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte(staleClaudeStubWithBlock()), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// AGENTS.md already exists (without a beads block yet).
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\nSome content.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	claudeContent := string(claudeData)
	if strings.Contains(claudeContent, "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md should have the stale beads block stripped:\n%s", claudeContent)
	}
	if !strings.Contains(claudeContent, "@AGENTS.md") {
		t.Fatalf("CLAUDE.md should still contain the @AGENTS.md import line:\n%s", claudeContent)
	}
	if !strings.Contains(claudeContent, "Shared instructions live in AGENTS.md.") {
		t.Fatalf("CLAUDE.md should keep its other stub content:\n%s", claudeContent)
	}

	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	agentsContent := string(agentsData)
	if got := strings.Count(agentsContent, "BEGIN BEADS INTEGRATION"); got != 1 {
		t.Fatalf("AGENTS.md should contain exactly one beads block, got %d:\n%s", got, agentsContent)
	}
}

// TestInstallClaudeRedirectSkipsStripWhenAgentsSymlink is a regression test
// for the delete-before-write gap: when the redirect target (AGENTS.md) is a
// symlink, installAgents skips injection (agents.go markSkipped) without
// writing the beads block anywhere. In that case installClaude must NOT
// strip the stale block already present in CLAUDE.md, or the project is left
// with no beads section at all.
func TestInstallClaudeRedirectSkipsStripWhenAgentsSymlink(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, stderr := newClaudeTestEnv(t)

	// CLAUDE.md is a stub that imports AGENTS.md but still carries a stale
	// beads block from an older bd install.
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte(staleClaudeStubWithBlock()), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// AGENTS.md is a symlink to a separate target file.
	target := filepath.Join(env.projectDir, "AGENTS_target.md")
	if err := os.WriteFile(target, []byte("# Shared instructions\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md target: %v", err)
	}
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.Symlink(target, agentsPath); err != nil {
		t.Fatalf("symlink AGENTS.md: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	// installAgents should have warned and skipped injection.
	if !strings.Contains(stderr.String(), "AGENTS.md is a symlink") {
		t.Fatalf("expected symlink warning on stderr, got:\n%s", stderr.String())
	}

	// The stale beads block in CLAUDE.md must be preserved: the redirect
	// never wrote a replacement, so stripping it would delete-before-write.
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md should keep its beads block when the AGENTS.md redirect is skipped:\n%s", claudeData)
	}

	// The symlink target must remain untouched.
	targetData, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read AGENTS.md target: %v", err)
	}
	if strings.Contains(string(targetData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("AGENTS.md symlink target should remain untouched:\n%s", targetData)
	}
}

func TestRemoveClaudeRedirectCleansStaleClaudeBlock(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)

	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.WriteFile(claudePath, []byte(staleClaudeStubWithBlock()), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\n"+agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v", err)
	}

	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if strings.Contains(string(claudeData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md should not contain a beads block after remove:\n%s", claudeData)
	}

	// AGENTS.md's own beads section is project-authoritative and shared with
	// other agents — removing Claude integration must not delete it, only the
	// stale duplicate left in CLAUDE.md.
	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("AGENTS.md should still contain its beads block after remove:\n%s", agentsData)
	}
}
