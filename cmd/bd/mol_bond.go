package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
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

  proto + proto → compound proto (reusable template)
  proto + mol   → spawn proto, attach to molecule
  mol + proto   → spawn proto, attach to molecule
  mol + mol     → join into compound molecule

Bond types:
  sequential (default) - B runs after A completes
  parallel            - B runs alongside A
  conditional         - B runs only if A fails

Phase control:
  By default, spawned protos follow the target's phase:
  - Attaching to mol → spawns as mol (liquid)
  - Attaching to wisp → spawns as wisp (vapor)

  Override with:
  --pour  Force spawn as liquid (persistent), even when attaching to wisp
  --wisp  Force spawn as vapor (ephemeral), even when attaching to mol

Dynamic bonding (Christmas Ornament pattern):
  Use --ref to specify a custom child reference with variable substitution.
  This creates IDs like "parent.child-ref" instead of random hashes.

  Example:
    bd mol bond mol-polecat-arm bd-patrol --ref arm-{{polecat_name}} --var polecat_name=ace
    # Creates: bd-patrol.arm-ace (and children like bd-patrol.arm-ace.capture)

Use cases:
  - Found important bug during patrol? Use --pour to persist it
  - Need ephemeral diagnostic on persistent feature? Use --wisp
  - Spawning per-worker arms on a patrol? Use --ref for readable IDs

