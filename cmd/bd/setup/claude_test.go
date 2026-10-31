package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
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

// userLevelSection is the managed section a global install writes to
// ~/.claude/CLAUDE.md.
func userLevelSection() string {
	return agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.UserLevelRenderOpts())
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
		removeHookCommand(io.Discard, hooks, "SessionStart", "bd prime")

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

	t.Run("reports whether it removed the command", func(t *testing.T) {
		hooks := map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "other-tool"},
					},
				},
			},
		}
		if removeHookCommand(io.Discard, hooks, "SessionStart", "bd prime") {
			t.Error("expected no removal reported for an absent command")
		}
		if removeHookCommand(io.Discard, hooks, "PreCompact", "bd prime") {
			t.Error("expected no removal reported for an absent event")
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !removeHookCommand(io.Discard, tt.existingHooks, tt.event, tt.command) {
				t.Errorf("expected the removal of %q reported", tt.command)
			}

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

	removeHookCommand(io.Discard, hooks, "SessionStart", "bd prime")

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

// bd init --quiet hands the Claude installer QuietProjectInstallOutput. When
// the project's settings still carry the bare `bd prime` hooks an older bd
// registered, the migration sweep's "Removed <event> hook" lines must go to
// that output like the rest of the installer's progress: removeHookCommand
// printed them straight to the process stdout, so a quiet init still wrote
// them.
func TestClaudeLegacyHookRemovalReportsThroughGivenOutput(t *testing.T) {
	legacyHook := func() []interface{} {
		return []interface{}{map[string]interface{}{
			"matcher": "",
			"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "bd prime"}},
		}}
	}
	for _, tc := range []struct {
		name string
		path func(projectDir string) string
	}{
		{name: "settings.json", path: projectSettingsPath},
		{name: "legacy settings.local.json", path: legacyProjectSettingsPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			stubClaudeEnvProvider(t, env, nil)
			writeSettings(t, tc.path(env.projectDir), map[string]interface{}{
				"hooks": map[string]interface{}{"SessionStart": legacyHook(), "PreCompact": legacyHook()},
			})

			var out, errOut bytes.Buffer
			processOut := captureProcessStdout(t, func() {
				if err := InstallClaudeProjectTo(false, ProjectInstallOutput{Stdout: &out, Stderr: &errOut}); err != nil {
					t.Fatalf("InstallClaudeProjectTo: %v", err)
				}
			})
			if processOut != "" {
				t.Errorf("Claude installer wrote to the process stdout instead of the given one: %q", processOut)
			}
			for _, want := range []string{"✓ Removed SessionStart hook\n", "✓ Removed PreCompact hook\n"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("given stdout is missing %q:\n%s", want, out.String())
				}
			}
			if errOut.Len() != 0 {
				t.Errorf("install wrote errors: %q", errOut.String())
			}
		})
	}
}

// captureProcessStdout runs fn with the process stdout sent to a temp file and
// returns what was written there. Swapping os.Stdout is why the tests in this
// package must not run in parallel.
func captureProcessStdout(t *testing.T, fn func()) string {
	t.Helper()
	capture, err := os.CreateTemp(t.TempDir(), "process-stdout-*")
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	orig := os.Stdout
	os.Stdout = capture
	func() {
		defer func() { os.Stdout = orig }()
		fn()
	}()
	if err := capture.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	return string(data)
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

// TestCheckClaudeWarnsOfRemovedSyncHooks pins the settings files --check
// warns about when a hook runs the removed bd sync: each file Claude Code
// loads hooks from here, the repository root's local settings included.
func TestCheckClaudeWarnsOfRemovedSyncHooks(t *testing.T) {
	cases := []struct {
		name string
		path func(claudeEnv) string
	}{
		{"project settings", func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }},
		{"local settings", func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }},
		{"root-local settings", func(env claudeEnv) string { return legacyProjectSettingsPath(env.localSettingsRoot) }},
		{"global settings", func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _, stderr := newClaudeTestEnv(t)
			env.localSettingsRoot = filepath.Dir(env.projectDir)
			hooks := map[string]interface{}{}
			addHookCommand(hooks, "PreCompact", "bd sync")
			writeSettings(t, tc.path(env), map[string]interface{}{"hooks": hooks})

			_ = checkClaude(env)
			want := "Warning: " + tc.path(env) + " contains a hook using removed \"bd sync\""
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("want %q, got:\n%s", want, stderr.String())
			}
		})
	}
}

// GH#7894: a hand-written hook that omits memories is still a bd prime hook.
func TestHasBeadsHooksWithNoMemories(t *testing.T) {
	for _, command := range []string{"bd prime --no-memories", "bd prime --no-memories --hook-json"} {
		settingsPath := filepath.Join(t.TempDir(), "settings.json")
		settings := fmt.Sprintf(`{"hooks": {"SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": %q}]}]}}`, command)
		if err := os.WriteFile(settingsPath, []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
		if !hasBeadsHooks(settingsPath) {
			t.Errorf("hasBeadsHooks with %q = false, want true", command)
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
	// The managed section goes to Claude Code's user-level instructions file,
	// beside the global settings, holding only the section, worded for a file
	// every project loads: none of the project scaffolding a new project
	// CLAUDE.md gets.
	instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
	if err != nil {
		t.Fatalf("expected user-level %s to be created: %v", claudeInstructionsFile, err)
	}
	if want := userLevelSection(); string(instructions) != want {
		t.Errorf("user-level %s should hold only the managed section:\ngot:\n%s\nwant:\n%s", claudeInstructionsFile, instructions, want)
	}
	// A global install must not touch the current directory's files.
	if _, err := os.Stat(filepath.Join(env.projectDir, claudeInstructionsFile)); !os.IsNotExist(err) {
		t.Errorf("global install should not create the project's %s (stat err: %v)", claudeInstructionsFile, err)
	}
	if !strings.Contains(stdout.String(), "globally") {
		t.Error("expected global installation message")
	}
}

// A global install or --remove rewrites ~/.claude/CLAUDE.md and
// ~/.claude/settings.json, which can hold API keys. Each must keep the mode
// the owner gave it, not come back 0644.
func TestClaudeGlobalFilesKeepTheirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions are not supported on Windows")
	}
	env, _, _ := newClaudeTestEnv(t)
	instructions := globalClaudeInstructionsPath(env.homeDir)
	settings := globalSettingsPath(env.homeDir)
	seedFileMode(t, instructions, "# Mine\n", 0o600)
	seedFileMode(t, settings, "{\"env\": {\"ANTHROPIC_API_KEY\": \"sk-test\"}}\n", 0o600)

	if err := installClaude(env, true, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	data, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("read %s: %v", instructions, err)
	}
	if !containsBeadsMarker(string(data)) {
		t.Fatalf("install did not add the beads section:\n%s", data)
	}
	if !hasBeadsHooks(settings) {
		t.Fatal("install did not add the beads hooks")
	}
	assertFileMode(t, instructions, 0o600)
	assertFileMode(t, settings, 0o600)

	for _, path := range []string{instructions, settings} {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}
	if err := removeClaude(env, true); err != nil {
		t.Fatalf("removeClaude: %v", err)
	}
	data, err = os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("read %s: %v", instructions, err)
	}
	if string(data) != "# Mine\n" {
		t.Errorf("--remove left %q, want %q", data, "# Mine\n")
	}
	if hasBeadsHooks(settings) {
		t.Error("--remove left the beads hooks")
	}
	assertFileMode(t, instructions, 0o600)
	assertFileMode(t, settings, 0o600)
}

// The user-level ~/.claude/CLAUDE.md is shared by every project, so a global
// install renders and checks it the same way whatever beads config the
// current directory has: otherwise --check from one project calls the section
// another project wrote stale, and following its hint flips it back.
func TestClaudeGlobalSectionIgnoresProjectConfig(t *testing.T) {
	configs := []struct {
		name string
		opts agents.RenderOpts
	}{
		{"remote", agents.RenderOpts{HasRemote: true}},
		{"no remote", agents.RenderOpts{HasRemote: false}},
		{"no push", agents.RenderOpts{HasRemote: true, NoPush: true}},
	}
	wantHash := agents.CurrentHashWithOpts(agents.ProfileMinimal, agents.UserLevelRenderOpts())
	orig := detectRenderOptsImpl
	t.Cleanup(func() { detectRenderOptsImpl = orig })
	for _, install := range configs {
		for _, check := range configs {
			t.Run(install.name+" install, "+check.name+" check", func(t *testing.T) {
				env, stdout, _ := newClaudeTestEnv(t)
				installOpts := install.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return installOpts }
				if err := installClaude(env, true, false); err != nil {
					t.Fatalf("installClaude: %v", err)
				}
				globalInstructions := globalClaudeInstructionsPath(env.homeDir)
				data, err := os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
				}
				if !strings.Contains(string(data), "hash:"+wantHash) {
					t.Errorf("expected hash %s regardless of project config, got:\n%s", wantHash, data)
				}
				checkOpts := check.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return checkOpts }
				stdout.Reset()
				if err := checkClaude(env); err != nil {
					t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
				}
				if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
					t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
				}
			})
		}
	}
}

