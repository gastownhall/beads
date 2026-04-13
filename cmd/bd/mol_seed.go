package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var molSeedCmd = &cobra.Command{
	Use:   "seed <formula-name>",
	Short: "Verify formula accessibility",
	Long: `Verify that a formula is accessible and can be cooked.

The seed command checks formula search paths to ensure a formula exists
and can be loaded. This is useful for verifying system health before
attempting to spawn work from a formula.

Formula search paths (checked in order):
  1. <resolved-beads-dir>/formulas/ (active project)
  2. ~/.beads/formulas/ (user level)
  3. $GT_ROOT/.beads/formulas/ (orchestrator level, if GT_ROOT set)

Examples:
  bd mol seed mol-feature                 # Verify specific formula
  bd mol seed mol-review --var name=test  # Verify with variable substitution`,
	Args: cobra.ExactArgs(1),
	Run:  runMolSeed,
}

func runMolSeed(cmd *cobra.Command, args []string) {
	varFlags, _ := cmd.Flags().GetStringArray("var")

	// Parse variables (for formula condition filtering if needed)
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			FatalError("invalid variable format '%s', expected 'key=value'", v)
		}
		vars[parts[0]] = parts[1]
	}

	// Verify single formula
	formulaName := args[0]
	if err := verifyFormula(formulaName, vars); err != nil {
		FatalError("%v", err)
	}

	if !jsonOutput {
		fmt.Printf("✓ Formula %q accessible\n", formulaName)
	} else {
		outputJSON(map[string]interface{}{
			"status":  "ok",
			"formula": formulaName,
		})
	}
}

// verifyFormula checks if a formula can be loaded and cooked
func verifyFormula(formulaName string, vars map[string]string) error {
	// Try to cook the formula - this verifies:
	// 1. Formula exists in search path
	// 2. Formula syntax is valid
	// 3. Formula can be resolved (extends, etc.)
	// 4. Formula can be cooked to subgraph
	_, err := resolveAndCookFormulaWithVars(formulaName, nil, vars)
	if err != nil {
		return fmt.Errorf("formula %q not accessible: %w", formulaName, err)
	}
	return nil
}

func init() {
	molSeedCmd.Flags().StringArray("var", []string{}, "Variable substitution for condition filtering (key=value)")
	molCmd.AddCommand(molSeedCmd)
}
