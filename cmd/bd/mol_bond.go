package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molBondCmd = &cobra.Command{
	Use:     "bond <A> <B>",
	Aliases: []string{"fart"}, // Easter egg: molecules can produce gas
	Short:   "Bond two protos or molecules together",
	Long: `Bond two protos or molecules to create a compound.

The bond command is polymorphic - it handles different operand types:

  formula + formula → cook both, compound proto
  formula + proto   → cook formula, compound proto
  formula + mol     → cook formula, spawn and attach
  proto + proto     → compound proto (reusable template)
  proto + mol       → spawn proto, attach to molecule
  mol + proto       → spawn proto, attach to molecule
  mol + mol         → join into compound molecule

Formula names (e.g., mol-polecat-arm) are cooked inline as ephemeral protos.
This avoids needing pre-cooked proto beads in the database.

Bond types:
  sequential (default) - B runs after A completes
  parallel            - B runs alongside A
  conditional         - B runs only if A fails

Phase control:
  By default, spawned protos follow the target's phase:
  - Attaching to mol (Ephemeral=false) → spawns as persistent (Ephemeral=false)
  - Attaching to ephemeral issue (Ephemeral=true) → spawns as ephemeral (Ephemeral=true)

  Override with:
  --pour  Force spawn as liquid (persistent, Ephemeral=false)
  --ephemeral  Force spawn as vapor (ephemeral, Ephemeral=true, excluded from JSONL export)

Dynamic bonding (Christmas Ornament pattern):
  Use --ref to specify a custom child reference with variable substitution.
  This creates IDs like "parent.child-ref" instead of random hashes.

  Example:
    bd mol bond mol-polecat-arm bd-patrol --ref arm-{{polecat_name}} --var polecat_name=ace
    # Creates: bd-patrol.arm-ace (and children like bd-patrol.arm-ace.capture)

Use cases:
  - Found important bug during patrol? Use --pour to persist it
  - Need ephemeral diagnostic on persistent feature? Use --ephemeral
  - Spawning per-worker arms on a patrol? Use --ref for readable IDs

Examples:
  bd mol bond mol-feature mol-deploy                    # Compound proto
  bd mol bond mol-feature mol-deploy --type parallel    # Run in parallel
  bd mol bond mol-feature bd-abc123                     # Attach proto to molecule
  bd mol bond bd-abc123 bd-def456                       # Join two molecules
  bd mol bond mol-critical-bug wisp-patrol --pour       # Persist found bug
  bd mol bond mol-temp-check bd-feature --ephemeral          # Ephemeral diagnostic
  bd mol bond mol-arm bd-patrol --ref arm-{{name}} --var name=ace  # Dynamic child ID`,
	Args: cobra.ExactArgs(2),
	Run:  runMolBond,
}

// BondResult holds the result of a bond operation
type BondResult struct {
	ResultID   string            `json:"result_id"`
	ResultType string            `json:"result_type"` // "compound_proto" or "compound_molecule"
	BondType   string            `json:"bond_type"`
	Spawned    int               `json:"spawned,omitempty"`    // Number of issues spawned (if proto was involved)
	IDMapping  map[string]string `json:"id_mapping,omitempty"` // Old ID -> new ID for spawned issues
}