// A section worded for a project, such as a project install run from
// ~/.claude leaves there, is stale in the user-level file: --check names the
// global install, which rewords the section in place and keeps the user's
// own text.
func TestInstallClaudeGlobalRewordsProjectSection(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	if err := installClaude(env, true, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	globalInstructions := globalClaudeInstructionsPath(env.homeDir)
	if err := os.WriteFile(globalInstructions, []byte("# Mine\n\n"+agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
	}
	stdout.Reset()
	if err := checkClaude(env); !errors.Is(err, errBeadsSectionStale) {
		t.Fatalf("expected errBeadsSectionStale, got %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run: bd setup claude --global (to update)") {
		t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
	}
	if err := installClaude(env, true, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	data, err := os.ReadFile(globalInstructions)
	if err != nil {
		t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
	}
	if want := "# Mine\n\n" + userLevelSection(); string(data) != want {
		t.Errorf("user-level %s after the global install:\ngot:\n%s\nwant:\n%s", claudeInstructionsFile, data, want)
	}
	stdout.Reset()
	if err := checkClaude(env); err != nil {
		t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
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

	// A settings file the install cannot read is not a missing one. Taken
	// for one, it was replaced by a file holding only bd's hook, and every
	// other setting in it was lost: the rename that replaces it needs write
	// access to the directory, not read access to the file. The install now
	// stops before it writes anything. The read error is injected, since a
	// chmod 000 file is still readable to root.
	for _, global := range []bool{false, true} {
		name := "unreadable project settings"
		if global {
			name = "unreadable global settings"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newClaudeTestEnv(t)
			settingsPath := projectSettingsPath(env.projectDir)
			instructionsPath := filepath.Join(env.projectDir, claudeInstructionsFile)
			if global {
				settingsPath = globalSettingsPath(env.homeDir)
				instructionsPath = globalClaudeInstructionsPath(env.homeDir)
			}
			const original = "{\n  \"env\": {\n    \"ANTHROPIC_API_KEY\": \"sk-test\"\n  }\n}\n"
			if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
				t.Fatalf("seed settings: %v", err)
			}
			readFile := env.readFile
			env.readFile = func(path string) ([]byte, error) {
				if path == settingsPath {
					return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
				}
				return readFile(path)
			}
			var written []string
			writeFile := env.writeFile
			env.writeFile = func(path string, data []byte) error {
				written = append(written, path)
				return writeFile(path, data)
			}

			if err := installClaude(env, global, false); !errors.Is(err, os.ErrPermission) {
				t.Errorf("installClaude error = %v, want %v\nstdout:\n%s", err, os.ErrPermission, stdout.String())
			}
			if !strings.Contains(stderr.String(), "failed to read "+settingsPath) {
				t.Errorf("expected the read error to name %s, got:\n%s", settingsPath, stderr.String())
			}
			if len(written) != 0 {
				t.Errorf("expected nothing written, wrote %v", written)
			}
			if data, err := os.ReadFile(settingsPath); err != nil || string(data) != original {
				t.Errorf("expected %s kept, got %q (%v)", settingsPath, data, err)
			}
			if _, err := os.Stat(instructionsPath); !os.IsNotExist(err) {
				t.Errorf("expected no %s written, stat error = %v", instructionsPath, err)
			}
		})
	}
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

	// From the home directory a project install would create ~/CLAUDE.md,
	// so the hint names the global install.
	t.Run("missing hooks checked from home", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("expected errClaudeHooksMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// From ~/.claude a project install would write project scaffolding into
	// the user-level ~/.claude/CLAUDE.md, so the hint names the global install.
	t.Run("missing hooks checked from the user-level directory", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
		if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
			t.Fatalf("mkdir user-level dir: %v", err)
		}
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("expected errClaudeHooksMissing, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
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
		// With global hooks and no instructions file in the project, the
		// missing section is the user-level one the global install writes.
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// A project CLAUDE.md without a section is still reported as the
	// project's: adding the section there is one way to finish the setup.
	t.Run("project instructions without a section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), map[string]interface{}{
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
		if err := os.WriteFile(filepath.Join(env.projectDir, claudeInstructionsFile), []byte("# Mine\n"), 0o644); err != nil {
			t.Fatalf("write project %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); !errors.Is(err, errBeadsSectionMissing) {
			t.Fatalf("expected errBeadsSectionMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude (to add beads section)") {
			t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
		}
	})

	primeHookSettings := map[string]interface{}{
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
	}

	// A global install is checked from any directory: with no beads section
	// in the project, the one in ~/.claude/CLAUDE.md is the one Claude loads.
	t.Run("global install from another directory", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		globalInstructions := globalClaudeInstructionsPath(env.homeDir)
		if err := os.WriteFile(globalInstructions, []byte(userLevelSection()), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// The common global case: the project has its own CLAUDE.md with no
	// beads section, and the user-level file carries the section.
	t.Run("global install beside project instructions without a section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		globalInstructions := globalClaudeInstructionsPath(env.homeDir)
		if err := os.WriteFile(globalInstructions, []byte(userLevelSection()), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := os.WriteFile(filepath.Join(env.projectDir, claudeInstructionsFile), []byte("# Mine\n"), 0o644); err != nil {
			t.Fatalf("write project %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// From the home directory a ~/CLAUDE.md without a section is not the
	// file to fix: the project hint would add the section there.
	t.Run("home instructions without a section checked from home", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		if err := os.WriteFile(filepath.Join(env.homeDir, claudeInstructionsFile), []byte("# Mine\n"), 0o644); err != nil {
			t.Fatalf("write home %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	t.Run("stale user-level section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		stale := "<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOld.\n<!-- END BEADS INTEGRATION -->\n"
		if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(stale), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); !errors.Is(err, errBeadsSectionStale) {
			t.Fatalf("expected errBeadsSectionStale, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global (to update)") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// The plugin's hooks are user-level too, so --global can supply the section.
	t.Run("plugin hooks with user-level section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		globalInstructions := globalClaudeInstructionsPath(env.homeDir)
		if err := os.WriteFile(globalInstructions, []byte(userLevelSection()), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// Claude Code runs a plugin enabled in a project's settings (project
	// scope) or settings.local.json (local scope) in that project only, so
	// its hooks are the project's: the hint names a project install, not
	// --global, whose hook another project would not get from the plugin.
	for _, scope := range []struct {
		name     string
		settings func(env claudeEnv) string
	}{
		{"project", func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }},
		{"local", func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }},
	} {
		t.Run(scope.name+" plugin without instructions names the project install", func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			writeSettings(t, scope.settings(env), settingsWithPlugin())
			if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
				t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "plugin-managed") {
				t.Errorf("expected the plugin-managed hooks line, got:\n%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), "Run: bd setup claude\n") {
				t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
			}
		})

		// As with project hooks, the user-level section does not stand in
		// for the project's missing one.
		t.Run(scope.name+" plugin ignores the user-level section", func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			writeSettings(t, scope.settings(env), settingsWithPlugin())
			if err := os.WriteFile(filepath.Join(env.projectDir, claudeInstructionsFile), []byte("# Mine\n"), 0o644); err != nil {
				t.Fatalf("write project %s: %v", claudeInstructionsFile, err)
			}
			if err := os.MkdirAll(filepath.Dir(globalClaudeInstructionsPath(env.homeDir)), 0o755); err != nil {
				t.Fatalf("mkdir user-level dir: %v", err)
			}
			if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(userLevelSection()), 0o644); err != nil {
				t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
			}
			if err := checkClaude(env); !errors.Is(err, errBeadsSectionMissing) {
				t.Fatalf("expected errBeadsSectionMissing, got %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "Run: bd setup claude (to add beads section)") {
				t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
			}
		})
	}

	// A plugin enabled in the user settings runs in every project, so its
	// hooks stay user-level even when the project enables it too.
	t.Run("user and project plugin with user-level section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())
		globalInstructions := globalClaudeInstructionsPath(env.homeDir)
		if err := os.WriteFile(globalInstructions, []byte(userLevelSection()), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// From the home directory the project's settings are the user-level
	// ones, so a plugin there keeps the --global hint.
	t.Run("plugin checked from home names the global install", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		writeSettings(t, legacyProjectSettingsPath(env.homeDir), settingsWithPlugin())
		if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// A project install is judged on the project's file alone: the
	// user-level section does not stand in for a missing project one.
	t.Run("project hooks ignore the user-level section", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), primeHookSettings)
		if err := os.MkdirAll(filepath.Dir(globalClaudeInstructionsPath(env.homeDir)), 0o755); err != nil {
			t.Fatalf("mkdir user-level dir: %v", err)
		}
		if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(userLevelSection()), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
	})

	// With global hooks the project's own section still wins over the
	// user-level one, as project hooks win over global ones.
	t.Run("project section preferred", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		stale := "<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOld.\n<!-- END BEADS INTEGRATION -->\n"
		if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(stale), 0o644); err != nil {
			t.Fatalf("write user-level %s: %v", claudeInstructionsFile, err)
		}
		projectInstructions := filepath.Join(env.projectDir, claudeInstructionsFile)
		if err := os.WriteFile(projectInstructions, []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
			t.Fatalf("write project %s: %v", claudeInstructionsFile, err)
		}
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), projectInstructions+" (current)") {
			t.Errorf("expected the project file reported, got:\n%s", stdout.String())
		}
	})

	// From the home directory the project's settings are the global ones and
	// its CLAUDE.md is ~/CLAUDE.md, so a global install must still be judged
	// on the user-level files.
	t.Run("global install checked from home", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		stdout := &bytes.Buffer{}
		env.stdout = stdout
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Global hooks installed") {
			t.Errorf("expected global hooks reported, got:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), globalClaudeInstructionsPath(env.homeDir)+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// The project hint would create ~/CLAUDE.md from the home directory.
	t.Run("missing section checked from home", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		writeSettings(t, globalSettingsPath(env.homeDir), primeHookSettings)
		if err := checkClaude(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Global hooks installed") {
			t.Errorf("expected global hooks reported, got:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// From ~/.claude the project's CLAUDE.md is the user-level file: it is
	// judged with the fixed global render options, not this directory's.
	t.Run("global install checked from the user-level directory", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		orig := detectRenderOptsImpl
		t.Cleanup(func() { detectRenderOptsImpl = orig })
		detectRenderOptsImpl = func() agents.RenderOpts { return agents.RenderOpts{HasRemote: false} }
		env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
		stdout := &bytes.Buffer{}
		env.stdout = stdout
		if err := checkClaude(env); err != nil {
			t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalClaudeInstructionsPath(env.homeDir)+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// A project that turns off a plugin the user settings enable runs no
	// plugin hook, so --check names the project install (Claude Code's
	// settings precedence: local over project over user).
	t.Run("plugin turned off here names the project install", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		writeSettings(t, legacyProjectSettingsPath(env.projectDir), settingsWithPluginSetTo(false))
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("expected errClaudeHooksMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup claude\n") {
			t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
		}
	})

	// Claude Code runs the plugin's hook beside a settings file's bd prime
	// hook, so --check reports the double run wherever both apply.
	for _, tc := range []struct {
		name      string
		plugin    func(env claudeEnv) string
		hooks     func(env claudeEnv) string
		pluginOff func(env claudeEnv) string
		wantTwice bool
	}{
		{"global hooks beside a project plugin", func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }, func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, nil, true},
		{"project hooks beside a user plugin", func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }, nil, true},
		{"global hooks beside a user plugin turned off here", func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			settings := map[string]map[string]interface{}{}
			add := func(path string, key string, value interface{}) {
				if settings[path] == nil {
					settings[path] = map[string]interface{}{}
				}
				settings[path][key] = value
			}
			add(tc.plugin(env), "enabledPlugins", map[string]interface{}{"beads@beads-marketplace": true})
			add(tc.hooks(env), "hooks", primeHookSettings["hooks"])
			if tc.pluginOff != nil {
				add(tc.pluginOff(env), "enabledPlugins", map[string]interface{}{"beads@beads-marketplace": false})
			}
			for path, content := range settings {
				writeSettings(t, path, content)
			}
			_ = checkClaude(env)
			if got := strings.Contains(stdout.String(), "⚠ bd prime runs twice per session here"); got != tc.wantTwice {
				t.Errorf("double run reported = %v, want %v:\n%s", got, tc.wantTwice, stdout.String())
			}
		})
	}

	// The home directory and ~/.claude are recognized when reached through a
	// symlink too (a symlinked /home, a linked checkout): sameDir compares
	// the directories, not their path strings.
	for _, link := range []struct {
		name   string
		target func(home string) string
	}{
		{"home", func(home string) string { return home }},
		{"the user-level directory", func(home string) string { return filepath.Dir(globalClaudeInstructionsPath(home)) }},
	} {
		// With no hooks installed, the hint names the global install there
		// too: a project install would write a project's files into the
		// user-level directory, or create ~/CLAUDE.md.
		t.Run("missing hooks checked from a symlink to "+link.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			target := link.target(env.homeDir)
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", target, err)
			}
			linkDir := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, linkDir); err != nil {
				t.Skipf("symlink: %v", err)
			}
			env.projectDir = linkDir
			if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
				t.Fatalf("expected errClaudeHooksMissing, got %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
				t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
			}
		})

		t.Run("global install checked from a symlink to "+link.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			if err := installClaude(env, true, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			linkDir := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(link.target(env.homeDir), linkDir); err != nil {
				t.Skipf("symlink: %v", err)
			}
			env.projectDir = linkDir
			stdout := &bytes.Buffer{}
			env.stdout = stdout
			if err := checkClaude(env); err != nil {
				t.Fatalf("checkClaude: %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "✓ Global hooks installed") {
				t.Errorf("expected global hooks reported, got:\n%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), globalClaudeInstructionsPath(env.homeDir)+" (current)") {
				t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
			}
		})
	}
}

func TestRemoveClaudeScenarios(t *testing.T) {
	// From the home directory a project removal removes the global hooks,
	// whose settings file is the same, so it removes their user-level
	// section too.
	t.Run("remove from home", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		if err := removeClaude(env, false); err != nil {
			t.Fatalf("removeClaude: %v", err)
		}
		data, err := os.ReadFile(globalSettingsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read global settings: %v", err)
		}
		if strings.Contains(string(data), "bd prime") {
			t.Error("expected bd prime hooks removed")
		}
		instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
		}
		if containsBeadsMarker(string(instructions)) {
			t.Errorf("expected the user-level section removed, got:\n%s\nstdout:\n%s", instructions, stdout.String())
		}
		// It acts on two instructions files, ~/CLAUDE.md and then
		// ~/.claude/CLAUDE.md; each removal names its own.
		for _, path := range []string{filepath.Join(env.homeDir, claudeInstructionsFile), globalClaudeInstructionsPath(env.homeDir)} {
			if !strings.Contains(stdout.String(), "Removing Claude Code integration from "+path+"...\n") {
				t.Errorf("expected the removal from %s named, got:\n%s", path, stdout.String())
			}
		}
	})

	// From ~/.claude a project removal removes the user-level section, whose
	// file is the same, so it removes the global hooks too.
	t.Run("remove from the user-level directory", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
		if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
			t.Fatalf("mkdir user-level dir: %v", err)
		}
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		if err := removeClaude(env, false); err != nil {
			t.Fatalf("removeClaude: %v", err)
		}
		data, err := os.ReadFile(globalSettingsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read global settings: %v", err)
		}
		if strings.Contains(string(data), "bd prime") {
			t.Errorf("expected bd prime hooks removed from the global settings, got:\n%s\nstdout:\n%s", data, stdout.String())
		}
		instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
		}
		if containsBeadsMarker(string(instructions)) {
			t.Errorf("expected the user-level section removed, got:\n%s\nstdout:\n%s", instructions, stdout.String())
		}
	})

	// The global hooks go before the section, so global settings that do not
	// parse stop the removal and leave the install whole.
	t.Run("remove from the user-level directory with unparsable global settings", func(t *testing.T) {
		env, _, stderr := newClaudeTestEnv(t)
		env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
		if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
			t.Fatalf("mkdir user-level dir: %v", err)
		}
		section := userLevelSection()
		if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(section), 0o644); err != nil {
			t.Fatalf("seed user-level %s: %v", claudeInstructionsFile, err)
		}
		if err := os.WriteFile(globalSettingsPath(env.homeDir), []byte("not json"), 0o644); err != nil {
			t.Fatalf("seed global settings: %v", err)
		}
		if err := removeClaude(env, false); err == nil {
			t.Fatal("expected parse error")
		}
		if !strings.Contains(stderr.String(), "failed to parse") {
			t.Errorf("expected parse error output, got:\n%s", stderr.String())
		}
		instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
		}
		if string(instructions) != section {
			t.Errorf("expected the user-level section kept, got:\n%s", instructions)
		}
	})

	// A global settings file that cannot be read is not a missing one: like
	// one that does not parse, it stops the removal with the section kept.
	t.Run("remove from the user-level directory with unreadable global settings", func(t *testing.T) {
		env, stdout, stderr := newClaudeTestEnv(t)
		env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
		if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
			t.Fatalf("mkdir user-level dir: %v", err)
		}
		section := userLevelSection()
		if err := os.WriteFile(globalClaudeInstructionsPath(env.homeDir), []byte(section), 0o644); err != nil {
			t.Fatalf("seed user-level %s: %v", claudeInstructionsFile, err)
		}
		settingsPath := globalSettingsPath(env.homeDir)
		readFile := env.readFile
		env.readFile = func(path string) ([]byte, error) {
			if path == settingsPath {
				return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
			}
			return readFile(path)
		}
		if err := removeClaude(env, false); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("removeClaude error = %v, want %v\nstdout:\n%s", err, os.ErrPermission, stdout.String())
		}
		if !strings.Contains(stderr.String(), "failed to read "+settingsPath) {
			t.Errorf("expected the read error to name %s, got:\n%s", settingsPath, stderr.String())
		}
		instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
		}
		if string(instructions) != section {
			t.Errorf("expected the user-level section kept, got:\n%s", instructions)
		}
	})

	// Once the global hooks are gone, a section the removal cannot take, as
	// from the symlinked ~/.claude/CLAUDE.md a dotfile manager leaves, fails
	// it: a warning and "✓ Claude hooks removed" would hide a half-removed
	// install. An @AGENTS.md line in the file, with ~/.claude/AGENTS.md
	// beside it, does not change that: from ~/.claude the file is the
	// user-level one, never a project stub redirected to AGENTS.md.
	for _, agentsImport := range []bool{false, true} {
		name := "remove from the user-level directory with a symlinked user-level CLAUDE.md"
		if agentsImport {
			name += " that imports AGENTS.md"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
			if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
				t.Fatalf("mkdir user-level dir: %v", err)
			}
			instructionsPath := globalClaudeInstructionsPath(env.homeDir)
			agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
			const agentsContent = "# Agent Instructions\n"
			if agentsImport {
				if err := os.WriteFile(instructionsPath, []byte("@AGENTS.md\n"), 0o644); err != nil {
					t.Fatalf("seed user-level %s: %v", claudeInstructionsFile, err)
				}
				if err := os.WriteFile(agentsPath, []byte(agentsContent), 0o644); err != nil {
					t.Fatalf("seed user-level AGENTS.md: %v", err)
				}
			}
			if err := installClaude(env, true, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			installed, err := os.ReadFile(instructionsPath)
			if err != nil {
				t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
			}
			target := filepath.Join(filepath.Dir(env.homeDir), "dotfiles", claudeInstructionsFile)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("mkdir dotfiles: %v", err)
			}
			if err := os.WriteFile(target, installed, 0o644); err != nil {
				t.Fatalf("seed dotfiles %s: %v", claudeInstructionsFile, err)
			}
			if err := os.Remove(instructionsPath); err != nil {
				t.Fatalf("remove user-level %s: %v", claudeInstructionsFile, err)
			}
			if err := os.Symlink(target, instructionsPath); err != nil {
				t.Fatalf("symlink user-level %s: %v", claudeInstructionsFile, err)
			}
			if err := removeClaude(env, false); !errors.Is(err, errRefuseSymlinkWrite) {
				t.Fatalf("removeClaude error = %v, want %v\nstdout:\n%s", err, errRefuseSymlinkWrite, stdout.String())
			}
			if strings.Contains(stdout.String(), "✓ Claude hooks removed") {
				t.Errorf("reported success with the section still in place:\n%s", stdout.String())
			}
			settings, err := os.ReadFile(globalSettingsPath(env.homeDir))
			if err != nil {
				t.Fatalf("read global settings: %v", err)
			}
			if strings.Contains(string(settings), "bd prime") {
				t.Errorf("expected bd prime hooks removed from the global settings before the section, got:\n%s\nstdout:\n%s", settings, stdout.String())
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read dotfiles %s: %v", claudeInstructionsFile, err)
			}
			if !bytes.Equal(data, installed) {
				t.Errorf("expected the symlinked section left as it was, got:\n%s", data)
			}
			if agentsImport {
				agentsData, err := os.ReadFile(agentsPath)
				if err != nil {
					t.Fatalf("read user-level AGENTS.md: %v", err)
				}
				if string(agentsData) != agentsContent {
					t.Errorf("expected the user-level AGENTS.md left as it was, got:\n%s", agentsData)
				}
			}
		})
	}

	// Global settings with no bd hook are not rewritten by the global pass:
	// a regular file keeps its bytes and mode, and a symlinked one, as a
	// dotfile manager leaves it, does not stop the removal. The settings are
	// in no form marshalSettings writes, so any rewrite changes their bytes.
	const userOnlySettings = "{\n    \"permissions\": {\"allow\": [\"Bash(make:*)\"]},\n    \"hooks\": {\n        \"Stop\": [{\"matcher\": \"\", \"hooks\": [{\"type\": \"command\", \"command\": \"notify-send done && paplay done.oga\"}]}]\n    }\n}\n"
	for _, symlinked := range []bool{false, true} {
		name := "remove from the user-level directory with no bd hook in the global settings"
		if symlinked {
			name += " behind a symlink"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newClaudeTestEnv(t)
			env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
			if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
				t.Fatalf("mkdir user-level dir: %v", err)
			}
			// The project install the old --check hint led to: its hooks go
			// to ~/.claude/.claude/settings.json and its section to
			// ~/.claude/CLAUDE.md.
			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			settingsFile := globalSettingsPath(env.homeDir)
			if symlinked {
				settingsFile = filepath.Join(filepath.Dir(env.homeDir), "dotfiles", "claude-settings.json")
				if err := os.MkdirAll(filepath.Dir(settingsFile), 0o755); err != nil {
					t.Fatalf("mkdir dotfiles: %v", err)
				}
			}
			if err := os.WriteFile(settingsFile, []byte(userOnlySettings), 0o600); err != nil {
				t.Fatalf("seed global settings: %v", err)
			}
			if symlinked {
				if err := os.Symlink(settingsFile, globalSettingsPath(env.homeDir)); err != nil {
					t.Fatalf("symlink global settings: %v", err)
				}
			}
			if err := removeClaude(env, false); err != nil {
				t.Fatalf("removeClaude: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			instructions, err := os.ReadFile(globalClaudeInstructionsPath(env.homeDir))
			if err != nil {
				t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
			}
			if containsBeadsMarker(string(instructions)) {
				t.Errorf("expected the section removed, got:\n%s\nstdout:\n%s", instructions, stdout.String())
			}
			data, err := os.ReadFile(settingsFile)
			if err != nil {
				t.Fatalf("read global settings: %v", err)
			}
			if string(data) != userOnlySettings {
				t.Errorf("expected the global settings left byte for byte, got:\n%s", data)
			}
			info, err := os.Stat(settingsFile)
			if err != nil {
				t.Fatalf("stat global settings: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("expected the global settings to keep mode 0600, got %v", info.Mode().Perm())
			}
			if symlinked {
				link, err := os.Lstat(globalSettingsPath(env.homeDir))
				if err != nil {
					t.Fatalf("lstat global settings: %v", err)
				}
				if link.Mode()&os.ModeSymlink == 0 {
					t.Errorf("expected the global settings to stay a symlink, got mode %v", link.Mode())
				}
			}
		})
	}

	// The legacy settings.local.json cleanup is not rewritten without a bd
	// hook either: the file keeps its bytes and mode.
	t.Run("remove with no bd hook in the legacy local settings", func(t *testing.T) {
		env, stdout, stderr := newClaudeTestEnv(t)
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
			t.Fatalf("mkdir legacy settings dir: %v", err)
		}
		if err := os.WriteFile(legacyPath, []byte(userOnlySettings), 0o600); err != nil {
			t.Fatalf("seed legacy settings: %v", err)
		}
		if err := removeClaude(env, false); err != nil {
			t.Fatalf("removeClaude: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			t.Fatalf("read legacy settings: %v", err)
		}
		if string(data) != userOnlySettings {
			t.Errorf("expected the legacy settings left byte for byte, got:\n%s", data)
		}
		info, err := os.Stat(legacyPath)
		if err != nil {
			t.Fatalf("stat legacy settings: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("expected the legacy settings to keep mode 0600, got %v", info.Mode().Perm())
		}
	})

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
		section := agents.RenderSection(agents.ProfileMinimal)
		globalInstructions := globalClaudeInstructionsPath(env.homeDir)
		if err := os.WriteFile(globalInstructions, []byte("# Mine\n\n"+section), 0o644); err != nil {
			t.Fatalf("seed user-level %s: %v", claudeInstructionsFile, err)
		}
		// The project's own section, as bd init leaves it, belongs to the
		// project install: a global removal must leave it alone.
		projectInstructions := filepath.Join(env.projectDir, claudeInstructionsFile)
		if err := os.WriteFile(projectInstructions, []byte(section), 0o644); err != nil {
			t.Fatalf("seed project %s: %v", claudeInstructionsFile, err)
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
		instructions, err := os.ReadFile(globalInstructions)
		if err != nil {
			t.Fatalf("read user-level %s: %v", claudeInstructionsFile, err)
		}
		if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") || !strings.HasPrefix(string(instructions), "# Mine\n") {
			t.Errorf("expected only the beads section removed from user-level %s, got:\n%s", claudeInstructionsFile, instructions)
		}
		project, err := os.ReadFile(projectInstructions)
		if err != nil {
			t.Fatalf("read project %s: %v", claudeInstructionsFile, err)
		}
		if string(project) != section {
			t.Errorf("global removal changed the project's %s:\n%s", claudeInstructionsFile, project)
		}
		if !strings.Contains(stdout.String(), "hooks removed") {
			t.Error("expected success message")
		}
	})

	// An older install can leave several bd prime variants, on PreCompact
	// as well as SessionStart: one removal takes every one of them.
	t.Run("remove every bd prime variant", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		path := projectSettingsPath(env.projectDir)
		writeSettings(t, path, map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": "bd prime"},
							map[string]interface{}{"type": "command", "command": "bd prime --hook-json"},
						},
					},
				},
				"PreCompact": []interface{}{
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
		if err := removeClaude(env, false); err != nil {
			t.Fatalf("removeClaude: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read project settings: %v", err)
		}
		if strings.Contains(string(data), "bd prime") || !strings.Contains(string(data), `"other"`) {
			t.Errorf("expected every bd prime hook removed and the other hook kept, got:\n%s\nstdout:\n%s", data, stdout.String())
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
	return settingsWithPluginSetTo(true)
}

// settingsWithPluginSetTo is a settings file whose enabledPlugins sets the
// beads plugin to enabled, as `claude plugin enable` / `disable` writes it.
func settingsWithPluginSetTo(enabled bool) map[string]interface{} {
	return map[string]interface{}{
		"enabledPlugins": map[string]interface{}{
			"beads@beads-marketplace": enabled,
		},
	}
}

func TestHasBeadsPlugin(t *testing.T) {
	t.Run("plugin in project settings", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())
		if !hasBeadsPlugin(env, false) {
			t.Error("expected plugin to be detected in project settings")
		}
	})

	t.Run("plugin in global settings", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		if !hasBeadsPlugin(env, false) {
			t.Error("expected plugin to be detected in global settings")
		}
	})

	// A global install counts only the user settings: Claude Code runs a
	// plugin enabled in a project's settings in that project only.
	t.Run("global counts only the user settings", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())
		writeSettings(t, legacyProjectSettingsPath(env.projectDir), settingsWithPlugin())
		if hasBeadsPlugin(env, true) {
			t.Error("plugin enabled in project and local settings should not count for a global install")
		}
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		if !hasBeadsPlugin(env, true) {
			t.Error("expected plugin in user settings to count for a global install")
		}
	})

	t.Run("plugin disabled", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, projectSettingsPath(env.projectDir), map[string]interface{}{
			"enabledPlugins": map[string]interface{}{
				"beads@beads-marketplace": false,
			},
		})
		if hasBeadsPlugin(env, false) {
			t.Error("disabled plugin should not be detected")
		}
	})

	t.Run("no plugin", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		if hasBeadsPlugin(env, false) {
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
		if hasBeadsPlugin(env, false) {
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
		if !hasBeadsPlugin(env, false) {
			t.Error("real beads@beads-marketplace should be detected even alongside a decoy")
		}
	})

	// Claude Code resolves each enabledPlugins id from the highest-precedence
	// settings file that sets it: managed, then local, then project, then
	// user. A narrower scope's false turns off a plugin a broader one enables.
	t.Run("precedence", func(t *testing.T) {
		local := func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }
		project := func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }
		user := func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }
		managed := func(env claudeEnv) string { return filepath.Join(env.managedDir, "managed-settings.json") }
		managedDropIn := func(env claudeEnv) string {
			return filepath.Join(env.managedDir, "managed-settings.d", "20-plugins.json")
		}
		dropIn := func(name string) func(claudeEnv) string {
			return func(env claudeEnv) string { return filepath.Join(env.managedDir, "managed-settings.d", name) }
		}
		type setting struct {
			path    func(claudeEnv) string
			enabled bool
		}
		cases := []struct {
			name        string
			settings    []setting
			wantProject bool
			wantGlobal  bool
		}{
			// Claude Code merges the drop-ins in name order, so the last
			// name wins, and ignores hidden and non-.json files.
			{"the last managed drop-in by name wins", []setting{{dropIn("10-a.json"), true}, {dropIn("20-b.json"), false}, {user, true}}, false, false},
			{"the last managed drop-in by name wins, swapped", []setting{{dropIn("10-a.json"), false}, {dropIn("20-b.json"), true}}, true, true},
			{"hidden and non-.json drop-ins are ignored", []setting{{user, true}, {dropIn(".hidden.json"), false}, {dropIn("notes.txt"), false}}, true, true},
			{"local false turns off a user plugin", []setting{{user, true}, {local, false}}, false, true},
			{"project false turns off a user plugin", []setting{{user, true}, {project, false}}, false, true},
			{"local true turns on a plugin the project turns off", []setting{{project, false}, {local, true}}, true, false},
			{"user false does not turn off a project plugin", []setting{{project, true}, {user, false}}, true, false},
			{"managed false blocks a user plugin", []setting{{user, true}, {managed, false}}, false, false},
			{"managed true force-enables a plugin local turns off", []setting{{local, false}, {managed, true}}, true, true},
			{"a managed drop-in overrides managed-settings.json", []setting{{managed, true}, {managedDropIn, false}, {user, true}}, false, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				env, _, _ := newClaudeTestEnv(t)
				env.managedDir = filepath.Join(t.TempDir(), "managed")
				for _, s := range tc.settings {
					writeSettings(t, s.path(env), settingsWithPluginSetTo(s.enabled))
				}
				if got := hasBeadsPlugin(env, false); got != tc.wantProject {
					t.Errorf("project hasBeadsPlugin = %v, want %v", got, tc.wantProject)
				}
				if got := hasBeadsPlugin(env, true); got != tc.wantGlobal {
					t.Errorf("global hasBeadsPlugin = %v, want %v", got, tc.wantGlobal)
				}
			})
		}
	})

	// Each id resolves on its own: turning off one marketplace's beads
	// plugin leaves another's enabled.
	t.Run("ids resolve independently", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		writeSettings(t, legacyProjectSettingsPath(env.projectDir), map[string]interface{}{
			"enabledPlugins": map[string]interface{}{"beads@another-marketplace": false},
		})
		if !hasBeadsPlugin(env, false) {
			t.Error("beads@beads-marketplace should stay enabled when only beads@another-marketplace is turned off")
		}
	})

	// Since v2.1.211 Claude Code keeps the local settings at the repository
	// root (the main checkout's root in a linked worktree) and still reads
	// one an older version left in the starting directory; the root's wins.
	t.Run("local settings at the repository root", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			rootLocal *bool
			dirLocal  *bool
			want      bool
		}{
			{"root enables", boolPtr(true), nil, true},
			{"root turns off a user plugin", boolPtr(false), nil, false},
			{"root wins over the starting directory", boolPtr(false), boolPtr(true), false},
			{"starting directory still read", nil, boolPtr(false), false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env, _, _ := newClaudeTestEnv(t)
				env.localSettingsRoot = env.projectDir
				env.projectDir = filepath.Join(env.localSettingsRoot, "sub")
				if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
					t.Fatalf("mkdir sub: %v", err)
				}
				if tc.rootLocal == nil || *tc.rootLocal == false {
					writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
				}
				if tc.rootLocal != nil {
					writeSettings(t, legacyProjectSettingsPath(env.localSettingsRoot), settingsWithPluginSetTo(*tc.rootLocal))
				}
				if tc.dirLocal != nil {
					writeSettings(t, legacyProjectSettingsPath(env.projectDir), settingsWithPluginSetTo(*tc.dirLocal))
				}
				if got := hasBeadsPlugin(env, false); got != tc.want {
					t.Errorf("hasBeadsPlugin = %v, want %v", got, tc.want)
				}
			})
		}
	})
}

