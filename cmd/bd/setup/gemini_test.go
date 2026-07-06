package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

func newGeminiTestEnv(t *testing.T) (geminiEnv, *bytes.Buffer, *bytes.Buffer) {
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
	env := geminiEnv{
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

func stubGeminiEnvProvider(t *testing.T, env geminiEnv, err error) {
	t.Helper()
	orig := geminiEnvProvider
	geminiEnvProvider = func() (geminiEnv, error) {
		if err != nil {
			return geminiEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { geminiEnvProvider = orig })
}

func writeGeminiSettings(t *testing.T, path string, settings map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func readGeminiSettings(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	return settings
}

func TestInstallGemini_Global(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := installGemini(env, false, false)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify settings file created
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)

	// Verify hooks structure
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("expected hooks map")
	}

	// Check SessionStart hook is registered with the --hook-json variant.
	// Gemini's hook contract requires JSON-on-stdout; --hook-json wraps
	// bd prime's markdown in the SessionStart envelope shape.
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) == 0 {
		t.Fatal("expected SessionStart hooks")
	}
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] != "bd prime --hook-json" {
		t.Errorf("expected SessionStart command 'bd prime --hook-json', got: %v", cmd["command"])
	}

	// PreCompress must NOT be registered: per Gemini docs it's advisory-only
	// and does not support additionalContext injection, so re-priming after
	// compression isn't possible there regardless of output format.
	if _, ok := hooks["PreCompress"]; ok {
		t.Error("PreCompress hook should not be registered (advisory-only event, no additionalContext support)")
	}

	// Verify output
	out := stdout.String()
	if !strings.Contains(out, "Installing Gemini CLI hooks globally") {
		t.Errorf("expected global install message, got: %s", out)
	}
	if !strings.Contains(out, "Gemini CLI integration installed") {
		t.Errorf("expected success message, got: %s", out)
	}
	instructionsPath := filepath.Join(env.projectDir, geminiInstructionsFile)
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read %s: %v", geminiInstructionsFile, err)
	}
	if !strings.Contains(string(instructions), "profile:minimal") {
		t.Fatalf("expected minimal profile in %s", geminiInstructionsFile)
	}
}

func TestInstallGemini_Project(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := installGemini(env, true, false)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify settings file created in project dir
	settingsPath := geminiProjectSettingsPath(env.projectDir)
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Fatalf("expected project settings file at %s", settingsPath)
	}

	out := stdout.String()
	if !strings.Contains(out, "Installing Gemini CLI hooks for this project") {
		t.Errorf("expected project install message, got: %s", out)
	}
	instructionsPath := filepath.Join(env.projectDir, geminiInstructionsFile)
	if _, err := os.Stat(instructionsPath); err != nil {
		t.Fatalf("expected %s to be created: %v", geminiInstructionsFile, err)
	}
}

func TestInstallGemini_Stealth(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	err := installGemini(env, false, true)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})

	if cmd["command"] != "bd prime --stealth --hook-json" {
		t.Errorf("expected stealth command 'bd prime --stealth --hook-json', got: %v", cmd["command"])
	}
}

func TestInstallGemini_Idempotent(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Install twice
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("second install: %v", err)
	}

	// Should only have one hook per event
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	if len(sessionStart) != 1 {
		t.Errorf("expected 1 SessionStart hook, got %d", len(sessionStart))
	}
}

// TestInstallGemini_MigratesLegacyHooks verifies that re-running bd setup gemini
// on a pre-fix installation (which had bare "bd prime" on SessionStart and/or
// PreCompress) results in exactly one canonical "bd prime --hook-json" entry
// on SessionStart and no PreCompress entries. Leaving stale entries alongside
// the new one would cause Gemini to invoke both, and the legacy command emits
// raw markdown that violates Gemini's strict stdout-must-be-JSON contract.
func TestInstallGemini_MigratesLegacyHooks(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Seed a settings file that mirrors a pre-fix installation.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	legacy := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
			},
			"PreCompress": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, legacy)

	// Re-run setup — must behave as a clean upgrade, not an accumulation.
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	// SessionStart: exactly one entry, the canonical --hook-json command.
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) != 1 {
		t.Fatalf("expected exactly 1 SessionStart hook after migration, got %v", hooks["SessionStart"])
	}
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] != "bd prime --hook-json" {
		t.Errorf("SessionStart command = %q, want 'bd prime --hook-json'", cmd["command"])
	}

	// PreCompress: must be absent or empty.
	if pc, ok := hooks["PreCompress"].([]interface{}); ok && len(pc) > 0 {
		t.Errorf("expected PreCompress cleared after migration, got %d entries", len(pc))
	}
}

