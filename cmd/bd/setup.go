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
	"github.com/steveyegge/beads/internal/metrics"
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
include cursor, claude, copilot, gemini, aider, factory, codex, mux, opencode, junie, windsurf, cody, and kilocode.

Examples:
  bd setup cursor          # Install Cursor IDE integration
  bd setup codex           # Install Codex skill + AGENTS.md guidance + native hooks
  bd setup codex --global  # Install global Codex skill + guidance + native hooks
  bd setup copilot         # Install Copilot CLI plugin + repository instructions
  bd setup mux --project   # Install Mux workspace layer (.mux/AGENTS.md)
  bd setup mux --global    # Install Mux global layer (~/.mux/AGENTS.md)
  bd setup mux --project --global  # Install both Mux layers
  bd setup --list          # Show all available recipes
  bd setup --print         # Print the template to stdout
  bd setup -o rules.md     # Write template to custom path
  bd setup --add myeditor .myeditor/rules.md  # Add custom recipe

Use 'bd setup <recipe> --check' to verify installation status.
Use 'bd setup <recipe> --remove' to uninstall.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runSetup,
}

func runSetup(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("setup")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if setupList {
		return listRecipes()
	}

	if setupPrint {
		fmt.Print(recipes.Template)
		return nil
	}

	if setupOutput != "" {
		if err := writeToPath(setupOutput); err != nil {
			return HandleError("%v", err)
		}
		fmt.Printf("✓ Wrote template to %s\n", setupOutput)
		return nil
	}

	if setupAdd != "" {
		if len(args) != 1 {
			return HandleErrorWithHint("--add requires a path argument", "Usage: bd setup --add <name> <path>")
		}
		if err := addRecipe(setupAdd, args[0]); err != nil {
			return HandleError("%v", err)
		}
		return nil
	}

	if len(args) == 0 {
		_ = cmd.Help()
		return nil
	}

	recipeName := strings.ToLower(args[0])
	return runRecipe(recipeName)
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

func listRecipes() error {
	allRecipes, usingWorkspaceRecipes, err := loadSetupRecipes()
	if err != nil {
		return HandleError("loading recipes: %v", err)
	}

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
	return nil
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

func runRecipe(name string) error {
	switch name {
	case "claude":
		return runClaudeRecipe()
	case "gemini":
		return runGeminiRecipe()
	case "factory":
		return runFactoryRecipe()
	case "codex":
		return runCodexRecipe()
	case "mux":
		return runMuxRecipe()
	case "opencode":
		return runOpenCodeRecipe()
	case "aider":
		return runAiderRecipe()
	case "cursor":
		return runCursorRecipe()
	case "junie":
		return runJunieRecipe()
	}

	recipe, err := lookupSetupRecipe(name)
	if err != nil {
		return HandleErrorWithHint(fmt.Sprintf("%v", err), "Use 'bd setup --list' to see available recipes.")
	}

	if recipe.Type != recipes.TypeFile && recipe.Type != recipes.TypeMultiFile {
		return HandleError("recipe '%s' has type '%s' which requires special handling", name, recipe.Type)
	}

	paths := recipe.Paths
	if recipe.Type == recipes.TypeFile {
		paths = []string{recipe.Path}
	}

	if setupCheck {
		var missing []string
		for _, path := range paths {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				missing = append(missing, path)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("✗ %s integration not installed\n", recipe.Name)
			fmt.Printf("  Run: bd setup %s\n", name)
			for _, path := range missing {
				fmt.Printf("  Missing: %s\n", path)
			}
			return SilentExit()
		}
		fmt.Printf("✓ %s integration installed\n", recipe.Name)
		for _, path := range paths {
			fmt.Printf("  File: %s\n", path)
		}
		return nil
	}

	if setupRemove {
		removed := false
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return HandleError("%v", err)
			}
			removed = true
			_ = os.Remove(filepath.Dir(path))
		}
		if !removed {
			fmt.Println("No integration files found")
			return nil
		}
		fmt.Printf("✓ Removed %s integration\n", recipe.Name)
		return nil
	}

	fmt.Printf("Installing %s integration...\n", recipe.Name)

	for _, path := range paths {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return HandleError("create directory: %v", err)
			}
		}

		content, err := recipes.ContentForPath(*recipe, path)
		if err != nil {
			return HandleError("%v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { // #nosec G306 -- config files need to be readable
			return HandleError("write file: %v", err)
		}
	}

	fmt.Printf("\n✓ %s integration installed\n", recipe.Name)
	for _, path := range paths {
		fmt.Printf("  File: %s\n", path)
	}
	return nil
}

func translateSetupError(err error) error {
	if err == nil {
		return nil
	}
	return SilentExit()
}

func runCursorRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckCursor())
	case setupRemove:
		return translateSetupError(setup.RemoveCursor())
	default:
		return translateSetupError(setup.InstallCursor())
	}
}

func runClaudeRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckClaude())
	case setupRemove:
		return translateSetupError(setup.RemoveClaude(setupGlobal))
	default:
		return translateSetupError(setup.InstallClaude(setupGlobal, setupStealth))
	}
}

func runGeminiRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckGemini())
	case setupRemove:
		return translateSetupError(setup.RemoveGemini(setupProject))
	default:
		return translateSetupError(setup.InstallGemini(setupProject, setupStealth))
	}
}

func runFactoryRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckFactory())
	case setupRemove:
		return translateSetupError(setup.RemoveFactory())
	default:
		return translateSetupError(setup.InstallFactory())
	}
}

func runCodexRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckCodex(setupGlobal))
	case setupRemove:
		return translateSetupError(setup.RemoveCodex(setupGlobal))
	default:
		return translateSetupError(setup.InstallCodex(setupGlobal))
	}
}

func runOpenCodeRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckOpenCode())
	case setupRemove:
		return translateSetupError(setup.RemoveOpenCode())
	default:
		return translateSetupError(setup.InstallOpenCode())
	}
}

func runMuxRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckMux(setupProject, setupGlobal))
	case setupRemove:
		return translateSetupError(setup.RemoveMux(setupProject, setupGlobal))
	default:
		return translateSetupError(setup.InstallMux(setupProject, setupGlobal))
	}
}

func runAiderRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckAider())
	case setupRemove:
		return translateSetupError(setup.RemoveAider())
	default:
		return translateSetupError(setup.InstallAider())
	}
}

func runJunieRecipe() error {
	switch {
	case setupCheck:
		return translateSetupError(setup.CheckJunie())
	case setupRemove:
		return translateSetupError(setup.RemoveJunie())
	default:
		return translateSetupError(setup.InstallJunie())
	}
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
	setupCmd.Flags().BoolVar(&setupGlobal, "global", false, "Install globally (claude/codex/mux; writes to ~/.claude/settings.json, $CODEX_HOME/AGENTS.md or ~/.codex/AGENTS.md, or ~/.mux/AGENTS.md)")
	setupCmd.Flags().BoolVar(&setupStealth, "stealth", false, "Use stealth mode (claude/gemini)")

	rootCmd.AddCommand(setupCmd)
}
