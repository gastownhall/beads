package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// cookCmd compiles a formula JSON into a proto bead.
var cookCmd = &cobra.Command{
	Use:   "cook <formula-file>",
	Short: "Compile a formula into a proto bead",
	Long: `Cook transforms a .formula.json file into a proto bead.

Formulas are high-level workflow templates that support:
  - Variable definitions with defaults and validation
  - Step definitions that become issue hierarchies
  - Composition rules for bonding formulas together
  - Inheritance via extends

The cook command parses the formula, resolves inheritance, and
creates a proto bead in the database that can be poured or spawned.

Examples:
  bd cook mol-feature.formula.json
  bd cook .beads/formulas/mol-release.formula.json --force
  bd cook mol-patrol.formula.json --search-path .beads/formulas

Output:
  Creates a proto bead with:
  - ID matching the formula name (e.g., mol-feature)
  - The "template" label for proto identification
  - Child issues for each step
  - Dependencies matching depends_on relationships`,
	Args: cobra.ExactArgs(1),
	Run:  runCook,
}

// cookResult holds the result of cooking a formula
type cookResult struct {
	ProtoID    string   `json:"proto_id"`
	Formula    string   `json:"formula"`
	Created    int      `json:"created"`
	Variables  []string `json:"variables"`
	BondPoints []string `json:"bond_points,omitempty"`
}

func runCook(cmd *cobra.Command, args []string) {
	CheckReadonly("cook")

	ctx := rootCtx

	// Cook requires direct store access for creating protos
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: cook requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon cook %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	searchPaths, _ := cmd.Flags().GetStringSlice("search-path")
	prefix, _ := cmd.Flags().GetString("prefix")

	// Create parser with search paths
	parser := formula.NewParser(searchPaths...)

	// Parse the formula file
	formulaPath := args[0]
	f, err := parser.ParseFile(formulaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing formula: %v\n", err)
		os.Exit(1)
	}

	// Resolve inheritance
	resolved, err := parser.Resolve(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving formula: %v\n", err)
		os.Exit(1)
	}

	// Apply advice transformations (gt-8tmz.2)
	if len(resolved.Advice) > 0 {
		resolved.Steps = formula.ApplyAdvice(resolved.Steps, resolved.Advice)
	}

	// Apply expansion operators (gt-8tmz.3)
	if resolved.Compose != nil && (len(resolved.Compose.Expand) > 0 || len(resolved.Compose.Map) > 0) {
		expandedSteps, err := formula.ApplyExpansions(resolved.Steps, resolved.Compose, parser)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error applying expansions: %v\n", err)
			os.Exit(1)
		}
		resolved.Steps = expandedSteps
	}

	// Apply prefix to proto ID if specified (bd-47qx)
	protoID := resolved.Formula
	if prefix != "" {
		protoID = prefix + resolved.Formula
	}

	// Check if proto already exists
	existingProto, err := store.GetIssue(ctx, protoID)
	if err == nil && existingProto != nil {
		if !force {
			fmt.Fprintf(os.Stderr, "Error: proto %s already exists\n", protoID)
			fmt.Fprintf(os.Stderr, "Hint: use --force to replace it\n")
			os.Exit(1)
		}
		// Delete existing proto and its children
		if err := deleteProtoSubgraph(ctx, store, protoID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting existing proto: %v\n", err)
			os.Exit(1)
		}
	}

	// Extract variables used in the formula
	vars := formula.ExtractVariables(resolved)

	// Collect bond points
	var bondPoints []string
	if resolved.Compose != nil {
		for _, bp := range resolved.Compose.BondPoints {
			bondPoints = append(bondPoints, bp.ID)
		}
	}

	if dryRun {
		fmt.Printf("\nDry run: would cook formula %s as proto %s\n\n", resolved.Formula, protoID)
		fmt.Printf("Steps (%d):\n", len(resolved.Steps))
		printFormulaSteps(resolved.Steps, "  ")

		if len(vars) > 0 {
			fmt.Printf("\nVariables: %s\n", strings.Join(vars, ", "))
		}
		if len(bondPoints) > 0 {
			fmt.Printf("Bond points: %s\n", strings.Join(bondPoints, ", "))
		}

		// Show variable definitions
		if len(resolved.Vars) > 0 {
			fmt.Printf("\nVariable definitions:\n")
			for name, def := range resolved.Vars {
				attrs := []string{}
				if def.Required {
					attrs = append(attrs, "required")
				}
				if def.Default != "" {
					attrs = append(attrs, fmt.Sprintf("default=%s", def.Default))
				}
				if len(def.Enum) > 0 {
					attrs = append(attrs, fmt.Sprintf("enum=[%s]", strings.Join(def.Enum, ",")))
				}
				attrStr := ""
				if len(attrs) > 0 {
					attrStr = fmt.Sprintf(" (%s)", strings.Join(attrs, ", "))
				}
				fmt.Printf("  {{%s}}: %s%s\n", name, def.Description, attrStr)
			}
		}
		return
	}

	// Create the proto bead from the formula
	result, err := cookFormula(ctx, store, resolved, protoID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error cooking formula: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(cookResult{
			ProtoID:    result.ProtoID,
			Formula:    resolved.Formula,
			Created:    result.Created,
			Variables:  vars,
			BondPoints: bondPoints,
		})
		return
	}

	fmt.Printf("%s Cooked proto: %s\n", ui.RenderPass("✓"), result.ProtoID)
	fmt.Printf("  Created %d issues\n", result.Created)
	if len(vars) > 0 {
		fmt.Printf("  Variables: %s\n", strings.Join(vars, ", "))
	}
	if len(bondPoints) > 0 {
		fmt.Printf("  Bond points: %s\n", strings.Join(bondPoints, ", "))
	}
	fmt.Printf("\nTo use: bd pour %s --var <name>=<value>\n", result.ProtoID)
}

