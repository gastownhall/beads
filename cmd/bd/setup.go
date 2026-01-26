package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/recipes"
)

var (
	setupProject bool
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
include cursor, claude, gemini, aider, factory, codex, windsurf, cody, and kilocode.

Examples:
  bd setup cursor          # Install Cursor IDE integration
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
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Wrote template to %s\n", setupOutput)
		return
	}

	// Handle --add flag (save custom recipe)
	if setupAdd != "" {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "Error: --add requires a path argument")
			fmt.Fprintln(os.Stderr, "Usage: bd setup --add <name> <path>")
			os.Exit(1)
		}
		if err := addRecipe(setupAdd, args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
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

func listRecipes() {
	beadsDir := findBeadsDir()
	allRecipes, err := recipes.GetAllRecipes(beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading recipes: %v\n", err)
		os.Exit(1)
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
	beadsDir := findBeadsDir()
	if beadsDir == "" {
		beadsDir = ".beads"
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
	beadsDir := findBeadsDir()
	recipe, err := recipes.GetRecipe(name, beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Use 'bd setup --list' to see available recipes.")
		os.Exit(1)
	}

	if recipe.Type != recipes.TypeFile {
		fmt.Fprintf(os.Stderr, "Error: recipe '%s' has type '%s' which requires special handling\n", name, recipe.Type)
		os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error: create directory: %v\n", err)
			os.Exit(1)
		}
	}

	if err := os.WriteFile(recipe.Path, []byte(recipes.Template), 0o644); err != nil { // #nosec G306 -- config files need to be readable
		fmt.Fprintf(os.Stderr, "Error: write file: %v\n", err)
		os.Exit(1)
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
		setup.RemoveClaude(setupProject)
		return
	}
	setup.InstallClaude(setupProject, setupStealth)
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

func findBeadsDir() string {
	// Check for .beads in current directory
	if info, err := os.Stat(".beads"); err == nil && info.IsDir() {
		return ".beads"
	}
	// Check for redirected beads directory
	redirectPath := ".beads/.redirect"
	if data, err := os.ReadFile(redirectPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ".beads"
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
	setupCmd.Flags().BoolVar(&setupProject, "project", false, "Install for this project only (claude/gemini)")
	setupCmd.Flags().BoolVar(&setupStealth, "stealth", false, "Use stealth mode (claude/gemini)")

	rootCmd.AddCommand(setupCmd)
}
