package main

import (
	"context"
	"encoding/json"
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
	Short: "Compile a formula into a proto (ephemeral by default)",
	Long: `Cook transforms a .formula.json file into a proto.

By default, cook outputs the resolved formula as JSON to stdout for
ephemeral use. The output can be inspected, piped, or saved to a file.

Two cooking modes are available (gt-8tmz.23):

  COMPILE-TIME (default, --mode=compile):
    Produces a proto with {{variable}} placeholders intact.
    Use for: modeling, estimation, contractor handoff, planning.
    Variables are NOT substituted - the output shows the template structure.

  RUNTIME (--mode=runtime or when --var flags provided):
    Produces a fully-resolved proto with variables substituted.
    Use for: final validation before pour, seeing exact output.
    Requires all variables to have values (via --var or defaults).

Formulas are high-level workflow templates that support:
  - Variable definitions with defaults and validation
  - Step definitions that become issue hierarchies
  - Composition rules for bonding formulas together
  - Inheritance via extends

The --persist flag enables the legacy behavior of writing the proto
to the database. This is useful when you want to reuse the same
proto multiple times without re-cooking.

For most workflows, prefer ephemeral protos: pour and wisp commands
accept formula names directly and cook inline (bd-rciw).

Examples:
  bd cook mol-feature.formula.json                    # Compile-time: keep {{vars}}
  bd cook mol-feature --var name=auth                 # Runtime: substitute vars
  bd cook mol-feature --mode=runtime --var name=auth  # Explicit runtime mode
  bd cook mol-feature --dry-run                       # Preview steps
  bd cook mol-release.formula.json --persist          # Write to database
  bd cook mol-release.formula.json --persist --force  # Replace existing

Output (default):
  JSON representation of the resolved formula with all steps.

Output (--persist):
  Creates a proto bead in the database with:
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
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	persist, _ := cmd.Flags().GetBool("persist")
	force, _ := cmd.Flags().GetBool("force")
	searchPaths, _ := cmd.Flags().GetStringSlice("search-path")
	prefix, _ := cmd.Flags().GetString("prefix")
	varFlags, _ := cmd.Flags().GetStringSlice("var")
	mode, _ := cmd.Flags().GetString("mode")

	// Parse variables (gt-8tmz.23)
	inputVars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
			os.Exit(1)
		}
		inputVars[parts[0]] = parts[1]
	}

	// Determine cooking mode (gt-8tmz.23)
	// Runtime mode is triggered by: explicit --mode=runtime OR providing --var flags
	runtimeMode := mode == "runtime" || len(inputVars) > 0
	if mode != "" && mode != "compile" && mode != "runtime" {
		fmt.Fprintf(os.Stderr, "Error: invalid mode '%s', must be 'compile' or 'runtime'\n", mode)
		os.Exit(1)
	}

	// Only need store access if persisting
	if persist {
		CheckReadonly("cook --persist")

		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: cook --persist requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon cook %s --persist ...\n", args[0])
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}
	}

	ctx := rootCtx

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

	// Apply control flow operators (gt-8tmz.4) - loops, branches, gates
	// This must happen before advice and expansions so they can act on expanded loop steps
	controlFlowSteps, err := formula.ApplyControlFlow(resolved.Steps, resolved.Compose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error applying control flow: %v\n", err)
		os.Exit(1)
	}
	resolved.Steps = controlFlowSteps

	// Apply advice transformations (gt-8tmz.2)
	if len(resolved.Advice) > 0 {
		resolved.Steps = formula.ApplyAdvice(resolved.Steps, resolved.Advice)
	}

	// Apply inline step expansions (gt-8tmz.35)
	// This processes Step.Expand fields before compose.expand/map rules
	inlineExpandedSteps, err := formula.ApplyInlineExpansions(resolved.Steps, parser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error applying inline expansions: %v\n", err)
		os.Exit(1)
	}
	resolved.Steps = inlineExpandedSteps

	// Apply expansion operators (gt-8tmz.3)
	if resolved.Compose != nil && (len(resolved.Compose.Expand) > 0 || len(resolved.Compose.Map) > 0) {
		expandedSteps, err := formula.ApplyExpansions(resolved.Steps, resolved.Compose, parser)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error applying expansions: %v\n", err)
			os.Exit(1)
		}
		resolved.Steps = expandedSteps
	}

	// Apply aspects from compose.aspects (gt-8tmz.5)
	if resolved.Compose != nil && len(resolved.Compose.Aspects) > 0 {
		for _, aspectName := range resolved.Compose.Aspects {
			aspectFormula, err := parser.LoadByName(aspectName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading aspect %q: %v\n", aspectName, err)
				os.Exit(1)
			}
			if aspectFormula.Type != formula.TypeAspect {
				fmt.Fprintf(os.Stderr, "Error: %q is not an aspect formula (type=%s)\n", aspectName, aspectFormula.Type)
				os.Exit(1)
			}
			if len(aspectFormula.Advice) > 0 {
				resolved.Steps = formula.ApplyAdvice(resolved.Steps, aspectFormula.Advice)
			}
		}
	}

	// Apply prefix to proto ID if specified (bd-47qx)
	protoID := resolved.Formula
	if prefix != "" {
		protoID = prefix + resolved.Formula
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
		// Determine mode label for display
		modeLabel := "compile-time"
		if runtimeMode {
			modeLabel = "runtime"
			// Apply defaults for runtime mode display
			for name, def := range resolved.Vars {
				if _, provided := inputVars[name]; !provided && def.Default != "" {
					inputVars[name] = def.Default
				}
			}
		}

		fmt.Printf("\nDry run: would cook formula %s as proto %s (%s mode)\n\n", resolved.Formula, protoID, modeLabel)

		// In runtime mode, show substituted steps
		if runtimeMode {
			// Create a copy with substituted values for display
			substituteFormulaVars(resolved, inputVars)
			fmt.Printf("Steps (%d) [variables substituted]:\n", len(resolved.Steps))
		} else {
			fmt.Printf("Steps (%d) [{{variables}} shown as placeholders]:\n", len(resolved.Steps))
		}
		printFormulaSteps(resolved.Steps, "  ")

		if len(vars) > 0 {
			fmt.Printf("\nVariables used: %s\n", strings.Join(vars, ", "))
		}

		// Show variable values in runtime mode
		if runtimeMode && len(inputVars) > 0 {
			fmt.Printf("\nVariable values:\n")
			for name, value := range inputVars {
				fmt.Printf("  {{%s}} = %s\n", name, value)
			}
		}

		if len(bondPoints) > 0 {
			fmt.Printf("Bond points: %s\n", strings.Join(bondPoints, ", "))
		}

		// Show variable definitions (more useful in compile-time mode)
		if !runtimeMode && len(resolved.Vars) > 0 {
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

	// Ephemeral mode (default): output resolved formula as JSON to stdout (bd-rciw)
	if !persist {
		// Runtime mode (gt-8tmz.23): substitute variables before output
		if runtimeMode {
			// Apply defaults from formula variable definitions
			for name, def := range resolved.Vars {
				if _, provided := inputVars[name]; !provided && def.Default != "" {
					inputVars[name] = def.Default
				}
			}

			// Check for missing required variables
			var missingVars []string
			for _, v := range vars {
				if _, ok := inputVars[v]; !ok {
					missingVars = append(missingVars, v)
				}
			}
			if len(missingVars) > 0 {
				fmt.Fprintf(os.Stderr, "Error: runtime mode requires all variables to have values\n")
				fmt.Fprintf(os.Stderr, "Missing: %s\n", strings.Join(missingVars, ", "))
				fmt.Fprintf(os.Stderr, "Provide with: --var %s=<value>\n", missingVars[0])
				os.Exit(1)
			}

			// Substitute variables in the formula
			substituteFormulaVars(resolved, inputVars)
		}
		outputJSON(resolved)
		return
	}

	// Persist mode: create proto bead in database (legacy behavior)
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

// cookFormulaToSubgraph creates an in-memory TemplateSubgraph from a resolved formula.
// This is the ephemeral proto implementation - no database storage.
// The returned subgraph can be passed directly to cloneSubgraph for instantiation.
//
//nolint:unparam // error return kept for API consistency with future error handling
func cookFormulaToSubgraph(f *formula.Formula, protoID string) (*TemplateSubgraph, error) {
	// Map step ID -> created issue
	issueMap := make(map[string]*types.Issue)

	// Collect all issues and dependencies
	var issues []*types.Issue
	var deps []*types.Dependency

	// Create root proto epic
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
	issueMap[protoID] = rootIssue

	// Collect issues for each step (use protoID as parent for step IDs)
	collectStepsToSubgraph(f.Steps, protoID, issueMap, &issues, &deps)

	// Collect dependencies from depends_on
	stepIDMapping := make(map[string]string)
	for _, step := range f.Steps {
		collectStepIDMappings(step, protoID, stepIDMapping)
	}
	for _, step := range f.Steps {
		collectDependenciesToSubgraph(step, stepIDMapping, &deps)
	}

	return &TemplateSubgraph{
		Root:         rootIssue,
		Issues:       issues,
		Dependencies: deps,
		IssueMap:     issueMap,
	}, nil
}

// collectStepsToSubgraph collects issues and dependencies for steps and their children.
// This is the in-memory version that doesn't create labels (since those require DB).
func collectStepsToSubgraph(steps []*formula.Step, parentID string, issueMap map[string]*types.Issue,
	issues *[]*types.Issue, deps *[]*types.Dependency) {

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
			ID:             issueID,
			Title:          step.Title, // Keep {{variables}} for substitution at pour time
			Description:    step.Description,
			Status:         types.StatusOpen,
			Priority:       priority,
			IssueType:      issueType,
			Assignee:       step.Assignee,
			IsTemplate:     true,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
			SourceFormula:  step.SourceFormula,  // Source tracing (gt-8tmz.18)
			SourceLocation: step.SourceLocation, // Source tracing (gt-8tmz.18)
		}

		// Store labels in the issue's Labels field for in-memory use
		issue.Labels = append(issue.Labels, step.Labels...)

		// Add gate label for waits_for field (bd-j4cr)
		if step.WaitsFor != "" {
			gateLabel := fmt.Sprintf("gate:%s", step.WaitsFor)
			issue.Labels = append(issue.Labels, gateLabel)
		}

		*issues = append(*issues, issue)
		issueMap[issueID] = issue

		// Add parent-child dependency
		*deps = append(*deps, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: parentID,
			Type:        types.DepParentChild,
		})

		// Recursively collect children
		if len(step.Children) > 0 {
			collectStepsToSubgraph(step.Children, issueID, issueMap, issues, deps)
		}
	}
}

// collectStepIDMappings builds a map from step ID to full issue ID
func collectStepIDMappings(step *formula.Step, parentID string, mapping map[string]string) {
	issueID := fmt.Sprintf("%s.%s", parentID, step.ID)
	mapping[step.ID] = issueID

	for _, child := range step.Children {
		collectStepIDMappings(child, issueID, mapping)
	}
}

// collectDependenciesToSubgraph collects blocking dependencies from depends_on and needs fields.
func collectDependenciesToSubgraph(step *formula.Step, idMapping map[string]string, deps *[]*types.Dependency) {
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

	// Process waits_for field (gt-8tmz.38) - fanout gate dependency
	if step.WaitsFor != "" {
		waitsForSpec := formula.ParseWaitsFor(step.WaitsFor)
		if waitsForSpec != nil {
			// Determine spawner ID
			spawnerStepID := waitsForSpec.SpawnerID
			if spawnerStepID == "" && len(step.Needs) > 0 {
				// Infer spawner from first need
				spawnerStepID = step.Needs[0]
			}

			if spawnerStepID != "" {
				if spawnerIssueID, ok := idMapping[spawnerStepID]; ok {
					// Create WaitsFor dependency with metadata
					meta := types.WaitsForMeta{
						Gate: waitsForSpec.Gate,
					}
					metaJSON, _ := json.Marshal(meta)

					*deps = append(*deps, &types.Dependency{
						IssueID:     issueID,
						DependsOnID: spawnerIssueID,
						Type:        types.DepWaitsFor,
						Metadata:    string(metaJSON),
					})
				}
			}
		}
	}

	// Recursively handle children
	for _, child := range step.Children {
		collectDependenciesToSubgraph(child, idMapping, deps)
	}
}

// resolveAndCookFormula loads a formula by name, resolves it, applies all transformations,
// and returns an in-memory TemplateSubgraph ready for instantiation.
// This is the main entry point for ephemeral proto cooking.
func resolveAndCookFormula(formulaName string, searchPaths []string) (*TemplateSubgraph, error) {
	// Create parser with search paths
	parser := formula.NewParser(searchPaths...)

	// Load formula by name
	f, err := parser.LoadByName(formulaName)
	if err != nil {
		return nil, fmt.Errorf("loading formula %q: %w", formulaName, err)
	}

	// Resolve inheritance
	resolved, err := parser.Resolve(f)
	if err != nil {
		return nil, fmt.Errorf("resolving formula %q: %w", formulaName, err)
	}

	// Apply control flow operators (gt-8tmz.4) - loops, branches, gates
	controlFlowSteps, err := formula.ApplyControlFlow(resolved.Steps, resolved.Compose)
	if err != nil {
		return nil, fmt.Errorf("applying control flow to %q: %w", formulaName, err)
	}
	resolved.Steps = controlFlowSteps

	// Apply advice transformations (gt-8tmz.2)
	if len(resolved.Advice) > 0 {
		resolved.Steps = formula.ApplyAdvice(resolved.Steps, resolved.Advice)
	}

	// Apply inline step expansions (gt-8tmz.35)
	inlineExpandedSteps, err := formula.ApplyInlineExpansions(resolved.Steps, parser)
	if err != nil {
		return nil, fmt.Errorf("applying inline expansions to %q: %w", formulaName, err)
	}
	resolved.Steps = inlineExpandedSteps

	// Apply expansion operators (gt-8tmz.3)
	if resolved.Compose != nil && (len(resolved.Compose.Expand) > 0 || len(resolved.Compose.Map) > 0) {
		expandedSteps, err := formula.ApplyExpansions(resolved.Steps, resolved.Compose, parser)
		if err != nil {
			return nil, fmt.Errorf("applying expansions to %q: %w", formulaName, err)
		}
		resolved.Steps = expandedSteps
	}

	// Apply aspects from compose.aspects (gt-8tmz.5)
	if resolved.Compose != nil && len(resolved.Compose.Aspects) > 0 {
		for _, aspectName := range resolved.Compose.Aspects {
			aspectFormula, err := parser.LoadByName(aspectName)
			if err != nil {
				return nil, fmt.Errorf("loading aspect %q: %w", aspectName, err)
			}
			if aspectFormula.Type != formula.TypeAspect {
				return nil, fmt.Errorf("%q is not an aspect formula (type=%s)", aspectName, aspectFormula.Type)
			}
			if len(aspectFormula.Advice) > 0 {
				resolved.Steps = formula.ApplyAdvice(resolved.Steps, aspectFormula.Advice)
			}
		}
	}

	// Cook to in-memory subgraph, including variable definitions for default handling
	return cookFormulaToSubgraphWithVars(resolved, resolved.Formula, resolved.Vars)
}

// cookFormulaToSubgraphWithVars creates an in-memory subgraph with variable info attached
func cookFormulaToSubgraphWithVars(f *formula.Formula, protoID string, vars map[string]*formula.VarDef) (*TemplateSubgraph, error) {
	subgraph, err := cookFormulaToSubgraph(f, protoID)
	if err != nil {
		return nil, err
	}
	// Attach variable definitions to the subgraph for default handling during pour
	// Convert from *VarDef to VarDef for simpler handling
	if vars != nil {
		subgraph.VarDefs = make(map[string]formula.VarDef)
		for k, v := range vars {
			if v != nil {
				subgraph.VarDefs[k] = *v
			}
		}
	}
	return subgraph, nil
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
			ID:             issueID,
			Title:          step.Title, // Keep {{variables}} for substitution at pour time
			Description:    step.Description,
			Status:         types.StatusOpen,
			Priority:       priority,
			IssueType:      issueType,
			Assignee:       step.Assignee,
			IsTemplate:     true,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
			SourceFormula:  step.SourceFormula,  // Source tracing (gt-8tmz.18)
			SourceLocation: step.SourceLocation, // Source tracing (gt-8tmz.18)
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

	// Process waits_for field (gt-8tmz.38) - fanout gate dependency
	if step.WaitsFor != "" {
		waitsForSpec := formula.ParseWaitsFor(step.WaitsFor)
		if waitsForSpec != nil {
			// Determine spawner ID
			spawnerStepID := waitsForSpec.SpawnerID
			if spawnerStepID == "" && len(step.Needs) > 0 {
				// Infer spawner from first need
				spawnerStepID = step.Needs[0]
			}

			if spawnerStepID != "" {
				if spawnerIssueID, ok := idMapping[spawnerStepID]; ok {
					// Create WaitsFor dependency with metadata
					meta := types.WaitsForMeta{
						Gate: waitsForSpec.Gate,
					}
					metaJSON, _ := json.Marshal(meta)

					*deps = append(*deps, &types.Dependency{
						IssueID:     issueID,
						DependsOnID: spawnerIssueID,
						Type:        types.DepWaitsFor,
						Metadata:    string(metaJSON),
					})
				}
			}
		}
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

		// Source tracing info (gt-8tmz.18)
		sourceStr := ""
		if step.SourceFormula != "" || step.SourceLocation != "" {
			sourceStr = fmt.Sprintf(" [from: %s@%s]", step.SourceFormula, step.SourceLocation)
		}

		fmt.Printf("%s%s %s: %s%s%s%s\n", indent, connector, step.ID, step.Title, typeStr, depStr, sourceStr)

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

// substituteFormulaVars substitutes {{variable}} placeholders in a formula (gt-8tmz.23).
// This is used in runtime mode to fully resolve the formula before output.
func substituteFormulaVars(f *formula.Formula, vars map[string]string) {
	// Substitute in top-level fields
	f.Description = substituteVariables(f.Description, vars)

	// Substitute in all steps recursively
	substituteStepVars(f.Steps, vars)
}

// substituteStepVars recursively substitutes variables in step titles and descriptions.
func substituteStepVars(steps []*formula.Step, vars map[string]string) {
	for _, step := range steps {
		step.Title = substituteVariables(step.Title, vars)
		step.Description = substituteVariables(step.Description, vars)
		if len(step.Children) > 0 {
			substituteStepVars(step.Children, vars)
		}
	}
}

func init() {
	cookCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	cookCmd.Flags().Bool("persist", false, "Persist proto to database (legacy behavior)")
	cookCmd.Flags().Bool("force", false, "Replace existing proto if it exists (requires --persist)")
	cookCmd.Flags().StringSlice("search-path", []string{}, "Additional paths to search for formula inheritance")
	cookCmd.Flags().String("prefix", "", "Prefix to prepend to proto ID (e.g., 'gt-' creates 'gt-mol-feature')")
	cookCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value), enables runtime mode")
	cookCmd.Flags().String("mode", "", "Cooking mode: compile (keep placeholders) or runtime (substitute vars)")

	rootCmd.AddCommand(cookCmd)
}
