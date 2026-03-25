package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCopilotTestEnv(t *testing.T) (copilotEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := copilotEnv{
		stdout:     stdout,
		stderr:     stderr,
		projectDir: projectDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}
	return env, stdout, stderr
}

func stubCopilotEnvProvider(t *testing.T, env copilotEnv, err error) {
	t.Helper()
	orig := copilotEnvProvider
	copilotEnvProvider = func() (copilotEnv, error) {
		if err != nil {
			return copilotEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { copilotEnvProvider = orig })
}

func writeCopilotHooks(t *testing.T, path string, config map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal hooks: %v", err)
	}
	if err := atomicWriteFile(path, data); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
}

func TestAddCopilotHook(t *testing.T) {
	tests := []struct {
		name      string
		hooks     map[string]interface{}
		event     string
		command   string
		wantAdded bool
	}{
		{
			name:      "add hook to empty hooks",
			hooks:     make(map[string]interface{}),
			event:     "sessionStart",
			command:   "bd prime",
			wantAdded: true,
		},
		{
			name:      "add stealth hook to empty hooks",
			hooks:     make(map[string]interface{}),
			event:     "sessionStart",
			command:   "bd prime --stealth",
			wantAdded: true,
		},
		{
			name: "hook already exists",
			hooks: map[string]interface{}{
				"sessionStart": []interface{}{
					map[string]interface{}{
						"type":       "command",
						"bash":       "bd prime",
						"powershell": "bd prime",
						"timeoutSec": float64(10),
					},
				},
			},
			event:     "sessionStart",
			command:   "bd prime",
			wantAdded: false,
		},
		{
			name: "add alongside existing hook",
			hooks: map[string]interface{}{
				"sessionStart": []interface{}{
					map[string]interface{}{
						"type": "command",
						"bash": "other command",
					},
				},
			},
			event:     "sessionStart",
			command:   "bd prime",
			wantAdded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addCopilotHook(tt.hooks, tt.event, tt.command)
			if got != tt.wantAdded {
				t.Errorf("addCopilotHook() = %v, want %v", got, tt.wantAdded)
			}

			eventHooks, ok := tt.hooks[tt.event].([]interface{})
			if !ok {
				t.Fatal("Event hooks not found")
			}

			found := false
			for _, hook := range eventHooks {
				hookMap := hook.(map[string]interface{})
				if hookMap["bash"] == tt.command {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Hook command %q not found in event %q", tt.command, tt.event)
			}
		})
	}
}

func TestRemoveCopilotHook(t *testing.T) {
	tests := []struct {
		name          string
		hooks         map[string]interface{}
		event         string
		command       string
		wantRemaining int
	}{
		{
			name: "remove only hook",
			hooks: map[string]interface{}{
				"sessionStart": []interface{}{
					map[string]interface{}{
						"type": "command",
						"bash": "bd prime",
					},
				},
			},
			event:         "sessionStart",
			command:       "bd prime",
			wantRemaining: 0,
		},
		{
			name: "remove stealth hook",
			hooks: map[string]interface{}{
				"sessionStart": []interface{}{
					map[string]interface{}{
						"type": "command",
						"bash": "bd prime --stealth",
					},
				},
			},
			event:         "sessionStart",
			command:       "bd prime --stealth",
			wantRemaining: 0,
		},
		{
			name: "remove one of multiple hooks",
			hooks: map[string]interface{}{
				"sessionStart": []interface{}{
					map[string]interface{}{
						"type": "command",
						"bash": "other command",
					},
					map[string]interface{}{
						"type": "command",
						"bash": "bd prime",
					},
				},
			},
			event:         "sessionStart",
			command:       "bd prime",
			wantRemaining: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removeCopilotHook(tt.hooks, tt.event, tt.command)

			eventHooks, ok := tt.hooks[tt.event].([]interface{})
			if !ok && tt.wantRemaining > 0 {
				t.Fatal("Event hooks not found")
			}

			if len(eventHooks) != tt.wantRemaining {
				t.Errorf("Expected %d remaining hooks, got %d", tt.wantRemaining, len(eventHooks))
			}

			for _, hook := range eventHooks {
				hookMap := hook.(map[string]interface{})
				if hookMap["bash"] == tt.command {
					t.Errorf("Hook command %q still present after removal", tt.command)
				}
			}
		})
	}
}