func TestInstallGemini_PreservesExistingSettings(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Create settings with existing content
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existingSettings := map[string]interface{}{
		"someOtherSetting": "value",
		"hooks": map[string]interface{}{
			"SomeOtherHook": []interface{}{
				map[string]interface{}{"custom": "hook"},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existingSettings)

	// Install Gemini hooks
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify existing settings preserved
	settings := readGeminiSettings(t, settingsPath)
	if settings["someOtherSetting"] != "value" {
		t.Error("existing setting was not preserved")
	}

	hooks := settings["hooks"].(map[string]interface{})
	if hooks["SomeOtherHook"] == nil {
		t.Error("existing hook was not preserved")
	}
}

// TestCheckGemini_LegacyInstall verifies that checkGemini returns
// errGeminiHooksLegacy and emits an upgrade advisory when the settings file
// contains a pre-fix "bd prime" registration (without --hook-json).
// Legacy hooks emit raw markdown that violates Gemini's JSON stdout contract,
// so --check must distinguish them from a working current install.
func TestCheckGemini_LegacyInstall(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Seed a pre-fix installation: bare "bd prime" on SessionStart.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	legacy := map[string]interface{}{
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
	writeGeminiSettings(t, settingsPath, legacy)

	err := checkGemini(env)
	if err != errGeminiHooksLegacy {
		t.Errorf("expected errGeminiHooksLegacy, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "legacy") {
		t.Errorf("expected 'legacy' in output, got: %s", out)
	}
	if !strings.Contains(out, "bd setup gemini") {
		t.Errorf("expected upgrade instruction in output, got: %s", out)
	}
}

func TestCheckGemini_NotInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := checkGemini(env)
	if err != errGeminiHooksMissing {
		t.Errorf("expected errGeminiHooksMissing, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "No hooks installed") {
		t.Errorf("expected 'No hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_GlobalInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install hooks first
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Reset stdout
	stdout.Reset()

	// Check should pass
	err := checkGemini(env)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Global hooks installed") {
		t.Errorf("expected 'Global hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_ProjectInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install project hooks
	if err := installGemini(env, true, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	stdout.Reset()

	err := checkGemini(env)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Project hooks installed") {
		t.Errorf("expected 'Project hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_MissingInstructions(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	if err := os.Remove(filepath.Join(env.projectDir, geminiInstructionsFile)); err != nil {
		t.Fatalf("remove %s: %v", geminiInstructionsFile, err)
	}

	stdout.Reset()
	err := checkGemini(env)
	if err != errAgentsFileMissing {
		t.Fatalf("expected errAgentsFileMissing, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, geminiInstructionsFile+" not found") {
		t.Fatalf("expected missing %s message, got: %s", geminiInstructionsFile, out)
	}
}

func TestRemoveGemini_Global(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install first
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	instructionsPath := filepath.Join(env.projectDir, geminiInstructionsFile)
	if err := os.WriteFile(instructionsPath, []byte(agents.RenderSection(agents.ProfileMinimal)), 0o644); err != nil {
		t.Fatalf("seed %s: %v", geminiInstructionsFile, err)
	}

	stdout.Reset()

	// Remove
	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	// Verify hooks removed
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if ok && len(sessionStart) > 0 {
		t.Error("SessionStart hooks should be empty")
	}
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read %s: %v", geminiInstructionsFile, err)
	}
	if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("expected beads section removed from %s", geminiInstructionsFile)
	}

	out := stdout.String()
	if !strings.Contains(out, "Gemini CLI hooks removed") {
		t.Errorf("expected removal message, got: %s", out)
	}
}

func TestRemoveGemini_NoSettingsFile(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Remove without installing first
	err := removeGemini(env, false)
	if err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "No settings file found") {
		t.Errorf("expected 'No settings file found' message, got: %s", out)
	}
}

func TestRemoveGemini_PreservesOtherHooks(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Create settings with other hooks
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "other-command"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existingSettings)

	// Remove bd prime hooks
	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	// Verify other hooks preserved
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	if len(sessionStart) != 1 {
		t.Errorf("expected 1 remaining hook, got %d", len(sessionStart))
	}

	// Verify it's the other command, not bd prime
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] == "bd prime" || cmd["command"] == "bd prime --stealth" {
		t.Error("bd prime hook should have been removed")
	}
}

func TestHasGeminiBeadsHooks(t *testing.T) {
	makeSessionStart := func(command string) map[string]interface{} {
		return map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": command},
						},
					},
				},
			},
		}
	}
	makePreCompress := func(command string) map[string]interface{} {
		return map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreCompress": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": command},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		settings map[string]interface{}
		want     bool
	}{
		// Current canonical commands (--hook-json)
		{"current bd prime --hook-json on SessionStart", makeSessionStart("bd prime --hook-json"), true},
		{"current bd prime --stealth --hook-json on SessionStart", makeSessionStart("bd prime --stealth --hook-json"), true},

		// Legacy commands — still detected so pre-hook-json installations show
		// the upgrade advisory rather than "not installed".
		{"legacy bd prime on SessionStart", makeSessionStart("bd prime"), true},
		{"legacy bd prime --stealth on SessionStart", makeSessionStart("bd prime --stealth"), true},

		// PreCompress is no longer in scope — even legacy installations there
		// must NOT be reported as installed (we want users to re-run setup).
		{"bd prime on PreCompress only — not detected", makePreCompress("bd prime"), false},
		{"bd prime --hook-json on PreCompress only — not detected", makePreCompress("bd prime --hook-json"), false},

		// Unrelated commands
		{"unrelated command", makeSessionStart("some-other-command"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")
			data, err := json.Marshal(tt.settings)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := hasGeminiBeadsHooks(settingsPath); got != tt.want {
				t.Errorf("hasGeminiBeadsHooks(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}

	// Edge cases: missing file and empty settings
	t.Run("missing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if hasGeminiBeadsHooks(filepath.Join(tmpDir, "missing.json")) {
			t.Error("expected false for missing file")
		}
	})
	t.Run("empty settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if hasGeminiBeadsHooks(settingsPath) {
			t.Error("expected false for empty settings")
		}
	})
}

