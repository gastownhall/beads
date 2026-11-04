package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	configData, err := os.ReadFile(codexConfigPath(env, false))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !codexHooksFeatureEnabled(string(configData)) {
		t.Fatalf("expected hooks feature enabled in %s", codexConfigPath(env, false))
	}
	if !codexHooksJSONCurrent(env, false) {
		t.Fatalf("expected managed Codex hooks in %s", codexHooksPath(env, false))
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
	if _, err := os.Stat(codexConfigPath(env, true)); err != nil {
		t.Fatalf("expected global Codex config: %v", err)
	}
	if _, err := os.Stat(codexHooksPath(env, true)); err != nil {
		t.Fatalf("expected global Codex hooks: %v", err)
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

// A global install or --remove rewrites ~/.codex/AGENTS.md, config.toml
// (which can hold MCP server tokens) and hooks.json. Each must keep the mode
// the owner gave it, not come back 0644.
func TestCodexGlobalFilesKeepTheirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions are not supported on Windows")
	}
	env, _, _ := newCodexTestEnv(t)
	paths := []string{
		codexInstructionsPath(env, true),
		codexConfigPath(env, true),
		codexHooksPath(env, true),
	}
	seeds := []string{
		"# Mine\n",
		"[mcp_servers.docs.env]\nAPI_TOKEN = \"secret\"\n",
		// A user hook, so --remove rewrites hooks.json rather than deleting it.
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`,
	}
	for i, path := range paths {
		seedFileMode(t, path, seeds[i], 0o600)
	}

	if err := installCodex(env, true); err != nil {
		t.Fatalf("installCodex: %v", err)
	}
	installed := make([]string, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) == seeds[i] {
			t.Errorf("install did not change %s", path)
		}
		installed[i] = string(data)
		assertFileMode(t, path, 0o600)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	if err := removeCodex(env, true); err != nil {
		t.Fatalf("removeCodex: %v", err)
	}
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) == installed[i] {
			t.Errorf("--remove did not change %s", path)
		}
		assertFileMode(t, path, 0o600)
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
	if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); err != nil {
		t.Fatalf("expected CODEX_HOME config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "hooks.json")); err != nil {
		t.Fatalf("expected CODEX_HOME hooks: %v", err)
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
	if !errors.Is(err, errCodexFeatureMissing) {
		t.Fatalf("expected errCodexFeatureMissing, got %v", err)
	}

	if err := installCodexNativeHooks(env, false); err != nil {
		t.Fatalf("install native hooks: %v", err)
	}
	err = checkCodex(env, false)
	if !errors.Is(err, errCodexInstructionsMissing) {
		t.Fatalf("expected errCodexInstructionsMissing, got %v", err)
	}
}

func TestCheckCodexDetectsStaleHooks(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	if err := os.WriteFile(codexHooksPath(env, false), []byte(`{"hooks":{"SessionStart":[]}}`), 0o644); err != nil {
		t.Fatalf("write stale hooks: %v", err)
	}
	err := checkCodex(env, false)
	if !errors.Is(err, errCodexHooksStale) {
		t.Fatalf("expected errCodexHooksStale, got %v", err)
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
	if _, err := os.Stat(codexHooksPath(env, false)); !os.IsNotExist(err) {
		t.Fatalf("expected managed Codex hooks removed, stat err=%v", err)
	}
}

func TestRemoveCodexKeepsSkillWhenCursorPresent(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}

	// Simulate a Cursor install that shares the .agents/skills/beads skill by
	// writing a bd-managed cursor hooks file under the same root.
	cursorDir := filepath.Join(env.projectDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatalf("mkdir .cursor: %v", err)
	}
	hooks := `{"version":1,"hooks":{"sessionStart":[{"command":"bd cursor-hook sessionStart"}]}}`
	if err := os.WriteFile(filepath.Join(cursorDir, "hooks.json"), []byte(hooks), 0o644); err != nil {
		t.Fatalf("write cursor hooks: %v", err)
	}

	if err := removeCodex(env, false); err != nil {
		t.Fatalf("removeCodex returned error: %v", err)
	}

	if _, err := os.Stat(agentSkillPath(env.projectDir)); err != nil {
		t.Errorf("skill should be kept while Cursor integration is present: %v", err)
	}
}

func TestCodexHooksConfigMergeIsIdempotent(t *testing.T) {
	env, _, _ := newCodexTestEnv(t)
	configPath := codexConfigPath(env, false)
	hooksPath := codexHooksPath(env, false)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	seedConfig := "[model]\nname = \"gpt-test\"\n\n[features]\nother = true\n"
	if err := os.WriteFile(configPath, []byte(seedConfig), 0o644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	seedHooks := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(seedHooks), 0o644); err != nil {
		t.Fatalf("write seed hooks: %v", err)
	}

	if err := installCodexNativeHooks(env, false); err != nil {
		t.Fatalf("install hooks first time: %v", err)
	}
	firstConfig, _ := os.ReadFile(configPath)
	firstHooks, _ := os.ReadFile(hooksPath)
	if err := installCodexNativeHooks(env, false); err != nil {
		t.Fatalf("install hooks second time: %v", err)
	}
	secondConfig, _ := os.ReadFile(configPath)
	secondHooks, _ := os.ReadFile(hooksPath)
	if string(firstConfig) != string(secondConfig) {
		t.Fatal("expected idempotent config merge")
	}
	if string(firstHooks) != string(secondHooks) {
		t.Fatal("expected idempotent hook merge")
	}
	if !strings.Contains(string(secondConfig), "other = true") || !strings.Contains(string(secondConfig), "hooks = true") {
		t.Fatalf("expected existing feature and hooks flag preserved:\n%s", string(secondConfig))
	}
	if !strings.Contains(string(secondHooks), "echo keep") || !strings.Contains(string(secondHooks), "bd codex-hook SessionStart") {
		t.Fatalf("expected existing and managed hooks preserved:\n%s", string(secondHooks))
	}
}

func TestCodexHooksFeatureMigratesDeprecatedKey(t *testing.T) {
	input := "[features]\ncodex_hooks = true\nother = true\n"

	output := upsertCodexHooksFeature(input)

	if strings.Contains(output, "codex_hooks") {
		t.Fatalf("expected deprecated codex_hooks key removed:\n%s", output)
	}
	if !strings.Contains(output, "hooks = true") {
		t.Fatalf("expected hooks feature enabled:\n%s", output)
	}
	if !strings.Contains(output, "other = true") {
		t.Fatalf("expected unrelated feature preserved:\n%s", output)
	}
	if !codexHooksFeatureEnabled(output) {
		t.Fatalf("expected migrated config to enable hooks:\n%s", output)
	}
}

func TestInstallCodexNativeHooksSkipsFallbackWhenPluginEnabled(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	configPath := codexConfigPath(env, false)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	seedConfig := "[plugins.\"beads@local\"]\nenabled = true\n"
	if err := os.WriteFile(configPath, []byte(seedConfig), 0o644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	if err := installCodexNativeHooks(env, false); err != nil {
		t.Fatalf("install native hooks: %v", err)
	}
	if _, err := os.Stat(codexHooksPath(env, false)); !os.IsNotExist(err) {
		t.Fatalf("plugin-managed setup should not write fallback hooks, stat err=%v", err)
	}
	if !strings.Contains(stdout.String(), "plugin-managed") {
		t.Fatalf("expected plugin-managed message, got %s", stdout.String())
	}
	if !codexConfigHasHooksFeature(env, false) {
		t.Fatal("expected hooks feature enabled even when hooks are plugin-managed")
	}
	if err := checkCodexNativeHooks(env, false); err != nil {
		t.Fatalf("plugin-managed check should pass: %v", err)
	}
}

func TestCodexConfigEnablesBeadsPlugin(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			// GH#4244: a plugin whose name merely contains "beads" (here
			// design-to-beads) must NOT be taken for the beads hook plugin, or
			// the hooks fallback write is wrongly skipped.
			name:    "design-to-beads not mistaken for beads plugin",
			content: "[plugins.\"design-to-beads@xexr-marketplace\"]\nenabled = true\n",
			want:    false,
		},
		{
			name:    "bare design-to-beads not mistaken for beads plugin",
			content: "[plugins.design-to-beads]\nenabled = true\n",
			want:    false,
		},
		{
			// The exact-name match must still find a real beads@<marketplace>;
			// this is the quoted form bd setup codex actually writes.
			name:    "real quoted beads plugin detected",
			content: "[plugins.\"beads@local\"]\nenabled = true\n",
			want:    true,
		},
		{
			name:    "bare beads plugin detected",
			content: "[plugins.beads]\nenabled = true\n",
			want:    true,
		},
		{
			// GH#3192 preserved: a real beads plugin is still found alongside a
			// *beads*-named decoy.
			name:    "real beads plugin detected past a decoy",
			content: "[plugins.\"design-to-beads@xexr-marketplace\"]\nenabled = true\n[plugins.\"beads@local\"]\nenabled = true\n",
			want:    true,
		},
		{
			name:    "beads plugin present but disabled",
			content: "[plugins.\"beads@local\"]\nenabled = false\n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexConfigEnablesBeadsPlugin(tc.content); got != tc.want {
				t.Errorf("codexConfigEnablesBeadsPlugin() = %v, want %v", got, tc.want)
			}
		})
	}
}

func codexTestHookEntry(commands ...string) map[string]interface{} {
	hooks := make([]interface{}, 0, len(commands))
	for _, command := range commands {
		hooks = append(hooks, map[string]interface{}{
			"type":    "command",
			"command": command,
		})
	}
	return map[string]interface{}{"hooks": hooks}
}

func codexTestEventCommands(config map[string]interface{}, event string) []string {
	hooks, _ := config["hooks"].(map[string]interface{})
	var commands []string
	for _, entry := range toInterfaceSlice(hooks[event]) {
		entryMap, _ := entry.(map[string]interface{})
		for _, hook := range toInterfaceSlice(entryMap["hooks"]) {
			hookMap, _ := hook.(map[string]interface{})
			if command, ok := hookMap["command"].(string); ok {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func codexTestCommandsContain(commands []string, want string) bool {
	for _, command := range commands {
		if command == want {
			return true
		}
	}
	return false
}

func TestCodexHooksMigratesExactLegacySessionStart(t *testing.T) {
	config := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{codexTestHookEntry("  " + codexLegacySessionStartCommand + "\n")},
		},
	}
	if !codexBeadsHooksPresent(config) {
		t.Fatal("expected exact legacy SessionStart hook to count as Codex integration")
	}

	upsertCodexManagedHooks(config)

	hooks := config["hooks"].(map[string]interface{})
	entries := toInterfaceSlice(hooks["SessionStart"])
	if len(entries) != 1 {
		t.Fatalf("SessionStart entries = %d, want one canonical entry", len(entries))
	}
	commands := codexTestEventCommands(config, "SessionStart")
	if !codexTestCommandsContain(commands, "bd codex-hook SessionStart") {
		t.Fatalf("canonical SessionStart hook missing: %v", commands)
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == codexLegacySessionStartCommand {
			t.Fatalf("legacy SessionStart hook remains: %v", commands)
		}
	}
}

func TestCodexHooksLegacyMigrationIsIdempotent(t *testing.T) {
	config := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{codexTestHookEntry(codexLegacySessionStartCommand)},
		},
	}
	upsertCodexManagedHooks(config)
	first, err := marshalHooksJSON(config)
	if err != nil {
		t.Fatalf("marshal first migration: %v", err)
	}
	upsertCodexManagedHooks(config)
	second, err := marshalHooksJSON(config)
	if err != nil {
		t.Fatalf("marshal second migration: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("legacy migration is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestCodexManagedHooksCurrentRejectsCanonicalPlusLegacy(t *testing.T) {
	config := map[string]interface{}{"hooks": codexManagedHooks()}
	hooks := config["hooks"].(map[string]interface{})
	hooks["SessionStart"] = append(toInterfaceSlice(hooks["SessionStart"]), codexTestHookEntry(codexLegacySessionStartCommand))
	if codexManagedHooksCurrent(config) {
		t.Fatal("canonical hooks with an exact legacy SessionStart hook must be stale")
	}

	upsertCodexManagedHooks(config)
	if !codexManagedHooksCurrent(config) {
		t.Fatal("hooks should be current after legacy migration")
	}
}

func TestCodexHooksLegacyMigrationPreservesUnrelatedEntries(t *testing.T) {
	nearLegacy := "bd prime --stealth | jq ."
	config := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				codexTestHookEntry("echo keep"),
				codexTestHookEntry(nearLegacy),
				codexTestHookEntry(codexLegacySessionStartCommand),
			},
		},
	}

	upsertCodexManagedHooks(config)

	commands := codexTestEventCommands(config, "SessionStart")
	for _, want := range []string{"echo keep", nearLegacy, "bd codex-hook SessionStart"} {
		if !codexTestCommandsContain(commands, want) {
			t.Fatalf("command %q was not preserved/installed: %v", want, commands)
		}
	}
}

func TestCodexHooksLegacyMigrationPreservesMixedEntryCommands(t *testing.T) {
	config := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				codexTestHookEntry(codexLegacySessionStartCommand, "echo keep"),
			},
		},
	}

	upsertCodexManagedHooks(config)

	commands := codexTestEventCommands(config, "SessionStart")
	if !codexTestCommandsContain(commands, "echo keep") || !codexTestCommandsContain(commands, "bd codex-hook SessionStart") {
		t.Fatalf("mixed entry command or canonical hook missing: %v", commands)
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == codexLegacySessionStartCommand {
			t.Fatalf("legacy command remains in mixed entry: %v", commands)
		}
	}
}

func TestRemoveCodexHooksRemovesLegacyAndPreservesUnrelated(t *testing.T) {
	nearLegacy := "bd prime --stealth"
	config := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				codexTestHookEntry(codexLegacySessionStartCommand),
				codexTestHookEntry(codexLegacySessionStartCommand, "echo keep"),
				codexTestHookEntry(nearLegacy),
				codexHookEntry("startup|resume|clear", "bd codex-hook SessionStart", "Loading Beads context"),
			},
		},
	}

	removeCodexManagedHooks(config)

	commands := codexTestEventCommands(config, "SessionStart")
	if !codexTestCommandsContain(commands, "echo keep") || !codexTestCommandsContain(commands, nearLegacy) {
		t.Fatalf("unrelated commands not preserved: %v", commands)
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == codexLegacySessionStartCommand || strings.HasPrefix(command, "bd codex-hook ") {
			t.Fatalf("managed command remains after removal: %v", commands)
		}
	}
	if codexBeadsHooksPresent(config) {
		t.Fatal("removed config should no longer be detected as a Codex Beads integration")
	}
}

// Remove takes the agent skill's files out of .agents/skills/beads and then
// removes the directories they were in, innermost first, each if that left
// it empty, but it never removes one that is a symlink, since that unlinks
// it whatever its target holds, nor any when it removed no file. Cursor's
// remove shares this (removeAgentSkill).
func TestRemoveCodexKeepsSkillDirsItDidNotEmpty(t *testing.T) {
	agents := ".agents"
	skills := filepath.Join(agents, "skills")
	beads := filepath.Join(skills, "beads")
	for _, tt := range []struct {
		name    string
		link    string // the directory that is a symlink to one beside the project
		mkdir   string // a real directory the user made
		install bool   // bd setup codex ran first
		drop    string // a file of bd's the user then deleted by hand
		want    map[string]string
	}{
		{name: "linked .agents bd installed nothing in", link: agents,
			want: map[string]string{agents: "link"}},
		{name: "linked .agents bd installed the skill through", link: agents, install: true,
			want: map[string]string{agents: "link", skills: "gone"}},
		{name: "linked .agents/skills bd installed the skill through", link: skills, install: true,
			want: map[string]string{agents: "dir", skills: "link", beads: "gone"}},
		{name: "linked .agents/skills/beads bd installed the skill through", link: beads, install: true,
			want: map[string]string{skills: "dir", beads: "link", filepath.Join(beads, "agents"): "gone"}},
		{name: "real .agents/skills bd installed nothing in", mkdir: skills,
			want: map[string]string{agents: "dir", skills: "dir"}},
		{name: "real .agents holding only bd's skill", install: true,
			want: map[string]string{agents: "gone"}},
		{name: "real empty agents dir bd took no file from", install: true,
			drop: filepath.Join(beads, "agents", "openai.yaml"),
			want: map[string]string{filepath.Join(beads, "agents"): "dir", beads: "dir"}},
		{name: "real dirs, SKILL.md deleted by hand", install: true,
			drop: filepath.Join(beads, "SKILL.md"),
			want: map[string]string{agents: "gone"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, _ := newCodexTestEnv(t)
			if tt.link != "" {
				link := filepath.Join(env.projectDir, tt.link)
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				symlinkDir(t, filepath.Join(filepath.Dir(env.projectDir), "shared"), link)
			}
			if tt.mkdir != "" {
				if err := os.MkdirAll(filepath.Join(env.projectDir, tt.mkdir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.install {
				if err := installCodex(env, false); err != nil {
					t.Fatalf("installCodex: %v\n%s", err, stdout.String())
				}
			}
			if tt.drop != "" {
				if err := os.Remove(filepath.Join(env.projectDir, tt.drop)); err != nil {
					t.Fatal(err)
				}
			}

			if err := removeCodex(env, false); err != nil {
				t.Fatalf("removeCodex: %v\n%s", err, stdout.String())
			}
			requireGone(t, agentSkillPath(env.projectDir))
			requireGone(t, agentSkillOpenAIYAMLPath(env.projectDir))
			for rel, want := range tt.want {
				requireState(t, filepath.Join(env.projectDir, rel), want)
			}
		})
	}
}

// removeAgentSkill removes the skill's emptied directories through the env's
// removeFile, as it does the skill's files, so an injected removal sees them.
func TestRemoveCodexRemovesSkillDirsThroughEnv(t *testing.T) {
	env, stdout, _ := newCodexTestEnv(t)
	if err := installCodex(env, false); err != nil {
		t.Fatalf("installCodex: %v\n%s", err, stdout.String())
	}
	agents := filepath.Join(env.projectDir, ".agents")
	injected := errors.New("injected")
	env.removeFile = func(path string) error {
		if path == agents {
			return injected
		}
		return os.Remove(path)
	}
	if err := removeCodex(env, false); !errors.Is(err, injected) {
		t.Fatalf("removeCodex err = %v, want the injected removal error for %s", err, agents)
	}
	requireDirKept(t, agents)
}