func boolPtr(b bool) *bool { return &b }

// TestDirWithin pins the check that keeps claudeLocalSettingsRoot from using
// a git context cached for another directory: a directory is within the
// root itself, its subdirectories and a symlink into it, and nothing else.
func TestDirWithin(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	other := t.TempDir()
	if !dirWithin(root, root) || !dirWithin(sub, root) {
		t.Error("the root and its subdirectories should be within the root")
	}
	if dirWithin(other, root) || dirWithin(root, sub) {
		t.Error("a sibling or a parent should not be within the root")
	}
	link := filepath.Join(other, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if !dirWithin(link, root) {
		t.Error("a symlink to a subdirectory should be within the root")
	}
}

// TestClaudeLocalSettingsRoot pins where claudeLocalSettingsRoot finds the
// local settings Claude Code reads for a session: the repository root from a
// subdirectory, the main checkout's root from a linked worktree, and nowhere
// when that root is the home directory. A directory other than the working
// directory, such as the one bd doctor <path> checks, is resolved in its own
// repository rather than the working directory's cached one, and in the
// working directory a git context cached for another directory is not used.
func TestClaudeLocalSettingsRoot(t *testing.T) {
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
	t.Cleanup(git.ResetCaches)

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	// Keep a repository above the temp directory out of the outside rows.
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0",
			"-c", "user.name=bd", "-c", "user.email=bd@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	newRepo := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		runGit(dir, "init", "-q")
		runGit(dir, "commit", "-q", "--allow-empty", "-m", "init")
	}

	home := filepath.Join(tmp, "home")
	main := filepath.Join(tmp, "main")
	wt := filepath.Join(tmp, "wt")
	newRepo(home)
	newRepo(main)
	runGit(main, "worktree", "add", "-q", "--detach", wt)
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nested := filepath.Join(main, "nested")
	newRepo(nested)

	cases := []struct {
		name string
		// cachedIn is where the git context is cached; workDir is the
		// directory claudeLocalSettingsRoot is asked about.
		cachedIn, workDir string
		want              string
	}{
		{"repository root", main, main, main},
		{"subdirectory of the main checkout", filepath.Join(main, "sub"), filepath.Join(main, "sub"), main},
		{"linked worktree", wt, wt, main},
		{"subdirectory of a linked worktree", filepath.Join(wt, "sub"), filepath.Join(wt, "sub"), main},
		{"repository root is the home directory", filepath.Join(home, "sub"), filepath.Join(home, "sub"), ""},
		{"outside any repository", outside, outside, ""},
		{"a repository nested in the main checkout", filepath.Join(nested, "sub"), filepath.Join(nested, "sub"), nested},
		// workDir is not the working directory: its own repository decides.
		{"subdirectory of a checkout other than the working directory's", outside, filepath.Join(main, "sub"), main},
		{"linked worktree other than the working directory's", outside, filepath.Join(wt, "sub"), main},
		{"repository nested in the working directory's", main, filepath.Join(nested, "sub"), nested},
		{"the working directory's repository from a nested one", nested, filepath.Join(main, "sub"), main},
		{"home directory's repository from another", main, filepath.Join(home, "sub"), ""},
		{"outside any repository, asked from one", main, outside, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cachedIn)
			git.ResetCaches()
			got := claudeLocalSettingsRoot(home, tc.workDir)
			if tc.want == "" {
				if got != "" {
					t.Errorf("claudeLocalSettingsRoot(%s) = %q, want \"\"", tc.workDir, got)
				}
				return
			}
			if got == "" || !sameDir(got, tc.want) {
				t.Errorf("claudeLocalSettingsRoot(%s) = %q, want %q", tc.workDir, got, tc.want)
			}
		})
	}
	// A git context cached before the process left its directory is another
	// directory's, so the working directory gets no root from it.
	t.Run("git context cached for another directory than the working directory", func(t *testing.T) {
		t.Chdir(main)
		git.ResetCaches()
		if got := git.GetRepoRoot(); got == "" || !sameDir(got, main) {
			t.Fatalf("git context cached in %s = %q", main, got)
		}
		t.Chdir(outside)
		if got := claudeLocalSettingsRoot(home, outside); got != "" {
			t.Errorf("claudeLocalSettingsRoot(%s) = %q, want \"\"", outside, got)
		}
	})
}

