package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

// CatalogEntry represents a formula in the catalog.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Steps       int      `json:"steps"`
	Vars        []string `json:"vars,omitempty"`
}

var molCatalogCmd = &cobra.Command{
	Use:     "catalog",
	Aliases: []string{"list", "ls"},
	Short:   "List available molecule formulas",
	Long: `List formulas available for bd pour / bd ephemeral create.

Formulas are ephemeral proto definitions stored as .formula.json files.
They are cooked inline when pouring, never stored as database beads.

Search paths (in priority order):
  1. .beads/formulas/       (project-level)
  2. ~/.beads/formulas/     (user-level)
  3. ~/gt/.beads/formulas/  (Gas Town level)`,
	Run: func(cmd *cobra.Command, args []string) {
		typeFilter, _ := cmd.Flags().GetString("type")

		// Get all search paths and scan for formulas
		searchPaths := getFormulaSearchPaths()
		seen := make(map[string]bool)
		var entries []CatalogEntry

		for _, dir := range searchPaths {
			formulas, err := scanFormulaDir(dir)
			if err != nil {
				continue // Skip inaccessible directories
			}

			for _, f := range formulas {
				if seen[f.Formula] {
					continue // Skip shadowed formulas
				}
				seen[f.Formula] = true

				// Apply type filter
				if typeFilter != "" && string(f.Type) != typeFilter {
					continue
				}

				// Extract variable names
				var varNames []string
				for name := range f.Vars {
					varNames = append(varNames, name)
				}
				sort.Strings(varNames)

				entries = append(entries, CatalogEntry{
					Name:        f.Formula,
					Type:        string(f.Type),
					Description: truncateDescription(f.Description, 60),
					Source:      f.Source,
					Steps:       countSteps(f.Steps),
					Vars:        varNames,
				})
			}
		}

		// Sort by name
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})

		if jsonOutput {
			outputJSON(entries)
			return
		}

		if len(entries) == 0 {
			fmt.Println("No formulas found.")
			fmt.Println("\nTo create a formula, write a .formula.json file:")
			fmt.Println("  .beads/formulas/my-workflow.formula.json")
			fmt.Println("\nOr distill from existing work:")
			fmt.Println("  bd mol distill <epic-id> my-workflow")
			fmt.Println("\nTo instantiate from formula:")
			fmt.Println("  bd pour <formula-name> --var key=value          # persistent mol")
			fmt.Println("  bd ephemeral create <formula-name> --var key=value   # ephemeral wisp")
			return
		}

		fmt.Printf("%s\n\n", ui.RenderPass("Formulas (for bd pour / bd ephemeral create):"))

		// Group by type for display
		byType := make(map[string][]CatalogEntry)
		for _, e := range entries {
			byType[e.Type] = append(byType[e.Type], e)
		}

		// Print workflow types first (most common for pour/wisp)
		typeOrder := []string{"workflow", "expansion", "aspect"}
		for _, t := range typeOrder {
			typeEntries := byType[t]
			if len(typeEntries) == 0 {
				continue
			}

			typeIcon := getTypeIcon(t)
			fmt.Printf("%s %s:\n", typeIcon, strings.Title(t))

			for _, e := range typeEntries {
				varInfo := ""
				if len(e.Vars) > 0 {
					varInfo = fmt.Sprintf(" (vars: %s)", strings.Join(e.Vars, ", "))
				}
				fmt.Printf("  %s: %s%s\n", ui.RenderAccent(e.Name), e.Description, varInfo)
			}
			fmt.Println()
		}
	},
}

func init() {
	molCatalogCmd.Flags().String("type", "", "Filter by formula type (workflow, expansion, aspect)")
	molCmd.AddCommand(molCatalogCmd)
}