func TestRemoveCopilotHookDeletesEmptyKey(t *testing.T) {
	hooks := map[string]interface{}{
		"sessionStart": []interface{}{
			map[string]interface{}{
				"type": "command",
				"bash": "bd prime",
			},
		},
	}

	removeCopilotHook(hooks, "sessionStart", "bd prime")

	if _, exists := hooks["sessionStart"]; exists {
		t.Error("Expected sessionStart key to be deleted after removing all hooks")
	}

	data, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("JSON contains null: %s", data)
	}
}

func TestInstallCopilotFresh(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	err := installCopilot(env, false)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "Registered sessionStart hook") {
		t.Error("Expected sessionStart hook to be registered")
	}
	if !strings.Contains(stdout.String(), "GitHub Copilot integration installed") {
		t.Error("Expected success message")
	}

	// Verify hooks file
	hooksPath := copilotHooksPath(env.projectDir)
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}

	if config["version"] != float64(1) {
		t.Errorf("Expected version 1, got %v", config["version"])
	}

	hooks := config["hooks"].(map[string]interface{})
	sessionStart := hooks["sessionStart"].([]interface{})
	if len(sessionStart) != 1 {
		t.Fatalf("Expected 1 sessionStart hook, got %d", len(sessionStart))
	}

	hook := sessionStart[0].(map[string]interface{})
	if hook["bash"] != "bd prime" {
		t.Errorf("Expected bash command 'bd prime', got %v", hook["bash"])
	}
	if hook["powershell"] != "bd prime" {
		t.Errorf("Expected powershell command 'bd prime', got %v", hook["powershell"])
	}
	if hook["timeoutSec"] != float64(10) {
		t.Errorf("Expected timeoutSec 10, got %v", hook["timeoutSec"])
	}
}

func TestInstallCopilotStealth(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	err := installCopilot(env, true)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	hooksPath := copilotHooksPath(env.projectDir)
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}

	hooks := config["hooks"].(map[string]interface{})
	sessionStart := hooks["sessionStart"].([]interface{})
	hook := sessionStart[0].(map[string]interface{})
	if hook["bash"] != "bd prime --stealth" {
		t.Errorf("Expected stealth command, got %v", hook["bash"])
	}
}

func TestInstallCopilotIdempotent(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	// Install twice
	if err := installCopilot(env, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installCopilot(env, false); err != nil {
		t.Fatalf("second install: %v", err)
	}

	// Should still have only one hook
	hooksPath := copilotHooksPath(env.projectDir)
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}

	hooks := config["hooks"].(map[string]interface{})
	sessionStart := hooks["sessionStart"].([]interface{})
	if len(sessionStart) != 1 {
		t.Errorf("Expected 1 hook after idempotent install, got %d", len(sessionStart))
	}
}

func TestInstallCopilotPreservesExistingHooks(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	// Pre-populate with existing hooks
	hooksPath := copilotHooksPath(env.projectDir)
	writeCopilotHooks(t, hooksPath, map[string]interface{}{
		"version": float64(1),
		"hooks": map[string]interface{}{
			"preToolUse": []interface{}{
				map[string]interface{}{
					"type": "command",
					"bash": "./scripts/security-check.sh",
				},
			},
		},
	})

	if err := installCopilot(env, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}

	hooks := config["hooks"].(map[string]interface{})

	// Existing preToolUse hook should be preserved
	preToolUse, ok := hooks["preToolUse"].([]interface{})
	if !ok || len(preToolUse) != 1 {
		t.Error("Expected existing preToolUse hook to be preserved")
	}

	// New sessionStart hook should be added
	sessionStart, ok := hooks["sessionStart"].([]interface{})
	if !ok || len(sessionStart) != 1 {
		t.Error("Expected sessionStart hook to be added")
	}
}

func TestCheckCopilotInstalled(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	// Install first
	if err := installCopilot(env, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	stdout.Reset()

	err := checkCopilot(env)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "✓ Hooks installed") {
		t.Error("Expected hooks installed message")
	}
}

func TestCheckCopilotNotInstalled(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	err := checkCopilot(env)
	if err != errCopilotHooksMissing {
		t.Errorf("Expected errCopilotHooksMissing, got %v", err)
	}

	if !strings.Contains(stdout.String(), "✗ No hooks installed") {
		t.Error("Expected not-installed message")
	}
}