// cookFormulaResult holds the result of cooking
type cookFormulaResult struct {
	ProtoID string
	Created int
}

// cookFormula creates a proto bead from a resolved formula.
// protoID is the final ID for the proto (may include a prefix).
func cookFormula(ctx context.Context, s storage.Storage, f *formula.Formula, protoID string) (*cookFormulaResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Check for SQLite store (needed for batch create with skip prefix)
	sqliteStore, ok := s.(*sqlite.SQLiteStorage)
	if !ok {
		return nil, fmt.Errorf("cook requires SQLite storage")
	}

	// Map step ID -> created issue ID
	idMapping := make(map[string]string)

	// Collect all issues and dependencies
	var issues []*types.Issue
	var deps []*types.Dependency
	var labels []struct{ issueID, label string }

	// Create root proto epic using provided protoID (may include prefix, bd-47qx)
	rootIssue := &types.Issue{
		ID:          protoID,
		Title:       f.Formula, // Title is the original formula name
		Description: f.Description,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeEpic,
		IsTemplate:  true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	issues = append(issues, rootIssue)
	labels = append(labels, struct{ issueID, label string }{protoID, MoleculeLabel})

	// Collect issues for each step (use protoID as parent for step IDs)
	collectStepsRecursive(f.Steps, protoID, idMapping, &issues, &deps, &labels)

	// Collect dependencies from depends_on
	for _, step := range f.Steps {
		collectDependencies(step, idMapping, &deps)
	}

	// Create all issues using batch with skip prefix validation
	opts := sqlite.BatchCreateOptions{
		SkipPrefixValidation: true, // Molecules use mol-* prefix
	}
	if err := sqliteStore.CreateIssuesWithFullOptions(ctx, issues, actor, opts); err != nil {
		return nil, fmt.Errorf("failed to create issues: %w", err)
	}

	// Track if we need cleanup on failure
	issuesCreated := true

	// Add labels and dependencies in a transaction
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add labels
		for _, l := range labels {
			if err := tx.AddLabel(ctx, l.issueID, l.label, actor); err != nil {
				return fmt.Errorf("failed to add label %s to %s: %w", l.label, l.issueID, err)
			}
		}

		// Add dependencies
		for _, dep := range deps {
			if err := tx.AddDependency(ctx, dep, actor); err != nil {
				return fmt.Errorf("failed to create dependency: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		// Clean up: delete the issues we created since labels/deps failed
		if issuesCreated {
			cleanupErr := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
				for i := len(issues) - 1; i >= 0; i-- {
					_ = tx.DeleteIssue(ctx, issues[i].ID) // Best effort cleanup
				}
				return nil
			})
			if cleanupErr != nil {
				return nil, fmt.Errorf("%w (cleanup also failed: %v)", err, cleanupErr)
			}
		}
		return nil, err
	}

	return &cookFormulaResult{
		ProtoID: protoID,
		Created: len(issues),
	}, nil
}

// collectStepsRecursive collects issues, dependencies, and labels for steps and their children.
func collectStepsRecursive(steps []*formula.Step, parentID string, idMapping map[string]string,
	issues *[]*types.Issue, deps *[]*types.Dependency, labels *[]struct{ issueID, label string }) {

	for _, step := range steps {
		// Generate issue ID (formula-name.step-id)
		issueID := fmt.Sprintf("%s.%s", parentID, step.ID)

		// Determine issue type
		issueType := types.TypeTask
		if step.Type != "" {
			switch step.Type {
			case "task":
				issueType = types.TypeTask
			case "bug":
				issueType = types.TypeBug
			case "feature":
				issueType = types.TypeFeature
			case "epic":
				issueType = types.TypeEpic
			case "chore":
				issueType = types.TypeChore
			}
		}

		// If step has children, it's an epic
		if len(step.Children) > 0 {
			issueType = types.TypeEpic
		}

		// Determine priority
		priority := 2
		if step.Priority != nil {
			priority = *step.Priority
		}

		issue := &types.Issue{
			ID:          issueID,
			Title:       step.Title, // Keep {{variables}} for substitution at pour time
			Description: step.Description,
			Status:      types.StatusOpen,
			Priority:    priority,
			IssueType:   issueType,
			Assignee:    step.Assignee,
			IsTemplate:  true,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		*issues = append(*issues, issue)

		// Collect labels
		for _, label := range step.Labels {
			*labels = append(*labels, struct{ issueID, label string }{issueID, label})
		}

		// Add gate label for waits_for field (bd-j4cr)
		if step.WaitsFor != "" {
			gateLabel := fmt.Sprintf("gate:%s", step.WaitsFor)
			*labels = append(*labels, struct{ issueID, label string }{issueID, gateLabel})
		}

		idMapping[step.ID] = issueID

		// Add parent-child dependency
		*deps = append(*deps, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: parentID,
			Type:        types.DepParentChild,
		})

		// Recursively collect children
		if len(step.Children) > 0 {
			collectStepsRecursive(step.Children, issueID, idMapping, issues, deps, labels)
		}
	}
}

// collectDependencies collects blocking dependencies from depends_on and needs fields.
func collectDependencies(step *formula.Step, idMapping map[string]string, deps *[]*types.Dependency) {
	issueID := idMapping[step.ID]

	// Process depends_on field
	for _, depID := range step.DependsOn {
		depIssueID, ok := idMapping[depID]
		if !ok {
			continue // Will be caught during validation
		}

		*deps = append(*deps, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: depIssueID,
			Type:        types.DepBlocks,
		})
	}

	// Process needs field (bd-hr39) - simpler alias for sibling dependencies
	for _, needID := range step.Needs {
		needIssueID, ok := idMapping[needID]
		if !ok {
			continue // Will be caught during validation
		}

		*deps = append(*deps, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: needIssueID,
			Type:        types.DepBlocks,
		})
	}

	// Recursively handle children
	for _, child := range step.Children {
		collectDependencies(child, idMapping, deps)
	}
}