// TestDefaultClaudeEnv pins that the environment the commands run with reads
// the managed settings and the local settings at the repository root, so a
// session started in a subdirectory resolves the plugin as Claude Code does.
func TestDefaultClaudeEnv(t *testing.T) {
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
	t.Cleanup(git.ResetCaches)

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	sub := filepath.Join(repo, "sub")
	for _, dir := range []string{home, sub} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("HOME", home)
	t.Chdir(sub)
	git.ResetCaches()

	env, err := defaultClaudeEnv()
	if err != nil {
		t.Fatalf("defaultClaudeEnv: %v", err)
	}
	if env.homeDir != home || !sameDir(env.projectDir, sub) {
		t.Errorf("homeDir, projectDir = %q, %q, want %q, %q", env.homeDir, env.projectDir, home, sub)
	}
	if env.localSettingsRoot == "" || !sameDir(env.localSettingsRoot, repo) {
		t.Errorf("localSettingsRoot = %q, want the repository root %q", env.localSettingsRoot, repo)
	}
	if env.managedDir == "" || env.managedDir != claudeManagedSettingsDir() {
		t.Errorf("managedDir = %q, want %q", env.managedDir, claudeManagedSettingsDir())
	}
}

// TestClaudeManagedSettingsDir pins the system directory Claude Code reads
// file-based managed settings from on this OS, as its managed-settings docs
// give it: a typo there would turn off every managed-settings rule while the
// tests that inject a managed directory stay green.
func TestClaudeManagedSettingsDir(t *testing.T) {
	want := "/etc/claude-code"
	switch runtime.GOOS {
	case "darwin":
		want = "/Library/Application Support/ClaudeCode"
	case "windows":
		want = `C:\Program Files\ClaudeCode`
	}
	if got := claudeManagedSettingsDir(); got != want {
		t.Errorf("claudeManagedSettingsDir() on %s = %q, want %q", runtime.GOOS, got, want)
	}
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

// TestInstallClaudeGlobalPluginScope pins which plugin enablements make a
// global install leave its hook to the plugin. A global install's hook is for
// every project, and Claude Code runs a plugin enabled in a project's
// .claude/settings.json or .claude/settings.local.json in that project only,
// so only ~/.claude/settings.json counts.
func TestInstallClaudeGlobalPluginScope(t *testing.T) {
	stubDetectRenderOpts(t)

	cases := []struct {
		name       string
		settings   func(env claudeEnv) string
		global     bool
		wantPlugin bool
		// wantTwice: the plugin's hook and the one the install wrote both
		// run in this project, and the install says so.
		wantTwice bool
	}{
		{"global install, project plugin", func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }, true, false, true},
		{"global install, local plugin", func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }, true, false, true},
		{"global install, user plugin", func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, true, true, false},
		// GH#3192: a project install still counts every scope.
		{"project install, project plugin", func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }, false, true, false},
		{"project install, local plugin", func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }, false, true, false},
		{"project install, user plugin", func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			writeSettings(t, tc.settings(env), settingsWithPlugin())
			if err := installClaude(env, tc.global, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			out := stdout.String()
			if got := strings.Contains(out, "plugin-managed"); got != tc.wantPlugin {
				t.Errorf("plugin-managed reported = %v, want %v:\n%s", got, tc.wantPlugin, out)
			}
			if got := strings.Contains(out, "Registered SessionStart hook"); got == tc.wantPlugin {
				t.Errorf("SessionStart hook registered = %v, want %v:\n%s", got, !tc.wantPlugin, out)
			}
			if got := strings.Contains(out, "⚠ bd prime runs twice per session here"); got != tc.wantTwice {
				t.Errorf("double run reported = %v, want %v:\n%s", got, tc.wantTwice, out)
			}
			target := projectSettingsPath(env.projectDir)
			if tc.global {
				target = globalSettingsPath(env.homeDir)
			}
			// An install that leaves the hooks to the plugin creates no
			// settings file of its own.
			data, err := env.readFile(target)
			if err != nil && !(tc.wantPlugin && os.IsNotExist(err)) {
				t.Fatalf("read %s: %v", target, err)
			}
			if got := strings.Contains(string(data), "bd prime --hook-json"); got == tc.wantPlugin {
				t.Errorf("%s has the bd prime hook = %v, want %v:\n%s", target, got, !tc.wantPlugin, data)
			}
		})
	}
}

// TestInstallClaudeGlobalFromHomeReportsDoubleRun pins the $HOME case: with
// the plugin enabled only in ~/.claude/settings.local.json, --check names the
// global install (a project install there would create ~/CLAUDE.md), and the
// global hook it registers runs beside the plugin's in $HOME sessions, which
// the install and the next --check report.
func TestInstallClaudeGlobalFromHomeReportsDoubleRun(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	env.projectDir = env.homeDir
	writeSettings(t, legacyProjectSettingsPath(env.homeDir), settingsWithPlugin())

	if err := checkClaude(env); err == nil {
		t.Fatalf("--check before the install should fail:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run: bd setup claude --global\n") {
		t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := installClaude(env, true, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	if !strings.Contains(stdout.String(), "⚠ bd prime runs twice per session here") {
		t.Errorf("global install from home should report the double run:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := checkClaude(env); err != nil {
		t.Fatalf("checkClaude after install: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "⚠ bd prime runs twice per session here") {
		t.Errorf("--check from home should report the double run:\n%s", stdout.String())
	}
}

// TestInstallClaudeGlobalReportsPluginOffHere pins the zero-run mirror of
// the double run: a global install that leaves the hooks to a plugin the
// user settings enable, run from a project whose own settings turn it off,
// leaves that project with no hook, and says so with the command that fixes
// it. Once the project has its own hook, or the plugin is on here, or the
// install runs from the home directory, it says nothing.
func TestInstallClaudeGlobalReportsPluginOffHere(t *testing.T) {
	stubDetectRenderOpts(t)
	const warning = "⚠ The beads plugin is off in this project ("

	t.Run("project turns the plugin off", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		local := legacyProjectSettingsPath(env.projectDir)
		writeSettings(t, local, settingsWithPluginSetTo(false))

		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, warning+local+"), so no hook runs bd prime here\n  Run: bd setup claude\n") {
			t.Errorf("expected the plugin-off warning naming %s:\n%s", local, out)
		}
		stdout.Reset()
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("--check should report missing hooks, got %v:\n%s", err, stdout.String())
		}

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		stdout.Reset()
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global again: %v", err)
		}
		if strings.Contains(stdout.String(), warning) {
			t.Errorf("warned although the project now has its own hook:\n%s", stdout.String())
		}
	})

	// A global hook installed before the user settings enabled the plugin
	// still runs bd prime in a project that turns the plugin off.
	t.Run("global hook already installed", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global without the plugin: %v", err)
		}
		data, err := os.ReadFile(globalSettingsPath(env.homeDir))
		if err != nil {
			t.Fatalf("read global settings: %v", err)
		}
		global := map[string]interface{}{}
		if err := json.Unmarshal(data, &global); err != nil {
			t.Fatalf("parse global settings: %v", err)
		}
		global["enabledPlugins"] = settingsWithPlugin()["enabledPlugins"]
		writeSettings(t, globalSettingsPath(env.homeDir), global)
		writeSettings(t, legacyProjectSettingsPath(env.projectDir), settingsWithPluginSetTo(false))
		stdout.Reset()
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global: %v", err)
		}
		if strings.Contains(stdout.String(), warning) || strings.Contains(stdout.String(), "Run: bd setup claude\n") {
			t.Errorf("warned although the global hook runs bd prime here:\n%s", stdout.String())
		}
		stdout.Reset()
		if err := checkClaude(env); err != nil {
			t.Fatalf("--check: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "✓ Global hooks installed: "+globalSettingsPath(env.homeDir)) {
			t.Errorf("--check should report the global hook:\n%s", stdout.String())
		}
	})

	// Only this user's local settings turn off a plugin the shared project
	// settings enable: a project install would put a hook in the shared
	// file that teammates with the plugin would run twice, so the warning
	// names turning the plugin back on instead.
	t.Run("only the local settings turn off a project plugin", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())
		local := legacyProjectSettingsPath(env.projectDir)
		writeSettings(t, local, settingsWithPluginSetTo(false))
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global: %v", err)
		}
		out := stdout.String()
		if strings.Contains(out, "Run: bd setup claude\n") {
			t.Errorf("suggested a project install that would write the shared settings:\n%s", out)
		}
		want := "⚠ The beads plugin is off for you (" + local + ") but on for everyone in " + projectSettingsPath(env.projectDir) +
			", so bd adds no hook there: teammates with the plugin would run bd prime twice\n  To run bd prime here: enable the plugin in " + local + "\n"
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
	})

	t.Run("plugin on here", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global: %v", err)
		}
		if strings.Contains(stdout.String(), warning) {
			t.Errorf("warned although the plugin runs here:\n%s", stdout.String())
		}
	})

	t.Run("from the home directory", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		writeSettings(t, legacyProjectSettingsPath(env.homeDir), settingsWithPluginSetTo(false))
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude --global: %v", err)
		}
		if strings.Contains(stdout.String(), warning) {
			t.Errorf("suggested a project install from the home directory:\n%s", stdout.String())
		}
	})
}

