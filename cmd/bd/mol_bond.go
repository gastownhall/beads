package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
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

Examples:
  bd mol bond mol-feature mol-deploy                    # Compound proto
  bd mol bond mol-feature mol-deploy --type parallel    # Run in parallel
  bd mol bond mol-feature bd-abc123                     # Attach proto to molecule
  bd mol bond bd-abc123 bd-def456                       # Join two molecules`,
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
	customID, _ := cmd.Flags().GetString("as")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringSlice("var")

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
		if aIsProto && bIsProto {
			fmt.Printf("  Result: compound proto\n")
			if customID != "" {
				fmt.Printf("  Custom ID: %s\n", customID)
			}
		} else if aIsProto || bIsProto {
			fmt.Printf("  Result: spawn proto, attach to molecule\n")
		} else {
			fmt.Printf("  Result: compound molecule\n")
		}
		return
	}

	// Dispatch based on operand types
	var result *BondResult
	switch {
	case aIsProto && bIsProto:
		result, err = bondProtoProto(ctx, store, issueA, issueB, bondType, customID, actor)
	case aIsProto && !bIsProto:
		result, err = bondProtoMol(ctx, store, issueA, issueB, bondType, vars, actor)
	case !aIsProto && bIsProto:
		result, err = bondMolProto(ctx, store, issueA, issueB, bondType, vars, actor)
	default:
		result, err = bondMolMol(ctx, store, issueA, issueB, bondType, actor)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error bonding: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
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
func bondProtoProto(ctx context.Context, s storage.Storage, protoA, protoB *types.Issue, bondType, customID, actorName string) (*BondResult, error) {
	// Create compound proto: a new root that references both protos as children
	// The compound root will be a new issue that ties them together
	compoundTitle := fmt.Sprintf("Compound: %s + %s", protoA.Title, protoB.Title)
	if customID != "" {
		compoundTitle = customID
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

		// For sequential bonding, add blocking dependency: B blocks on A
		if bondType == types.BondTypeSequential {
			seqDep := &types.Dependency{
				IssueID:     protoB.ID,
				DependsOnID: protoA.ID,
				Type:        types.DepBlocks,
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

// bondProtoMol bonds a proto to an existing molecule by spawning the proto
func bondProtoMol(ctx context.Context, s storage.Storage, proto, mol *types.Issue, bondType string, vars map[string]string, actorName string) (*BondResult, error) {
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

	// Spawn the proto (wisp by default for molecule execution - bd-2vh3)
	spawnResult, err := spawnMolecule(ctx, s, subgraph, vars, "", actorName, true)
	if err != nil {
		return nil, fmt.Errorf("spawning proto: %w", err)
	}

	// Attach spawned molecule to existing molecule
	err = s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add dependency from spawned root to molecule
		// For sequential: use blocks (captures workflow semantics)
		// For parallel/conditional: use parent-child (organizational)
		// Note: Schema only allows one dependency per (issue_id, depends_on_id) pair
		depType := types.DepParentChild
		if bondType == types.BondTypeSequential {
			depType = types.DepBlocks
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
func bondMolProto(ctx context.Context, s storage.Storage, mol, proto *types.Issue, bondType string, vars map[string]string, actorName string) (*BondResult, error) {
	// Same as bondProtoMol but with arguments swapped
	return bondProtoMol(ctx, s, proto, mol, bondType, vars, actorName)
}

// bondMolMol bonds two molecules together
func bondMolMol(ctx context.Context, s storage.Storage, molA, molB *types.Issue, bondType, actorName string) (*BondResult, error) {
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add dependency: B links to A
		// For sequential: use blocks (captures workflow semantics)
		// For parallel/conditional: use parent-child (organizational)
		// Note: Schema only allows one dependency per (issue_id, depends_on_id) pair
		depType := types.DepParentChild
		if bondType == types.BondTypeSequential {
			depType = types.DepBlocks
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

	molCmd.AddCommand(molBondCmd)
}
