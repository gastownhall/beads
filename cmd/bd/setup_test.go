//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/recipes"
)

func resetSetupResolutionCaches(t *testing.T) {
	t.Helper()
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})
}

func resetSetupGlobals(t *testing.T) {
	t.Helper()

	originalProject := setupProject
	originalGlobal := setupGlobal
	originalCheck := setupCheck
	originalRemove := setupRemove
	originalStealth := setupStealth
	originalPrint := setupPrint
	originalOutput := setupOutput
	originalList := setupList
	originalAdd := setupAdd

	setupProject = false
	setupGlobal = false
	setupCheck = false
	setupRemove = false
	setupStealth = false
	setupPrint = false
	setupOutput = ""
	setupList = false
	setupAdd = ""

	t.Cleanup(func() {
		setupProject = originalProject
		setupGlobal = originalGlobal
		setupCheck = originalCheck
		setupRemove = originalRemove
		setupStealth = originalStealth
		setupPrint = originalPrint
		setupOutput = originalOutput
		setupList = originalList
		setupAdd = originalAdd
	})
}

func TestLoadSetupRecipes_NoWorkspaceUsesBuiltinsOnly(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	allRecipes, usingWorkspaceRecipes, err := loadSetupRecipes()
	if err != nil {
		t.Fatalf("loadSetupRecipes: %v", err)
	}
	if usingWorkspaceRecipes {
		t.Fatal("expected built-in-only recipe set without an active workspace")
	}
	if _, ok := allRecipes["cursor"]; !ok {
		t.Fatal("expected built-in cursor recipe to be available")
	}
	if _, ok := allRecipes["myeditor"]; ok {
		t.Fatal("unexpected orphan custom recipe outside active workspace")
	}
}

func TestListRecipes_NoWorkspaceShowsBuiltinOnlyNote(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	out := captureStdout(t, func() error {
		listRecipes()
		return nil
	})

	if !strings.Contains(out, "cursor") {
		t.Fatalf("expected built-in recipes in output, got:\n%s", out)
	}
	if !strings.Contains(out, "copilot") {
		t.Fatalf("expected copilot recipe in output, got:\n%s", out)
	}
	if strings.Contains(out, "myeditor") {
		t.Fatalf("unexpected orphan custom recipe in output:\n%s", out)
	}
	if !strings.Contains(out, "Note: No active beads workspace found. Showing built-in recipes only.") {
		t.Fatalf("expected built-in-only note in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Hint: "+diagHint()) {
		t.Fatalf("expected diagnostic hint in output, got:\n%s", out)
	}
}

func TestAddRecipe_NoWorkspaceReturnsActiveWorkspaceError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	err := addRecipe("myeditor", ".myeditor/rules.md")
	if err == nil {
		t.Fatal("expected addRecipe to fail without active workspace")
	}
	if !strings.Contains(err.Error(), activeWorkspaceNotFoundError()) {
		t.Fatalf("expected active-workspace error, got: %v", err)
	}
	if !strings.Contains(err.Error(), diagHint()) {
		t.Fatalf("expected diagnostic hint, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(tmpDir, ".beads", "recipes.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no local recipes.toml, got err=%v", statErr)
	}
}

func TestLookupSetupRecipe_NoWorkspaceIgnoresOrphanCustomRecipes(t *testing.T) {
	tmpDir := t.TempDir()
	orphanBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := recipes.SaveUserRecipe(orphanBeadsDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)

	_, err := lookupSetupRecipe("myeditor")
	if err == nil {
		t.Fatal("expected custom recipe lookup to fail without active workspace")
	}
	if !strings.Contains(err.Error(), "workspace-local custom recipes require an active beads workspace") {
		t.Fatalf("expected explicit no-workspace message, got: %v", err)
	}
}

func TestRunRecipe_BuiltinFileRecipeWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("windsurf")
		return nil
	})

	if !strings.Contains(out, "Installing Windsurf integration") {
		t.Fatalf("expected install output, got:\n%s", out)
	}

	installedPath := filepath.Join(tmpDir, ".windsurf", "rules", "beads.md")
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", installedPath, err)
	}
	if string(data) != recipes.Template {
		t.Fatalf("installed recipe contents did not match template")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no local .beads directory to be created, got err=%v", err)
	}
}

