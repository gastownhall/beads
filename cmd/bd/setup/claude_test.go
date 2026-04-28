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
	for _, event := range []string{"SessionStart", "PreCompact"} {
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
		`"command": "bd prime"`,
		`"SessionStart"`,
		`"PreCompact"`,
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
					"PreCompact": []interface{}{
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

func TestClaudeWrappersExit(t *testing.T) {
	t.Run("install provider error", func(t *testing.T) {
		cap := stubSetupExit(t)
		stubClaudeEnvProvider(t, claudeEnv{}, errors.New("boom"))
		InstallClaude(false, false)
		if !cap.called || cap.code != 1 {
			t.Fatal("InstallClaude should exit on provider error")
		}
	})

	t.Run("install internal error", func(t *testing.T) {
		cap := stubSetupExit(t)
		env, _, _ := newClaudeTestEnv(t)
		env.ensureDir = func(string, os.FileMode) error { return errors.New("boom") }
		stubClaudeEnvProvider(t, env, nil)
		// global=false → project-local (default)
		InstallClaude(false, false)
		if !cap.called || cap.code != 1 {
			t.Fatal("InstallClaude should exit when installClaude fails")
		}
	})

	t.Run("check missing hooks", func(t *testing.T) {
		cap := stubSetupExit(t)
		env, _, _ := newClaudeTestEnv(t)
		stubClaudeEnvProvider(t, env, nil)
		CheckClaude()
		if !cap.called || cap.code != 1 {
			t.Fatal("CheckClaude should exit when hooks missing")
		}
	})

	t.Run("remove parse error", func(t *testing.T) {
		cap := stubSetupExit(t)
		env, _, _ := newClaudeTestEnv(t)
		// Write invalid JSON to project settings path (default target)
		path := projectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("oops"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		stubClaudeEnvProvider(t, env, nil)
		// global=false → project-local (default)
		RemoveClaude(false)
		if !cap.called || cap.code != 1 {
			t.Fatal("RemoveClaude should exit on parse error")
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

func TestCheckClaudePluginManaged(t *testing.T) {
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
