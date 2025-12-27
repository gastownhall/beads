package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/ui"
)

// formulaCmd is the parent command for formula operations.
var formulaCmd = &cobra.Command{
	Use:   "formula",
	Short: "Manage workflow formulas",
	Long: `Manage workflow formulas - the source layer for molecule templates.

Formulas are JSON files (.formula.json) that define workflows with composition rules.
They are "cooked" into ephemeral protos which can then be poured or wisped.

The Rig → Cook → Run lifecycle:
  - Rig: Compose formulas (extends, compose)
  - Cook: Transform to proto (bd cook expands macros, applies aspects)
  - Run: Agents execute poured mols or wisps

Search paths (in order):
  1. .beads/formulas/ (project)
  2. ~/.beads/formulas/ (user)
  3. ~/gt/.beads/formulas/ (town)

Commands:
  list   List available formulas from all search paths
  show   Show formula details, steps, and composition rules`,
}

// formulaListCmd lists all available formulas.
var formulaListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available formulas",
	Long: `List all formulas from search paths.

Search paths (in order of priority):
  1. .beads/formulas/ (project - highest priority)
  2. ~/.beads/formulas/ (user)
  3. ~/gt/.beads/formulas/ (town)

Formulas in earlier paths shadow those with the same name in later paths.

Examples:
  bd formula list
  bd formula list --json
  bd formula list --type workflow
  bd formula list --type aspect`,
	Run: runFormulaList,
}

// formulaShowCmd shows details of a specific formula.
var formulaShowCmd = &cobra.Command{
	Use:   "show <formula-name>",
	Short: "Show formula details",
	Long: `Show detailed information about a formula.

Displays:
  - Formula metadata (name, type, description)
  - Variables with defaults and constraints
  - Steps with dependencies
  - Composition rules (extends, aspects, expansions)
  - Bond points for external composition

Examples:
  bd formula show shiny
  bd formula show rule-of-five
  bd formula show security-audit --json`,
	Args: cobra.ExactArgs(1),
	Run:  runFormulaShow,
}

// FormulaListEntry represents a formula in the list output.
type FormulaListEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Steps       int    `json:"steps"`
	Vars        int    `json:"vars"`
}

func runFormulaList(cmd *cobra.Command, args []string) {
	typeFilter, _ := cmd.Flags().GetString("type")

	// Get all search paths
	searchPaths := getFormulaSearchPaths()

	// Track seen formulas (first occurrence wins)
	seen := make(map[string]bool)
	var entries []FormulaListEntry

	// Scan each search path
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

			entries = append(entries, FormulaListEntry{
				Name:        f.Formula,
				Type:        string(f.Type),
				Description: truncateDescription(f.Description, 60),
				Source:      f.Source,
				Steps:       countSteps(f.Steps),
				Vars:        len(f.Vars),
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
		fmt.Println("\nSearch paths:")
		for _, p := range searchPaths {
			fmt.Printf("  %s\n", p)
		}
		return
	}

	fmt.Printf("📜 Formulas (%d found)\n\n", len(entries))

	// Group by type
	byType := make(map[string][]FormulaListEntry)
	for _, e := range entries {
		byType[e.Type] = append(byType[e.Type], e)
	}

	// Print in type order: workflow, expansion, aspect
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
			if e.Vars > 0 {
				varInfo = fmt.Sprintf(" (%d vars)", e.Vars)
			}
			fmt.Printf("  %-25s %s%s\n", e.Name, e.Description, varInfo)
		}
		fmt.Println()
	}
}

