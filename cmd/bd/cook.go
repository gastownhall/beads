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
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// stepTypeToIssueType converts a formula step type string to a types.IssueType.
// Returns types.TypeTask for empty or unrecognized types.
func stepTypeToIssueType(stepType string) types.IssueType {
	switch stepType {
	case "task":
		return types.TypeTask
	case "bug":
		return types.TypeBug
	case "feature":
		return types.TypeFeature
	case "epic":
		return types.TypeEpic
	case "chore":
		return types.TypeChore
	default:
		return types.TypeTask
	}
}

// cookCmd compiles a formula JSON into a proto bead.
var cookCmd = &cobra.Command{
	Use:   "cook <formula-file>",
	Short: "Compile a formula into a proto (ephemeral by default)",
	Long: `Cook transforms a .formula.json file into a proto.

By default, cook outputs the resolved formula as JSON to stdout for
ephemeral use. The output can be inspected, piped, or saved to a file.

Two cooking modes are available:

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
accept formula names directly and cook inline.

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

// cookFlags holds parsed command-line flags for the cook command
type cookFlags struct {
	dryRun      bool
	persist     bool
	force       bool
	searchPaths []string
	prefix      string
	inputVars   map[string]string
	runtimeMode bool
	formulaPath string
}

// parseCookFlags parses and validates cook command flags
func parseCookFlags(cmd *cobra.Command, args []string) (*cookFlags, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	persist, _ := cmd.Flags().GetBool("persist")
	force, _ := cmd.Flags().GetBool("force")
	searchPaths, _ := cmd.Flags().GetStringSlice("search-path")
	prefix, _ := cmd.Flags().GetString("prefix")
	varFlags, _ := cmd.Flags().GetStringArray("var")
	mode, _ := cmd.Flags().GetString("mode")

	// Parse variables
	inputVars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid variable format '%s', expected 'key=value'", v)
		}
		inputVars[parts[0]] = parts[1]
	}

	// Validate mode
	if mode != "" && mode != "compile" && mode != "runtime" {
		return nil, fmt.Errorf("invalid mode '%s', must be 'compile' or 'runtime'", mode)
	}

	// Runtime mode is triggered by: explicit --mode=runtime OR providing --var flags
	runtimeMode := mode == "runtime" || len(inputVars) > 0

	return &cookFlags{
		dryRun:      dryRun,
		persist:     persist,
		force:       force,
		searchPaths: searchPaths,
		prefix:      prefix,
		inputVars:   inputVars,
		runtimeMode: runtimeMode,
		formulaPath: args[0],
	}, nil
}

// loadAndResolveFormula parses a formula file and applies all transformations.
// It first tries to load by name from the formula registry (.beads/formulas/),
// and falls back to parsing as a file path if that fails.
func loadAndResolveFormula(formulaPath string, searchPaths []string) (*formula.Formula, error) {
	parser := formula.NewParser(searchPaths...)

	// Try to load by name first (from .beads/formulas/ registry)
	f, err := parser.LoadByName(formulaPath)
	if err != nil {
		// Fall back to parsing as a file path
		f, err = parser.ParseFile(formulaPath)
		if err != nil {
			return nil, fmt.Errorf("parsing formula: %w", err)
		}
	}

	// Resolve inheritance
	resolved, err := parser.Resolve(f)
	if err != nil {
		return nil, fmt.Errorf("resolving formula: %w", err)
	}

	// Apply control flow operators - loops, branches, gates
	controlFlowSteps, err := formula.ApplyControlFlow(resolved.Steps, resolved.Compose)
	if err != nil {
		return nil, fmt.Errorf("applying control flow: %w", err)
	}
	resolved.Steps = controlFlowSteps

	// Apply advice transformations
	if len(resolved.Advice) > 0 {
		resolved.Steps = formula.ApplyAdvice(resolved.Steps, resolved.Advice)
	}

	// Apply inline step expansions
	inlineExpandedSteps, err := formula.ApplyInlineExpansions(resolved.Steps, parser)
	if err != nil {
		return nil, fmt.Errorf("applying inline expansions: %w", err)
	}
	resolved.Steps = inlineExpandedSteps

	// Apply expansion operators
	if resolved.Compose != nil && (len(resolved.Compose.Expand) > 0 || len(resolved.Compose.Map) > 0) {
		expandedSteps, err := formula.ApplyExpansions(resolved.Steps, resolved.Compose, parser)
		if err != nil {
			return nil, fmt.Errorf("applying expansions: %w", err)
		}
		resolved.Steps = expandedSteps
	}

	// Apply aspects from compose.aspects
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

	return resolved, nil
}

// outputCookDryRun displays a dry-run preview of what would be cooked
func outputCookDryRun(resolved *formula.Formula, protoID string, runtimeMode bool, inputVars map[string]string, vars, bondPoints []string) {
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
}

// outputCookEphemeral outputs the resolved formula as JSON (ephemeral mode)
func outputCookEphemeral(resolved *formula.Formula, runtimeMode bool, inputVars map[string]string, vars []string) error {
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
			return fmt.Errorf("runtime mode requires all variables to have values\nMissing: %s\nProvide with: --var %s=<value>",
				strings.Join(missingVars, ", "), missingVars[0])
		}

		// Substitute variables in the formula
		substituteFormulaVars(resolved, inputVars)
	}
	outputJSON(resolved)
	return nil
}

// persistCookFormula creates a proto bead in the database (persist mode)
func persistCookFormula(ctx context.Context, resolved *formula.Formula, protoID string, force bool, vars, bondPoints []string) error {
	// Check if proto already exists
	existingProto, err := store.GetIssue(ctx, protoID)
	if err == nil && existingProto != nil {
		if !force {
			return fmt.Errorf("proto %s already exists (use --force to replace)", protoID)
		}
		// Delete existing proto and its children
		if err := deleteProtoSubgraph(ctx, store, protoID); err != nil {
			return fmt.Errorf("deleting existing proto: %w", err)
		}
	}

	// Create the proto bead from the formula
	result, err := cookFormula(ctx, store, resolved, protoID)
	if err != nil {
		return fmt.Errorf("cooking formula: %w", err)
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
		return nil
	}

	fmt.Printf("%s Cooked proto: %s\n", ui.RenderPass("✓"), result.ProtoID)
	fmt.Printf("  Created %d issues\n", result.Created)
	if len(vars) > 0 {
		fmt.Printf("  Variables: %s\n", strings.Join(vars, ", "))
	}
	if len(bondPoints) > 0 {
		fmt.Printf("  Bond points: %s\n", strings.Join(bondPoints, ", "))
	}
	fmt.Printf("\nTo use: bd mol pour %s --var <name>=<value>\n", result.ProtoID)
	return nil
}

func runCook(cmd *cobra.Command, args []string) {
	// Parse and validate flags
	flags, err := parseCookFlags(cmd, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Validate store access for persist mode
	if flags.persist {
		CheckReadonly("cook --persist")
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: cook --persist requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon cook %s --persist ...\n", flags.formulaPath)
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}
	}

	// Load and resolve the formula
	resolved, err := loadAndResolveFormula(flags.formulaPath, flags.searchPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Apply prefix to proto ID if specified
	protoID := resolved.Formula
	if flags.prefix != "" {
		protoID = flags.prefix + resolved.Formula
	}

	// Extract variables and bond points
	vars := formula.ExtractVariables(resolved)
	var bondPoints []string
	if resolved.Compose != nil {
		for _, bp := range resolved.Compose.BondPoints {
			bondPoints = append(bondPoints, bp.ID)
		}
	}

	// Handle dry-run mode
	if flags.dryRun {
		outputCookDryRun(resolved, protoID, flags.runtimeMode, flags.inputVars, vars, bondPoints)
		return
	}

	// Handle ephemeral mode (default)
	if !flags.persist {
		if err := outputCookEphemeral(resolved, flags.runtimeMode, flags.inputVars, vars); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Handle persist mode
	if err := persistCookFormula(rootCtx, resolved, protoID, flags.force, vars, bondPoints); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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

	// Determine root title: use {{title}} placeholder if the variable is defined,
	// otherwise fall back to formula name (GH#852)
	rootTitle := f.Formula
	if _, hasTitle := f.Vars["title"]; hasTitle {
		rootTitle = "{{title}}"
	}

	// Determine root description: use {{desc}} placeholder if the variable is defined,
	// otherwise fall back to formula description (GH#852)
	rootDesc := f.Description
	if _, hasDesc := f.Vars["desc"]; hasDesc {
		rootDesc = "{{desc}}"
	}

	// Create root proto epic
	rootIssue := &types.Issue{
		ID:          protoID,
		Title:       rootTitle,
		Description: rootDesc,
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
	// The unified collectSteps builds both issueMap and idMapping
	idMapping := make(map[string]string)
	collectSteps(f.Steps, protoID, idMapping, issueMap, &issues, &deps, nil) // nil = keep labels on issues

	// Collect dependencies from depends_on using the idMapping built above
	for _, step := range f.Steps {
		collectDependencies(step, idMapping, &deps)
	}

	return &TemplateSubgraph{
		Root:         rootIssue,
		Issues:       issues,
		Dependencies: deps,
		IssueMap:     issueMap,
	}, nil
}

// createGateIssue creates a gate issue for a step with a Gate field.
// Gate issues have type=gate and block the step they guard.
// Returns the gate issue and its ID.
func createGateIssue(step *formula.Step, parentID string) *types.Issue {
	if step.Gate == nil {
		return nil
	}

	// Generate gate issue ID: {parentID}.gate-{step.ID}
	gateID := fmt.Sprintf("%s.gate-%s", parentID, step.ID)

	// Build title from gate type and ID
	title := fmt.Sprintf("Gate: %s", step.Gate.Type)
	if step.Gate.ID != "" {
		title = fmt.Sprintf("Gate: %s %s", step.Gate.Type, step.Gate.ID)
	}

	// Parse timeout if specified
	var timeout time.Duration
	if step.Gate.Timeout != "" {
		if parsed, err := time.ParseDuration(step.Gate.Timeout); err == nil {
			timeout = parsed
		}
	}

	return &types.Issue{
		ID:          gateID,
		Title:       title,
		Description: fmt.Sprintf("Async gate for step %s", step.ID),
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   "gate",
		AwaitType:   step.Gate.Type,
		AwaitID:     step.Gate.ID,
		Timeout:     timeout,
		IsTemplate:  true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// processStepToIssue converts a formula.Step to a types.Issue.
// The issue includes all fields including Labels populated from step.Labels and waits_for.
// This is the shared core logic used by both DB-persisted and in-memory cooking.
func processStepToIssue(step *formula.Step, parentID string) *types.Issue {
	// Generate issue ID (formula-name.step-id)
	issueID := fmt.Sprintf("%s.%s", parentID, step.ID)

	// Determine issue type (children override to epic)
	issueType := stepTypeToIssueType(step.Type)
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
		SourceFormula:  step.SourceFormula,  // Source tracing
		SourceLocation: step.SourceLocation, // Source tracing
	}

	// Populate labels from step
	issue.Labels = append(issue.Labels, step.Labels...)

	// Add gate label for waits_for field
	if step.WaitsFor != "" {
		gateLabel := fmt.Sprintf("gate:%s", step.WaitsFor)
		issue.Labels = append(issue.Labels, gateLabel)
	}

	return issue
}

// collectSteps collects issues and dependencies for steps and their children.
// This is the unified implementation used by both DB-persisted and in-memory cooking.
//
// Parameters:
//   - idMapping: step.ID → issue.ID (always populated, used for dependency resolution)
//   - issueMap: issue.ID → issue (optional, nil for DB path, populated for in-memory path)
//   - labelHandler: callback for each label (if nil, labels stay on issue; if set, labels are
//     extracted and issue.Labels is cleared - use for DB path)
func collectSteps(steps []*formula.Step, parentID string,
	idMapping map[string]string,
	issueMap map[string]*types.Issue,
	issues *[]*types.Issue,
	deps *[]*types.Dependency,
	labelHandler func(issueID, label string)) {

	for _, step := range steps {
		issue := processStepToIssue(step, parentID)
		*issues = append(*issues, issue)

		// Build mappings
		idMapping[step.ID] = issue.ID
		if issueMap != nil {
			issueMap[issue.ID] = issue
		}

		// Handle labels: extract via callback (DB path) or keep on issue (in-memory path)
		if labelHandler != nil {
			for _, label := range issue.Labels {
				labelHandler(issue.ID, label)
			}
			issue.Labels = nil // DB stores labels separately
		}

		// Add parent-child dependency
		*deps = append(*deps, &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: parentID,
			Type:        types.DepParentChild,
		})

		// Create gate issue if step has a Gate (bd-7zka.2)
		if step.Gate != nil {
			gateIssue := createGateIssue(step, parentID)
			*issues = append(*issues, gateIssue)

			// Add gate to mapping (use gate-{step.ID} as key)
			gateKey := fmt.Sprintf("gate-%s", step.ID)
			idMapping[gateKey] = gateIssue.ID
			if issueMap != nil {
				issueMap[gateIssue.ID] = gateIssue
			}

			// Handle gate labels if needed
			if labelHandler != nil && len(gateIssue.Labels) > 0 {
				for _, label := range gateIssue.Labels {
					labelHandler(gateIssue.ID, label)
				}
				gateIssue.Labels = nil
			}

			// Gate is a child of the parent (same level as the step)
			*deps = append(*deps, &types.Dependency{
				IssueID:     gateIssue.ID,
				DependsOnID: parentID,
				Type:        types.DepParentChild,
			})

			// Step depends on gate (gate blocks the step)
			*deps = append(*deps, &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: gateIssue.ID,
				Type:        types.DepBlocks,
			})
		}

		// Recursively collect children
		if len(step.Children) > 0 {
			collectSteps(step.Children, issue.ID, idMapping, issueMap, issues, deps, labelHandler)
		}
	}
}

// resolveAndCookFormula loads a formula by name, resolves it, applies all transformations,
// and returns an in-memory TemplateSubgraph ready for instantiation.
// This is the main entry point for ephemeral proto cooking.
func resolveAndCookFormula(formulaName string, searchPaths []string) (*TemplateSubgraph, error) {
	return resolveAndCookFormulaWithVars(formulaName, searchPaths, nil)
}

// resolveAndCookFormulaWithVars loads a formula and optionally filters steps by condition.
// If conditionVars is provided, steps with conditions that evaluate to false are excluded.
// Pass nil for conditionVars to include all steps (condition filtering skipped).
func resolveAndCookFormulaWithVars(formulaName string, searchPaths []string, conditionVars map[string]string) (*TemplateSubgraph, error) {
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

	// Apply control flow operators - loops, branches, gates
	controlFlowSteps, err := formula.ApplyControlFlow(resolved.Steps, resolved.Compose)
	if err != nil {
		return nil, fmt.Errorf("applying control flow to %q: %w", formulaName, err)
	}
	resolved.Steps = controlFlowSteps

	// Apply advice transformations
	if len(resolved.Advice) > 0 {
		resolved.Steps = formula.ApplyAdvice(resolved.Steps, resolved.Advice)
	}

	// Apply inline step expansions
	inlineExpandedSteps, err := formula.ApplyInlineExpansions(resolved.Steps, parser)
	if err != nil {
		return nil, fmt.Errorf("applying inline expansions to %q: %w", formulaName, err)
	}
	resolved.Steps = inlineExpandedSteps

	// Apply expansion operators
	if resolved.Compose != nil && (len(resolved.Compose.Expand) > 0 || len(resolved.Compose.Map) > 0) {
		expandedSteps, err := formula.ApplyExpansions(resolved.Steps, resolved.Compose, parser)
		if err != nil {
			return nil, fmt.Errorf("applying expansions to %q: %w", formulaName, err)
		}
		resolved.Steps = expandedSteps
	}

	// Apply aspects from compose.aspects
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

	// Apply step condition filtering if vars provided (bd-7zka.1)
	// This filters out steps whose conditions evaluate to false
	if conditionVars != nil {
		// Merge with formula defaults for complete evaluation
		mergedVars := make(map[string]string)
		for name, def := range resolved.Vars {
			if def != nil && def.Default != "" {
				mergedVars[name] = def.Default
			}
		}
		for k, v := range conditionVars {
			mergedVars[k] = v
		}

		filteredSteps, err := formula.FilterStepsByCondition(resolved.Steps, mergedVars)
		if err != nil {
			return nil, fmt.Errorf("filtering steps by condition: %w", err)
		}
		resolved.Steps = filteredSteps
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
	// Attach recommended phase from formula (warn on pour of vapor formulas)
	subgraph.Phase = f.Phase
	return subgraph, nil
}

// cookFormula creates a proto bead from a resolved formula.
// protoID is the final ID for the proto (may include a prefix).
func cookFormula(ctx context.Context, s storage.Storage, f *formula.Formula, protoID string) (*cookFormulaResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Map step ID -> created issue ID
	idMapping := make(map[string]string)

	// Collect all issues and dependencies
	var issues []*types.Issue
	var deps []*types.Dependency
	var labels []struct{ issueID, label string }

	// Determine root title: use {{title}} placeholder if the variable is defined,
	// otherwise fall back to formula name (GH#852)
	rootTitle := f.Formula
	if _, hasTitle := f.Vars["title"]; hasTitle {
		rootTitle = "{{title}}"
	}

	// Determine root description: use {{desc}} placeholder if the variable is defined,
	// otherwise fall back to formula description (GH#852)
	rootDesc := f.Description
	if _, hasDesc := f.Vars["desc"]; hasDesc {
		rootDesc = "{{desc}}"
	}

	// Create root proto epic using provided protoID (may include prefix)
	rootIssue := &types.Issue{
		ID:          protoID,
		Title:       rootTitle,
		Description: rootDesc,
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
	// Use labelHandler to extract labels for separate DB storage
	collectSteps(f.Steps, protoID, idMapping, nil, &issues, &deps, func(issueID, label string) {
		labels = append(labels, struct{ issueID, label string }{issueID, label})
	})

	// Collect dependencies from depends_on
	for _, step := range f.Steps {
		collectDependencies(step, idMapping, &deps)
	}

	// Create all issues using batch with skip prefix validation
	opts := storage.BatchCreateOptions{
		SkipPrefixValidation: true, // Molecules use mol-* prefix
		OrphanHandling:       storage.OrphanAllow,
	}
	if err := s.CreateIssuesWithFullOptions(ctx, issues, actor, opts); err != nil {
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

// collectDependencies collects blocking dependencies from depends_on, needs, and waits_for fields.
// This is the shared implementation used by both DB-persisted and in-memory subgraph cooking.
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

	// Process needs field - simpler alias for sibling dependencies
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

	// Process waits_for field - fanout gate dependency
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

		// Source tracing info
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

// substituteFormulaVars substitutes {{variable}} placeholders in a formula.
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
	cookCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value), enables runtime mode")
	cookCmd.Flags().String("mode", "", "Cooking mode: compile (keep placeholders) or runtime (substitute vars)")

	rootCmd.AddCommand(cookCmd)
}