func TestRunRecipe_KiroWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("kiro")
		return nil
	})

	if !strings.Contains(out, "Installing Kiro CLI integration") {
		t.Fatalf("expected install output, got:\n%s", out)
	}

	installedPath := filepath.Join(tmpDir, ".kiro", "steering", "beads.md")
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", installedPath, err)
	}
	if string(data) != recipes.Template {
		t.Fatalf("installed Kiro recipe contents did not match template")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no local .beads directory to be created, got err=%v", err)
	}
}

func TestRunRecipe_CopilotWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	if !strings.Contains(out, "GitHub Copilot CLI integration installed") {
		t.Fatalf("expected copilot install output, got:\n%s", out)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected copilot plugin manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".github", "copilot-instructions.md")); err != nil {
		t.Fatalf("expected copilot instructions: %v", err)
	}
}

func TestRunRecipe_CopilotCheckWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	runRecipe("copilot")

	resetSetupGlobals(t)
	setupCheck = true

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	if !strings.Contains(out, ".copilot-plugin/plugin.json") {
		t.Fatalf("expected plugin manifest in check output, got:\n%s", out)
	}
	if !strings.Contains(out, ".github/copilot-instructions.md") {
		t.Fatalf("expected instructions file in check output, got:\n%s", out)
	}
}

func TestRunRecipe_CopilotPreservesExistingInstructions(t *testing.T) {
	tmpDir := t.TempDir()

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	const userContent = "# Project conventions\n\nAlways run make check.\n"
	if err := os.WriteFile(instructions, []byte(userContent), 0o644); err != nil {
		t.Fatalf("seed instructions: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	data, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "Always run make check.") {
		t.Fatalf("user-authored instructions were lost, got:\n%s", got)
	}
	if !strings.Contains(got, "BEGIN BEADS INTEGRATION") || !strings.Contains(got, "bd prime") {
		t.Fatalf("beads section missing after install, got:\n%s", got)
	}
	if strings.Count(got, "BEGIN BEADS INTEGRATION") != 1 {
		t.Fatalf("expected exactly one beads section, got:\n%s", got)
	}
	if !strings.Contains(out, "Added beads section") {
		t.Fatalf("expected the install to report an added section, got:\n%s", out)
	}

	// The manifest is beads-owned, so it is still written as a whole file.
	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected copilot plugin manifest: %v", err)
	}

	// Re-running replaces the section in place instead of appending a second one.
	resetSetupGlobals(t)
	captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})
	again, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("re-read instructions: %v", err)
	}
	if string(again) != got {
		t.Fatalf("re-running setup was not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func TestRunRecipe_CopilotCheckReportsMissingSection(t *testing.T) {
	tmpDir := t.TempDir()

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.WriteFile(instructions, []byte("# Only mine\n"), 0o644); err != nil {
		t.Fatalf("seed instructions: %v", err)
	}
	manifest := filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatalf("mkdir .copilot-plugin: %v", err)
	}
	if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)
	setupCheck = true

	out := captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	if !strings.Contains(out, "No beads section") || !strings.Contains(out, ".github/copilot-instructions.md") {
		t.Fatalf("expected --check to report the missing section, got:\n%s", out)
	}
}

func TestRunRecipe_CopilotRemoveKeepsUserInstructions(t *testing.T) {
	tmpDir := t.TempDir()

	instructions := filepath.Join(tmpDir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(instructions), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	const userContent = "# Project conventions\n\nAlways run make check.\n"
	if err := os.WriteFile(instructions, []byte(userContent), 0o644); err != nil {
		t.Fatalf("seed instructions: %v", err)
	}

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	resetSetupGlobals(t)
	setupRemove = true
	captureStdout(t, func() error {
		runRecipe("copilot")
		return nil
	})

	data, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatalf("instructions should survive --remove: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "Always run make check.") {
		t.Fatalf("user-authored instructions were removed, got:\n%s", got)
	}
	if strings.Contains(got, "BEGIN BEADS INTEGRATION") {
		t.Fatalf("beads section survived --remove, got:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("expected the beads-owned manifest to be removed, got err=%v", err)
	}
}

func TestRunRecipe_CopilotRemoveWorksWithoutWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)
	t.Setenv("BEADS_DIR", "")
	resetSetupResolutionCaches(t)
	resetSetupGlobals(t)

	runRecipe("copilot")

	resetSetupGlobals(t)
	setupRemove = true
	runRecipe("copilot")

	if _, err := os.Stat(filepath.Join(tmpDir, ".copilot-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatalf("expected plugin manifest to be removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".github", "copilot-instructions.md")); !os.IsNotExist(err) {
		t.Fatalf("expected copilot instructions to be removed, got err=%v", err)
	}
}
