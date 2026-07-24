package main

import (
	"context"
	"errors"
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

	in, err := gatherMolBondInput(cmd, args)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if usesProxiedServer() {
		return runMolBondProxiedServer(rootCtx, in)
	}

	ctx := rootCtx

	if store == nil {
		return HandleErrorRespectJSON("no database connection")
	}

	if in.dryRun {
		issueA, formulaA, err := resolveOrDescribeWithRouting(ctx, store, in.argA, in.vars)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		issueB, formulaB, err := resolveOrDescribeWithRouting(ctx, store, in.argB, in.vars)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		renderMolBondDryRun(in, issueA, formulaA, issueB, formulaB)
		return nil
	}

	ops, cleanup, err := resolveMolBondOperands(ctx, store, [2]string{in.argA, in.argB}, in.vars)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer cleanup()
	opA, opB := ops[0], ops[1]

	// The bond mutation runs against the store that owns the operands. When an
	// operand was resolved via routing (e.g. a prefix-routed rig bead), that is
	// the routed store, not the local one — otherwise the bond would write to
	// the wrong database or fail on a cross-store reference (mirrors #3609).
	activeStore, err := pickBondStore(store, opA, opB)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// No cleanup needed - in-memory subgraphs don't pollute the DB
	subgraphA, cookedA := opA.subgraph, opA.cooked
	subgraphB, cookedB := opB.subgraph, opB.cooked
	issueA := subgraphA.Root
	issueB := subgraphB.Root
	aIsProto := issueA.IsTemplate || cookedA
	bIsProto := issueB.IsTemplate || cookedB

	// Dispatch based on operand types
	// All operations use activeStore; phase flags determine ephemeral vs persistent.
	var result *BondResult
	switch {
	case aIsProto && bIsProto:
		// Compound protos are templates - always persistent
		// Note: Proto+proto bonding from formulas is a DB operation, not in-memory
		result, err = bondProtoProto(ctx, activeStore, issueA, issueB, in.bondType, in.customTitle, actor)
	case aIsProto && !bIsProto:
		if cookedA {
			result, err = bondProtoMolWithSubgraph(ctx, activeStore, subgraphA, issueA, issueB, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		} else {
			result, err = bondProtoMol(ctx, activeStore, issueA, issueB, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		}
	case !aIsProto && bIsProto:
		if cookedB {
			result, err = bondProtoMolWithSubgraph(ctx, activeStore, subgraphB, issueB, issueA, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		} else {
			result, err = bondMolProto(ctx, activeStore, issueA, issueB, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		}
	default:
		result, err = bondMolMol(ctx, activeStore, issueA, issueB, in.bondType, actor)
	}
	if err != nil {
		return HandleErrorRespectJSON("bonding: %v", err)
	}

	return renderMolBondResult(result, issueA.ID, issueB.ID, in.ephemeral, in.pour)
}

type molBondInput struct {
	argA        string
	argB        string
	bondType    string
	customTitle string
	dryRun      bool
	vars        map[string]string
	ephemeral   bool
	pour        bool
	childRef    string
}

func gatherMolBondInput(cmd *cobra.Command, args []string) (molBondInput, error) {
	in := molBondInput{argA: args[0], argB: args[1]}
	in.bondType, _ = cmd.Flags().GetString("type")
	in.customTitle, _ = cmd.Flags().GetString("as")
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")
	in.ephemeral, _ = cmd.Flags().GetBool("ephemeral")
	in.pour, _ = cmd.Flags().GetBool("pour")
	in.childRef, _ = cmd.Flags().GetString("ref")

	if in.ephemeral && in.pour {
		return in, fmt.Errorf("cannot use both --ephemeral and --pour")
	}
	if in.bondType != types.BondTypeSequential && in.bondType != types.BondTypeParallel && in.bondType != types.BondTypeConditional {
		return in, fmt.Errorf("invalid bond type '%s', must be: sequential, parallel, or conditional", in.bondType)
	}

	varFlags, _ := cmd.Flags().GetStringArray("var")
	vars, err := parseVarFlags(varFlags)
	if err != nil {
		return in, err
	}
	in.vars = vars
	return in, nil
}

func renderMolBondDryRun(in molBondInput, issueA *types.Issue, formulaA string, issueB *types.Issue, formulaB string) {
	idA := in.argA
	idB := in.argB
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
	fmt.Printf("  Bond type: %s\n", in.bondType)
	if in.ephemeral {
		fmt.Printf("  Phase override: vapor (--ephemeral)\n")
	} else if in.pour {
		fmt.Printf("  Phase override: liquid (--pour)\n")
	}
	if in.childRef != "" {
		resolvedRef := substituteVariables(in.childRef, in.vars)
		fmt.Printf("  Child ref: %s (resolved: %s)\n", in.childRef, resolvedRef)
	}
	if aIsProto && bIsProto {
		fmt.Printf("  Result: compound proto\n")
		if in.customTitle != "" {
			fmt.Printf("  Custom title: %s\n", in.customTitle)
		}
	} else if aIsProto || bIsProto {
		fmt.Printf("  Result: spawn proto, attach to molecule\n")
	} else {
		fmt.Printf("  Result: compound molecule\n")
	}
	if formulaA != "" || formulaB != "" {
		fmt.Printf("\n  Note: Cooked formulas are ephemeral and deleted after bonding.\n")
	}
}

func renderMolBondResult(result *BondResult, idA, idB string, ephemeral, pour bool) error {
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
	var result *BondResult
	err := transact(ctx, s, fmt.Sprintf("bd: bond protos %s + %s", protoA.ID, protoB.ID), func(tx storage.Transaction) error {
		r, err := bondProtoProtoInto(ctx, storeMolWriter{DoltStorage: s, tx: tx}, protoA, protoB, bondType, customTitle, actorName)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func bondProtoProtoInto(ctx context.Context, w molWriter, protoA, protoB *types.Issue, bondType, customTitle, actorName string) (*BondResult, error) {
	// Create compound proto: a new root that references both protos as children
	// The compound root will be a new issue that ties them together
	compoundTitle := fmt.Sprintf("Compound: %s + %s", protoA.Title, protoB.Title)
	if customTitle != "" {
		compoundTitle = customTitle
	}

	// Create compound root issue. The molecule label travels IN the create
	// rather than in a write of its own: a create decides which table the row
	// goes to, and carrying the labels with it means one decision places both.
	// The separate write this replaces was made through molWriter.AddLabel,
	// whose two implementations each resolved the plane again by a different
	// predicate, so the row and its label were free to disagree.
	compound := &types.Issue{
		Title:       compoundTitle,
		Description: fmt.Sprintf("Compound proto bonding %s and %s", protoA.ID, protoB.ID),
		Status:      types.StatusOpen,
		Priority:    minPriority(protoA.Priority, protoB.Priority),
		IssueType:   types.TypeEpic,
		Labels:      []string{MoleculeLabel},
		BondedFrom: []types.BondRef{
			{SourceID: protoA.ID, BondType: bondType, BondPoint: ""},
			{SourceID: protoB.ID, BondType: bondType, BondPoint: ""},
		},
	}
	if err := w.CreateIssue(ctx, compound, actorName); err != nil {
		return nil, fmt.Errorf("creating compound: %w", err)
	}
	compoundID := compound.ID

	// Add parent-child dependencies from compound to both proto roots
	depA := &types.Dependency{
		IssueID:     protoA.ID,
		DependsOnID: compoundID,
		Type:        types.DepParentChild,
	}
	if err := w.AddDependency(ctx, depA, actorName); err != nil {
		return nil, fmt.Errorf("linking proto A: %w", err)
	}

	depB := &types.Dependency{
		IssueID:     protoB.ID,
		DependsOnID: compoundID,
		Type:        types.DepParentChild,
	}
	if err := w.AddDependency(ctx, depB, actorName); err != nil {
		return nil, fmt.Errorf("linking proto B: %w", err)
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
		if err := w.AddDependency(ctx, seqDep, actorName); err != nil {
			return nil, fmt.Errorf("adding sequence dep: %w", err)
		}
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
	if protoSubgraph == nil {
		var err error
		protoSubgraph, err = loadTemplateSubgraph(ctx, s, proto.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto: %w", err)
		}
	}
	opts, err := buildAttachCloneOpts(protoSubgraph, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
	if err != nil {
		return nil, err
	}
	spawnResult, err := cloneSubgraph(ctx, s, protoSubgraph, opts)
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

// bondProtoMolAttachInto is bondProtoMolWithSubgraph's counterpart for
// callers that already have an open molWriter (the proxied-server duals),
// so spawn + attach happen inside the caller's own transaction.
func bondProtoMolAttachInto(ctx context.Context, w molWriter, protoSubgraph *TemplateSubgraph, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (*BondResult, error) {
	if protoSubgraph == nil {
		var err error
		protoSubgraph, err = loadTemplateSubgraph(ctx, w, proto.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto: %w", err)
		}
	}
	opts, err := buildAttachCloneOpts(protoSubgraph, mol, bondType, vars, childRef, actorName, ephemeralFlag, pourFlag)
	if err != nil {
		return nil, err
	}
	spawnResult, err := cloneSubgraphInto(ctx, w, protoSubgraph, opts)
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

func buildAttachCloneOpts(subgraph *TemplateSubgraph, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string, ephemeralFlag, pourFlag bool) (CloneOptions, error) {
	requiredVars := extractAllVariables(subgraph)
	var missingVars []string
	for _, v := range requiredVars {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		return CloneOptions{}, fmt.Errorf("missing required variables: %s (use --var)", strings.Join(missingVars, ", "))
	}

	makeEphemeral := mol.Ephemeral
	if ephemeralFlag {
		makeEphemeral = true
	} else if pourFlag {
		makeEphemeral = false
	}

	var depType types.DependencyType
	switch bondType {
	case types.BondTypeSequential:
		depType = types.DepBlocks
	case types.BondTypeConditional:
		depType = types.DepConditionalBlocks
	default:
		depType = types.DepParentChild
	}

	opts := CloneOptions{
		Vars:          vars,
		Actor:         actorName,
		Ephemeral:     makeEphemeral,
		AttachToID:    mol.ID,
		AttachDepType: depType,
	}
	if childRef != "" {
		opts.ParentID = mol.ID
		opts.ChildRef = childRef
	}
	return opts, nil
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
func wouldCreateCycle(ctx context.Context, s molReader, newDepID, newDependsOnID string) (bool, []string) {
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
	if hasCycle, cyclePath := wouldCreateCycle(ctx, s, molB.ID, molA.ID); hasCycle {
		return nil, fmt.Errorf("cannot bond %s → %s: would create a transitive dependency cycle: %s",
			molA.ID, molB.ID, strings.Join(cyclePath, " → "))
	}

	var result *BondResult
	err := transact(ctx, s, fmt.Sprintf("bd: bond molecules %s + %s", molA.ID, molB.ID), func(tx storage.Transaction) error {
		r, err := bondMolMolInto(ctx, storeMolWriter{DoltStorage: s, tx: tx}, molA, molB, bondType, actorName)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("linking molecules: %w", err)
	}
	return result, nil
}

func bondMolMolInto(ctx context.Context, w molWriter, molA, molB *types.Issue, bondType, actorName string) (*BondResult, error) {
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
	if err := w.AddDependency(ctx, dep, actorName); err != nil {
		return nil, fmt.Errorf("linking molecules: %w", err)
	}

	// Note: bonded_from field tracking is not yet supported by storage layer.
	// The dependency relationship captures the bonding semantics.
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

// molBondOperand is a resolved mol-bond operand together with the store that
// owns it. For an existing issue, store is whichever store resolution found the
// issue in (local or routed) and cooked is false. For a formula cooked inline,
// store is nil (the cooked protos are in-memory until the bond writes them) and
// cooked is true. Routed store handles are owned by the cleanup function
// returned from resolveMolBondOperands, not by the operand.
type molBondOperand struct {
	subgraph *TemplateSubgraph
	cooked   bool                // cooked inline from a formula (no owning store yet)
	store    storage.DoltStorage // store that owns the issue; nil for cooked formulas
	routed   bool                // issue was found via routing, not the local store
	writable bool                // false only for contributor auto-routed stores, which open read-only
}

// operandFromIssue wraps a resolved issue as a molBondOperand, loading the full
// proto subgraph when the issue is a proto and wrapping a plain molecule as a
// single-issue subgraph otherwise.
func operandFromIssue(ctx context.Context, s storage.DoltStorage, issue *types.Issue, routed, writable bool) (*molBondOperand, error) {
	if isProto(issue) {
		subgraph, err := loadTemplateSubgraph(ctx, s, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto subgraph '%s': %w", issue.ID, err)
		}
		return &molBondOperand{subgraph: subgraph, store: s, routed: routed, writable: writable}, nil
	}
	return &molBondOperand{
		subgraph: &TemplateSubgraph{
			Root:     issue,
			Issues:   []*types.Issue{issue},
			IssueMap: map[string]*types.Issue{issue.ID: issue},
		},
		store:    s,
		routed:   routed,
		writable: writable,
	}, nil
}

// resolveMolBondOperands resolves both mol-bond operands as existing issues or,
// failing that, inline formulas. It mirrors resolveCloseTargets: per operand the
// fallback order is the local store, then a routed store already opened for the
// other operand of this bond, then a prefix-routed rig via routes.jsonl (opened
// write-intent so the bond commits where the issue lives, #4141), then the
// shared contributor auto-routed store. Contributor auto-routed stores open
// read-only — they hydrate foreign projects that must never be mutated — so
// their operands are marked non-writable and pickBondStore rejects the bond
// before any mutation is attempted.
//
// Reusing an already-opened routed handle means two operands that live in the
// same routed rig share one store handle instead of opening the same database
// twice (and pickBondStore sees one identity, not a spurious cross-store pair).
//
// The caller must invoke cleanup() once when done; it releases every routed
// handle opened for this bond.
//
// The vars parameter is used for step condition filtering (bd-7zka.1).
// Formulas are cooked to in-memory subgraphs (gt-4v1eo, no DB storage).
func resolveMolBondOperands(ctx context.Context, localStore storage.DoltStorage, operands [2]string, vars map[string]string) ([2]*molBondOperand, func(), error) {
	type routedHandle struct {
		store    storage.DoltStorage
		writable bool
	}
	var handles []routedHandle
	var closers []func()
	cleanup := func() {
		for _, c := range closers {
			c()
		}
	}

	var sharedAuto storage.DoltStorage
	sharedAutoTried := false
	ensureSharedAuto := func() (storage.DoltStorage, error) {
		if sharedAuto != nil {
			return sharedAuto, nil
		}
		if sharedAutoTried {
			return nil, fmt.Errorf("no auto-routed store available")
		}
		sharedAutoTried = true
		rs, routed, err := openRoutedReadStore(ctx, localStore)
		if err != nil {
			return nil, err
		}
		if !routed {
			return nil, fmt.Errorf("no auto-routed store available")
		}
		sharedAuto = rs
		closers = append(closers, func() { _ = rs.Close() })
		return rs, nil
	}

	resolveOne := func(operand string) (*molBondOperand, error) {
		// Local first.
		if rr, err := resolveAndGetFromStore(ctx, localStore, operand, false); err == nil {
			return operandFromIssue(ctx, localStore, rr.Issue, false, true)
		} else if !isNotFoundErr(err) {
			return nil, err
		}
		// A routed store already opened for the other operand of this bond.
		for _, h := range handles {
			if rr, err := resolveAndGetFromStore(ctx, h.store, operand, true); err == nil {
				return operandFromIssue(ctx, h.store, rr.Issue, true, h.writable)
			}
		}
		// Write-intent prefix routing: the routed target opens writable so the
		// bond commits on the target head, like bd close (#4141, #3608/#3609).
		if rr, err := resolveViaPrefixRoutingWithAccess(ctx, operand, true); err == nil {
			handles = append(handles, routedHandle{store: rr.Store, writable: true})
			closers = append(closers, rr.Close)
			return operandFromIssue(ctx, rr.Store, rr.Issue, true, true)
		}
		// Contributor auto-routing uses one shared read-only store (GH#2345).
		if rs, rerr := ensureSharedAuto(); rerr == nil {
			if rr, err := resolveAndGetFromStore(ctx, rs, operand, true); err == nil {
				return operandFromIssue(ctx, rs, rr.Issue, true, false)
			}
		}
		// Not found as issue in any store — fall through to the formula registry.
		// resolveAndCookFormulaWithVars lets parser.LoadByName decide (it returns
		// "formula %q not found in search paths" for genuinely unknown names,
		// which is the right error). This matches the resolution behavior of
		// bd formula show / bd mol seed / bd mol pour / bd cook (#3874).
		subgraph, cookErr := resolveAndCookFormulaWithVars(operand, nil, vars)
		if cookErr != nil {
			return nil, fmt.Errorf("'%s' not found as issue or formula: %w", operand, cookErr)
		}
		return &molBondOperand{subgraph: subgraph, cooked: true, writable: true}, nil
	}

	opA, err := resolveOne(operands[0])
	if err != nil {
		cleanup()
		return [2]*molBondOperand{}, func() {}, err
	}
	opB, err := resolveOne(operands[1])
	if err != nil {
		cleanup()
		return [2]*molBondOperand{}, func() {}, err
	}
	return [2]*molBondOperand{opA, opB}, cleanup, nil
}

// pickBondStore returns the store the bond mutation must run against.
//
// Cooked-formula operands are in-memory and store-agnostic, so they impose no
// constraint. Each existing-issue operand pins the bond to the store that owns
// it. When both operands are existing issues owned by different stores the bond
// is a cross-store operation with no well-defined home, so it is rejected rather
// than silently written to the wrong database. When nothing pins a store (both
// operands are cooked formulas) the bond stays on the local store.
//
// A pinned store that opened read-only (a contributor auto-routed store) is
// rejected here, before any mutation is attempted, instead of surfacing a
// store-layer write refusal halfway through the bond.
func pickBondStore(localStore storage.DoltStorage, a, b *molBondOperand) (storage.DoltStorage, error) {
	active := localStore
	var pinnedBy *molBondOperand
	for _, op := range []*molBondOperand{a, b} {
		if op.cooked || op.store == nil {
			continue
		}
		if pinnedBy == nil {
			active = op.store
			pinnedBy = op
			continue
		}
		if !sameBondStore(op.store, active) {
			return nil, fmt.Errorf("cannot bond %s and %s: they live in different stores/rigs; run the bond from the rig that owns them",
				pinnedBy.subgraph.Root.ID, op.subgraph.Root.ID)
		}
	}
	if pinnedBy != nil && !pinnedBy.writable {
		return nil, fmt.Errorf("%s lives in the contributor auto-routed store, which opens read-only; bd mol bond cannot write there — run the bond from the project that owns it",
			pinnedBy.subgraph.Root.ID)
	}
	return active, nil
}

// sameBondStore reports whether two store handles refer to the same underlying
// database. Handle identity covers shared handles; the StoreLocator comparison
// covers separately-opened handles onto the same database (each route-open
// constructs a fresh store in server mode), which must not be rejected as a
// cross-store bond.
func sameBondStore(a, b storage.DoltStorage) bool {
	if a == b {
		return true
	}
	la, aok := a.(storage.StoreLocator)
	lb, bok := b.(storage.StoreLocator)
	if !aok || !bok {
		return false
	}
	// CLIDir pins the concrete database (path + database name); Path alone can
	// collide across databases sharing a server root.
	dirA, dirB := la.CLIDir(), lb.CLIDir()
	return dirA != "" && dirA == dirB
}

// resolveOrDescribeWithRouting checks if an operand is an issue or formula
// without cooking. It is the embedded/server-store dry-run resolver; the
// proxied-server path retains resolveOrDescribe over its transaction reader.
//
// Issue resolution uses the read-only routing fallback (local, then prefix-routed
// rig, then contributor auto-routing) so a dry run previews routed operands the
// same way the real bond resolves them.
func resolveOrDescribeWithRouting(ctx context.Context, localStore storage.DoltStorage, operand string, vars map[string]string) (*types.Issue, string, error) {
	rr, err := resolveAndGetIssueWithRouting(ctx, localStore, operand)
	if err == nil {
		issue := rr.Issue
		rr.Close()
		return issue, "", nil
	}
	if !isNotFoundErr(err) {
		return nil, "", err
	}

	// Not found as issue — fall through to the formula registry. parser.LoadByName
	// returns "formula %q not found in search paths" for genuinely unknown names,
	// which is the right error. This matches the resolution behavior of
	// bd formula show / bd mol seed / bd mol pour / bd cook.
	parser := formula.NewParser()
	f, err := parser.LoadByName(operand)
	if err != nil {
		return nil, "", fmt.Errorf("'%s' not found as issue or formula: %w", operand, err)
	}

	// A dry-run must fail the same way the real bond would: an enum/pattern/
	// provided-empty violation in --var values fails resolveOrCookToSubgraph,
	// so reporting "will be cooked" here would be a false preview.
	if err := formula.ValidateProvidedVars(f, vars); err != nil {
		return nil, "", err
	}

	return nil, f.Formula, nil
}

// resolveOrDescribe checks if an operand is an issue or formula without cooking.
// Used by proxied-server dry-run mode. Returns (issue, formulaName, error).
func resolveOrDescribe(ctx context.Context, s molReader, operand string, vars map[string]string) (*types.Issue, string, error) {
	id, err := utils.ResolvePartialID(ctx, s, operand)
	if err == nil {
		issue, err := s.GetIssue(ctx, id)
		if err == nil {
			return issue, "", nil
		}
	}

	parser := formula.NewParser()
	f, err := parser.LoadByName(operand)
	if err != nil {
		return nil, "", fmt.Errorf("'%s' not found as issue or formula: %w", operand, err)
	}

	// A dry-run must fail the same way the real bond would: an enum/pattern/
	// provided-empty violation in --var values fails resolveOrCookToSubgraph,
	// so reporting "will be cooked" here would be a false preview.
	if err := formula.ValidateProvidedVars(f, vars); err != nil {
		return nil, "", err
	}

	return nil, f.Formula, nil
}

// resolveOrCookToSubgraph tries to resolve an operand as an issue ID or formula.
// If it's an issue, loads the subgraph from DB. If it's a formula, cooks inline to subgraph.
// Returns the subgraph, whether it was cooked from formula, and any error.
//
// The vars parameter is used for step condition filtering (bd-7zka.1).
// This implements gt-4v1eo: formulas are cooked to in-memory subgraphs (no DB storage).
func resolveOrCookToSubgraph(ctx context.Context, s molReader, operand string, vars map[string]string) (*TemplateSubgraph, bool, error) {
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

	// Not found as issue — fall through to the formula registry. Same rationale
	// as resolveOrDescribe above: let the parser decide. Pass vars for step
	// condition filtering (bd-7zka.1).
	subgraph, err := resolveAndCookFormulaWithVars(operand, nil, vars)
	if err != nil {
		if errors.Is(err, formula.ErrVarValidation) {
			// Don't double-wrap: operand IS a formula, and the --var values
			// it was given fail enum/pattern/required-empty constraints,
			// which is a distinct condition from "not found".
			return nil, false, err
		}
		return nil, false, fmt.Errorf("'%s' not found as issue or formula: %w", operand, err)
	}

	return subgraph, true, nil
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
