package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates"
)

// inTempDir chdir's into a fresh temp directory for the duration of the test.
func inTempDir(t *testing.T) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
}

func readCursorHooksFile(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(cursorHooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}
	return cfg
}

func cursorHookCommandsForEvent(t *testing.T, cfg map[string]interface{}, event string) []string {
	t.Helper()
	hooks, _ := cfg["hooks"].(map[string]interface{})
	entries, _ := hooks[event].([]interface{})
	var cmds []string
	for _, e := range entries {
		if m, ok := e.(map[string]interface{}); ok {
			if c, ok := m["command"].(string); ok {
				cmds = append(cmds, c)
			}
		}
	}
	return cmds
}

func TestInstallCursorInstallsHooks(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)

	if !FileExists(cursorHooksPath) {
		t.Fatalf("hooks file %s not created", cursorHooksPath)
	}
	cfg := readCursorHooksFile(t)
	for event, want := range cursorManagedHooks() {
		cmds := cursorHookCommandsForEvent(t, cfg, event)
		found := false
		for _, c := range cmds {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("event %q missing command %q (got %v)", event, want, cmds)
		}
	}
	ok, err := cursorHooksInstalled(cursorHooksPath)
	if err != nil || !ok {
		t.Errorf("cursorHooksInstalled = %v, %v; want true, nil", ok, err)
	}
}

func TestInstallCursorInstallsSkill(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)

	skillPath := agentSkillPath(".")
	if !FileExists(skillPath) {
		t.Fatalf("agent skill not installed at %s", skillPath)
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if string(data) != templates.BeadsAgentSkill() {
		t.Error("installed skill content does not match managed template")
	}
}

func TestRemoveCursorRemovesSkillWhenCodexAbsent(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)
	if !FileExists(agentSkillPath(".")) {
		t.Fatal("precondition: skill should exist after install")
	}

	RemoveCursor(false)

	if FileExists(agentSkillPath(".")) {
		t.Error("skill should be removed when no other integration relies on it")
	}
}

func TestRemoveCursorKeepsSkillWhenCodexPresent(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)

	// Simulate a Codex install that shares the .agents/skills/beads skill by
	// writing its managed AGENTS.md section.
	if err := os.WriteFile(codexInstructionsFile, []byte(codexManagedSection()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	RemoveCursor(false)

	if !FileExists(agentSkillPath(".")) {
		t.Error("skill should be kept while Codex integration is present")
	}
}

func TestRemoveCursorKeepsSkillWhenCodexHooksPresent(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)

	// Simulate a Codex install that shares the .agents/skills/beads skill but is
	// visible only via .codex/hooks.json (no AGENTS.md section). codexIntegrationInstalled
	// must detect this too, symmetric with cursorIntegrationInstalledAt's hooks check.
	if err := os.MkdirAll(codexConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codexHooks := `{"version":1,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd codex-hook SessionStart"}]}]}}`
	if err := os.WriteFile(filepath.Join(codexConfigDir, codexHooksFile), []byte(codexHooks), 0o644); err != nil {
		t.Fatal(err)
	}

	RemoveCursor(false)

	if !FileExists(agentSkillPath(".")) {
		t.Error("skill should be kept while a bd-managed .codex/hooks.json is present")
	}
}

func TestInstallCursorHooksIdempotent(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)
	InstallCursor(false)

	cfg := readCursorHooksFile(t)
	for event := range cursorManagedHooks() {
		cmds := cursorHookCommandsForEvent(t, cfg, event)
		if len(cmds) != 1 {
			t.Errorf("event %q has %d commands after double install, want 1: %v", event, len(cmds), cmds)
		}
	}
}