// TestWarnIfPrimeRunsTwiceNamesARemedy pins the remedy line under the
// double-run warning: drop a global hook only once the user settings enable
// the plugin, keep a shared project hook teammates need when only this
// user's settings enable it, drop it when the shared settings enable the
// plugin for everyone, whichever file decided it for this user, never
// suggest turning off a plugin that managed settings enable, and turn it off
// in the local settings Claude Code reads first.
func TestWarnIfPrimeRunsTwiceNamesARemedy(t *testing.T) {
	user := func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }
	project := func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }
	local := func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }
	managed := func(env claudeEnv) string { return filepath.Join(env.managedDir, "managed-settings.json") }
	cases := []struct {
		name         string
		plugin, hook func(claudeEnv) string
		want         func(env claudeEnv) string
		// projectToo also enables the plugin in the project's settings.
		projectToo bool
	}{
		{"global hook, project plugin", project, user, func(env claudeEnv) string {
			return "turn the plugin off in " + local(env) + ", or enable it in " + user(env) +
				" and run bd setup claude --remove --global, then bd setup claude --global"
		}, false},
		{"global hook, user plugin", user, user, func(claudeEnv) string {
			return "run bd setup claude --remove --global, then bd setup claude --global"
		}, false},
		{"shared project hook, personal plugin", user, project, func(env claudeEnv) string {
			return "turn the plugin off in " + local(env)
		}, false},
		{"shared project hook, shared plugin", project, project, func(env claudeEnv) string {
			return "remove the bd prime hook from " + project(env)
		}, false},
		{"shared project hook, local and project plugin", local, project, func(env claudeEnv) string {
			return "remove the bd prime hook from " + project(env)
		}, true},
		{"local hook, project plugin", project, local, func(env claudeEnv) string {
			return "remove the bd prime hook from " + local(env)
		}, false},
		{"local hook, local plugin", local, local, func(env claudeEnv) string {
			return "remove the bd prime hook from " + local(env) + ", or turn the plugin off in " + local(env)
		}, false},
		{"project hook, managed plugin", managed, project, func(env claudeEnv) string {
			return "remove the bd prime hook from " + project(env)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			env.managedDir = filepath.Join(t.TempDir(), "managed")
			settings := settingsWithPlugin()
			if tc.plugin(env) != tc.hook(env) {
				writeSettings(t, tc.plugin(env), settings)
				settings = map[string]interface{}{}
			}
			if tc.projectToo {
				if tc.hook(env) == project(env) {
					settings = settingsWithPlugin()
				} else {
					writeSettings(t, project(env), settingsWithPlugin())
				}
			}
			hooks := map[string]interface{}{}
			addHookCommand(hooks, "SessionStart", "bd prime --hook-json")
			settings["hooks"] = hooks
			writeSettings(t, tc.hook(env), settings)

			warnIfPrimeRunsTwice(env)
			want := "  To run it once: " + tc.want(env) + "\n"
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("want remedy %q, got:\n%s", want, stdout.String())
			}
		})
	}

	// From a subdirectory or linked worktree the repository root's local
	// settings outrank the directory's own, so the plugin is turned off
	// there: off in the directory's file, the root's would still enable it.
	t.Run("shared project hook, plugin in the repository root's local settings", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.managedDir = filepath.Join(t.TempDir(), "managed")
		env.localSettingsRoot = filepath.Dir(env.projectDir)
		rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
		writeSettings(t, rootLocal, settingsWithPlugin())
		writeSettings(t, project(env), map[string]interface{}{"hooks": primeHooks("bd prime --hook-json")})

		warnIfPrimeRunsTwice(env)
		want := "  To run it once: turn the plugin off in " + rootLocal + "\n"
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("want remedy %q, got:\n%s", want, stdout.String())
		}
	})
}

// TestInstallClaudePluginPrecedence pins a project install where settings
// files disagree about the beads plugin. Claude Code takes each plugin's
// setting from the highest-precedence file that names it (local, then
// project, then user), so a project that turns off a plugin the user
// settings enable gets the hook, and --check, which named the plugin, now
// names the project install first. When only this user's local settings
// turn off a plugin the shared project settings enable, the install adds no
// hook to the shared file, where teammates with the plugin would run bd
// prime twice, and both it and --check say how this user can run it here.
func TestInstallClaudePluginPrecedence(t *testing.T) {
	stubDetectRenderOpts(t)

	user := func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }
	project := func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }
	local := func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }
	type setting struct {
		path    func(claudeEnv) string
		enabled bool
	}
	cases := []struct {
		name     string
		settings []setting
		wantHook bool
		// offForYou is the remedy named when only this user's settings turn
		// the plugin off; "" when they do not.
		offForYou func(claudeEnv) string
	}{
		{"user plugin turned off in the local settings", []setting{{user, true}, {local, false}}, true, nil},
		{"user plugin turned off in the project settings", []setting{{user, true}, {project, false}}, true, nil},
		{"project plugin turned off in the local settings", []setting{{project, true}, {local, false}}, false, func(env claudeEnv) string {
			return "enable the plugin in " + local(env) + ", or run bd setup claude --global"
		}},
		{"project and user plugin turned off in the local settings", []setting{{user, true}, {project, true}, {local, false}}, false, func(env claudeEnv) string {
			return "enable the plugin in " + local(env)
		}},
		{"user plugin left on", []setting{{user, true}, {project, true}}, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			for _, s := range tc.settings {
				writeSettings(t, s.path(env), settingsWithPluginSetTo(s.enabled))
			}
			// Before the install --check reports missing hooks exactly where
			// the plugin is off for this user, and names the project install
			// unless the shared settings enable the plugin.
			wantMissing := tc.wantHook || tc.offForYou != nil
			err := checkClaude(env)
			if got := errors.Is(err, errClaudeHooksMissing); got != wantMissing {
				t.Fatalf("--check reported missing hooks = %v (err %v), want %v:\n%s", got, err, wantMissing, stdout.String())
			}
			if got := strings.Contains(stdout.String(), "  Run: bd setup claude\n"); got != tc.wantHook {
				t.Errorf("--check named the project install = %v, want %v:\n%s", got, tc.wantHook, stdout.String())
			}
			if tc.offForYou != nil {
				want := "  The beads plugin is off for you (" + local(env) + ") but on for everyone in " + project(env) +
					", so bd adds no hook there: teammates with the plugin would run bd prime twice\n  To run bd prime here: " + tc.offForYou(env) + "\n"
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("--check should name this user's remedy %q:\n%s", want, stdout.String())
				}
			}
			stdout.Reset()
			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			data, err := os.ReadFile(projectSettingsPath(env.projectDir))
			if err != nil {
				t.Fatalf("read project settings: %v", err)
			}
			if got := strings.Contains(string(data), "bd prime --hook-json"); got != tc.wantHook {
				t.Errorf("project settings have the bd prime hook = %v, want %v:\n%s\n%s", got, tc.wantHook, data, stdout.String())
			}
			if tc.offForYou != nil {
				want := "⚠ The beads plugin is off for you (" + local(env) + ") but on for everyone in " + project(env) +
					", so bd adds no hook there: teammates with the plugin would run bd prime twice\n  To run bd prime here: " + tc.offForYou(env) + "\n"
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("the install should say why it added no hook, %q:\n%s", want, stdout.String())
				}
			}
			stdout.Reset()
			err = checkClaude(env)
			if tc.offForYou != nil {
				if !errors.Is(err, errClaudeHooksMissing) {
					t.Fatalf("checkClaude after install should still report missing hooks, got %v\n%s", err, stdout.String())
				}
			} else if err != nil {
				t.Fatalf("checkClaude after install: %v\n%s", err, stdout.String())
			}
			if strings.Contains(stdout.String(), "runs twice") {
				t.Errorf("--check reported a double run after the install:\n%s", stdout.String())
			}
		})
	}
}

// TestInstallClaudeOffForYouNamesTheHookThatRuns pins a project install
// where only this user's local settings turn off a plugin the shared
// project settings enable, and a hook already runs bd prime here: a global
// hook, a shared project hook a teammate committed, or a hook in this
// user's own .claude/settings.local.json, here or at the repository root.
// The install names that hook
// instead of warning that none runs, keeps the local hook (it cannot move
// to the shared file), and --check agrees without suggesting a migration.
func TestInstallClaudeOffForYouNamesTheHookThatRuns(t *testing.T) {
	stubDetectRenderOpts(t)
	cases := []struct {
		name string
		hook func(*claudeEnv) string
	}{
		{"global hook", func(env *claudeEnv) string { return globalSettingsPath(env.homeDir) }},
		{"shared project hook", func(env *claudeEnv) string { return projectSettingsPath(env.projectDir) }},
		{"local hook", func(env *claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }},
		{"local hook at the repository root", func(env *claudeEnv) string {
			env.localSettingsRoot = filepath.Dir(env.projectDir)
			return legacyProjectSettingsPath(env.localSettingsRoot)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			project := projectSettingsPath(env.projectDir)
			local := legacyProjectSettingsPath(env.projectDir)
			files := map[string]map[string]interface{}{
				project: settingsWithPlugin(),
				local:   settingsWithPluginSetTo(false),
			}
			hookFile := tc.hook(&env)
			if files[hookFile] == nil {
				files[hookFile] = map[string]interface{}{}
			}
			hooks := map[string]interface{}{}
			addHookCommand(hooks, "SessionStart", "bd prime --hook-json")
			files[hookFile]["hooks"] = hooks
			for path, settings := range files {
				writeSettings(t, path, settings)
			}

			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			out := stdout.String()
			want := "✓ The beads plugin is off for you (" + local + "); bd prime runs here from the hook in " + hookFile + "\n"
			if !strings.Contains(out, want) {
				t.Errorf("want %q, got:\n%s", want, out)
			}
			for _, unwanted := range []string{"⚠", "To run bd prime here", "Migrated hooks", "Removed SessionStart hook"} {
				if strings.Contains(out, unwanted) {
					t.Errorf("install printed %q:\n%s", unwanted, out)
				}
			}
			if !hasBeadsHooks(hookFile) {
				t.Errorf("the hook in %s is gone:\n%s", hookFile, out)
			}

			stdout.Reset()
			if err := checkClaude(env); err != nil {
				t.Fatalf("--check: %v\n%s", err, stdout.String())
			}
			if strings.Contains(stdout.String(), "migrate") || strings.Contains(stdout.String(), "runs twice") {
				t.Errorf("--check should report the hook alone:\n%s", stdout.String())
			}
		})
	}
}

