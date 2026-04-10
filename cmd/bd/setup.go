package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/recipes"
)

var (
	setupProject bool
	setupGlobal  bool
	setupCheck   bool
	setupRemove  bool
	setupStealth bool
	setupPrint   bool
	setupOutput  string
	setupList    bool
	setupAdd     string
)

var setupCmd = &cobra.Command{
	Use:     "setup [recipe]",
	GroupID: "setup",
	Short:   "Setup integration with AI editors",
	Long: `Setup integration files for AI editors and coding assistants.

Recipes define where beads workflow instructions are written. Built-in recipes
include cursor, claude, gemini, aider, factory, codex, mux, opencode, junie, windsurf, cody, and kilocode.

Examples:
  bd setup cursor          # Install Cursor IDE integration
  bd setup mux --project   # Install Mux workspace layer (.mux/AGENTS.md)
  bd setup mux --global    # Install Mux global layer (~/.mux/AGENTS.md)
  bd setup mux --project --global  # Install both Mux layers
  bd setup --list          # Show all available recipes
  bd setup --print         # Print the template to stdout
  bd setup -o rules.md     # Write template to custom path
  bd setup --add myeditor .myeditor/rules.md  # Add custom recipe

Use 'bd setup <recipe> --check' to verify installation status.
Use 'bd setup <recipe> --remove' to uninstall.`,
	Args: cobra.MaximumNArgs(1),
	Run:  runSetup,
}

func runSetup(cmd *cobra.Command, args []string) {
	// Handle --list flag
	if setupList {
		listRecipes()
		return
	}

	// Handle --print flag (no recipe needed)
	if setupPrint {
		fmt.Print(recipes.Template)
		return
	}

	// Handle -o flag (write to arbitrary path)
	if setupOutput != "" {
		if err := writeToPath(setupOutput); err != nil {
			FatalError("%v", err)
		}
		fmt.Printf("✓ Wrote template to %s\n", setupOutput)
		return
	}

	// Handle --add flag (save custom recipe)
	if setupAdd != "" {
		if len(args) != 1 {
			FatalErrorWithHint("--add requires a path argument", "Usage: bd setup --add <name> <path>")
		}
		if err := addRecipe(setupAdd, args[0]); err != nil {
			FatalError("%v", err)
		}
		return
	}

	// Require a recipe name for install/check/remove
	if len(args) == 0 {
		_ = cmd.Help()
		return
	}

	recipeName := strings.ToLower(args[0])
	runRecipe(recipeName)
}

func setupWorkspaceError() error {
	return fmt.Errorf("%s; %s", activeWorkspaceNotFoundError(), diagHint())
}

func builtinSetupRecipes() map[string]recipes.Recipe {
	allRecipes := make(map[string]recipes.Recipe, len(recipes.BuiltinRecipes))
	for name, recipe := range recipes.BuiltinRecipes {
		allRecipes[name] = recipe
	}
	return allRecipes
}

func loadSetupRecipes() (map[string]recipes.Recipe, bool, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return builtinSetupRecipes(), false, nil
	}

	allRecipes, err := recipes.GetAllRecipes(beadsDir)
	if err != nil {
		return nil, false, err
	}
	return allRecipes, true, nil
}

func lookupSetupRecipe(name string) (*recipes.Recipe, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		normalized := strings.ToLower(strings.Trim(name, "-"))
		recipe, ok := recipes.BuiltinRecipes[normalized]
		if !ok {
			return nil, fmt.Errorf("unknown recipe: %s (workspace-local custom recipes require an active beads workspace)", normalized)
		}
		resolved := recipe
		return &resolved, nil
	}

	return recipes.GetRecipe(name, beadsDir)
}

func listRecipes() {
	allRecipes, usingWorkspaceRecipes, err := loadSetupRecipes()
	if err != nil {
		FatalError("loading recipes: %v", err)
	}

	// Sort recipe names
	names := make([]string, 0, len(allRecipes))
	for name := range allRecipes {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("Available recipes:")
	fmt.Println()
	for _, name := range names {
		r := allRecipes[name]
		source := "built-in"
		if !recipes.IsBuiltin(name) {
			source = "user"
		}
		fmt.Printf("  %-12s  %-25s  (%s)\n", name, r.Description, source)
	}
	fmt.Println()
	if !usingWorkspaceRecipes {
		fmt.Printf("Note: %s Showing built-in recipes only.\n", activeWorkspaceNotFoundMessage())
		fmt.Printf("Hint: %s\n", diagHint())
		fmt.Println()
	}
	fmt.Println("Use 'bd setup <recipe>' to install.")
	fmt.Println("Use 'bd setup --add <name> <path>' to add a custom recipe.")
}

func writeToPath(path string) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	if err := os.WriteFile(path, []byte(recipes.Template), 0o644); err != nil { // #nosec G306 -- config files need to be readable
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func addRecipe(name, path string) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return setupWorkspaceError()
	}

	if err := recipes.SaveUserRecipe(beadsDir, name, path); err != nil {
		return err
	}

	fmt.Printf("✓ Added recipe '%s' → %s\n", name, path)
	fmt.Printf("  Config: %s/recipes.toml\n", beadsDir)
	fmt.Println()
	fmt.Printf("Install with: bd setup %s\n", name)
	return nil
}

