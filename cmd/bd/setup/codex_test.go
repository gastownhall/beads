package setup

import (
	"bytes"
	"errors"
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
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := codexEnv{
		stdout:     stdout,
		stderr:     stderr,
		homeDir:    homeDir,
		projectDir: projectDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
		getenv: func(string) string {
			return ""
		},
	}
	return env, stdout, stderr
}

func TestInstallCodexCreatesProjectSkillAndInstructions(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
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
	data, err = os.ReadFile(agentSkillOpenAIYAMLPath(env.projectDir))
	if err != nil {
		t.Fatalf("read agent skill metadata: %v", err)
	}
	if string(data) != templates.BeadsAgentSkillOpenAIYAML() {
		t.Fatal("expected managed agent skill metadata")
	}

	instructionsPath := codexInstructionsPath(env, false)
	data, err = os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read Codex instructions: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, codexBeginMarker) || !strings.Contains(content, "`beads` skill") {
		t.Fatalf("expected managed Codex skill guidance in %s", instructionsPath)
	}
	if !strings.Contains(content, "bd ready") || !strings.Contains(content, "bd remember") {
		t.Fatalf("expected managed Codex guidance to include Beads workflow reminders")
	}
	if instructionsPath != filepath.Join(env.projectDir, "AGENTS.md") {
		t.Fatalf("project instructions path = %s, want root AGENTS.md", instructionsPath)
	}
}

func TestInstallCodexGlobalCreatesGlobalSkillAndInstructions(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, true); err != nil {
		t.Fatalf("installCodex global returned error: %v", err)
	}
	if _, err := os.Stat(agentSkillPath(env.homeDir)); err != nil {
		t.Fatalf("expected global skill: %v", err)
	}
	if _, err := os.Stat(codexInstructionsPath(env, true)); err != nil {
		t.Fatalf("expected global Codex instructions: %v", err)
	}
	if got, want := codexInstructionsPath(env, true), filepath.Join(env.homeDir, ".codex", "AGENTS.md"); got != want {
		t.Fatalf("global instructions path = %s, want %s", got, want)
	}
	if _, err := os.Stat(agentSkillPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatal("global setup should not create project skill")
	}
	if _, err := os.Stat(filepath.Join(env.homeDir, ".agents", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("global setup should not create ~/.agents/AGENTS.md")
	}
}

func TestInstallCodexGlobalRespectsCodexHome(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	codexHome := filepath.Join(env.homeDir, "custom-codex-home")
	env.getenv = func(key string) string {
		if key == codexHomeEnvVar {
			return codexHome
		}
		return ""
	}
	if err := installCodex(env, true); err != nil {
		t.Fatalf("installCodex global returned error: %v", err)
	}
	if got, want := codexInstructionsPath(env, true), filepath.Join(codexHome, "AGENTS.md"); got != want {
		t.Fatalf("global instructions path = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "AGENTS.md")); err != nil {
		t.Fatalf("expected CODEX_HOME instructions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.homeDir, ".codex", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("global setup should not write ~/.codex/AGENTS.md when CODEX_HOME is set")
	}
}

func TestInstallCodexInstructionsUpdatesExistingSection(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	path := codexInstructionsPath(env, false)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := "# Existing\n\nkeep me\n\n" + codexBeginMarker + "\nold managed text\n" + codexEndMarker + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := installCodexInstructions(env, false); err != nil {
		t.Fatalf("installCodexInstructions returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "keep me") {
		t.Fatal("expected existing content to be preserved")
	}
	if strings.Contains(content, "old managed text") {
		t.Fatal("expected stale managed section to be replaced")
	}
	if strings.Count(content, codexBeginMarker) != 1 {
		t.Fatal("expected exactly one managed section")
	}
}

func TestCheckCodexMissingPieces(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	err := checkCodex(env, false)
	if !errors.Is(err, errAgentSkillMissing) {
		t.Fatalf("expected errAgentSkillMissing, got %v", err)
	}
	if !strings.Contains(stdout.String(), "bd setup codex") {
		t.Error("expected setup guidance for codex")
	}

	if err := installAgentSkill(codexAgentSkillEnv(env, false)); err != nil {
		t.Fatalf("install skill: %v", err)
	}
	err = checkCodex(env, false)
	if !errors.Is(err, errCodexInstructionsMissing) {
		t.Fatalf("expected errCodexInstructionsMissing, got %v", err)
	}
}

func TestCheckCodexDetectsStaleInstructions(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	path := codexInstructionsPath(env, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	stale := strings.Replace(string(data), "Use the `beads` skill", "Use something else", 1)
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatalf("write stale instructions: %v", err)
	}
	err = checkCodex(env, false)
	if !errors.Is(err, errCodexInstructionsStale) {
		t.Fatalf("expected errCodexInstructionsStale, got %v", err)
	}
}

func TestRemoveCodexRemovesSkillAndInstructionsSection(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	path := codexInstructionsPath(env, false)
	if err := removeCodex(env, false); err != nil {
		t.Fatalf("removeCodex returned error: %v", err)
	}
	if _, err := os.Stat(agentSkillPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatal("expected agent skill to be removed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Codex instructions: %v", err)
	}
	if strings.Contains(string(data), codexBeginMarker) {
		t.Fatal("expected managed Codex section removed")
	}
}
