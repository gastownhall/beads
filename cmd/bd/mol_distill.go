package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molDistillCmd = &cobra.Command{
	Use:   "distill <epic-id> [formula-name]",
	Short: "Extract a formula from an existing epic",
	Long: `Distill a molecule by extracting a reusable formula from an existing epic.

This is the reverse of pour: instead of formula → molecule, it's molecule → formula.

The distill command:
  1. Loads the existing epic and all its children
  2. Converts the structure to a .formula.json file
  3. Replaces concrete values with {{variable}} placeholders (via --var flags)

Use cases:
  - Team develops good workflow organically, wants to reuse it
  - Capture tribal knowledge as executable templates
  - Create starting point for similar future work

Variable syntax (both work - we detect which side is the concrete value):
  --var branch=feature-auth    Spawn-style: variable=value (recommended)
  --var feature-auth=branch    Substitution-style: value=variable

Output locations (first writable wins):
  1. .beads/formulas/       (project-level, default)
  2. ~/.beads/formulas/     (user-level, if project not writable)

Examples:
  bd mol distill bd-o5xe my-workflow
  bd mol distill bd-abc release-workflow --var feature_name=auth-refactor`,
	Args: cobra.RangeArgs(1, 2),
	Run:  runMolDistill,
}

// DistillResult holds the result of a distill operation
type DistillResult struct {
	FormulaName string   `json:"formula_name"`
	FormulaPath string   `json:"formula_path"`
	Steps       int      `json:"steps"`     // number of steps in formula
	Variables   []string `json:"variables"` // variables introduced
}

// collectSubgraphText gathers all searchable text from a molecule subgraph
func collectSubgraphText(subgraph *MoleculeSubgraph) string {
	var parts []string
	for _, issue := range subgraph.Issues {
		parts = append(parts, issue.Title)
		parts = append(parts, issue.Description)
		parts = append(parts, issue.Design)
		parts = append(parts, issue.AcceptanceCriteria)
		parts = append(parts, issue.Notes)
	}
	return strings.Join(parts, " ")
}

// parseDistillVar parses a --var flag with smart detection of syntax.
// Accepts both spawn-style (variable=value) and substitution-style (value=variable).
// Returns (findText, varName, error).
func parseDistillVar(varFlag, searchableText string) (string, string, error) {
	parts := strings.SplitN(varFlag, "=", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid format '%s', expected 'variable=value' or 'value=variable'", varFlag)
	}

	left, right := parts[0], parts[1]
	leftFound := strings.Contains(searchableText, left)
	rightFound := strings.Contains(searchableText, right)

	switch {
	case rightFound && !leftFound:
		// spawn-style: --var branch=feature-auth
		// left is variable name, right is the value to find
		return right, left, nil
	case leftFound && !rightFound:
		// substitution-style: --var feature-auth=branch
		// left is value to find, right is variable name
		return left, right, nil
	case leftFound && rightFound:
		// Both found - prefer spawn-style (more natural guess)
		// Agent likely typed: --var varname=concrete_value
		return right, left, nil
	default:
		return "", "", fmt.Errorf("neither '%s' nor '%s' found in epic text", left, right)
	}
}

// runMolDistill implements the distill command
func runMolDistill(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	// mol distill requires direct store access for reading the epic
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		os.Exit(1)
	}

	varFlags, _ := cmd.Flags().GetStringArray("var")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	outputDir, _ := cmd.Flags().GetString("output")

	// Resolve epic ID
	epicID, err := utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: '%s' not found\n", args[0])
		os.Exit(1)
	}

	// Load the epic subgraph
	subgraph, err := loadTemplateSubgraph(ctx, store, epicID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading epic: %v\n", err)
		os.Exit(1)
	}

	// Determine formula name
	formulaName := ""
	if len(args) > 1 {
		formulaName = args[1]
	} else {
		// Derive from epic title
		formulaName = sanitizeFormulaName(subgraph.Root.Title)
	}

	// Parse variable substitutions with smart detection
	replacements := make(map[string]string)
	if len(varFlags) > 0 {
		searchableText := collectSubgraphText(subgraph)
		for _, v := range varFlags {
			findText, varName, err := parseDistillVar(v, searchableText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			replacements[findText] = varName
		}
	}

	// Convert to formula
	f := subgraphToFormula(subgraph, formulaName, replacements)

	// Determine output path
	outputPath := ""
	if outputDir != "" {
		outputPath = filepath.Join(outputDir, formulaName+formula.FormulaExt)
	} else {
		// Find first writable formula directory
		outputPath = findWritableFormulaDir(formulaName)
		if outputPath == "" {
			fmt.Fprintf(os.Stderr, "Error: no writable formula directory found\n")
			fmt.Fprintf(os.Stderr, "Try: mkdir -p .beads/formulas\n")
			os.Exit(1)
		}
	}

	if dryRun {
		fmt.Printf("\nDry run: would distill %d steps from %s into formula\n\n", countSteps(f.Steps), epicID)
		fmt.Printf("Formula: %s\n", formulaName)
		fmt.Printf("Output: %s\n", outputPath)
		if len(replacements) > 0 {
			fmt.Printf("\nVariables:\n")
			for value, varName := range replacements {
				fmt.Printf("  %s: \"%s\" → {{%s}}\n", varName, value, varName)
			}
		}
		fmt.Printf("\nStructure:\n")
		printFormulaStepsTree(f.Steps, "")
		return
	}

	// Ensure output directory exists
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s: %v\n", dir, err)
		os.Exit(1)
	}

	// Write formula
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding formula: %v\n", err)
		os.Exit(1)
	}

	// #nosec G306 -- Formula files are not sensitive
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing formula: %v\n", err)
		os.Exit(1)
	}

	result := &DistillResult{
		FormulaName: formulaName,
		FormulaPath: outputPath,
		Steps:       countSteps(f.Steps),
		Variables:   getVarNames(replacements),
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Distilled formula: %d steps\n", ui.RenderPass("✓"), result.Steps)
	fmt.Printf("  Formula: %s\n", result.FormulaName)
	fmt.Printf("  Path: %s\n", result.FormulaPath)
	if len(result.Variables) > 0 {
		fmt.Printf("  Variables: %s\n", strings.Join(result.Variables, ", "))
	}
	fmt.Printf("\nTo instantiate:\n")
	fmt.Printf("  bd mol pour %s", result.FormulaName)
	for _, v := range result.Variables {
		fmt.Printf(" --var %s=<value>", v)
	}
	fmt.Println()
}