func TestInstallCursorHooksPreservesUserHooks(t *testing.T) {
	inTempDir(t)
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"version":1,"hooks":{"afterFileEdit":[{"command":"./format.sh"}]}}`
	if err := os.WriteFile(cursorHooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	InstallCursor(false)

	cfg := readCursorHooksFile(t)
	edits := cursorHookCommandsForEvent(t, cfg, "afterFileEdit")
	if len(edits) != 1 || edits[0] != "./format.sh" {
		t.Errorf("user afterFileEdit hook not preserved: %v", edits)
	}
	if cmds := cursorHookCommandsForEvent(t, cfg, "sessionStart"); len(cmds) != 1 {
		t.Errorf("sessionStart hook not installed alongside user hook: %v", cmds)
	}
}

func TestRemoveCursorRemovesManagedHooks(t *testing.T) {
	inTempDir(t)
	InstallCursor(false)
	RemoveCursor(false)

	if FileExists(cursorHooksPath) {
		t.Errorf("hooks file should be removed when only bd hooks were present")
	}
	if FileExists(cursorRulesPath) {
		t.Errorf("rules file should be removed")
	}
}

func TestRemoveCursorPreservesUserHooks(t *testing.T) {
	inTempDir(t)
	if err := os.MkdirAll(".cursor", 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"version":1,"hooks":{"afterFileEdit":[{"command":"./format.sh"}]}}`
	if err := os.WriteFile(cursorHooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	InstallCursor(false)
	RemoveCursor(false)

	if !FileExists(cursorHooksPath) {
		t.Fatalf("hooks file should remain because a user hook is present")
	}
	cfg := readCursorHooksFile(t)
	edits := cursorHookCommandsForEvent(t, cfg, "afterFileEdit")
	if len(edits) != 1 || edits[0] != "./format.sh" {
		t.Errorf("user hook not preserved after remove: %v", edits)
	}
	for event := range cursorManagedHooks() {
		if cmds := cursorHookCommandsForEvent(t, cfg, event); len(cmds) != 0 {
			t.Errorf("managed event %q should be removed: %v", event, cmds)
		}
	}
}

func TestCursorRulesTemplate(t *testing.T) {
	// Verify template contains required content
	requiredContent := []string{
		"bd prime",
		"bd ready",
		"bd create",
		"bd update",
		"bd close",
		"bd dolt push",
		"BEADS INTEGRATION",
	}

	for _, req := range requiredContent {
		if !strings.Contains(cursorRulesTemplate, req) {
			t.Errorf("cursorRulesTemplate missing required content: %q", req)
		}
	}
}

func TestCursorRulesTemplate_AlwaysApplyFrontmatter(t *testing.T) {
	// Cursor .mdc files without alwaysApply frontmatter default to Manual mode,
	// requiring users to @-reference the rule. The template must include
	// alwaysApply: true so beads context loads automatically every session.
	if !strings.HasPrefix(cursorRulesTemplate, "---\nalwaysApply: true\n---\n") {
		t.Error("cursorRulesTemplate must start with alwaysApply: true frontmatter")
	}
}

func TestInstallCursor(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	InstallCursor(false)

	// Verify file was created
	rulesPath := ".cursor/rules/beads.mdc"
	if !FileExists(rulesPath) {
		t.Fatal("Cursor rules file was not created")
	}

	// Verify content
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("failed to read rules file: %v", err)
	}

	if string(data) != cursorRulesTemplate {
		t.Error("Rules file content doesn't match template")
	}
}

func TestInstallCursor_ExistingDirectory(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Pre-create the directory
	if err := os.MkdirAll(".cursor/rules", 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Should not fail
	InstallCursor(false)

	// Verify file was created
	if !FileExists(".cursor/rules/beads.mdc") {
		t.Fatal("Cursor rules file was not created")
	}
}

func TestInstallCursor_OverwriteExisting(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Create existing file with different content
	rulesPath := ".cursor/rules/beads.mdc"
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(rulesPath, []byte("old content"), 0644); err != nil {
		t.Fatalf("failed to create old file: %v", err)
	}

	InstallCursor(false)

	// Verify content was overwritten
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("failed to read rules file: %v", err)
	}

	if string(data) == "old content" {
		t.Error("Old content was not overwritten")
	}
	if string(data) != cursorRulesTemplate {
		t.Error("Content doesn't match template")
	}
}