// TestInstallClaudeFromHomeTreatsSettingsAsShared pins a project install
// from the home directory, where the project's .claude/settings.json is
// ~/.claude/settings.json, which every other project loads. Those projects
// take the teammates' place: with the plugin enabled there and turned off in
// ~/.claude/settings.local.json, the install adds no hook there, which would
// make every other project run bd prime twice, keeps a hook already in
// ~/.claude/settings.local.json, and names the remedy --check names. With
// the plugin enabled only in ~/.claude/settings.local.json, an older bare
// global hook is replaced, since the other projects still need it.
func TestInstallClaudeFromHomeTreatsSettingsAsShared(t *testing.T) {
	stubDetectRenderOpts(t)
	primeHook := func() map[string]interface{} {
		hooks := map[string]interface{}{}
		addHookCommand(hooks, "SessionStart", "bd prime --hook-json")
		return hooks
	}

	t.Run("plugin off for you, no hook", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		global := globalSettingsPath(env.homeDir)
		local := legacyProjectSettingsPath(env.homeDir)
		writeSettings(t, global, settingsWithPlugin())
		writeSettings(t, local, settingsWithPluginSetTo(false))

		remedy := "  To run bd prime here: enable the plugin in " + local + "\n"
		if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("--check should report missing hooks, got %v:\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), remedy) {
			t.Errorf("want %q, got:\n%s", remedy, stdout.String())
		}
		if strings.Contains(stdout.String(), "Run: bd setup claude --global") {
			t.Errorf("--check from home named a remedy that changes nothing there:\n%s", stdout.String())
		}

		stdout.Reset()
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		out := stdout.String()
		if hasBeadsHooks(global) {
			t.Errorf("install from home put a hook in %s, which every other project loads:\n%s", global, out)
		}
		want := "⚠ The beads plugin is off for you (" + local + ") but on in every project through " + global +
			", so bd adds no hook there: those projects would run bd prime twice\n" + remedy
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
		if strings.Contains(out, "teammates") || strings.Contains(out, "Registered") {
			t.Errorf("install from home misdescribed the shared settings:\n%s", out)
		}
	})

	t.Run("plugin off for you, local hook", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		global := globalSettingsPath(env.homeDir)
		local := legacyProjectSettingsPath(env.homeDir)
		writeSettings(t, global, settingsWithPlugin())
		localSettings := settingsWithPluginSetTo(false)
		localSettings["hooks"] = primeHook()
		writeSettings(t, local, localSettings)

		if err := checkClaude(env); errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("--check reported missing hooks:\n%s", stdout.String())
		}
		if strings.Contains(stdout.String(), "migrate") {
			t.Errorf("--check suggested moving the hook into %s:\n%s", global, stdout.String())
		}

		stdout.Reset()
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		out := stdout.String()
		if hasBeadsHooks(global) {
			t.Errorf("install from home put a hook in %s:\n%s", global, out)
		}
		if !hasBeadsHooks(local) {
			t.Errorf("install from home dropped the hook in %s:\n%s", local, out)
		}
		want := "✓ The beads plugin is off for you (" + local + "); bd prime runs here from the hook in " + local + "\n"
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
		if strings.Contains(out, "Migrated") || strings.Contains(out, "⚠") {
			t.Errorf("install from home did more than name the hook:\n%s", out)
		}
	})

	t.Run("plugin enabled only locally, old global hook", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.projectDir = env.homeDir
		global := globalSettingsPath(env.homeDir)
		local := legacyProjectSettingsPath(env.homeDir)
		hooks := map[string]interface{}{}
		addHookCommand(hooks, "SessionStart", "bd prime")
		writeSettings(t, global, map[string]interface{}{"hooks": hooks})
		writeSettings(t, local, settingsWithPlugin())

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		out := stdout.String()
		data, err := os.ReadFile(global)
		if err != nil {
			t.Fatalf("read global settings: %v", err)
		}
		if strings.Contains(string(data), `"bd prime"`) || !strings.Contains(string(data), "bd prime --hook-json") {
			t.Errorf("%s should hold the current hook in place of the old one:\n%s\n%s", global, data, out)
		}
		want := "✓ Registered SessionStart hook in place of the old one: the beads plugin is enabled only in " + local +
			", and your other projects still need the hook\n"
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
	})
}

// TestInstallClaudeFromUserLevelDirTreatsSettingsAsPersonal pins a project
// install from ~/.claude, whose .claude/settings.json Claude Code loads only
// in sessions started there, so no teammate or other project shares it:
// with the plugin enabled there and turned off in the local settings, the
// install registers the hook rather than leaving it out of a shared file.
func TestInstallClaudeFromUserLevelDirTreatsSettingsAsPersonal(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	env.projectDir = filepath.Dir(globalClaudeInstructionsPath(env.homeDir))
	project := projectSettingsPath(env.projectDir)
	writeSettings(t, project, settingsWithPlugin())
	writeSettings(t, legacyProjectSettingsPath(env.projectDir), settingsWithPluginSetTo(false))

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	if !hasBeadsHooks(project) {
		t.Errorf("install from ~/.claude registered no hook in %s:\n%s", project, stdout.String())
	}
	if strings.Contains(stdout.String(), "off for you") {
		t.Errorf("install from ~/.claude treated its settings as shared:\n%s", stdout.String())
	}
}

// TestInstallClaudeOffForYouSweepsTheOldSharedHook pins a project install
// where only this user's local settings turn off a plugin the shared
// project settings enable, and the shared file holds an older bare bd prime
// hook. The sweep removes that hook, so the install reports that none runs
// bd prime here rather than naming the hook it just removed.
func TestInstallClaudeOffForYouSweepsTheOldSharedHook(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	project := projectSettingsPath(env.projectDir)
	local := legacyProjectSettingsPath(env.projectDir)
	shared := settingsWithPlugin()
	hooks := map[string]interface{}{}
	addHookCommand(hooks, "SessionStart", "bd prime")
	shared["hooks"] = hooks
	writeSettings(t, project, shared)
	writeSettings(t, local, settingsWithPluginSetTo(false))

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	out := stdout.String()
	if hasBeadsHooks(project) {
		t.Errorf("the shared file still has a bd prime hook:\n%s", out)
	}
	for _, want := range []string{
		"✓ Removed SessionStart hook",
		"⚠ The beads plugin is off for you (" + local + ") but on for everyone in " + project,
		"  To run bd prime here: enable the plugin in " + local + ", or run bd setup claude --global\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "runs here from the hook") {
		t.Errorf("install named the hook it removed:\n%s", out)
	}
}

// TestCheckClaudeMigrateHint pins when --check suggests a project install
// to migrate a hook in the local settings: only when that install moves it
// to .claude/settings.json. It moves only the current directory's file, and
// only when it adds a hook in its place: not when the project's or the
// user's settings enable the plugin, which runs bd prime instead, and not
// beside a hook in the repository root's local settings, which already runs
// it here. The install that follows each --check pins that it does what the
// hint says.
func TestCheckClaudeMigrateHint(t *testing.T) {
	stubDetectRenderOpts(t)
	projectFile := func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }
	userFile := func(env claudeEnv) string { return globalSettingsPath(env.homeDir) }
	cases := []struct {
		name             string
		local, rootLocal bool
		// plugin is the settings file that enables the beads plugin; nil
		// for none.
		plugin   func(claudeEnv) string
		wantHint bool
	}{
		{"local hook", true, false, nil, true},
		{"local hook, project plugin", true, false, projectFile, false},
		{"local hook, user plugin", true, false, userFile, false},
		{"root-local hook", false, true, nil, false},
		{"local and root-local hooks", true, true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			env.localSettingsRoot = filepath.Dir(env.projectDir)
			project := projectSettingsPath(env.projectDir)
			local := legacyProjectSettingsPath(env.projectDir)
			rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
			if tc.local {
				writeSettings(t, local, map[string]interface{}{"hooks": primeHooks("bd prime --hook-json")})
			}
			if tc.rootLocal {
				writeSettings(t, rootLocal, map[string]interface{}{"hooks": primeHooks("bd prime --hook-json")})
			}
			if tc.plugin != nil {
				writeSettings(t, tc.plugin(env), settingsWithPlugin())
			}
			hookFile := local
			if !tc.local {
				hookFile = rootLocal
			}

			_ = checkClaude(env)
			out := stdout.String()
			if !strings.Contains(out, "✓ Project hooks installed (legacy): "+hookFile+"\n") {
				t.Errorf("--check should report the hook in %s:\n%s", hookFile, out)
			}
			if got := strings.Contains(out, "Consider running 'bd setup claude' to migrate"); got != tc.wantHint {
				t.Errorf("--check suggested a migration = %v, want %v:\n%s", got, tc.wantHint, out)
			}

			stdout.Reset()
			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			if got := hasBeadsHooks(project); got != tc.wantHint {
				t.Errorf("the install put a hook in %s = %v, want %v, as --check said:\n%s", project, got, tc.wantHint, stdout.String())
			}
		})
	}
}

// TestCheckClaudeFindsTheRootLocalHook pins that --check, run from a
// repository subdirectory, counts a hook in the repository root's
// .claude/settings.local.json, which Claude Code loads there and the
// double-run and plugin-off warnings already count. A project install from
// the subdirectory does not migrate that file, so no migration is
// suggested.
func TestCheckClaudeFindsTheRootLocalHook(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	env.localSettingsRoot = filepath.Dir(env.projectDir)
	rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
	settings := settingsWithPluginSetTo(false)
	hooks := map[string]interface{}{}
	addHookCommand(hooks, "SessionStart", "bd prime --hook-json")
	settings["hooks"] = hooks
	writeSettings(t, rootLocal, settings)
	writeSettings(t, projectSettingsPath(env.projectDir), settingsWithPlugin())

	// No CLAUDE.md is installed, so only the hook result is checked.
	if err := checkClaude(env); errors.Is(err, errClaudeHooksMissing) {
		t.Fatalf("--check reported missing hooks:\n%s", stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "✓ Project hooks installed (legacy): "+rootLocal+"\n") {
		t.Errorf("--check should report the root-local hook:\n%s", out)
	}
	if strings.Contains(out, "migrate") || strings.Contains(out, "off for you") {
		t.Errorf("--check disagreed with the hook it found:\n%s", out)
	}
}

// primeHooks returns a settings "hooks" object whose SessionStart hooks run
// the given commands.
func primeHooks(commands ...string) map[string]interface{} {
	hooks := map[string]interface{}{}
	for _, command := range commands {
		addHookCommand(hooks, "SessionStart", command)
	}
	return hooks
}

// TestInstallClaudeLeavesBdPrimeToTheRootLocalHook pins a project install
// from a repository subdirectory or linked worktree, where Claude Code loads
// the repository root's .claude/settings.local.json beside the directory's
// own settings. When that file has a bd prime hook, such as the bare one an
// older install wrote there, and the project's .claude/settings.json has
// none, the install adds no second hook to run beside it, names the hook
// that runs, and --check reports that hook alone. A hook in the directory's
// own .claude/settings.local.json stays too, since no hook takes its place:
// when it runs a different command from the root's, the install and --check
// report the double run and name it as the one to remove, rather than
// suggesting or reporting a migration. When the project's settings hold an
// older bare hook, which teammates rely on, the install still replaces it,
// and it and --check report the double run this leaves, naming the root's
// hook as the one to remove.
func TestInstallClaudeLeavesBdPrimeToTheRootLocalHook(t *testing.T) {
	stubDetectRenderOpts(t)

	t.Run("no project hook", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.localSettingsRoot = filepath.Dir(env.projectDir)
		rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
		hooks := primeHooks("bd prime")
		addHookCommand(hooks, "PreCompact", "bd prime")
		writeSettings(t, rootLocal, map[string]interface{}{"hooks": hooks})

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		out := stdout.String()
		want := "✓ bd prime already runs here from the hook in " + rootLocal + " — skipping\n"
		if !strings.Contains(out, want) {
			t.Errorf("want %q, got:\n%s", want, out)
		}
		for _, unwanted := range []string{"Registered SessionStart hook", "runs twice", "Settings: "} {
			if strings.Contains(out, unwanted) {
				t.Errorf("install printed %q:\n%s", unwanted, out)
			}
		}
		if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
			t.Errorf("install wrote %s beside the root's hook (stat err %v):\n%s", projectSettingsPath(env.projectDir), err, out)
		}
		if !hasBeadsHooks(rootLocal) {
			t.Errorf("the hook in %s is gone:\n%s", rootLocal, out)
		}

		stdout.Reset()
		if err := checkClaude(env); err != nil {
			t.Fatalf("--check: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "✓ Project hooks installed (legacy): "+rootLocal+"\n") {
			t.Errorf("--check should report the hook in %s:\n%s", rootLocal, stdout.String())
		}
		if strings.Contains(stdout.String(), "runs twice") || strings.Contains(stdout.String(), "migrate") {
			t.Errorf("--check should report the hook alone:\n%s", stdout.String())
		}
	})

	t.Run("local hook beside the root's", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.localSettingsRoot = filepath.Dir(env.projectDir)
		rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
		local := legacyProjectSettingsPath(env.projectDir)
		rootHooks := primeHooks("bd prime")
		addHookCommand(rootHooks, "PreCompact", "bd prime")
		writeSettings(t, rootLocal, map[string]interface{}{"hooks": rootHooks})
		writeSettings(t, local, map[string]interface{}{"hooks": primeHooks("bd prime --hook-json")})
		localBefore, err := os.ReadFile(local)
		if err != nil {
			t.Fatalf("read %s: %v", local, err)
		}
		twice := "⚠ bd prime runs twice per session here\n  The hooks in " + local + " and " + rootLocal +
			" run different bd prime commands, and Claude Code runs both\n  To run it once: remove the bd prime hook from " + local + "\n"

		if err := checkClaude(env); errors.Is(err, errClaudeHooksMissing) {
			t.Fatalf("--check reported missing hooks:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "✓ Project hooks installed (legacy): "+local+"\n") || !strings.Contains(stdout.String(), twice) {
			t.Errorf("--check should report the hook in %s and the double run, %q:\n%s", local, twice, stdout.String())
		}
		if strings.Contains(stdout.String(), "migrate") {
			t.Errorf("--check suggested a migration the install does not make:\n%s", stdout.String())
		}

		stdout.Reset()
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		out := stdout.String()
		for _, want := range []string{"✓ bd prime already runs here from the hook in " + rootLocal + " — skipping\n", twice} {
			if !strings.Contains(out, want) {
				t.Errorf("want %q, got:\n%s", want, out)
			}
		}
		for _, unwanted := range []string{"Migrated", "Removed", "Registered SessionStart hook"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("install printed %q:\n%s", unwanted, out)
			}
		}
		if localAfter, err := os.ReadFile(local); err != nil || !bytes.Equal(localAfter, localBefore) {
			t.Errorf("install changed %s (read err %v):\n%s\n%s", local, err, localAfter, out)
		}
		if !hasBeadsHooks(rootLocal) {
			t.Errorf("the hook in %s is gone:\n%s", rootLocal, out)
		}
		if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
			t.Errorf("install wrote %s (stat err %v):\n%s", projectSettingsPath(env.projectDir), err, out)
		}
	})

	t.Run("older bare project hook", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		env.localSettingsRoot = filepath.Dir(env.projectDir)
		rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
		project := projectSettingsPath(env.projectDir)
		writeSettings(t, rootLocal, map[string]interface{}{"hooks": primeHooks("bd prime")})
		writeSettings(t, project, map[string]interface{}{"hooks": primeHooks("bd prime")})

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		data, err := os.ReadFile(project)
		if err != nil {
			t.Fatalf("read project settings: %v", err)
		}
		if strings.Contains(string(data), `"bd prime"`) || !strings.Contains(string(data), "bd prime --hook-json") {
			t.Errorf("%s should hold the current hook in place of the old one:\n%s\n%s", project, data, stdout.String())
		}
		twice := "⚠ bd prime runs twice per session here\n  The hooks in " + project + " and " + rootLocal +
			" run different bd prime commands, and Claude Code runs both\n  To run it once: remove the bd prime hook from " + rootLocal + "\n"
		if !strings.Contains(stdout.String(), twice) {
			t.Errorf("the install should report the double run, %q:\n%s", twice, stdout.String())
		}
		stdout.Reset()
		if err := checkClaude(env); err != nil {
			t.Fatalf("--check: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), twice) {
			t.Errorf("--check should report the double run, %q:\n%s", twice, stdout.String())
		}
	})
}

