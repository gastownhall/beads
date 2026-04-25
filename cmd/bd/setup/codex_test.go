package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates"
)

func newCodexTestEnv(t *testing.T) (codexEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := codexEnv{
		stdout:     stdout,
		stderr:     stderr,
		projectDir: projectDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
	}
	return env, stdout, stderr
}

func stubCodexEnvProvider(t *testing.T, env codexEnv, err error) {
	t.Helper()
	orig := codexEnvProvider
	codexEnvProvider = func() (codexEnv, error) {
		if err != nil {
			return codexEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { codexEnvProvider = orig })
}

func stubAgentSkillEnvProvider(t *testing.T, env agentSkillEnv, err error) {
	t.Helper()
	orig := agentSkillEnvProvider
	agentSkillEnvProvider = func() (agentSkillEnv, error) {
		if err != nil {
			return agentSkillEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { agentSkillEnvProvider = orig })
}

func TestInstallCodexCreatesNewFile(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	if err := installCodex(env, false, false, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Beads agent skill installed") {
		t.Error("expected agent skill install success message")
	}
	data, err := os.ReadFile(agentSkillPath(env.projectDir))
	if err != nil {
		t.Fatalf("read agent skill: %v", err)
	}
	if string(data) != templates.BeadsAgentSkill() {
		t.Fatal("expected managed agent skill content")
	}
	if _, err := os.Stat(filepath.Join(env.projectDir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("AGENTS.md should not be created unless --agents is requested")
	}
}

func TestInstallCodexAgentsFallbackCreatesAgentsFile(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, false, false, true); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(env.projectDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !containsBeadsMarker(string(data)) {
		t.Fatal("expected beads section in AGENTS.md")
	}
}

func TestInstallCodexHooksCreatesProjectFiles(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	if err := installCodex(env, true, false, false); err != nil {
		t.Fatalf("installCodex hooks returned error: %v", err)
	}

	hooksPath := codexHooksPath(env.projectDir)
	if !hasCodexBeadsHooks(hooksPath) {
		t.Fatalf("expected Codex hooks in %s", hooksPath)
	}

	configPath := codexConfigPath(env.projectDir)
	if !codexHooksFeatureEnabled(configPath) {
		t.Fatalf("expected codex_hooks enabled in %s", configPath)
	}
	gitignoreData, err := os.ReadFile(codexGitignorePath(env.projectDir))
	if err != nil {
		t.Fatalf("read .codex/.gitignore: %v", err)
	}
	if !strings.Contains(string(gitignoreData), codexRunHookStateFile) {
		t.Fatalf("expected .codex/.gitignore to ignore %q", codexRunHookStateFile)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var hooksSettings map[string]interface{}
	if err := json.Unmarshal(data, &hooksSettings); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	hooks := hooksSettings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	hookEntry := sessionStart[0].(map[string]interface{})
	commands := hookEntry["hooks"].([]interface{})
	cmd := commands[0].(map[string]interface{})
	if cmd["command"] != codexSessionStartHookCommand {
		t.Fatalf("expected %q, got %v", codexSessionStartHookCommand, cmd["command"])
	}

	userPromptSubmit := hooks["UserPromptSubmit"].([]interface{})
	hookEntry = userPromptSubmit[0].(map[string]interface{})
	commands = hookEntry["hooks"].([]interface{})
	cmd = commands[0].(map[string]interface{})
	if cmd["command"] != codexUserPromptSubmitHookCommand {
		t.Fatalf("expected %q, got %v", codexUserPromptSubmitHookCommand, cmd["command"])
	}

	if !strings.Contains(stdout.String(), "Enabled codex_hooks feature") {
		t.Error("expected codex_hooks enablement message")
	}
}

func TestInstallCodexHooksStealthUsesStealthCommand(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, true, true, false); err != nil {
		t.Fatalf("installCodex hooks stealth returned error: %v", err)
	}
	data, err := os.ReadFile(codexHooksPath(env.projectDir))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if !strings.Contains(string(data), codexSessionStartHookStealthCommand) {
		t.Fatalf("expected hooks.json to contain %q", codexSessionStartHookStealthCommand)
	}
	if !strings.Contains(string(data), codexUserPromptSubmitHookStealthCommand) {
		t.Fatalf("expected hooks.json to contain %q", codexUserPromptSubmitHookStealthCommand)
	}
}

func TestInstallProjectAgentSkillCanBeQuiet(t *testing.T) {
	env, stdout, stderr := newCodexTestEnv(t)
	stubAgentSkillEnvProvider(t, agentSkillEnvFromCodex(env), nil)

	if err := InstallProjectAgentSkillWithOutput(io.Discard, io.Discard); err != nil {
		t.Fatalf("InstallProjectAgentSkillWithOutput returned error: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("expected quiet install, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCheckCodexMissingFile(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	err := checkCodex(env, false, false)
	if err == nil {
		t.Fatal("expected error for missing agent skill")
	}
	if !strings.Contains(stdout.String(), "bd setup codex") {
		t.Error("expected setup guidance for codex")
	}
}

func TestCheckCodexHooksMissing(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	if err := installCodex(env, false, false, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	err := checkCodex(env, true, false)
	if !errors.Is(err, errCodexHooksMissing) {
		t.Fatalf("expected errCodexHooksMissing, got %v", err)
	}
	if !strings.Contains(stdout.String(), "bd setup codex --hooks") {
		t.Error("expected setup guidance for Codex hooks")
	}
}

func TestRemoveCodexHooksRemovesHookAndFeature(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, true, true, true); err != nil {
		t.Fatalf("installCodex hooks returned error: %v", err)
	}
	if err := removeCodex(env, true, true); err != nil {
		t.Fatalf("removeCodex hooks returned error: %v", err)
	}
	if hasCodexBeadsHooks(codexHooksPath(env.projectDir)) {
		t.Fatal("expected SessionStart hook to be removed")
	}
	if codexHooksFeatureEnabled(codexConfigPath(env.projectDir)) {
		t.Fatal("expected codex_hooks feature to be removed")
	}
	agentsData, err := os.ReadFile(filepath.Join(env.projectDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if containsBeadsMarker(string(agentsData)) {
		t.Fatal("expected beads section removed from AGENTS.md")
	}
	if _, err := os.Stat(agentSkillPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatal("expected agent skill to be removed")
	}
}

func TestRemoveCodexHooksDoesNotCreateMissingConfig(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)

	if err := removeCodex(env, true, false); err != nil {
		t.Fatalf("removeCodex hooks returned error: %v", err)
	}
	if _, err := os.Stat(codexConfigPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatalf("config.toml should not be created during remove, got err=%v", err)
	}
}
