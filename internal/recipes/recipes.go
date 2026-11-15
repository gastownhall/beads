// Package recipes provides recipe-based configuration for bd setup.
// Recipes define where beads workflow instructions are written for different AI tools.
package recipes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	beadsplugin "github.com/steveyegge/beads/plugins/beads"
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
	Content     string     `toml:"-"`           // Optional static content for TypeFile
	// Optional fields for complex recipes
	GlobalPath  string            `toml:"global_path"`  // Global settings path (for hooks)
	ProjectPath string            `toml:"project_path"` // Project settings path (for hooks)
	Paths       []string          `toml:"paths"`        // Multiple paths (for multifile)
	Contents    map[string]string `toml:"-"`            // Optional static contents for TypeMultiFile
	// Workspace marks a recipe read from a workspace's .beads/recipes.toml
	// (LoadUserRecipes), which a repository can commit, so bd setup confines
	// the files it names to the directory it runs in (PathInside). A
	// built-in recipe's paths are bd's own, and leave it false.
	Workspace bool `toml:"-"`
}

// BuiltinRecipes contains the default recipe definitions.
// These are compiled into the binary.
var BuiltinRecipes = map[string]Recipe{
	"cursor": {
		Name:        "Cursor IDE",
		Path:        ".cursor/rules/beads.mdc",
		Type:        TypeFile,
		Description: "Cursor IDE rules file + agent hooks (prime on session start, restore after compaction)",
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
	"kiro": {
		Name:        "Kiro CLI",
		Path:        ".kiro/steering/beads.md",
		Type:        TypeFile,
		Description: "Kiro steering file",
	},
	"claude": {
		Name:        "Claude Code",
		Type:        TypeHooks,
		Description: "Claude Code hooks (SessionStart)",
		GlobalPath:  "~/.claude/settings.json",
		ProjectPath: ".claude/settings.json",
	},
	"gemini": {
		Name:        "Gemini CLI",
		Type:        TypeHooks,
		Description: "Gemini CLI hooks (SessionStart)",
		GlobalPath:  "~/.gemini/settings.json",
		ProjectPath: ".gemini/settings.json",
	},
	"copilot": {
		Name:        "GitHub Copilot CLI",
		Type:        TypeMultiFile,
		Description: "Copilot CLI plugin manifest + instructions",
		Paths: []string{
			".copilot-plugin/plugin.json",
			".github/copilot-instructions.md",
		},
		Contents: map[string]string{
			".copilot-plugin/plugin.json":     beadsplugin.CopilotPluginManifest(),
			".github/copilot-instructions.md": CopilotInstructionsTemplate,
		},
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
		Description: "Codex CLI skill guidance",
	},
	"mux": {
		Name:        "Mux",
		Path:        "AGENTS.md",
		Type:        TypeSection,
		Description: "Mux AGENTS.md section",
	},
	"opencode": {
		Name:        "OpenCode",
		Path:        "AGENTS.md",
		Type:        TypeSection,
		Description: "OpenCode AGENTS.md section",
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
		recipe.Workspace = true
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
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
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

// ErrPathOutside and ErrPathUnfollowable are the reasons PathInside gives for
// a path that does not stay inside the directory.
var (
	// ErrPathOutside: the path is absolute, leaves the directory through
	// .., or leads out of it through a symbolic link.
	ErrPathOutside = errors.New("path is not inside the directory")
	// ErrPathUnfollowable: the path runs through a symbolic link that
	// cannot be followed (a dangling one, a loop), so where it leads
	// cannot be told.
	ErrPathUnfollowable = errors.New("path runs through a symbolic link that cannot be followed")
)

// PathInside reports, by a nil error, that path, a file a recipe names,
// stays inside dir, the directory bd setup runs in and resolves the path
// against: path is local to dir (filepath.IsLocal: relative, not empty, and
// not leaving dir through ..), and neither the directory that holds its
// file, nor the file, nor the directory filepath.Dir(path) names (which
// install creates and --remove drops when empty), as far as each exists,
// leads out of dir through a symbolic link. The path is checked both as
// Linux and macOS walk it, where a .. goes to the parent of what the name
// before it resolves to, so a .. after a linked directory is not cleaned
// away, and in its cleaned form (filepath.Clean), which is what Windows
// opens, since it cleans .. away before it follows links. The file is
// followed when it is a link, since writing it writes the link's target.
// Otherwise it returns ErrPathOutside, or ErrPathUnfollowable for a link
// that cannot be followed (a dangling one, a loop), since where a write
// through it lands cannot be told, or the error that dir could not be
// resolved (a deleted working directory).
func PathInside(dir, path string) error {
	if !filepath.IsLocal(path) {
		return ErrPathOutside
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve the directory: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve the directory: %w", err)
	}
	full := abs
	for _, elem := range strings.FieldsFunc(path, isPathSeparator) {
		switch elem {
		case ".":
		case "..":
			resolved, ok := resolveExisting(full)
			if !ok {
				return ErrPathUnfollowable
			}
			full = filepath.Dir(resolved)
		default:
			full = filepath.Join(full, elem)
		}
	}
	for _, p := range []string{filepath.Dir(full), full, filepath.Join(abs, filepath.Dir(path)), filepath.Join(abs, path)} {
		if err := resolvesInside(root, p); err != nil {
			return err
		}
	}
	return nil
}

// isPathSeparator reports whether r separates the elements of a path.
func isPathSeparator(r rune) bool {
	return r < 0x80 && os.IsPathSeparator(uint8(r))
}

// resolvesInside reports, by a nil error, that path's longest existing
// part, a link at its end included, resolves to root or a path inside it.
func resolvesInside(root, path string) error {
	resolved, ok := resolveExisting(path)
	if !ok {
		return ErrPathUnfollowable
	}
	if rel, err := filepath.Rel(root, resolved); err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return ErrPathOutside
	}
	return nil
}

// resolveExisting resolves the symbolic links in path's longest existing
// part, a link at its end included, and joins the rest of path to it. It
// fails when that part cannot be resolved (a dangling link, a loop).
func resolveExisting(path string) (string, bool) {
	existing, rest := path, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", false
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", false
	}
	return filepath.Join(resolved, rest), true
}

// IsBuiltin returns true if the recipe is a built-in (not user-defined).
func IsBuiltin(name string) bool {
	_, ok := BuiltinRecipes[name]
	return ok
}
