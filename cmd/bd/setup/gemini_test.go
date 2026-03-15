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

	// Check SessionStart hook
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) == 0 {
		t.Fatal("expected SessionStart hooks")
	}

	// Check PreCompress hook (Gemini uses PreCompress, not PreCompact)
	preCompress, ok := hooks["PreCompress"].([]interface{})
	if !ok || len(preCompress) == 0 {
		t.Fatal("expected PreCompress hooks")
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

	if cmd["command"] != "bd prime --stealth" {
		t.Errorf("expected stealth command, got: %v", cmd["command"])
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
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// No file
	if hasGeminiBeadsHooks(settingsPath) {
		t.Error("expected false for missing file")
	}

	// Empty file
	if err := os.WriteFile(settingsPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasGeminiBeadsHooks(settingsPath) {
		t.Error("expected false for empty settings")
	}

	// With bd prime hook
	settings := map[string]interface{}{
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
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasGeminiBeadsHooks(settingsPath) {
		t.Error("expected true for settings with bd prime hook")
	}

	// With stealth hook
	settings["hooks"].(map[string]interface{})["SessionStart"] = []interface{}{
		map[string]interface{}{
			"matcher": "",
			"hooks": []interface{}{
				map[string]interface{}{"type": "command", "command": "bd prime --stealth"},
			},
		},
	}
	data, _ = json.Marshal(settings)
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasGeminiBeadsHooks(settingsPath) {
		t.Error("expected true for settings with bd prime --stealth hook")
	}
}

func TestGeminiSettingsPaths(t *testing.T) {
	projectPath := geminiProjectSettingsPath("/my/project")
	if projectPath != "/my/project/.gemini/settings.json" {
		t.Errorf("unexpected project path: %s", projectPath)
	}

	globalPath := geminiGlobalSettingsPath("/home/user")
	if globalPath != "/home/user/.gemini/settings.json" {
		t.Errorf("unexpected global path: %s", globalPath)
	}
}