// TestRemoveGemini_CleansAllVariants verifies that removeGemini cleans up
// every known command variant from BOTH SessionStart and PreCompress —
// migration safety for pre-fix installations that registered legacy commands
// on PreCompress.
func TestRemoveGemini_CleansAllVariants(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Seed a settings file with a current --hook-json registration on SessionStart
	// and legacy bare-command registrations on PreCompress.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --hook-json"},
					},
				},
			},
			"PreCompress": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --stealth"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existing)

	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	if ss, ok := hooks["SessionStart"].([]interface{}); ok && len(ss) > 0 {
		t.Errorf("expected SessionStart cleared, got %d entries", len(ss))
	}
	if pc, ok := hooks["PreCompress"].([]interface{}); ok && len(pc) > 0 {
		t.Errorf("expected legacy PreCompress entries cleared, got %d entries", len(pc))
	}
}

func TestGeminiSettingsPaths(t *testing.T) {
	projectPath := geminiProjectSettingsPath("/my/project")
	if want := filepath.Join("/my/project", ".gemini", "settings.json"); projectPath != want {
		t.Errorf("project path = %q, want %q", projectPath, want)
	}

	globalPath := geminiGlobalSettingsPath("/home/user")
	if want := filepath.Join("/home/user", ".gemini", "settings.json"); globalPath != want {
		t.Errorf("global path = %q, want %q", globalPath, want)
	}
}