// deleteProtoSubgraph deletes a proto and all its children.
func deleteProtoSubgraph(ctx context.Context, s storage.Storage, protoID string) error {
	// Load the subgraph
	subgraph, err := loadTemplateSubgraph(ctx, s, protoID)
	if err != nil {
		return fmt.Errorf("load proto: %w", err)
	}

	// Delete in reverse order (children first)
	return s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		for i := len(subgraph.Issues) - 1; i >= 0; i-- {
			issue := subgraph.Issues[i]
			if err := tx.DeleteIssue(ctx, issue.ID); err != nil {
				return fmt.Errorf("delete %s: %w", issue.ID, err)
			}
		}
		return nil
	})
}

// printFormulaSteps prints steps in a tree format.
func printFormulaSteps(steps []*formula.Step, indent string) {
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
			printFormulaSteps(step.Children, childIndent)
		}
	}
}

func init() {
	cookCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	cookCmd.Flags().Bool("force", false, "Replace existing proto if it exists")
	cookCmd.Flags().StringSlice("search-path", []string{}, "Additional paths to search for formula inheritance")
	cookCmd.Flags().String("prefix", "", "Prefix to prepend to proto ID (e.g., 'gt-' creates 'gt-mol-feature')")

	rootCmd.AddCommand(cookCmd)
}