func TestInstallCursorIdempotent(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Run twice
	InstallCursor(false)
	firstData, _ := os.ReadFile(".cursor/rules/beads.mdc")

	InstallCursor(false)
	secondData, _ := os.ReadFile(".cursor/rules/beads.mdc")

	if string(firstData) != string(secondData) {
		t.Error("InstallCursor should be idempotent")
	}
}

func TestRemoveCursor(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install first
	InstallCursor(false)

	// Verify file exists
	rulesPath := ".cursor/rules/beads.mdc"
	if !FileExists(rulesPath) {
		t.Fatal("File should exist before removal")
	}

	// Remove
	RemoveCursor(false)

	// Verify file is gone
	if FileExists(rulesPath) {
		t.Error("File should have been removed")
	}
}

func TestRemoveCursor_NoFile(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Should not panic when file doesn't exist
	RemoveCursor(false)
}

func TestCheckCursor_NotInstalled(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	if err := CheckCursor(false); err == nil {
		t.Fatal("CheckCursor should return error when not installed")
	}
}

func TestCheckCursor_Installed(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	// Install first
	InstallCursor(false)

	// Should not panic or exit
	CheckCursor(false)
}

func TestCursorRulesPath(t *testing.T) {
	// Verify the path is correct for Cursor IDE
	expectedPath := ".cursor/rules/beads.mdc"

	// These are the paths used in the implementation
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	InstallCursor(false)

	// Verify the file was created at the expected path
	if !FileExists(expectedPath) {
		t.Errorf("Expected file at %s", expectedPath)
	}
}

func TestInstallCursorProject(t *testing.T) {
	inTempDir(t)

	if err := InstallCursorProject(); err != nil {
		t.Fatalf("InstallCursorProject: %v", err)
	}
	if !FileExists(cursorRulesPath) {
		t.Errorf("project rules not installed")
	}
	ok, err := cursorHooksInstalled(cursorHooksPath)
	if err != nil || !ok {
		t.Errorf("project hooks not installed: ok=%v err=%v", ok, err)
	}
}

func TestInstallCursorGlobalHooksOnly(t *testing.T) {
	inTempDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	InstallCursor(true)

	globalHooks := filepath.Join(home, ".cursor", "hooks.json")
	if !FileExists(globalHooks) {
		t.Fatalf("global hooks file not created at %s", globalHooks)
	}
	// Global scope must not write a project rules file.
	if FileExists(cursorRulesPath) {
		t.Errorf("global install should not create a project rules file")
	}
	// Skills do have a global location (~/.agents/skills/), so --global installs one.
	if !FileExists(agentSkillPath(home)) {
		t.Errorf("global agent skill not installed at %s", agentSkillPath(home))
	}
	ok, err := cursorHooksInstalled(globalHooks)
	if err != nil || !ok {
		t.Errorf("global cursorHooksInstalled = %v, %v; want true, nil", ok, err)
	}
}

func TestRemoveCursorGlobal(t *testing.T) {
	inTempDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	InstallCursor(true)
	RemoveCursor(true)

	globalHooks := filepath.Join(home, ".cursor", "hooks.json")
	if FileExists(globalHooks) {
		t.Errorf("global hooks file should be removed when only bd hooks present")
	}
}

func TestCursorTemplateFormatting(t *testing.T) {
	// Verify template is well-formed
	template := cursorRulesTemplate

	// Should have both markers
	if !strings.Contains(template, "BEGIN BEADS INTEGRATION") {
		t.Error("Missing BEGIN marker")
	}
	if !strings.Contains(template, "END BEADS INTEGRATION") {
		t.Error("Missing END marker")
	}

	// Should have workflow section
	if !strings.Contains(template, "## Workflow") {
		t.Error("Missing Workflow section")
	}

	// Should have context loading section
	if !strings.Contains(template, "## Context Loading") {
		t.Error("Missing Context Loading section")
	}
}
