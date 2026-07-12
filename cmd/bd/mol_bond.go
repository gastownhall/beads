package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
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
  --ephemeral  Force spawn as vapor (ephemeral, Ephemeral=true, excluded from Dolt sync via dolt_ignore)

Dynamic bonding (Christmas Ornament pattern):
  Use --ref to specify a custom child reference with variable substitution.
  This creates IDs like "parent.child-ref" instead of random hashes.

  Example:
    bd mol bond mol-worker-arm bd-patrol --ref arm-{{worker_name}} --var worker_name=ace
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
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runMolBond,
}

// BondResult holds the result of a bond operation
type BondResult struct {
	ResultID   string            `json:"result_id"`
	ResultType string            `json:"result_type"` // "compound_proto" or "compound_molecule"
	BondType   string            `json:"bond_type"`
	Spawned    int               `json:"spawned,omitempty"`    // Number of issues spawned (if proto was involved)
	IDMapping  map[string]string `json:"id_mapping,omitempty"` // Old ID -> new ID for spawned issues
}

func runMolBond(cmd *cobra.Command, args []string) error {
	CheckReadonly("mol bond")

	evt := metrics.NewCommandEvent("mol-bond")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	if store == nil {
		return HandleErrorRespectJSON("no database connection")
	}

	bondType, _ := cmd.Flags().GetString("type")
	customTitle, _ := cmd.Flags().GetString("as")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringArray("var")
	ephemeral, _ := cmd.Flags().GetBool("ephemeral")
	pour, _ := cmd.Flags().GetBool("pour")
	childRef, _ := cmd.Flags().GetString("ref")

	if ephemeral && pour {
		return HandleErrorRespectJSON("cannot use both --ephemeral and --pour")
	}

	if bondType != types.BondTypeSequential && bondType != types.BondTypeParallel && bondType != types.BondTypeConditional {
		return HandleErrorRespectJSON("invalid bond type '%s', must be: sequential, parallel, or conditional", bondType)
	}

	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return HandleErrorRespectJSON("invalid variable format '%s', expected 'key=value'", v)
		}
		vars[parts[0]] = parts[1]
	}

	discA, err := discoverMolBondOperand(ctx, store, args[0])
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer discA.Close()
	discB, err := discoverMolBondOperand(ctx, store, args[1])
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer discB.Close()

	targetID, targetStoreKey, err := validateMolBondHomes(store, discA, discB)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// Dry-run shares the same read-only discovery and store-policy validation
	// as execution, but never reopens the accepted target writable or cooks.
	if dryRun {
		issueA, formulaA := discA.issue, discA.formula
		issueB, formulaB := discB.issue, discB.formula

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
		return nil
	}

	// Close read-only discovery handles before opening the single accepted home
	// writable. Unsupported cross-store operations return above without any
	// writable foreign-store open or its migration/open-time side effects.
	discA.Close()
	discB.Close()
	activeStore := store
	closeActiveStore := func() {}
	if targetID != "" && targetStoreKey != storeIdentityKey(store) {
		rr, routeErr := resolveAndGetIssueForMutation(ctx, store, targetID)
		if routeErr != nil {
			return HandleErrorRespectJSON("%v", routeErr)
		}
		if rr.MutationForbidden {
			rr.Close()
			return HandleErrorRespectJSON("cannot bond issue %s: contributor auto-routing forbids mutation; run the bond from the project that owns the issue", targetID)
		}
		activeStore = rr.Store
		closeActiveStore = rr.Close
	}
	defer closeActiveStore()

	opA, err := materializeMolBondOperand(ctx, activeStore, discA, vars)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	opB, err := materializeMolBondOperand(ctx, activeStore, discB, vars)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// No cleanup needed - in-memory subgraphs don't pollute the DB
	subgraphA, cookedA := opA.subgraph, opA.cooked
	subgraphB, cookedB := opB.subgraph, opB.cooked
	issueA := subgraphA.Root
	issueB := subgraphB.Root
	idA := issueA.ID
	idB := issueB.ID

	// Determine operand types
	aIsProto := issueA.IsTemplate || cookedA
	bIsProto := issueB.IsTemplate || cookedB

	// Dispatch based on operand types
	// All operations use activeStore; wisp flag determines ephemeral vs persistent
	var result *BondResult
	switch {
	case aIsProto && bIsProto:
		// Compound protos are templates - always persistent
		// Note: Proto+proto bonding from formulas is a DB operation, not in-memory
		result, err = bondProtoProto(ctx, activeStore, issueA, issueB, bondType, customTitle, actor)
	case aIsProto && !bIsProto:
		// Pass subgraph directly if cooked from formula
		if cookedA {
			result, err = bondProtoMolWithSubgraph(ctx, activeStore, subgraphA, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		} else {
			result, err = bondProtoMol(ctx, activeStore, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		}
	case !aIsProto && bIsProto:
		// Pass subgraph directly if cooked from formula
		if cookedB {
			result, err = bondProtoMolWithSubgraph(ctx, activeStore, subgraphB, issueB, issueA, bondType, vars, childRef, actor, ephemeral, pour)
		} else {
			result, err = bondMolProto(ctx, activeStore, issueA, issueB, bondType, vars, childRef, actor, ephemeral, pour)
		}
	default:
		result, err = bondMolMol(ctx, activeStore, issueA, issueB, bondType, actor)
	}

	if err != nil {
		return HandleErrorRespectJSON("bonding: %v", err)
	}

	if jsonOutput {
		return outputJSON(result)
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
	return nil
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
func bondProtoProto(ctx context.Context, s storage.DoltStorage, protoA, protoB *types.Issue, bondType, customTitle, actorName string) (*BondResult, error) {
	// Create compound proto: a new root that references both protos as children
	// The compound root will be a new issue that ties them together
	compoundTitle := fmt.Sprintf("Compound: %s + %s", protoA.Title, protoB.Title)
	if customTitle != "" {
		compoundTitle = customTitle
	}

	var compoundID string
	err := transact(ctx, s, fmt.Sprintf("bd: bond protos %s + %s", protoA.ID, protoB.ID), func(tx storage.Transaction) error {
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
func bondProtoMol(ctx context.Context, s storage.DoltStorage, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	return bondProtoMolWithSubgraph(ctx, s, nil, proto, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
}

// bondProtoMolWithSubgraph is the internal implementation that accepts a pre-loaded subgraph.
func bondProtoMolWithSubgraph(ctx context.Context, s storage.DoltStorage, protoSubgraph *TemplateSubgraph, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
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

	// Determine dependency type for attachment
	// Sequential: use blocks (B runs after A completes)
	// Conditional: use conditional-blocks (B runs only if A fails)
	// Parallel: use parent-child (organizational, no blocking)
	var depType types.DependencyType
	switch bondType {
	case types.BondTypeSequential:
		depType = types.DepBlocks
	case types.BondTypeConditional:
		depType = types.DepConditionalBlocks
	default:
		depType = types.DepParentChild
	}

	// Build CloneOptions for spawning
	// AttachToID ensures spawn + attach happen in a single transaction (bd-wvplu)
	opts := CloneOptions{
		Vars:          vars,
		Actor:         actorName,
		Ephemeral:     makeEphemeral,
		AttachToID:    mol.ID,
		AttachDepType: depType,
	}

	// Dynamic bonding: use custom IDs if childRef is provided
	if childRef != "" {
		opts.ParentID = mol.ID
		opts.ChildRef = childRef
	}

	// Spawn the proto and atomically attach to molecule
	spawnResult, err := spawnMoleculeWithOptions(ctx, s, subgraph, opts)
	if err != nil {
		return nil, fmt.Errorf("spawning and attaching proto: %w", err)
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
func bondMolProto(ctx context.Context, s storage.DoltStorage, mol, proto *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	// Same as bondProtoMol but with arguments swapped
	return bondProtoMol(ctx, s, proto, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
}

// wouldCreateCycle checks whether adding an edge (newDepID depends on newDependsOnID)
// would create a cycle in the dependency graph. It does a BFS from newDependsOnID
// following "depends on" edges; if newDepID is reachable, a cycle would be formed.
// Returns (hasCycle, cyclePath) where cyclePath shows the chain if found.
func wouldCreateCycle(ctx context.Context, s storage.DoltStorage, newDepID, newDependsOnID string) (bool, []string) {
	visited := map[string]bool{newDependsOnID: true}
	// parent tracks how we reached each node, for path reconstruction.
	parent := map[string]string{newDependsOnID: ""}
	queue := []string{newDependsOnID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		deps, err := s.GetDependencyRecords(ctx, current)
		if err != nil {
			// If we can't query deps for a node, skip it rather than failing.
			continue
		}
		for _, dep := range deps {
			next := dep.DependsOnID
			if next == newDepID {
				// Found the cycle. Reconstruct the path.
				path := []string{newDepID}
				for node := current; node != ""; node = parent[node] {
					path = append(path, node)
				}
				// Reverse to get forward direction.
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				// Append newDepID again to show the cycle closing.
				path = append(path, newDepID)
				return true, path
			}
			if !visited[next] {
				visited[next] = true
				parent[next] = current
				queue = append(queue, next)
			}
		}
	}
	return false, nil
}

// bondMolMol bonds two molecules together.
// It checks for transitive cycles in the dependency graph (GH#2719).
func bondMolMol(ctx context.Context, s storage.DoltStorage, molA, molB *types.Issue, bondType, actorName string) (*BondResult, error) {
	// The bond creates: molB depends on molA (IssueID=molB.ID, DependsOnID=molA.ID).
	// A cycle exists if molA already transitively depends on molB, because then
	// adding molB→molA would close the loop: molA→...→molB→molA.
	hasCycle, cyclePath := wouldCreateCycle(ctx, s, molB.ID, molA.ID)
	if hasCycle {
		return nil, fmt.Errorf("cannot bond %s → %s: would create a transitive dependency cycle: %s",
			molA.ID, molB.ID, strings.Join(cyclePath, " → "))
	}

	err := transact(ctx, s, fmt.Sprintf("bd: bond molecules %s + %s", molA.ID, molB.ID), func(tx storage.Transaction) error {
		// Add dependency: B links to A
		// Sequential: use blocks (B runs after A completes)
		// Conditional: use conditional-blocks (B runs only if A fails)
		// Parallel: use parent-child (organizational, no blocking)
		// Note: Schema only allows one dependency per (issue_id, target) pair (target = typed column)
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

type molBondOperand struct {
	subgraph *TemplateSubgraph
	cooked   bool
}

// molBondDiscovery is the read-only first phase of operand resolution. Existing
// issues carry their logical store identity; formulas remain storeless.
type molBondDiscovery struct {
	operand           string
	issue             *types.Issue
	formula           string
	storeKey          string
	mutationForbidden bool
	closeFn           func()
}

func (d *molBondDiscovery) Close() {
	if d != nil && d.closeFn != nil {
		d.closeFn()
		d.closeFn = nil
	}
}

// discoverMolBondOperand resolves through read-only routing so unsupported
// cross-store operations can be rejected before any writable foreign-store
// open. Formula lookup remains issue-first.
func discoverMolBondOperand(ctx context.Context, localStore storage.DoltStorage, operand string) (*molBondDiscovery, error) {
	rr, err := resolveAndGetIssueWithRouting(ctx, localStore, operand)
	if err == nil {
		return &molBondDiscovery{
			operand:           operand,
			issue:             rr.Issue,
			storeKey:          storeIdentityKey(rr.Store),
			mutationForbidden: rr.MutationForbidden,
			closeFn:           rr.Close,
		}, nil
	}
	if !isNotFoundErr(err) {
		return nil, err
	}
	if !looksLikeFormulaName(operand) {
		return nil, fmt.Errorf("'%s' not found (not an issue ID or formula name)", operand)
	}
	parser := formula.NewParser()
	f, loadErr := parser.LoadByName(operand)
	if loadErr != nil {
		return nil, fmt.Errorf("'%s' not found as issue or formula: %w", operand, loadErr)
	}
	return &molBondDiscovery{operand: operand, formula: f.Formula}, nil
}

// validateMolBondHomes returns the issue ID and logical store key that pin the
// mutation. Formula-only bonds stay local. All checks happen on read-only
// discovery handles.
func validateMolBondHomes(localStore storage.DoltStorage, discoveries ...*molBondDiscovery) (string, string, error) {
	var targetID, targetKey string
	for _, d := range discoveries {
		if d == nil || d.issue == nil {
			continue
		}
		if d.mutationForbidden {
			return "", "", fmt.Errorf("cannot bond issue %s: contributor auto-routing forbids mutation; run the bond from the project that owns the issue", d.issue.ID)
		}
		if targetKey == "" {
			targetID, targetKey = d.issue.ID, d.storeKey
			continue
		}
		if d.storeKey != targetKey {
			return "", "", fmt.Errorf("cannot bond operands that live in different stores/rigs; run the bond from the rig that owns them")
		}
	}
	if targetKey == "" {
		return "", storeIdentityKey(localStore), nil
	}
	return targetID, targetKey, nil
}

// materializeMolBondOperand is the second phase: after one store home has been
// accepted and opened writable, load an issue/proto there or cook a formula in
// memory for the bond operation.
func materializeMolBondOperand(ctx context.Context, activeStore storage.DoltStorage, d *molBondDiscovery, vars map[string]string) (*molBondOperand, error) {
	if d == nil {
		return nil, fmt.Errorf("missing mol bond operand")
	}
	if d.issue == nil {
		subgraph, err := resolveAndCookFormulaWithVars(d.operand, nil, vars)
		if err != nil {
			return nil, fmt.Errorf("'%s' not found as issue or formula: %w", d.operand, err)
		}
		return &molBondOperand{subgraph: subgraph, cooked: true}, nil
	}
	rr, err := resolveAndGetFromStore(ctx, activeStore, d.issue.ID, false)
	if err != nil {
		return nil, err
	}
	issue := rr.Issue
	if isProto(issue) {
		subgraph, err := loadTemplateSubgraph(ctx, activeStore, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto subgraph '%s': %w", issue.ID, err)
		}
		return &molBondOperand{subgraph: subgraph}, nil
	}
	return &molBondOperand{
		subgraph: &TemplateSubgraph{
			Root:     issue,
			Issues:   []*types.Issue{issue},
			IssueMap: map[string]*types.Issue{issue.ID: issue},
		},
	}, nil
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
