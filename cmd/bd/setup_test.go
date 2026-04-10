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