func runRecipe(name string) {
	// Check for legacy recipes that need special handling
	switch name {
	case "claude":
		runClaudeRecipe()
		return
	case "gemini":
		runGeminiRecipe()
		return
	case "factory":
		runFactoryRecipe()
		return
	case "codex":
		runCodexRecipe()
		return
	case "mux":
		runMuxRecipe()
		return
	case "opencode":
		runOpenCodeRecipe()
		return
	case "aider":
		runAiderRecipe()
		return
	case "cursor":
		runCursorRecipe()
		return
	case "junie":
		runJunieRecipe()
		return
	}

	// For all other recipes (built-in or user), use generic file-based install
	recipe, err := lookupSetupRecipe(name)
	if err != nil {
		FatalErrorWithHint(fmt.Sprintf("%v", err), "Use 'bd setup --list' to see available recipes.")
	}

	if recipe.Type != recipes.TypeFile {
		FatalError("recipe '%s' has type '%s' which requires special handling", name, recipe.Type)
	}

	// Handle --check
	if setupCheck {
		if _, err := os.Stat(recipe.Path); os.IsNotExist(err) {
			fmt.Printf("✗ %s integration not installed\n", recipe.Name)
			fmt.Printf("  Run: bd setup %s\n", name)
			os.Exit(1)
		}
		fmt.Printf("✓ %s integration installed: %s\n", recipe.Name, recipe.Path)
		return
	}

	// Handle --remove
	if setupRemove {
		if err := os.Remove(recipe.Path); err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No integration file found")
				return
			}
			FatalError("%v", err)
		}
		fmt.Printf("✓ Removed %s integration\n", recipe.Name)
		return
	}

	// Install
	fmt.Printf("Installing %s integration...\n", recipe.Name)

	// Ensure parent directory exists
	dir := filepath.Dir(recipe.Path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			FatalError("create directory: %v", err)
		}
	}

	if err := os.WriteFile(recipe.Path, []byte(recipes.Template), 0o644); err != nil { // #nosec G306 -- config files need to be readable
		FatalError("write file: %v", err)
	}

	fmt.Printf("\n✓ %s integration installed\n", recipe.Name)
	fmt.Printf("  File: %s\n", recipe.Path)
}

// Legacy recipe handlers that delegate to existing implementations

func runCursorRecipe() {
	if setupCheck {
		setup.CheckCursor()
		return
	}
	if setupRemove {
		setup.RemoveCursor()
		return
	}
	setup.InstallCursor()
}

func runClaudeRecipe() {
	if setupCheck {
		setup.CheckClaude()
		return
	}
	if setupRemove {
		setup.RemoveClaude(setupGlobal)
		return
	}
	setup.InstallClaude(setupGlobal, setupStealth)
}

func runGeminiRecipe() {
	if setupCheck {
		setup.CheckGemini()
		return
	}
	if setupRemove {
		setup.RemoveGemini(setupProject)
		return
	}
	setup.InstallGemini(setupProject, setupStealth)
}

func runFactoryRecipe() {
	if setupCheck {
		setup.CheckFactory()
		return
	}
	if setupRemove {
		setup.RemoveFactory()
		return
	}
	setup.InstallFactory()
}

func runCodexRecipe() {
	if setupCheck {
		setup.CheckCodex()
		return
	}
	if setupRemove {
		setup.RemoveCodex()
		return
	}
	setup.InstallCodex()
}

func runOpenCodeRecipe() {
	if setupCheck {
		setup.CheckOpenCode()
		return
	}
	if setupRemove {
		setup.RemoveOpenCode()
		return
	}
	setup.InstallOpenCode()
}

func runMuxRecipe() {
	if setupCheck {
		setup.CheckMux(setupProject, setupGlobal)
		return
	}
	if setupRemove {
		setup.RemoveMux(setupProject, setupGlobal)
		return
	}
	setup.InstallMux(setupProject, setupGlobal)
}

func runAiderRecipe() {
	if setupCheck {
		setup.CheckAider()
		return
	}
	if setupRemove {
		setup.RemoveAider()
		return
	}
	setup.InstallAider()
}

func runJunieRecipe() {
	if setupCheck {
		setup.CheckJunie()
		return
	}
	if setupRemove {
		setup.RemoveJunie()
		return
	}
	setup.InstallJunie()
}

func init() {
	// Global flags for the setup command
	setupCmd.Flags().BoolVar(&setupList, "list", false, "List all available recipes")
	setupCmd.Flags().BoolVar(&setupPrint, "print", false, "Print the template to stdout")
	setupCmd.Flags().StringVarP(&setupOutput, "output", "o", "", "Write template to custom path")
	setupCmd.Flags().StringVar(&setupAdd, "add", "", "Add a custom recipe with given name")

	// Per-recipe flags
	setupCmd.Flags().BoolVar(&setupCheck, "check", false, "Check if integration is installed")
	setupCmd.Flags().BoolVar(&setupRemove, "remove", false, "Remove the integration")
	setupCmd.Flags().BoolVar(&setupProject, "project", false, "Install for this project only (gemini/mux)")
	setupCmd.Flags().BoolVar(&setupGlobal, "global", false, "Install globally (claude/mux; writes to ~/.claude/settings.json or ~/.mux/AGENTS.md)")
	setupCmd.Flags().BoolVar(&setupStealth, "stealth", false, "Use stealth mode (claude/gemini)")

	rootCmd.AddCommand(setupCmd)
}