// TestInstallClaudeAtTheRepositoryRootMigratesTheLocalHook pins a project
// install at the repository root, where defaultClaudeEnv sets
// localSettingsRoot to the project directory: the local settings Claude Code
// reads there are the directory's own .claude/settings.local.json, not a
// second, root-local file. --check suggests migrating its hook, the install
// moves it to .claude/settings.json, and --remove names the file once.
func TestInstallClaudeAtTheRepositoryRootMigratesTheLocalHook(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	env.localSettingsRoot = env.projectDir
	project := projectSettingsPath(env.projectDir)
	local := legacyProjectSettingsPath(env.projectDir)
	hooks := primeHooks("bd prime")
	addHookCommand(hooks, "PreCompact", "bd prime")
	writeSettings(t, local, map[string]interface{}{"hooks": hooks})

	_ = checkClaude(env)
	if !strings.Contains(stdout.String(), "Consider running 'bd setup claude' to migrate") {
		t.Errorf("--check should suggest migrating the hook in %s:\n%s", local, stdout.String())
	}

	stdout.Reset()
	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	out := stdout.String()
	if !hasBeadsHooks(project) || hasBeadsHooks(local) {
		t.Errorf("want the hook moved from %s to %s:\n%s", local, project, out)
	}
	if !strings.Contains(out, "✓ Migrated hooks from "+local+"\n") || strings.Contains(out, "already runs here") {
		t.Errorf("install should report the migration:\n%s", out)
	}

	writeSettings(t, local, map[string]interface{}{"hooks": primeHooks("bd prime")})
	stdout.Reset()
	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v\n%s", err, stdout.String())
	}
	if got := strings.Count(stdout.String(), "✓ Removed bd prime hooks from "+local+"\n"); got != 1 {
		t.Errorf("--remove named %s %d times, want once:\n%s", local, got, stdout.String())
	}
	if hasBeadsHooks(project) || hasBeadsHooks(local) {
		t.Errorf("--remove left a bd prime hook:\n%s", stdout.String())
	}
}

// TestInstallClaudeGlobalIgnoresTheRootLocalHook pins that only a project
// install leaves bd prime to a hook in the repository root's local
// settings. A global install's hook is for every project, so run from a
// subdirectory beside such a hook it still registers its own.
func TestInstallClaudeGlobalIgnoresTheRootLocalHook(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	env.localSettingsRoot = filepath.Dir(env.projectDir)
	rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
	writeSettings(t, rootLocal, map[string]interface{}{"hooks": primeHooks("bd prime")})

	if err := installClaude(env, true, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "✓ Registered SessionStart hook\n") || strings.Contains(out, "already runs here") {
		t.Errorf("a global install should register its hook:\n%s", out)
	}
	if !hasBeadsHooks(globalSettingsPath(env.homeDir)) {
		t.Errorf("no hook in %s:\n%s", globalSettingsPath(env.homeDir), out)
	}
	if !hasBeadsHooks(rootLocal) {
		t.Errorf("a global install changed %s:\n%s", rootLocal, out)
	}
}

// TestCheckClaudeReportsDifferentSettingsHooks pins the double run of two
// project settings hooks with the plugin off. Claude Code runs a hook
// defined in several settings files once, but each different one, so --check
// reports two files whose bd prime hooks differ, also when the file it
// reports as installed hides the other, and names the one to remove: a
// local file rather than the project's shared settings, and the current
// directory's rather than the repository root's.
func TestCheckClaudeReportsDifferentSettingsHooks(t *testing.T) {
	stubDetectRenderOpts(t)
	project := func(env claudeEnv) string { return projectSettingsPath(env.projectDir) }
	local := func(env claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }
	rootLocal := func(env claudeEnv) string { return legacyProjectSettingsPath(env.localSettingsRoot) }
	type hook struct {
		path    func(claudeEnv) string
		command string
	}
	cases := []struct {
		name  string
		hooks []hook
		// first and second are the files the warning names and remove the
		// one its remedy names; nil when no double run is reported.
		first, second, remove func(claudeEnv) string
	}{
		{"project hook beside an older root-local one", []hook{{project, "bd prime --hook-json"}, {rootLocal, "bd prime"}}, project, rootLocal, rootLocal},
		{"project hook beside an older local one", []hook{{project, "bd prime --hook-json"}, {local, "bd prime"}}, project, local, local},
		{"local hook beside a different root-local one", []hook{{local, "bd prime"}, {rootLocal, "bd prime --stealth --hook-json"}}, local, rootLocal, local},
		{"identical project and root-local hooks", []hook{{project, "bd prime --hook-json"}, {rootLocal, "bd prime --hook-json"}}, nil, nil, nil},
		{"root-local hook alone", []hook{{rootLocal, "bd prime"}}, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			env.localSettingsRoot = filepath.Dir(env.projectDir)
			for _, h := range tc.hooks {
				writeSettings(t, h.path(env), map[string]interface{}{"hooks": primeHooks(h.command)})
			}
			if err := checkClaude(env); errors.Is(err, errClaudeHooksMissing) {
				t.Fatalf("--check reported missing hooks:\n%s", stdout.String())
			}
			out := stdout.String()
			if tc.remove == nil {
				if strings.Contains(out, "runs twice") {
					t.Errorf("--check reported a double run:\n%s", out)
				}
				return
			}
			want := "⚠ bd prime runs twice per session here\n  The hooks in " + tc.first(env) + " and " + tc.second(env) +
				" run different bd prime commands, and Claude Code runs both\n  To run it once: remove the bd prime hook from " + tc.remove(env) + "\n"
			if !strings.Contains(out, want) {
				t.Errorf("want %q, got:\n%s", want, out)
			}
		})
	}
}

// TestRemoveClaudeRemovesTheRootLocalHook pins a project removal from a
// repository subdirectory or linked worktree: Claude Code loads the
// repository root's .claude/settings.local.json there, so the removal takes
// the bd prime hooks out of that file too, keeps its other hooks and names
// it, and --check then finds no hook rather than the one the removal left.
func TestRemoveClaudeRemovesTheRootLocalHook(t *testing.T) {
	env, stdout, _ := newClaudeTestEnv(t)
	env.localSettingsRoot = filepath.Dir(env.projectDir)
	rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
	hooks := primeHooks("bd prime")
	addHookCommand(hooks, "PreCompact", "bd prime")
	addHookCommand(hooks, "Stop", "notify-send done")
	writeSettings(t, rootLocal, map[string]interface{}{"hooks": hooks})

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v\n%s", err, stdout.String())
	}
	data, err := os.ReadFile(rootLocal)
	if err != nil {
		t.Fatalf("read %s: %v", rootLocal, err)
	}
	if hasBeadsHooks(rootLocal) || !strings.Contains(string(data), "notify-send done") {
		t.Errorf("want only the bd prime hooks removed from %s, got:\n%s\n%s", rootLocal, data, stdout.String())
	}
	if !strings.Contains(stdout.String(), "✓ Removed bd prime hooks from "+rootLocal+"\n") {
		t.Errorf("the removal should name %s:\n%s", rootLocal, stdout.String())
	}

	stdout.Reset()
	if err := checkClaude(env); !errors.Is(err, errClaudeHooksMissing) {
		t.Fatalf("--check after the removal should report missing hooks, got %v:\n%s", err, stdout.String())
	}
}

// TestRemoveClaudeWarnsWhenALocalHookStays pins a project removal that
// cannot write a local settings file it found a bd prime hook in, such as a
// symlinked one, which bd does not write through: it warns, naming the
// file, rather than reporting the hook removed.
func TestRemoveClaudeWarnsWhenALocalHookStays(t *testing.T) {
	env, stdout, stderr := newClaudeTestEnv(t)
	env.localSettingsRoot = filepath.Dir(env.projectDir)
	rootLocal := legacyProjectSettingsPath(env.localSettingsRoot)
	target := filepath.Join(t.TempDir(), "settings.local.json")
	writeSettings(t, target, map[string]interface{}{"hooks": primeHooks("bd prime")})
	if err := os.MkdirAll(filepath.Dir(rootLocal), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, rootLocal); err != nil {
		t.Skipf("symlink: %v", err)
	}

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("removeClaude: %v\n%s", err, stdout.String())
	}
	want := "Warning: failed to remove bd prime hooks from " + rootLocal + ": "
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("want %q on stderr, got:\n%s", want, stderr.String())
	}
	if strings.Contains(stdout.String(), "✓ Removed bd prime hooks from "+rootLocal) {
		t.Errorf("the removal reported the hook removed:\n%s", stdout.String())
	}
	if !hasBeadsHooks(target) {
		t.Errorf("the hook in %s is gone", target)
	}
}

// TestInstallClaudePersonalPluginKeepsSharedLegacyHook pins what a project
// install does with an older bare bd prime hook in the shared
// .claude/settings.json when the beads plugin is enabled. Enabled in a
// shared file (the project's own or managed settings), the plugin runs for
// everyone, so the old hook is dropped, even when a personal file enables it
// for this user too. Enabled only in a personal file (the user's settings
// or a local one), teammates without the plugin still need a hook from the
// shared file, so the install replaces the old hook with the current one and
// reports the double run this leaves for this user.
func TestInstallClaudePersonalPluginKeepsSharedLegacyHook(t *testing.T) {
	stubDetectRenderOpts(t)

	cases := []struct {
		name     string
		plugin   func(env *claudeEnv) string
		wantHook bool
		// sharedToo also enables the plugin in the shared settings.
		sharedToo bool
	}{
		{"user settings", func(env *claudeEnv) string { return globalSettingsPath(env.homeDir) }, true, false},
		{"local settings", func(env *claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }, true, false},
		{"local settings at the repository root", func(env *claudeEnv) string {
			env.localSettingsRoot = filepath.Dir(env.projectDir)
			return legacyProjectSettingsPath(env.localSettingsRoot)
		}, true, false},
		{"managed settings", func(env *claudeEnv) string { return filepath.Join(env.managedDir, "managed-settings.json") }, false, false},
		{"local and project settings", func(env *claudeEnv) string { return legacyProjectSettingsPath(env.projectDir) }, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stdout, _ := newClaudeTestEnv(t)
			env.managedDir = filepath.Join(t.TempDir(), "managed")
			shared := projectSettingsPath(env.projectDir)
			legacy := map[string]interface{}{"hooks": map[string]interface{}{}}
			if tc.sharedToo {
				legacy = settingsWithPlugin()
				legacy["hooks"] = map[string]interface{}{}
			}
			addHookCommand(legacy["hooks"].(map[string]interface{}), "SessionStart", "bd prime")
			writeSettings(t, shared, legacy)
			writeSettings(t, tc.plugin(&env), settingsWithPlugin())

			if err := installClaude(env, false, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			out := stdout.String()
			data, err := os.ReadFile(shared)
			if err != nil {
				t.Fatalf("read shared settings: %v", err)
			}
			if strings.Contains(string(data), `"bd prime"`) {
				t.Errorf("the old bare hook stayed in %s:\n%s", shared, data)
			}
			if got := strings.Contains(string(data), "bd prime --hook-json"); got != tc.wantHook {
				t.Errorf("%s has the current hook = %v, want %v:\n%s\n%s", shared, got, tc.wantHook, data, out)
			}
			if got := strings.Contains(out, "plugin-managed, skipping"); got == tc.wantHook {
				t.Errorf("plugin-managed skip reported = %v, want %v:\n%s", got, !tc.wantHook, out)
			}
			if got := strings.Contains(out, "⚠ bd prime runs twice per session here"); got != tc.wantHook {
				t.Errorf("double run reported = %v, want %v:\n%s", got, tc.wantHook, out)
			}
		})
	}

	t.Run("project settings", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		settings := settingsWithPlugin()
		settings["hooks"] = map[string]interface{}{}
		addHookCommand(settings["hooks"].(map[string]interface{}), "SessionStart", "bd prime")
		writeSettings(t, projectSettingsPath(env.projectDir), settings)
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		data, err := os.ReadFile(projectSettingsPath(env.projectDir))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(string(data), "bd prime") {
			t.Errorf("a plugin the shared settings enable should replace the old hook, not sit beside a new one:\n%s\n%s", data, stdout.String())
		}
	})
}

