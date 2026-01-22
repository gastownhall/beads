// Package recipes provides recipe-based configuration for bd setup.
// Recipes define where beads workflow instructions are written for different AI tools.
package recipes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// RecipeType indicates how the recipe is installed.
type RecipeType string

const (
	// TypeFile writes template to a file path (simple case)
	TypeFile RecipeType = "file"
	// TypeHooks modifies JSON settings to add hooks (claude, gemini)
	TypeHooks RecipeType = "hooks"
	// TypeSection injects a marked section into existing file (factory)
	TypeSection RecipeType = "section"
	// TypeMultiFile writes multiple files (aider)
	TypeMultiFile RecipeType = "multifile"
)

// Recipe defines an AI tool integration.
type Recipe struct {
	Name        string     `toml:"name"`        // Display name (e.g., "Cursor IDE")
	Path        string     `toml:"path"`        // Primary file path (for TypeFile)
	Type        RecipeType `toml:"type"`        // How to install
	Description string     `toml:"description"` // Brief description
	// Optional fields for complex recipes
	GlobalPath  string   `toml:"global_path"`  // Global settings path (for hooks)
	ProjectPath string   `toml:"project_path"` // Project settings path (for hooks)
	Paths       []string `toml:"paths"`        // Multiple paths (for multifile)
}

// BuiltinRecipes contains the default recipe definitions.
// These are compiled into the binary.
var BuiltinRecipes = map[string]Recipe{
	"cursor": {
		Name:        "Cursor IDE",
		Path:        ".cursor/rules/beads.mdc",
		Type:        TypeFile,
		Description: "Cursor IDE rules file",
	},
	"windsurf": {
		Name:        "Windsurf",
		Path:        ".windsurf/rules/beads.md",
		Type:        TypeFile,
		Description: "Windsurf editor rules file",
	},
	"cody": {
		Name:        "Sourcegraph Cody",
		Path:        ".cody/rules/beads.md",
		Type:        TypeFile,
		Description: "Cody AI rules file",
	},
	"kilocode": {
		Name:        "Kilo Code",
		Path:        ".kilocode/rules/beads.md",
		Type:        TypeFile,
		Description: "Kilo Code rules file",
	},
	"claude": {
		Name:        "Claude Code",
		Type:        TypeHooks,
		Description: "Claude Code hooks (SessionStart, PreCompact)",
		GlobalPath:  "~/.claude/settings.json",
		ProjectPath: ".claude/settings.local.json",
	},
	"gemini": {
		Name:        "Gemini CLI",
		Type:        TypeHooks,
		Description: "Gemini CLI hooks (SessionStart, PreCompress)",
		GlobalPath:  "~/.gemini/settings.json",
		ProjectPath: ".gemini/settings.json",
	},
	"factory": {
		Name:        "Factory.ai (Droid)",
		Path:        "AGENTS.md",
		Type:        TypeSection,
		Description: "Factory Droid AGENTS.md section",
	},
	"codex": {
		Name:        "Codex CLI",
		Path:        "AGENTS.md",
		Type:        TypeSection,
		Description: "Codex CLI AGENTS.md section",
	},
	"aider": {
		Name:        "Aider",
		Type:        TypeMultiFile,
		Description: "Aider config and instruction files",
		Paths:       []string{".aider.conf.yml", ".aider/BEADS.md", ".aider/README.md"},
	},
	"junie": {
		Name:        "Junie",
		Type:        TypeMultiFile,
		Description: "Junie guidelines and MCP configuration",
		Paths:       []string{".junie/guidelines.md", ".junie/mcp/mcp.json"},
	},
}

// UserRecipes holds recipes loaded from user config file.
type UserRecipes struct {
	Recipes map[string]Recipe `toml:"recipes"`
}

// LoadUserRecipes loads recipes from .beads/recipes.toml if it exists.
func LoadUserRecipes(beadsDir string) (map[string]Recipe, error) {
	path := filepath.Join(beadsDir, "recipes.toml")
	data, err := os.ReadFile(path) // #nosec G304 -- path is constructed from validated beadsDir
	if os.IsNotExist(err) {
		return nil, nil // No user recipes, that's fine
	}
	if err != nil {
		return nil, fmt.Errorf("read recipes.toml: %w", err)
	}

	var userRecipes UserRecipes
	if err := toml.Unmarshal(data, &userRecipes); err != nil {
		return nil, fmt.Errorf("parse recipes.toml: %w", err)
	}

	// Set defaults for user recipes
	for name, recipe := range userRecipes.Recipes {
		if recipe.Type == "" {
			recipe.Type = TypeFile
		}
		if recipe.Name == "" {
			recipe.Name = name
		}
		userRecipes.Recipes[name] = recipe
	}

	return userRecipes.Recipes, nil
}

// GetAllRecipes returns merged built-in and user recipes.
// User recipes override built-in recipes with the same name.
func GetAllRecipes(beadsDir string) (map[string]Recipe, error) {
	result := make(map[string]Recipe)

	// Start with built-in recipes
	for name, recipe := range BuiltinRecipes {
		result[name] = recipe
	}

	// Load and merge user recipes
	userRecipes, err := LoadUserRecipes(beadsDir)
	if err != nil {
		return nil, err
	}
	for name, recipe := range userRecipes {
		result[name] = recipe
	}

	return result, nil
}

// GetRecipe looks up a recipe by name, checking user recipes first.
func GetRecipe(name string, beadsDir string) (*Recipe, error) {
	// Normalize name (lowercase, strip leading/trailing hyphens)
	name = strings.ToLower(strings.Trim(name, "-"))

	recipes, err := GetAllRecipes(beadsDir)
	if err != nil {
		return nil, err
	}

	recipe, ok := recipes[name]
	if !ok {
		return nil, fmt.Errorf("unknown recipe: %s", name)
	}

	return &recipe, nil
}

// SaveUserRecipe adds or updates a recipe in .beads/recipes.toml.
func SaveUserRecipe(beadsDir, name, path string) error {
	recipesPath := filepath.Join(beadsDir, "recipes.toml")

	// Load existing user recipes
	var userRecipes UserRecipes
	data, err := os.ReadFile(recipesPath) // #nosec G304 -- path is constructed from validated beadsDir
	if err == nil {
		if err := toml.Unmarshal(data, &userRecipes); err != nil {
			return fmt.Errorf("parse recipes.toml: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read recipes.toml: %w", err)
	}

	if userRecipes.Recipes == nil {
		userRecipes.Recipes = make(map[string]Recipe)
	}

	// Add/update the recipe
	userRecipes.Recipes[name] = Recipe{
		Name: name,
		Path: path,
		Type: TypeFile,
	}

	// Ensure directory exists
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		return fmt.Errorf("create beads dir: %w", err)
	}

	// Write back
	f, err := os.Create(recipesPath) // #nosec G304 -- path is constructed from validated beadsDir
	if err != nil {
		return fmt.Errorf("create recipes.toml: %w", err)
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(userRecipes); err != nil {
		return fmt.Errorf("encode recipes.toml: %w", err)
	}

	return nil
}

// ListRecipeNames returns sorted list of all recipe names.
func ListRecipeNames(beadsDir string) ([]string, error) {
	recipes, err := GetAllRecipes(beadsDir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(recipes))
	for name := range recipes {
		names = append(names, name)
	}

	// Sort alphabetically
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}

	return names, nil
}

// IsBuiltin returns true if the recipe is a built-in (not user-defined).
func IsBuiltin(name string) bool {
	_, ok := BuiltinRecipes[name]
	return ok
}
