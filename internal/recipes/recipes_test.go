package recipes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	beadsplugin "github.com/steveyegge/beads/plugins/beads"
)

func TestBuiltinRecipes(t *testing.T) {
	// Ensure all expected built-in recipes exist
	expected := []string{"cursor", "windsurf", "cody", "kilocode", "kiro", "claude", "copilot", "gemini", "factory", "codex", "mux", "aider", "junie"}

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

// TestPathInside pins the rule bd setup holds a .beads/recipes.toml recipe's
// paths to: a relative path that stays inside the directory bd setup runs in,
// with nothing that exists along it leading out of that directory through a
// symbolic link.
func TestPathInside(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(dir, "sub", "deep"), filepath.Join(outside, "deep")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(dir, "file"), filepath.Join(dir, "sub", "real.md"), filepath.Join(outside, "victim.md")} {
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"up":           "..",                      // a directory link out of dir
		"in":           "sub",                     // a directory link inside dir
		"absin":        filepath.Join(dir, "sub"), // an absolute link inside dir
		"sub/out.md":   filepath.Join("..", "..", "outside", "victim.md"),
		"sub/in.md":    "real.md",
		"sub/dangling": "missing.md",
		"sub/loop":     "loop",
		"o":            filepath.Join("..", "outside"),         // .. after it leads to base
		"x":            filepath.Join("sub", "deep"),           // .. after it leads to sub
		"od":           filepath.Join("..", "outside", "deep"), // .. after it leads to outside
		// a link outside dir whose target is inside: removing it through
		// up removes an entry outside dir
		filepath.Join("..", "outside", "back.md"): filepath.Join(dir, "sub", "real.md"),
	}
	symlinks := true
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			symlinks = false
			t.Logf("symlink: %v", err)
			break
		}
	}
	// raw joins elements without cleaning them, as a recipe spells a path.
	raw := func(elem ...string) string { return strings.Join(elem, string(filepath.Separator)) }
	for _, tt := range []struct {
		path         string
		want         bool
		link         bool // needs the links above
		unfollowable bool // refused as ErrPathUnfollowable, not ErrPathOutside
	}{
		{path: ".myeditor/rules.md", want: true},
		{path: "sub/real.md", want: true},
		{path: raw("a", "..", "b.md"), want: true},
		{path: filepath.Join("file", "x.md"), want: true}, // the filesystem refuses it later, as before
		{path: "", want: false},
		{path: filepath.Join("..", "outside", "victim.md"), want: false},
		{path: raw("sub", "..", "..", "outside", "victim.md"), want: false},
		{path: filepath.Join(dir, "sub", "real.md"), want: false}, // absolute, even inside
		{path: filepath.Join(outside, "victim.md"), want: false},
		{path: filepath.Join("up", "outside", "victim.md"), want: false, link: true},
		{path: filepath.Join("up", "outside", "new", "x.md"), want: false, link: true},
		{path: filepath.Join("in", "real.md"), want: true, link: true},
		{path: filepath.Join("absin", "new.md"), want: true, link: true},
		{path: filepath.Join("sub", "out.md"), want: false, link: true},
		{path: filepath.Join("sub", "in.md"), want: true, link: true},
		{path: filepath.Join("sub", "dangling"), want: false, link: true, unfollowable: true},
		{path: filepath.Join("sub", "loop", "x.md"), want: false, link: true, unfollowable: true},
		{path: filepath.Join("up", "ws", "sub", "real.md"), want: true, link: true},
		{path: filepath.Join("up", "outside", "back.md"), want: false, link: true},
		// The OS takes .. after a link to the parent of its target.
		{path: raw("o", "..", "outside", "victim.md"), want: false, link: true},
		{path: raw("o", "..", "ws", "sub", "real.md"), want: true, link: true},
		{path: raw("in", "..", "sub", "real.md"), want: true, link: true},
		{path: raw("new", "..", "sub", "real.md"), want: true},
		{path: raw("sub", "dangling", "..", "real.md"), want: false, link: true, unfollowable: true},
		// The OS walks x/../up/new to sub/up/new, inside, but the directory
		// install creates and --remove drops is filepath.Dir of the path,
		// cleaned to up/new, which is outside.
		{path: raw("x", "..", "up", "new", "x.md"), want: false, link: true},
		{path: raw("x", "..", "sub", "deep", "x.md"), want: true, link: true},
		// The other way round: the cleaned directory is dir, but the OS
		// walks od/.. to outside, where back.md, a link to a file inside,
		// is an entry --remove would delete.
		{path: raw("od", "..", "back.md"), want: false, link: true},
		// Windows cleans .. away before it follows links, so it opens
		// x/../sub/out.md as sub/out.md, a link out: the cleaned path is
		// checked too.
		{path: raw("x", "..", "sub", "out.md"), want: false, link: true},
		// Each form needs its own check: the OS walks o/../ws/sub/out.md to
		// the linked file out, while its cleaned form, ws/sub/out.md under
		// dir, does not exist; and x/../up/outside/back.md cleans to a link
		// back inside whose directory, up/outside, is outside.
		{path: raw("o", "..", "ws", "sub", "out.md"), want: false, link: true},
		{path: raw("x", "..", "up", "outside", "back.md"), want: false, link: true},
	} {
		if tt.link && !symlinks {
			continue
		}
		err := PathInside(dir, tt.path)
		if got := err == nil; got != tt.want {
			t.Errorf("PathInside(dir, %q) = %v, want inside %v", tt.path, err, tt.want)
			continue
		}
		if want := ErrPathOutside; !tt.want {
			if tt.unfollowable {
				want = ErrPathUnfollowable
			}
			if !errors.Is(err, want) {
				t.Errorf("PathInside(dir, %q) = %v, want %v", tt.path, err, want)
			}
		}
	}
}

// TestPathInsideUnresolvableDir pins that a directory that cannot be resolved
// (a deleted working directory) is reported as such, not as a path outside.
func TestPathInsideUnresolvableDir(t *testing.T) {
	err := PathInside(filepath.Join(t.TempDir(), "gone"), "x.md")
	if err == nil || errors.Is(err, ErrPathOutside) || errors.Is(err, ErrPathUnfollowable) {
		t.Fatalf("PathInside(gone, x.md) = %v, want a directory resolution error", err)
	}
}

// TestLoadUserRecipesMarksWorkspaceRecipes pins that a recipe read from
// .beads/recipes.toml is marked Workspace, an override of a built-in
// included, and a built-in is not, and that SaveUserRecipe does not write the
// mark into the file.
func TestLoadUserRecipesMarksWorkspaceRecipes(t *testing.T) {
	beadsDir := t.TempDir()
	for name, path := range map[string]string{"myeditor": ".myeditor/rules.md", "windsurf": ".my-windsurf/beads.md"} {
		if err := SaveUserRecipe(beadsDir, name, path); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(beadsDir, "recipes.toml"))
	if err != nil || strings.Contains(strings.ToLower(string(data)), "workspace") {
		t.Fatalf("recipes.toml holds the Workspace mark: %v\n%s", err, data)
	}
	for name, want := range map[string]bool{"myeditor": true, "windsurf": true, "kiro": false, "copilot": false} {
		recipe, err := GetRecipe(name, beadsDir)
		if err != nil {
			t.Fatalf("GetRecipe(%s): %v", name, err)
		}
		if recipe.Workspace != want {
			t.Errorf("GetRecipe(%s).Workspace = %v, want %v", name, recipe.Workspace, want)
		}
	}
	for name, recipe := range BuiltinRecipes {
		if recipe.Workspace {
			t.Errorf("built-in recipe %s is marked Workspace", name)
		}
	}
}
