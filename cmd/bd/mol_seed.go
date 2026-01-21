package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var molSeedCmd = &cobra.Command{
	Use:   "seed [formula-name]",
	Short: "Verify formula accessibility or seed patrol formulas",
	Long: `Verify that formulas are accessible and can be cooked.

The seed command checks formula search paths to ensure formulas exist
and can be loaded. This is useful for verifying system health before
patrols attempt to spawn work.

WITH --patrol FLAG:
  Verifies all three patrol formulas are accessible:
    - mol-deacon-patrol
    - mol-witness-patrol
    - mol-refinery-patrol

WITHOUT --patrol:
  Verifies the specified formula is accessible.

Formula search paths (checked in order):
  1. .beads/formulas/ (project level)
  2. ~/.beads/formulas/ (user level)
  3. $GT_ROOT/.beads/formulas/ (orchestrator level, if GT_ROOT set)

Examples:
  bd mol seed --patrol                    # Verify all patrol formulas
  bd mol seed mol-feature                 # Verify specific formula
  bd mol seed mol-review --var name=test  # Verify with variable substitution`,
	Args: cobra.MaximumNArgs(1),
	Run:  runMolSeed,
}

func runMolSeed(cmd *cobra.Command, args []string) {
	patrol, _ := cmd.Flags().GetBool("patrol")
	varFlags, _ := cmd.Flags().GetStringArray("var")

	// Parse variables (for formula condition filtering if needed)
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
			os.Exit(1)
		}
		vars[parts[0]] = parts[1]
	}

	if patrol {
		// Verify all patrol formulas
		if err := verifyPatrolFormulas(vars); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if !jsonOutput {
			fmt.Println("✓ All patrol formulas accessible")
		} else {
			outputJSON(map[string]interface{}{
				"status":   "ok",
				"formulas": []string{"mol-deacon-patrol", "mol-witness-patrol", "mol-refinery-patrol"},
			})
		}
		return
	}

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: formula name required (or use --patrol flag)\n")
		os.Exit(1)
	}

	// Verify single formula
	formulaName := args[0]
	if err := verifyFormula(formulaName, vars); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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

// verifyPatrolFormulas checks that all patrol formulas are accessible
func verifyPatrolFormulas(vars map[string]string) error {
	patrolFormulas := []string{
		"mol-deacon-patrol",
		"mol-witness-patrol",
		"mol-refinery-patrol",
	}

	var missing []string
	for _, formula := range patrolFormulas {
		if err := verifyFormula(formula, vars); err != nil {
			missing = append(missing, formula)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("patrol formulas not accessible: %v", missing)
	}

	return nil
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
	molSeedCmd.Flags().Bool("patrol", false, "Verify all patrol formulas (mol-deacon-patrol, mol-witness-patrol, mol-refinery-patrol)")
	molSeedCmd.Flags().StringArray("var", []string{}, "Variable substitution for condition filtering (key=value)")
	molCmd.AddCommand(molSeedCmd)
}