func runFormulaShow(cmd *cobra.Command, args []string) {
	name := args[0]

	// Create parser with default search paths
	parser := formula.NewParser()

	// Try to load the formula
	f, err := parser.LoadByName(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nSearch paths:\n")
		for _, p := range getFormulaSearchPaths() {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(f)
		return
	}

	// Print header
	typeIcon := getTypeIcon(string(f.Type))
	fmt.Printf("\n%s %s\n", typeIcon, f.Formula)
	fmt.Printf("   Type: %s\n", f.Type)
	if f.Description != "" {
		fmt.Printf("   Description: %s\n", f.Description)
	}
	fmt.Printf("   Source: %s\n", f.Source)

	// Print extends
	if len(f.Extends) > 0 {
		fmt.Printf("\n%s Extends:\n", ui.RenderAccent("📎"))
		for _, ext := range f.Extends {
			fmt.Printf("   - %s\n", ext)
		}
	}

	// Print variables
	if len(f.Vars) > 0 {
		fmt.Printf("\n%s Variables:\n", ui.RenderWarn("📝"))
		// Sort for consistent output
		varNames := make([]string, 0, len(f.Vars))
		for name := range f.Vars {
			varNames = append(varNames, name)
		}
		sort.Strings(varNames)

		for _, name := range varNames {
			v := f.Vars[name]
			attrs := []string{}
			if v.Required {
				attrs = append(attrs, ui.RenderFail("required"))
			}
			if v.Default != "" {
				attrs = append(attrs, fmt.Sprintf("default=%q", v.Default))
			}
			if len(v.Enum) > 0 {
				attrs = append(attrs, fmt.Sprintf("enum=[%s]", strings.Join(v.Enum, ",")))
			}
			if v.Pattern != "" {
				attrs = append(attrs, fmt.Sprintf("pattern=%q", v.Pattern))
			}
			attrStr := ""
			if len(attrs) > 0 {
				attrStr = fmt.Sprintf(" [%s]", strings.Join(attrs, ", "))
			}
			desc := ""
			if v.Description != "" {
				desc = fmt.Sprintf(": %s", v.Description)
			}
			fmt.Printf("   {{%s}}%s%s\n", name, desc, attrStr)
		}
	}

	// Print steps
	if len(f.Steps) > 0 {
		fmt.Printf("\n%s Steps (%d):\n", ui.RenderPass("🌲"), countSteps(f.Steps))
		printFormulaStepsTree(f.Steps, "   ")
	}

	// Print template (for expansion formulas)
	if len(f.Template) > 0 {
		fmt.Printf("\n%s Template (%d steps):\n", ui.RenderAccent("📐"), len(f.Template))
		printFormulaStepsTree(f.Template, "   ")
	}

	// Print advice rules
	if len(f.Advice) > 0 {
		fmt.Printf("\n%s Advice:\n", ui.RenderWarn("💡"))
		for _, a := range f.Advice {
			parts := []string{}
			if a.Before != nil {
				parts = append(parts, fmt.Sprintf("before: %s", a.Before.ID))
			}
			if a.After != nil {
				parts = append(parts, fmt.Sprintf("after: %s", a.After.ID))
			}
			if a.Around != nil {
				parts = append(parts, "around")
			}
			fmt.Printf("   %s → %s\n", a.Target, strings.Join(parts, ", "))
		}
	}

	// Print compose rules
	if f.Compose != nil {
		hasCompose := len(f.Compose.BondPoints) > 0 || len(f.Compose.Expand) > 0 ||
			len(f.Compose.Map) > 0 || len(f.Compose.Aspects) > 0

		if hasCompose {
			fmt.Printf("\n%s Composition:\n", ui.RenderAccent("🔗"))

			if len(f.Compose.BondPoints) > 0 {
				fmt.Printf("   Bond Points:\n")
				for _, bp := range f.Compose.BondPoints {
					loc := ""
					if bp.AfterStep != "" {
						loc = fmt.Sprintf("after %s", bp.AfterStep)
					} else if bp.BeforeStep != "" {
						loc = fmt.Sprintf("before %s", bp.BeforeStep)
					}
					fmt.Printf("     - %s (%s)\n", bp.ID, loc)
				}
			}

			if len(f.Compose.Expand) > 0 {
				fmt.Printf("   Expansions:\n")
				for _, e := range f.Compose.Expand {
					fmt.Printf("     - %s → %s\n", e.Target, e.With)
				}
			}

			if len(f.Compose.Map) > 0 {
				fmt.Printf("   Maps:\n")
				for _, m := range f.Compose.Map {
					fmt.Printf("     - %s → %s\n", m.Select, m.With)
				}
			}

			if len(f.Compose.Aspects) > 0 {
				fmt.Printf("   Aspects: %s\n", strings.Join(f.Compose.Aspects, ", "))
			}
		}
	}

	// Print pointcuts (for aspects)
	if len(f.Pointcuts) > 0 {
		fmt.Printf("\n%s Pointcuts:\n", ui.RenderWarn("🎯"))
		for _, p := range f.Pointcuts {
			parts := []string{}
			if p.Glob != "" {
				parts = append(parts, fmt.Sprintf("glob=%q", p.Glob))
			}
			if p.Type != "" {
				parts = append(parts, fmt.Sprintf("type=%q", p.Type))
			}
			if p.Label != "" {
				parts = append(parts, fmt.Sprintf("label=%q", p.Label))
			}
			fmt.Printf("   - %s\n", strings.Join(parts, ", "))
		}
	}

	fmt.Println()
}

// getFormulaSearchPaths returns the formula search paths in priority order.
func getFormulaSearchPaths() []string {
	var paths []string

	// Project-level formulas
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".beads", "formulas"))
	}

	// User-level formulas
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".beads", "formulas"))
		// Gas Town formulas
		paths = append(paths, filepath.Join(home, "gt", ".beads", "formulas"))
	}

	return paths
}