Examples:
  bd mol bond mol-feature mol-deploy                    # Compound proto
  bd mol bond mol-feature mol-deploy --type parallel    # Run in parallel
  bd mol bond mol-feature bd-abc123                     # Attach proto to molecule
  bd mol bond bd-abc123 bd-def456                       # Join two molecules
  bd mol bond mol-critical-bug wisp-patrol --pour       # Persist found bug
  bd mol bond mol-temp-check bd-feature --wisp          # Ephemeral diagnostic
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
	varFlags, _ := cmd.Flags().GetStringSlice("var")
	wisp, _ := cmd.Flags().GetBool("wisp")
	pour, _ := cmd.Flags().GetBool("pour")
	childRef, _ := cmd.Flags().GetString("ref")

	// Validate phase flags are not both set
	if wisp && pour {
		fmt.Fprintf(os.Stderr, "Error: cannot use both --wisp and --pour\n")
		os.Exit(1)
	}

	// Determine which store to use for spawning
	// Default: follow target's phase. Override with --wisp or --pour.
	targetStore := store
	if wisp {
		// Explicit --wisp: use wisp storage
		wispStore, err := beads.NewWispStorage(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open wisp storage: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = wispStore.Close() }()
		targetStore = wispStore

		// Ensure wisp directory is gitignored
		if err := beads.EnsureWispGitignore(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not update .gitignore: %v\n", err)
		}
	}
	// Note: --pour means use permanent storage (which is the default targetStore)

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

	// Resolve both IDs
	idA, err := utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: '%s' not found\n", args[0])
		os.Exit(1)
	}
	idB, err := utils.ResolvePartialID(ctx, store, args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: '%s' not found\n", args[1])
		os.Exit(1)
	}

	// Load both issues
	issueA, err := store.GetIssue(ctx, idA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", idA, err)
		os.Exit(1)
	}
	issueB, err := store.GetIssue(ctx, idB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", idB, err)
		os.Exit(1)
	}

	// Determine operand types
	aIsProto := isProto(issueA)
	bIsProto := isProto(issueB)

	if dryRun {
		fmt.Printf("\nDry run: bond %s + %s\n", idA, idB)
		fmt.Printf("  A: %s (%s)\n", issueA.Title, operandType(aIsProto))
		fmt.Printf("  B: %s (%s)\n", issueB.Title, operandType(bIsProto))
		fmt.Printf("  Bond type: %s\n", bondType)
		if wisp {
			fmt.Printf("  Phase override: vapor (--wisp)\n")
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
			if wisp || pour {
				fmt.Printf("  Note: phase flags ignored for proto+proto (templates stay in permanent storage)\n")
			}
			if childRef != "" {
				fmt.Printf("  Note: --ref ignored for proto+proto (use when bonding to molecule)\n")
			}
		} else if aIsProto || bIsProto {
			fmt.Printf("  Result: spawn proto, attach to molecule\n")
			if !wisp && !pour {
				fmt.Printf("  Phase: follows target's phase\n")
			}
			if childRef != "" {
				// Determine parent molecule
				parentID := idB
				if bIsProto {
					parentID = idA
				}
				resolvedRef := substituteVariables(childRef, vars)
				fmt.Printf("  Bonded ID: %s.%s\n", parentID, resolvedRef)
			}
		} else {
			fmt.Printf("  Result: compound molecule\n")
			if childRef != "" {
				fmt.Printf("  Note: --ref ignored for mol+mol (use when bonding proto to molecule)\n")
			}
		}
		return
	}

	// Dispatch based on operand types
	// Note: proto+proto creates templates (permanent storage), others use targetStore
	var result *BondResult
	switch {
	case aIsProto && bIsProto:
		// Compound protos are templates - always use permanent storage
		result, err = bondProtoProto(ctx, store, issueA, issueB, bondType, customTitle, actor)
	case aIsProto && !bIsProto:
		result, err = bondProtoMol(ctx, targetStore, issueA, issueB, bondType, vars, childRef, actor)
	case !aIsProto && bIsProto:
		result, err = bondMolProto(ctx, targetStore, issueA, issueB, bondType, vars, childRef, actor)
	default:
		result, err = bondMolMol(ctx, targetStore, issueA, issueB, bondType, actor)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error bonding: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush (only for non-wisp, wisps don't sync)
	if !wisp {
		markDirtyAndScheduleFlush()
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Bonded: %s + %s\n", ui.RenderPass("✓"), idA, idB)
	fmt.Printf("  Result: %s (%s)\n", result.ResultID, result.ResultType)
	if result.Spawned > 0 {
		fmt.Printf("  Spawned: %d issues\n", result.Spawned)
	}
	if wisp {
		fmt.Printf("  Phase: vapor (ephemeral in .beads-wisp/)\n")
	} else if pour {
		fmt.Printf("  Phase: liquid (persistent in .beads/)\n")
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
				{ProtoID: protoA.ID, BondType: bondType, BondPoint: ""},
				{ProtoID: protoB.ID, BondType: bondType, BondPoint: ""},
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
		// Conditional: B runs only if A fails (bd-kzda)
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
func bondProtoMol(ctx context.Context, s storage.Storage, proto, mol *types.Issue, bondType string, vars map[string]string, childRef string, actorName string) (*BondResult, error) {
	// Load proto subgraph
	subgraph, err := loadTemplateSubgraph(ctx, s, proto.ID)
	if err != nil {
		return nil, fmt.Errorf("loading proto: %w", err)
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

	// Build CloneOptions for spawning
	opts := CloneOptions{
		Vars:  vars,
		Actor: actorName,
		Wisp:  true, // wisp by default for molecule execution - bd-2vh3
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
		// Conditional: use conditional-blocks (B runs only if A fails) (bd-kzda)
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
func bondMolProto(ctx context.Context, s storage.Storage, mol, proto *types.Issue, bondType string, vars map[string]string, childRef string, actorName string) (*BondResult, error) {
	// Same as bondProtoMol but with arguments swapped
	return bondProtoMol(ctx, s, proto, mol, bondType, vars, childRef, actorName)
}

// bondMolMol bonds two molecules together
func bondMolMol(ctx context.Context, s storage.Storage, molA, molB *types.Issue, bondType, actorName string) (*BondResult, error) {
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add dependency: B links to A
		// Sequential: use blocks (B runs after A completes)
		// Conditional: use conditional-blocks (B runs only if A fails) (bd-kzda)
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

func init() {
	molBondCmd.Flags().String("type", types.BondTypeSequential, "Bond type: sequential, parallel, or conditional")
	molBondCmd.Flags().String("as", "", "Custom title for compound proto (proto+proto only)")
	molBondCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molBondCmd.Flags().StringSlice("var", []string{}, "Variable substitution for spawned protos (key=value)")
	molBondCmd.Flags().Bool("wisp", false, "Force spawn as vapor (ephemeral in .beads-wisp/)")
	molBondCmd.Flags().Bool("pour", false, "Force spawn as liquid (persistent in .beads/)")
	molBondCmd.Flags().String("ref", "", "Custom child reference with {{var}} substitution (e.g., arm-{{polecat_name}})")

	molCmd.AddCommand(molBondCmd)
}
