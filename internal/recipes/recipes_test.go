package recipes

import (
	"os"
	"path/filepath"
	"testing"

	beadsplugin "github.com/steveyegge/beads/plugins/beads"
)

func TestBuiltinRecipes(t *testing.T) {
	// Ensure all expected built-in recipes exist
	expected := []string{"cursor", "windsurf", "cody", "kilocode", "claude", "copilot", "gemini", "factory", "codex", "mux", "aider", "junie"}

	for _, name := range expected {
		recipe, ok := BuiltinRecipes[name]
		if !ok {
			t.Errorf("missing built-in recipe: %s", name)
			continue
		}
		if recipe.Name == "" {
			t.Errorf("recipe %s has empty Name", name)
		}
		if recipe.Type == "" {
			t.Errorf("recipe %s has empty Type", name)
		}
	}
}

func TestGetRecipe(t *testing.T) {
	// Test getting a built-in recipe
	recipe, err := GetRecipe("cursor", "")
	if err != nil {
		t.Fatalf("GetRecipe(cursor): %v", err)
	}
	if recipe.Name != "Cursor IDE" {
		t.Errorf("got Name=%q, want 'Cursor IDE'", recipe.Name)
	}
	if recipe.Path != ".cursor/rules/beads.mdc" {
		t.Errorf("got Path=%q, want '.cursor/rules/beads.mdc'", recipe.Path)
	}

	// Test unknown recipe
	_, err = GetRecipe("nonexistent", "")
	if err == nil {
		t.Error("GetRecipe(nonexistent) should return error")
	}
}

func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("cursor") {
		t.Error("cursor should be builtin")
	}
	if IsBuiltin("myeditor") {
		t.Error("myeditor should not be builtin")
	}
}

func TestListRecipeNames(t *testing.T) {
	names, err := ListRecipeNames("")
	if err != nil {
		t.Fatalf("ListRecipeNames: %v", err)
	}

	// Check that it's sorted
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("names not sorted: %v", names)
			break
		}
	}

	// Check expected recipes present
	found := make(map[string]bool)
	for _, name := range names {
		found[name] = true
	}
	for _, expected := range []string{"cursor", "claude", "aider", "mux"} {
		if !found[expected] {
			t.Errorf("expected recipe %s not in list", expected)
		}
	}
}

func TestUserRecipes(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "recipes-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save a user recipe
	if err := SaveUserRecipe(tmpDir, "myeditor", ".myeditor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	// Verify file was created
	recipesPath := filepath.Join(tmpDir, "recipes.toml")
	if _, err := os.Stat(recipesPath); os.IsNotExist(err) {
		t.Error("recipes.toml was not created")
	}

	// Load user recipes
	userRecipes, err := LoadUserRecipes(tmpDir)
	if err != nil {
		t.Fatalf("LoadUserRecipes: %v", err)
	}

	recipe, ok := userRecipes["myeditor"]
	if !ok {
		t.Fatal("myeditor recipe not found")
	}
	if recipe.Path != ".myeditor/rules.md" {
		t.Errorf("got Path=%q, want '.myeditor/rules.md'", recipe.Path)
	}
	if recipe.Type != TypeFile {
		t.Errorf("got Type=%q, want 'file'", recipe.Type)
	}

	// GetRecipe should find user recipe
	r, err := GetRecipe("myeditor", tmpDir)
	if err != nil {
		t.Fatalf("GetRecipe(myeditor): %v", err)
	}
	if r.Path != ".myeditor/rules.md" {
		t.Errorf("got Path=%q, want '.myeditor/rules.md'", r.Path)
	}
}

func TestUserRecipeOverride(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "recipes-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save a user recipe that overrides cursor
	if err := SaveUserRecipe(tmpDir, "cursor", ".my-cursor/rules.md"); err != nil {
		t.Fatalf("SaveUserRecipe: %v", err)
	}

	// GetRecipe should return user's version
	r, err := GetRecipe("cursor", tmpDir)
	if err != nil {
		t.Fatalf("GetRecipe(cursor): %v", err)
	}
	if r.Path != ".my-cursor/rules.md" {
		t.Errorf("user override failed: got Path=%q, want '.my-cursor/rules.md'", r.Path)
	}
}

func TestLoadUserRecipesNoFile(t *testing.T) {
	// Should return nil, nil when no file exists
	recipes, err := LoadUserRecipes("/nonexistent/path")
	if err != nil {
		t.Errorf("LoadUserRecipes should not error on missing file: %v", err)
	}
	if recipes != nil {
		t.Error("LoadUserRecipes should return nil for missing file")
	}
}

func TestTemplate(t *testing.T) {
	// Basic sanity check that template is not empty
	if len(Template) < 100 {
		t.Error("Template is suspiciously short")
	}
	// Check for key content
	if !containsAll(Template, "Beads", "bd ready", "bd create", "bd close") {
		t.Error("Template missing expected content")
	}
}

func TestContentForPathCopilot(t *testing.T) {
	recipe := BuiltinRecipes["copilot"]

	manifest, err := ContentForPath(recipe, ".copilot-plugin/plugin.json")
	if err != nil {
		t.Fatalf("ContentForPath(plugin.json): %v", err)
	}
	if manifest != beadsplugin.CopilotPluginManifest() {
		t.Fatalf("copilot manifest does not match embedded shared plugin manifest")
	}

	instructions, err := ContentForPath(recipe, ".github/copilot-instructions.md")
	if err != nil {
		t.Fatalf("ContentForPath(copilot-instructions.md): %v", err)
	}
	if !containsAll(instructions, "GitHub Copilot Instructions", "bd ready", "bd prime") {
		t.Fatalf("unexpected Copilot instructions:\n%s", instructions)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