// scanFormulaDir scans a directory for formula files.
func scanFormulaDir(dir string) ([]*formula.Formula, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	parser := formula.NewParser(dir)
	var formulas []*formula.Formula

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), formula.FormulaExt) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		f, err := parser.ParseFile(path)
		if err != nil {
			continue // Skip invalid formulas
		}
		formulas = append(formulas, f)
	}

	return formulas, nil
}

// countSteps recursively counts steps including children.
func countSteps(steps []*formula.Step) int {
	count := len(steps)
	for _, s := range steps {
		count += countSteps(s.Children)
	}
	return count
}

// truncateDescription truncates a description to maxLen characters.
func truncateDescription(desc string, maxLen int) string {
	// Take first line only
	if idx := strings.Index(desc, "\n"); idx >= 0 {
		desc = desc[:idx]
	}
	if len(desc) > maxLen {
		return desc[:maxLen-3] + "..."
	}
	return desc
}

// getTypeIcon returns an icon for the formula type.
func getTypeIcon(t string) string {
	switch t {
	case "workflow":
		return "📋"
	case "expansion":
		return "📐"
	case "aspect":
		return "🎯"
	default:
		return "📜"
	}
}

// printFormulaStepsTree prints steps in a tree format.
func printFormulaStepsTree(steps []*formula.Step, indent string) {
	for i, step := range steps {
		connector := "├──"
		if i == len(steps)-1 {
			connector = "└──"
		}

		// Collect dependency info
		var depParts []string
		if len(step.DependsOn) > 0 {
			depParts = append(depParts, fmt.Sprintf("depends: %s", strings.Join(step.DependsOn, ", ")))
		}
		if len(step.Needs) > 0 {
			depParts = append(depParts, fmt.Sprintf("needs: %s", strings.Join(step.Needs, ", ")))
		}
		if step.WaitsFor != "" {
			depParts = append(depParts, fmt.Sprintf("waits_for: %s", step.WaitsFor))
		}

		depStr := ""
		if len(depParts) > 0 {
			depStr = fmt.Sprintf(" [%s]", strings.Join(depParts, ", "))
		}

		typeStr := ""
		if step.Type != "" && step.Type != "task" {
			typeStr = fmt.Sprintf(" (%s)", step.Type)
		}

		fmt.Printf("%s%s %s: %s%s%s\n", indent, connector, step.ID, step.Title, typeStr, depStr)

		if len(step.Children) > 0 {
			childIndent := indent
			if i == len(steps)-1 {
				childIndent += "    "
			} else {
				childIndent += "│   "
			}
			printFormulaStepsTree(step.Children, childIndent)
		}
	}
}

func init() {
	formulaListCmd.Flags().String("type", "", "Filter by type (workflow, expansion, aspect)")

	formulaCmd.AddCommand(formulaListCmd)
	formulaCmd.AddCommand(formulaShowCmd)
	rootCmd.AddCommand(formulaCmd)
}