// TestInstallClaudePluginManagedLeavesSettingsAlone pins that an install
// which leaves the hooks to the plugin writes no hook, so it adds no empty
// "hooks" object to a settings file and creates none, nor the .claude
// directory one would go in.
func TestInstallClaudePluginManagedLeavesSettingsAlone(t *testing.T) {
	stubDetectRenderOpts(t)
	const pluginSettings = "{\n  \"enabledPlugins\": {\n    \"beads@beads-marketplace\": true\n  }\n}\n"

	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%v settings keep their bytes", global), func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			path := projectSettingsPath(env.projectDir)
			if global {
				path = globalSettingsPath(env.homeDir)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(pluginSettings), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := installClaude(env, global, false); err != nil {
				t.Fatalf("installClaude: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(data) != pluginSettings {
				t.Errorf("%s changed:\n%s", path, data)
			}
		})
	}

	t.Run("no settings file created", func(t *testing.T) {
		env, stdout, _ := newClaudeTestEnv(t)
		writeSettings(t, globalSettingsPath(env.homeDir), settingsWithPlugin())
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
			t.Errorf("project settings created (stat err %v)", err)
		}
		if _, err := os.Stat(filepath.Dir(projectSettingsPath(env.projectDir))); !os.IsNotExist(err) {
			t.Errorf("empty .claude directory created (stat err %v)", err)
		}
		if strings.Contains(stdout.String(), "Settings: ") {
			t.Errorf("summary names a settings file the install did not write:\n%s", stdout.String())
		}
	})

	// A global install still creates ~/.claude when no file there enables
	// the plugin, because its CLAUDE.md section goes there.
	t.Run("global install creates the user-level directory for CLAUDE.md", func(t *testing.T) {
		env, stdout, stderr := newClaudeTestEnv(t)
		env.managedDir = filepath.Join(t.TempDir(), "managed")
		writeSettings(t, filepath.Join(env.managedDir, "managed-settings.json"), settingsWithPlugin())
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		if _, err := os.Stat(globalClaudeInstructionsPath(env.homeDir)); err != nil {
			t.Errorf("user-level CLAUDE.md not written: %v\n%s\n%s", err, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(globalSettingsPath(env.homeDir)); !os.IsNotExist(err) {
			t.Errorf("user settings created (stat err %v)", err)
		}
	})
}

// TestInstallClaudePluginManagedRemovesTheLocalHook pins a project install
// that leaves the hooks to the beads plugin while the current directory's
// .claude/settings.local.json still has the bd prime hooks an older install
// wrote there. The plugin runs bd prime here, so the install removes them,
// keeps the file's other hooks and says why, rather than reporting a
// migration to a .claude/settings.json it adds no hook to; --check then
// reports the plugin's hooks alone.
func TestInstallClaudePluginManagedRemovesTheLocalHook(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newClaudeTestEnv(t)
	project := projectSettingsPath(env.projectDir)
	local := legacyProjectSettingsPath(env.projectDir)
	writeSettings(t, project, settingsWithPlugin())
	hooks := primeHooks("bd prime")
	addHookCommand(hooks, "PreCompact", "bd prime")
	addHookCommand(hooks, "Stop", "notify-send done")
	writeSettings(t, local, map[string]interface{}{"hooks": hooks})

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	out := stdout.String()
	want := "✓ Removed bd prime hooks from " + local + ": the beads plugin runs bd prime here\n"
	if !strings.Contains(out, want) {
		t.Errorf("want %q, got:\n%s", want, out)
	}
	if strings.Contains(out, "Migrated") || strings.Contains(out, "runs twice") {
		t.Errorf("install misreported the hooks it removed:\n%s", out)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("read %s: %v", local, err)
	}
	if hasBeadsHooks(local) || !strings.Contains(string(data), "notify-send done") {
		t.Errorf("want only the bd prime hooks removed from %s, got:\n%s\n%s", local, data, out)
	}
	if hasBeadsHooks(project) {
		t.Errorf("install added a hook to %s beside the plugin:\n%s", project, out)
	}

	stdout.Reset()
	if err := checkClaude(env); err != nil {
		t.Fatalf("--check: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "✓ Hooks provided by beads plugin (plugin-managed): "+project+"\n") || strings.Contains(stdout.String(), "runs twice") {
		t.Errorf("--check should report the plugin's hooks alone:\n%s", stdout.String())
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
		{
			// A file that documents the pattern is not using it. Claude Code
			// does not expand an @-import shown as code, and treating this as a
			// stub relocates the managed block and deletes it from here.
			name:       "directive inside a fenced code block",
			content:    "# Claude Code\n\nTo adopt the redirect, put this in CLAUDE.md:\n\n```\n@AGENTS.md\n```\n\nFull instructions follow.\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			name:       "directive inside a fence with an info string",
			content:    "# Claude Code\n\n```markdown\n@AGENTS.md\n```\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			name:       "directive inside a tilde fence",
			content:    "# Claude Code\n\n~~~\n@AGENTS.md\n~~~\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			// A backtick run inside a tilde block is content, not a terminator;
			// if it closed the block the directive after it would leak through.
			name:       "unmatched fence character does not end the block",
			content:    "# Claude Code\n\n~~~\n```\n@AGENTS.md\n~~~\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			// The whole point is that fencing suppresses only what is fenced.
			name:       "real directive after a fenced example still counts",
			content:    "# Claude Code\n\n```\n@OTHER.md\n```\n\n@AGENTS.md\n",
			agentsFile: "AGENTS.md",
			want:       true,
		},
		{
			// An unterminated fence swallows the rest of the file. That is what
			// a Markdown renderer does too, so the conservative reading (not a
			// stub) is the correct one rather than an accident.
			name:       "unclosed fence suppresses the rest of the file",
			content:    "# Claude Code\n\n```\n@AGENTS.md\n",
			agentsFile: "AGENTS.md",
			want:       false,
		},
		{
			// Four-space indentation is deliberately NOT treated as code: it is
			// equally a list continuation, which is a plausible home for a real
			// directive.
			name:       "indented directive still counts",
			content:    "# Claude Code\n\n- Shared instructions:\n\n    @AGENTS.md\n",
			agentsFile: "AGENTS.md",
			want:       true,
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

// The unit cases above pin the predicate; this pins what the predicate costs
// when it is wrong. A CLAUDE.md that is authoritative but happens to SHOW the
// redirect directive in a fenced example is not a stub, and misreading it is
// destructive rather than merely misrouted: the redirect activates and
// stripStaleClaudeBlock then removes the managed block from the file that was
// actually in charge of it.
func TestInstallClaudeKeepsBlockWhenTheDirectiveIsOnlyDocumented(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, _ := newClaudeTestEnv(t)

	// Authoritative CLAUDE.md that documents the stub pattern rather than using it.
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	documented := "# Claude Code\n\nFull project instructions live here.\n\n" +
		"To redirect this file to AGENTS.md instead, reduce it to:\n\n" +
		"```markdown\n@AGENTS.md\n```\n\n" +
		"Until then, this file is authoritative.\n"
	if err := os.WriteFile(claudePath, []byte(documented), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	// AGENTS.md exists, so the redirect would fire if the stub check said yes.
	agentsPath := filepath.Join(env.projectDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# Agent Instructions\n\nShared notes.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}

	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("CLAUDE.md is authoritative and must carry the beads block; a fenced example redirected it away:\n%s", claudeData)
	}
	if !strings.Contains(string(claudeData), "Until then, this file is authoritative.") {
		t.Fatalf("CLAUDE.md lost its own content:\n%s", claudeData)
	}

	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(agentsData), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("AGENTS.md must not receive the block when CLAUDE.md is not a stub:\n%s", agentsData)
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

// countingClaudeEnv wraps env.writeFile with a per-path write counter so tests
// can assert that an unchanged settings file is not rewritten.
func countingClaudeEnv(env claudeEnv, counts map[string]int) claudeEnv {
	inner := env.writeFile
	env.writeFile = func(path string, data []byte) error {
		counts[path]++
		return inner(path, data)
	}
	return env
}

func assertTrailingNewline(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Errorf("%s should end with a trailing newline, got %q", path, tailOf(data))
	}
}

func tailOf(data []byte) string {
	if len(data) > 24 {
		return string(data[len(data)-24:])
	}
	return string(data)
}

// GH#5693: bd setup claude must leave settings.json POSIX-clean (trailing
// newline) instead of stripping the newline json.MarshalIndent omits.
func TestInstallClaudeSettingsEndsWithTrailingNewline(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		assertTrailingNewline(t, projectSettingsPath(env.projectDir))
	})
	t.Run("global", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		if err := installClaude(env, true, false); err != nil {
			t.Fatalf("installClaude: %v", err)
		}
		assertTrailingNewline(t, globalSettingsPath(env.homeDir))
	})
}

// GH#5693: a second bd setup claude with nothing to change must not rewrite
// settings.json.
func TestInstallClaudeSkipsRewriteWhenSettingsUnchanged(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)
	counts := map[string]int{}
	env = countingClaudeEnv(env, counts)
	settingsPath := projectSettingsPath(env.projectDir)

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("first installClaude: %v", err)
	}
	if counts[settingsPath] != 1 {
		t.Fatalf("first install should write settings once, got %d", counts[settingsPath])
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("second installClaude: %v", err)
	}
	if got := counts[settingsPath]; got != 1 {
		t.Errorf("second install rewrote unchanged settings: %d total writes, want 1", got)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("re-read settings: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("second install changed settings content:\nbefore=%q\nafter=%q", before, after)
	}
}

// GH#5693: the same two properties must hold for the removeClaude write path,
// which marshals and writes settings.json through a separate call site.
func TestRemoveClaudeSettingsNewlineAndSkipsUnchangedRewrite(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)
	counts := map[string]int{}
	env = countingClaudeEnv(env, counts)
	settingsPath := projectSettingsPath(env.projectDir)

	if err := installClaude(env, false, false); err != nil {
		t.Fatalf("installClaude: %v", err)
	}
	writesAfterInstall := counts[settingsPath]

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("first removeClaude: %v", err)
	}
	assertTrailingNewline(t, settingsPath)
	writesAfterRemove := counts[settingsPath]
	if writesAfterRemove != writesAfterInstall+1 {
		t.Fatalf("first remove should write settings once, got %d writes (install left %d)",
			writesAfterRemove-writesAfterInstall, writesAfterInstall)
	}

	if err := removeClaude(env, false); err != nil {
		t.Fatalf("second removeClaude: %v", err)
	}
	if got := counts[settingsPath]; got != writesAfterRemove {
		t.Errorf("second remove rewrote unchanged settings: %d writes, want %d", got, writesAfterRemove)
	}
}

// GH#5693: the legacy settings.local.json migration writes are the two other
// marshal-and-write call sites in this file; they strip the newline too.
func TestClaudeLegacySettingsWritesEndWithNewline(t *testing.T) {
	legacyWithBeadsHook := func() map[string]interface{} {
		return map[string]interface{}{
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
		}
	}

	// The second install writes settings.local.json zero times, but not via
	// writeSettingsIfChanged: installClaude's migration
	// (removeLocalClaudeHooks) writes only after removing a bd prime hook,
	// and none is left once the first install has migrated them away, so it
	// returns before the write. That is also why the skip inside
	// writeSettingsIfChanged is unreachable on this call site — whenever the
	// write is reached, a hook was just removed, so the marshaled bytes
	// always differ.
	// What this subtest pins is therefore the user-visible property (a repeat
	// bd setup claude leaves settings.local.json alone) plus the newline, not
	// the no-op-skip helper.
	t.Run("install migration then no further legacy writes", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		counts := map[string]int{}
		env = countingClaudeEnv(env, counts)
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		writeSettings(t, legacyPath, legacyWithBeadsHook())

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("first installClaude: %v", err)
		}
		if hasBeadsHooks(legacyPath) {
			t.Fatal("legacy beads hooks should have been migrated away")
		}
		assertTrailingNewline(t, legacyPath)
		writesAfterFirst := counts[legacyPath]
		if writesAfterFirst != 1 {
			t.Fatalf("first install should write legacy settings once, got %d", writesAfterFirst)
		}
		before, err := os.ReadFile(legacyPath)
		if err != nil {
			t.Fatalf("read legacy settings: %v", err)
		}

		if err := installClaude(env, false, false); err != nil {
			t.Fatalf("second installClaude: %v", err)
		}
		if got := counts[legacyPath]; got != writesAfterFirst {
			t.Errorf("second install rewrote migrated legacy settings: %d writes, want %d", got, writesAfterFirst)
		}
		after, err := os.ReadFile(legacyPath)
		if err != nil {
			t.Fatalf("re-read legacy settings: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("second install changed legacy settings content:\nbefore=%q\nafter=%q", before, after)
		}
	})

	t.Run("remove cleanup", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		counts := map[string]int{}
		env = countingClaudeEnv(env, counts)
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		writeSettings(t, legacyPath, legacyWithBeadsHook())

		if err := removeClaude(env, false); err != nil {
			t.Fatalf("first removeClaude: %v", err)
		}
		assertTrailingNewline(t, legacyPath)
		writesAfterFirst := counts[legacyPath]
		if writesAfterFirst != 1 {
			t.Fatalf("first remove should write legacy settings once, got %d", writesAfterFirst)
		}

		if err := removeClaude(env, false); err != nil {
			t.Fatalf("second removeClaude: %v", err)
		}
		if got := counts[legacyPath]; got != writesAfterFirst {
			t.Errorf("second remove rewrote unchanged legacy settings: %d writes, want %d", got, writesAfterFirst)
		}
	})
}