// sanitizeFormulaName converts a title to a valid formula name
func sanitizeFormulaName(title string) string {
	// Convert to lowercase and replace spaces/special chars with hyphens
	re := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	name := re.ReplaceAllString(strings.ToLower(title), "-")
	// Remove leading/trailing hyphens and collapse multiple hyphens
	name = regexp.MustCompile(`-+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "untitled"
	}
	return name
}

// findWritableFormulaDir finds the first writable formula directory
func findWritableFormulaDir(formulaName string) string {
	searchPaths := getFormulaSearchPaths()
	for _, dir := range searchPaths {
		// Try to create the directory if it doesn't exist
		if err := os.MkdirAll(dir, 0755); err == nil {
			// Check if we can write to it
			testPath := filepath.Join(dir, ".write-test")
			if f, err := os.Create(testPath); err == nil { //nolint:gosec // testPath is constructed from known search paths
				_ = f.Close()           // Best effort cleanup
				_ = os.Remove(testPath) // Best effort cleanup of temp file
				return filepath.Join(dir, formulaName+formula.FormulaExt)
			}
		}
	}
	return ""
}

// getVarNames extracts variable names from replacements map
func getVarNames(replacements map[string]string) []string {
	var names []string
	for _, varName := range replacements {
		names = append(names, varName)
	}
	return names
}

// subgraphToFormula converts a molecule subgraph to a formula
func subgraphToFormula(subgraph *TemplateSubgraph, name string, replacements map[string]string) *formula.Formula {
	// Helper to apply replacements
	applyReplacements := func(text string) string {
		result := text
		for value, varName := range replacements {
			result = strings.ReplaceAll(result, value, "{{"+varName+"}}")
		}
		return result
	}

	// Build ID mapping for step references
	idToStepID := make(map[string]string)
	for _, issue := range subgraph.Issues {
		// Create a sanitized step ID from the issue ID
		stepID := sanitizeFormulaName(issue.Title)
		if stepID == "" {
			stepID = issue.ID
		}
		idToStepID[issue.ID] = stepID
	}

	// Build dependency map (issue ID -> list of depends-on IDs)
	depsByIssue := make(map[string][]string)
	for _, dep := range subgraph.Dependencies {
		depsByIssue[dep.IssueID] = append(depsByIssue[dep.IssueID], dep.DependsOnID)
	}

	// Convert issues to steps
	var steps []*formula.Step
	for _, issue := range subgraph.Issues {
		if issue.ID == subgraph.Root.ID {
			continue // Root becomes the formula itself
		}

		step := &formula.Step{
			ID:          idToStepID[issue.ID],
			Title:       applyReplacements(issue.Title),
			Description: applyReplacements(issue.Description),
			Type:        string(issue.IssueType),
		}

		// Copy priority if set
		if issue.Priority > 0 {
			p := issue.Priority
			step.Priority = &p
		}

		// Copy labels (excluding internal ones)
		for _, label := range issue.Labels {
			if label != MoleculeLabel && !strings.HasPrefix(label, "mol:") {
				step.Labels = append(step.Labels, label)
			}
		}

		// Convert dependencies to depends_on (skip root)
		if deps, ok := depsByIssue[issue.ID]; ok {
			for _, depID := range deps {
				if depID == subgraph.Root.ID {
					continue // Skip dependency on root (becomes formula itself)
				}
				if stepID, ok := idToStepID[depID]; ok {
					step.DependsOn = append(step.DependsOn, stepID)
				}
			}
		}

		steps = append(steps, step)
	}

	// Build variable definitions
	vars := make(map[string]*formula.VarDef)
	for _, varName := range replacements {
		vars[varName] = &formula.VarDef{
			Description: fmt.Sprintf("Value for %s", varName),
			Required:    true,
		}
	}

	return &formula.Formula{
		Formula:     name,
		Description: applyReplacements(subgraph.Root.Description),
		Version:     1,
		Type:        formula.TypeWorkflow,
		Vars:        vars,
		Steps:       steps,
	}
}

func init() {
	molDistillCmd.Flags().StringArray("var", []string{}, "Replace value with {{variable}} placeholder (variable=value)")
	molDistillCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molDistillCmd.Flags().String("output", "", "Output directory for formula file")

	molCmd.AddCommand(molDistillCmd)
}