// runMolBond implements the polymorphic bond command
func runMolBond(cmd *cobra.Command, args []string) {
	CheckReadonly("mol bond")

	ctx := rootCtx

	// mol bond requires direct store access
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: mol bond requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol bond %s %s ...\n", args[0], args[1])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	bondType, _ := cmd.Flags().GetString("type")
	customTitle, _ := cmd.Flags().GetString("as")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringArray("var")
	ephemeral, _ := cmd.Flags().GetBool("ephemeral")
	pour, _ := cmd.Flags().GetBool("pour")
	childRef, _ := cmd.Flags().GetString("ref")

	// Validate phase flags are not both set
	if ephemeral && pour {
		fmt.Fprintf(os.Stderr, "Error: cannot use both --ephemeral and --pour\n")
		os.Exit(1)
	}

	// All issues go in the main store; ephemeral vs pour determines the Wisp flag
	// --ephemeral: create with Ephemeral=true (ephemeral, excluded from JSONL export)
	// --pour: create with Ephemeral=false (persistent, exported to JSONL)
	// Default: follow target's phase (ephemeral if target is ephemeral, otherwise persistent)

	// Validate bond type
	if bondType != types.BondTypeSequential && bondType != types.BondTypeParallel && bondType != types.BondTypeConditional {
		fmt.Fprintf(os.Stderr, "Error: invalid bond type '%s', must be: sequential, parallel, or conditional\n", bondType)
		os.Exit(1)
	}

	// Parse variables
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
			os.Exit(1)
		}
		vars[parts[0]] = parts[1]
	}

	// For dry-run, just check if operands can be resolved (don't cook)
	if dryRun {
		issueA, formulaA, err := resolveOrDescribe(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		issueB, formulaB, err := resolveOrDescribe(ctx, store, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		idA := args[0]
		idB := args[1]
		aIsProto := false
		bIsProto := false

		if issueA != nil {
			idA = issueA.ID
			aIsProto = isProto(issueA)
		}
		if issueB != nil {
			idB = issueB.ID
			bIsProto = isProto(issueB)
		}

		// Formulas are treated as protos for dry-run display
		if formulaA != "" {
			aIsProto = true
		}
		if formulaB != "" {
			bIsProto = true
		}

		fmt.Printf("\nDry run: bond %s + %s\n", idA, idB)
		if formulaA != "" {
			fmt.Printf("  A: %s (formula → will cook as proto)\n", formulaA)
		} else if issueA != nil {
			fmt.Printf("  A: %s (%s)\n", issueA.Title, operandType(aIsProto))
		}
		if formulaB != "" {
			fmt.Printf("  B: %s (formula → will cook as proto)\n", formulaB)
		} else if issueB != nil {
			fmt.Printf("  B: %s (%s)\n", issueB.Title, operandType(bIsProto))
		}
		fmt.Printf("  Bond type: %s\n", bondType)
		if ephemeral {
			fmt.Printf("  Phase override: vapor (--ephemeral)\n")
		} else if pour {
			fmt.Printf("  Phase override: liquid (--pour)\n")
		}
		if childRef != "" {
			resolvedRef := substituteVariables(childRef, vars)
			fmt.Printf("  Child ref: %s (resolved: %s)\n", childRef, resolvedRef)
		}
		if aIsProto && bIsProto {
			fmt.Printf("  Result: compound proto\n")
			if customTitle != "" {
				fmt.Printf("  Custom title: %s\n", customTitle)
			}
		} else if aIsProto || bIsProto {
			fmt.Printf("  Result: spawn proto, attach to molecule\n")
		} else {
			fmt.Printf("  Result: compound molecule\n")
		}
		if formulaA != "" || formulaB != "" {
			fmt.Printf("\n  Note: Cooked formulas are ephemeral and deleted after bonding.\n")
		}
		return
	}

	// Resolve both operands - can be issue IDs or formula names
	// Formula names are cooked inline to in-memory subgraphs
	// Pass vars for step condition filtering (bd-7zka.1)
	subgraphA, cookedA, err := resolveOrCookToSubgraph(ctx, store, args[0], vars)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	subgraphB, cookedB, err := resolveOrCookToSubgraph(ctx, store, args[1], vars)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// No cleanup needed - in-memory subgraphs don't pollute the DB
	issueA := subgraphA.Root
	issueB := subgraphB.Root
	idA := issueA.ID
	idB := issueB.ID

	// Determine operand types
	aIsProto := issueA.IsTemplate || cookedA
	bIsProto := issueB.IsTemplate || cookedB

	// Dispatch based on operand types
	// All operations use the main store; wisp flag determines ephemeral vs persistent
	var result *BondResult
	switch {
	case aIsProto && bIsProto:
		// Compound protos are templates - always persistent
		// Note: Proto+proto bonding from formulas is a DB operation, not in-memory
		result, err = bondProtoProto(ctx, store, issueA, issueB, bondType, customTitle, actor)
	case aIsProto && !bIsProto:
		// Pass subgraph directly if cooked from formula
		if cookedA {
			result, err = bondProtoMolWithSubgraph(ctx, store, subgraphA, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		} else {
			result, err = bondProtoMol(ctx, store, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		}
	case !aIsProto && bIsProto:
		// Pass subgraph directly if cooked from formula
		if cookedB {
			result, err = bondProtoMolWithSubgraph(ctx, store, subgraphB, issueB, issueA, bondType, vars, childRef, actor, ephemeral, pour)
		} else {
			result, err = bondMolProto(ctx, store, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		}
	default:
		result, err = bondMolMol(ctx, store, issueA, issueB, bondType, actor)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error bonding: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush - wisps are in main DB now, but JSONL export skips them
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Bonded: %s + %s\n", ui.RenderPass("✓"), idA, idB)
	fmt.Printf("  Result: %s (%s)\n", result.ResultID, result.ResultType)
	if result.Spawned > 0 {
		fmt.Printf("  Spawned: %d issues\n", result.Spawned)
	}
	if ephemeral {
		fmt.Printf("  Phase: vapor (ephemeral, Ephemeral=true)\n")
	} else if pour {
		fmt.Printf("  Phase: liquid (persistent, Ephemeral=false)\n")
	}
}

// isProto checks if an issue is a proto (has the template label)
func isProto(issue *types.Issue) bool {
	for _, label := range issue.Labels {
		if label == MoleculeLabel {
			return true
		}
	}
	return false
}

// operandType returns a human-readable type string
func operandType(isProtoIssue bool) string {
	if isProtoIssue {
		return "proto"
	}
	return "molecule"
}

// bondProtoProto bonds two protos to create a compound proto
func bondProtoProto(ctx context.Context, s storage.Storage, protoA, protoB *types.Issue, bondType, customTitle, actorName string) (*BondResult, error) {
	// Create compound proto: a new root that references both protos as children
	// The compound root will be a new issue that ties them together
	compoundTitle := fmt.Sprintf("Compound: %s + %s", protoA.Title, protoB.Title)
	if customTitle != "" {
		compoundTitle = customTitle
	}

	var compoundID string
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create compound root issue
		compound := &types.Issue{
			Title:       compoundTitle,
			Description: fmt.Sprintf("Compound proto bonding %s and %s", protoA.ID, protoB.ID),
			Status:      types.StatusOpen,
			Priority:    minPriority(protoA.Priority, protoB.Priority),
			IssueType:   types.TypeEpic,
			BondedFrom: []types.BondRef{
				{SourceID: protoA.ID, BondType: bondType, BondPoint: ""},
				{SourceID: protoB.ID, BondType: bondType, BondPoint: ""},
			},
		}
		if err := tx.CreateIssue(ctx, compound, actorName); err != nil {
			return fmt.Errorf("creating compound: %w", err)
		}
		compoundID = compound.ID

		// Add template label (labels are stored separately, not in issue table)
		if err := tx.AddLabel(ctx, compoundID, MoleculeLabel, actorName); err != nil {
			return fmt.Errorf("adding template label: %w", err)
		}

		// Add parent-child dependencies from compound to both proto roots
		depA := &types.Dependency{
			IssueID:     protoA.ID,
			DependsOnID: compoundID,
			Type:        types.DepParentChild,
		}
		if err := tx.AddDependency(ctx, depA, actorName); err != nil {
			return fmt.Errorf("linking proto A: %w", err)
		}

		depB := &types.Dependency{
			IssueID:     protoB.ID,
			DependsOnID: compoundID,
			Type:        types.DepParentChild,
		}
		if err := tx.AddDependency(ctx, depB, actorName); err != nil {
			return fmt.Errorf("linking proto B: %w", err)
		}

		// For sequential/conditional bonding, add blocking dependency: B blocks on A
		// Sequential: B runs after A completes (any outcome)
		// Conditional: B runs only if A fails
		if bondType == types.BondTypeSequential || bondType == types.BondTypeConditional {
			depType := types.DepBlocks
			if bondType == types.BondTypeConditional {
				depType = types.DepConditionalBlocks
			}
			seqDep := &types.Dependency{
				IssueID:     protoB.ID,
				DependsOnID: protoA.ID,
				Type:        depType,
			}
			if err := tx.AddDependency(ctx, seqDep, actorName); err != nil {
				return fmt.Errorf("adding sequence dep: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &BondResult{
		ResultID:   compoundID,
		ResultType: "compound_proto",
		BondType:   bondType,
		Spawned:    0,
	}, nil
}

// bondProtoMol bonds a proto to an existing molecule by spawning the proto.
// If childRef is provided, generates custom IDs like "parent.childref" (dynamic bonding).
// protoSubgraph can be nil if proto is from DB (will be loaded), or pre-loaded for formulas.
func bondProtoMol(ctx context.Context, s storage.Storage, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	return bondProtoMolWithSubgraph(ctx, s, nil, proto, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
}

// bondProtoMolWithSubgraph is the internal implementation that accepts a pre-loaded subgraph.
func bondProtoMolWithSubgraph(ctx context.Context, s storage.Storage, protoSubgraph *TemplateSubgraph, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	// Use provided subgraph or load from DB
	subgraph := protoSubgraph
	if subgraph == nil {
		var err error
		subgraph, err = loadTemplateSubgraph(ctx, s, proto.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto: %w", err)
		}
	}

	// Check for missing variables
	requiredVars := extractAllVariables(subgraph)
	var missingVars []string
	for _, v := range requiredVars {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		return nil, fmt.Errorf("missing required variables: %s (use --var)", strings.Join(missingVars, ", "))
	}

	// Determine ephemeral flag based on explicit flags or target's phase
	// --ephemeral: force ephemeral=true, --pour: force ephemeral=false, neither: follow target
	makeEphemeral := mol.Ephemeral // Default: follow target's phase
	if ephemeralFlag {
		makeEphemeral = true
	} else if pourFlag {
		makeEphemeral = false
	}

	// Build CloneOptions for spawning
	opts := CloneOptions{
		Vars:      vars,
		Actor:     actorName,
		Ephemeral: makeEphemeral,
	}

	// Dynamic bonding: use custom IDs if childRef is provided
	if childRef != "" {
		opts.ParentID = mol.ID
		opts.ChildRef = childRef
	}

	// Spawn the proto with options
	spawnResult, err := spawnMoleculeWithOptions(ctx, s, subgraph, opts)
	if err != nil {
		return nil, fmt.Errorf("spawning proto: %w", err)
	}

	// Attach spawned molecule to existing molecule
	err = s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add dependency from spawned root to molecule
		// Sequential: use blocks (B runs after A completes)
		// Conditional: use conditional-blocks (B runs only if A fails)
		// Parallel: use parent-child (organizational, no blocking)
		// Note: Schema only allows one dependency per (issue_id, depends_on_id) pair
		var depType types.DependencyType
		switch bondType {
		case types.BondTypeSequential:
			depType = types.DepBlocks
		case types.BondTypeConditional:
			depType = types.DepConditionalBlocks
		default:
			depType = types.DepParentChild
		}
		dep := &types.Dependency{
			IssueID:     spawnResult.NewEpicID,
			DependsOnID: mol.ID,
			Type:        depType,
		}
		return tx.AddDependency(ctx, dep, actorName)
		// Note: bonded_from field tracking is not yet supported by storage layer.
		// The dependency relationship captures the bonding semantics.
	})

	if err != nil {
		return nil, fmt.Errorf("attaching to molecule: %w", err)
	}

	return &BondResult{
		ResultID:   mol.ID,
		ResultType: "compound_molecule",
		BondType:   bondType,
		Spawned:    spawnResult.Created,
		IDMapping:  spawnResult.IDMapping,
	}, nil
}

// bondMolProto bonds a molecule to a proto (symmetric with bondProtoMol)
func bondMolProto(ctx context.Context, s storage.Storage, mol, proto *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	// Same as bondProtoMol but with arguments swapped
	return bondProtoMol(ctx, s, proto, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
}

// bondMolMol bonds two molecules together
func bondMolMol(ctx context.Context, s storage.Storage, molA, molB *types.Issue, bondType, actorName string) (*BondResult, error) {
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add dependency: B links to A
		// Sequential: use blocks (B runs after A completes)
		// Conditional: use conditional-blocks (B runs only if A fails)
		// Parallel: use parent-child (organizational, no blocking)
		// Note: Schema only allows one dependency per (issue_id, depends_on_id) pair
		var depType types.DependencyType
		switch bondType {
		case types.BondTypeSequential:
			depType = types.DepBlocks
		case types.BondTypeConditional:
			depType = types.DepConditionalBlocks
		default:
			depType = types.DepParentChild
		}
		dep := &types.Dependency{
			IssueID:     molB.ID,
			DependsOnID: molA.ID,
			Type:        depType,
		}
		if err := tx.AddDependency(ctx, dep, actorName); err != nil {
			return fmt.Errorf("linking molecules: %w", err)
		}

		// Note: bonded_from field tracking is not yet supported by storage layer.
		// The dependency relationship captures the bonding semantics.
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("linking molecules: %w", err)
	}

	return &BondResult{
		ResultID:   molA.ID,
		ResultType: "compound_molecule",
		BondType:   bondType,
	}, nil
}

// minPriority returns the higher priority (lower number)
func minPriority(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// resolveOrDescribe checks if an operand is an issue or formula without cooking.
// Used for dry-run mode. Returns (issue, formulaName, error).
// If it's an issue, issue is set. If it's a formula, formulaName is set.
func resolveOrDescribe(ctx context.Context, s storage.Storage, operand string) (*types.Issue, string, error) {
	// First, try to resolve as an existing issue
	id, err := utils.ResolvePartialID(ctx, s, operand)
	if err == nil {
		issue, err := s.GetIssue(ctx, id)
		if err == nil {
			return issue, "", nil
		}
	}

	// Not found as issue - check if it looks like a formula name
	if !looksLikeFormulaName(operand) {
		return nil, "", fmt.Errorf("'%s' not found (not an issue ID or formula name)", operand)
	}

	// Try to load the formula (but don't cook it)
	parser := formula.NewParser()
	f, err := parser.LoadByName(operand)
	if err != nil {
		return nil, "", fmt.Errorf("'%s' not found as issue or formula: %w", operand, err)
	}

	return nil, f.Formula, nil
}

// resolveOrCookToSubgraph tries to resolve an operand as an issue ID or formula.
// If it's an issue, loads the subgraph from DB. If it's a formula, cooks inline to subgraph.
// Returns the subgraph, whether it was cooked from formula, and any error.
//
// The vars parameter is used for step condition filtering (bd-7zka.1).
// This implements gt-4v1eo: formulas are cooked to in-memory subgraphs (no DB storage).
func resolveOrCookToSubgraph(ctx context.Context, s storage.Storage, operand string, vars map[string]string) (*TemplateSubgraph, bool, error) {
	// First, try to resolve as an existing issue
	id, err := utils.ResolvePartialID(ctx, s, operand)
	if err == nil {
		issue, err := s.GetIssue(ctx, id)
		if err == nil {
			// Check if it's a proto (template)
			if isProto(issue) {
				subgraph, err := loadTemplateSubgraph(ctx, s, id)
				if err != nil {
					return nil, false, fmt.Errorf("loading proto subgraph '%s': %w", id, err)
				}
				return subgraph, false, nil
			}
			// It's a molecule, not a proto - wrap it as a single-issue subgraph
			return &TemplateSubgraph{
				Root:     issue,
				Issues:   []*types.Issue{issue},
				IssueMap: map[string]*types.Issue{issue.ID: issue},
			}, false, nil
		}
	}

	// Not found as issue - check if it looks like a formula name
	if !looksLikeFormulaName(operand) {
		return nil, false, fmt.Errorf("'%s' not found (not an issue ID or formula name)", operand)
	}

	// Try to cook formula inline to in-memory subgraph
	// Pass vars for step condition filtering (bd-7zka.1)
	subgraph, err := resolveAndCookFormulaWithVars(operand, nil, vars)
	if err != nil {
		return nil, false, fmt.Errorf("'%s' not found as issue or formula: %w", operand, err)
	}

	return subgraph, true, nil
}

// looksLikeFormulaName checks if an operand looks like a formula name.
// Formula names typically start with "mol-" or contain ".formula" patterns.
func looksLikeFormulaName(operand string) bool {
	// Common formula prefixes
	if strings.HasPrefix(operand, "mol-") {
		return true
	}
	// Formula file references
	if strings.Contains(operand, ".formula") {
		return true
	}
	// If it contains a path separator, might be a formula path
	if strings.Contains(operand, "/") || strings.Contains(operand, "\\") {
		return true
	}
	return false
}

func init() {
	molBondCmd.Flags().String("type", types.BondTypeSequential, "Bond type: sequential, parallel, or conditional")
	molBondCmd.Flags().String("as", "", "Custom title for compound proto (proto+proto only)")
	molBondCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molBondCmd.Flags().StringArray("var", []string{}, "Variable substitution for spawned protos (key=value)")
	molBondCmd.Flags().Bool("ephemeral", false, "Force spawn as vapor (ephemeral, Ephemeral=true)")
	molBondCmd.Flags().Bool("pour", false, "Force spawn as liquid (persistent, Ephemeral=false)")
	molBondCmd.Flags().String("ref", "", "Custom child reference with {{var}} substitution (e.g., arm-{{polecat_name}})")

	molCmd.AddCommand(molBondCmd)
}