func TestRemoveCopilotHooks(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	// Install then remove
	if err := installCopilot(env, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	stdout.Reset()

	if err := removeCopilot(env); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "GitHub Copilot hooks removed") {
		t.Error("Expected removal message")
	}

	// Hooks file should be deleted (no other hooks remain)
	hooksPath := copilotHooksPath(env.projectDir)
	if FileExists(hooksPath) {
		t.Error("Expected hooks file to be removed when empty")
	}
}

func TestRemoveCopilotPreservesOtherHooks(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	// Create hooks file with both beads and user hooks
	hooksPath := copilotHooksPath(env.projectDir)
	writeCopilotHooks(t, hooksPath, map[string]interface{}{
		"version": float64(1),
		"hooks": map[string]interface{}{
			"sessionStart": []interface{}{
				map[string]interface{}{
					"type": "command",
					"bash": "bd prime",
				},
			},
			"preToolUse": []interface{}{
				map[string]interface{}{
					"type": "command",
					"bash": "./scripts/security-check.sh",
				},
			},
		},
	})

	if err := removeCopilot(env); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	// File should still exist with preToolUse hook
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}

	hooks := config["hooks"].(map[string]interface{})
	if _, exists := hooks["sessionStart"]; exists {
		t.Error("Expected sessionStart to be removed")
	}

	preToolUse, ok := hooks["preToolUse"].([]interface{})
	if !ok || len(preToolUse) != 1 {
		t.Error("Expected preToolUse hook to be preserved")
	}
}

func TestHasCopilotBeadsHooks(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name   string
		config map[string]interface{}
		want   bool
	}{
		{
			name: "has bd prime hook",
			config: map[string]interface{}{
				"version": float64(1),
				"hooks": map[string]interface{}{
					"sessionStart": []interface{}{
						map[string]interface{}{
							"type": "command",
							"bash": "bd prime",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "has stealth hook",
			config: map[string]interface{}{
				"version": float64(1),
				"hooks": map[string]interface{}{
					"sessionStart": []interface{}{
						map[string]interface{}{
							"type": "command",
							"bash": "bd prime --stealth",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "no beads hooks",
			config: map[string]interface{}{
				"version": float64(1),
				"hooks": map[string]interface{}{
					"sessionStart": []interface{}{
						map[string]interface{}{
							"type": "command",
							"bash": "other command",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "empty hooks",
			config: map[string]interface{}{
				"version": float64(1),
				"hooks":   map[string]interface{}{},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hooksPath := filepath.Join(tmpDir, tt.name+".json")
			writeCopilotHooks(t, hooksPath, tt.config)

			got := hasCopilotBeadsHooks(hooksPath)
			if got != tt.want {
				t.Errorf("hasCopilotBeadsHooks() = %v, want %v", got, tt.want)
			}
		})
	}

	// Non-existent file
	t.Run("file not found", func(t *testing.T) {
		got := hasCopilotBeadsHooks(filepath.Join(tmpDir, "nonexistent.json"))
		if got {
			t.Error("Expected false for non-existent file")
		}
	})
}

func TestInstallCopilotPublicAPI(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	stubCopilotEnvProvider(t, env, nil)
	cap := stubSetupExit(t)

	InstallCopilot(false)

	if cap.called {
		t.Errorf("Expected no exit, but setupExit was called with code %d", cap.code)
	}

	hooksPath := copilotHooksPath(env.projectDir)
	if !FileExists(hooksPath) {
		t.Error("Expected hooks file to be created")
	}
}

func TestCheckCopilotPublicAPI(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	stubCopilotEnvProvider(t, env, nil)
	cap := stubSetupExit(t)

	// Not installed — should call setupExit(1)
	CheckCopilot()

	if !cap.called || cap.code != 1 {
		t.Error("Expected setupExit(1) when hooks not installed")
	}
}

func TestRemoveCopilotPublicAPI(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	stubCopilotEnvProvider(t, env, nil)
	cap := stubSetupExit(t)

	RemoveCopilot()

	if cap.called {
		t.Errorf("Expected no exit for remove on empty state, but got code %d", cap.code)
	}
}

func TestCopilotEnvProviderError(t *testing.T) {
	stubCopilotEnvProvider(t, copilotEnv{}, errors.New("env error"))
	cap := stubSetupExit(t)

	InstallCopilot(false)

	if !cap.called || cap.code != 1 {
		t.Error("Expected setupExit(1) on env provider error")
	}
}
