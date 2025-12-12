package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

func TestAddAllowedTool(t *testing.T) {
	tests := []struct {
		name             string
		existingSettings map[string]interface{}
		tool             string
		wantAdded        bool
		wantLen          int
	}{
		{
			name:             "add tool to empty settings",
			existingSettings: make(map[string]interface{}),
			tool:             "Bash(bd *)",
			wantAdded:        true,
			wantLen:          1,
		},
		{
			name: "add tool to existing allowedTools",
			existingSettings: map[string]interface{}{
				"allowedTools": []interface{}{"Bash(git *)"},
			},
			tool:      "Bash(bd *)",
			wantAdded: true,
			wantLen:   2,
		},
		{
			name: "tool already exists",
			existingSettings: map[string]interface{}{
				"allowedTools": []interface{}{"Bash(bd *)"},
			},
			tool:      "Bash(bd *)",
			wantAdded: false,
			wantLen:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addAllowedTool(tt.existingSettings, tt.tool)
			if got != tt.wantAdded {
				t.Errorf("addAllowedTool() = %v, want %v", got, tt.wantAdded)
			}

			allowedTools, ok := tt.existingSettings["allowedTools"].([]interface{})
			if !ok {
				t.Fatal("allowedTools not found")
			}

			if len(allowedTools) != tt.wantLen {
				t.Errorf("Expected %d tools, got %d", tt.wantLen, len(allowedTools))
			}

			// Verify tool exists in list
			found := false
			for _, tool := range allowedTools {
				if tool == tt.tool {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Tool %q not found in allowedTools", tt.tool)
			}
		})
	}
}

func TestRemoveAllowedTool(t *testing.T) {
	tests := []struct {
		name             string
		existingSettings map[string]interface{}
		tool             string
		wantLen          int
	}{
		{
			name: "remove only tool",
			existingSettings: map[string]interface{}{
				"allowedTools": []interface{}{"Bash(bd *)"},
			},
			tool:    "Bash(bd *)",
			wantLen: 0,
		},
		{
			name: "remove one of multiple tools",
			existingSettings: map[string]interface{}{
				"allowedTools": []interface{}{"Bash(git *)", "Bash(bd *)", "Bash(npm *)"},
			},
			tool:    "Bash(bd *)",
			wantLen: 2,
		},
		{
			name: "remove non-existent tool",
			existingSettings: map[string]interface{}{
				"allowedTools": []interface{}{"Bash(git *)"},
			},
			tool:    "Bash(bd *)",
			wantLen: 1,
		},
		{
			name:             "remove from empty settings",
			existingSettings: make(map[string]interface{}),
			tool:             "Bash(bd *)",
			wantLen:          0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removeAllowedTool(tt.existingSettings, tt.tool)

			allowedTools, ok := tt.existingSettings["allowedTools"].([]interface{})
			if !ok {
				// If allowedTools doesn't exist, treat as empty
				if tt.wantLen != 0 {
					t.Errorf("Expected %d tools, got 0 (allowedTools not found)", tt.wantLen)
				}
				return
			}

			if len(allowedTools) != tt.wantLen {
				t.Errorf("Expected %d remaining tools, got %d", tt.wantLen, len(allowedTools))
			}

			// Verify tool is actually gone
			for _, tool := range allowedTools {
				if tool == tt.tool {
					t.Errorf("Tool %q still present after removal", tt.tool)
				}
			}
		})
	}
}
