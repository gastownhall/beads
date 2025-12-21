package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// Molecule commands - work templates for agent workflows
//
// Terminology:
//   - Proto: Uninstantiated template (easter egg: 'protomolecule' alias)
//   - Molecule: A spawned instance of a proto
//   - Spawn: Instantiate a proto, creating real issues from the template
//   - Bond: Polymorphic combine operation (proto+proto, proto+mol, mol+mol)
//   - Distill: Extract ad-hoc epic → reusable proto
//   - Compound: Result of bonding
//
// Usage:
//   bd mol catalog                        # List available protos
//   bd mol show <id>                      # Show proto/molecule structure
//   bd mol spawn <id> --var key=value     # Instantiate proto → molecule

// MoleculeLabel is the label used to identify molecules (templates)
// Molecules use the same label as templates - they ARE templates with workflow semantics
const MoleculeLabel = BeadsTemplateLabel

// MoleculeSubgraph is an alias for TemplateSubgraph
// Molecules and templates share the same subgraph structure
type MoleculeSubgraph = TemplateSubgraph

var molCmd = &cobra.Command{
	Use:     "mol",
	Aliases: []string{"protomolecule"}, // Easter egg for The Expanse fans
	Short:   "Molecule commands (work templates)",
	Long: `Manage molecules - work templates for agent workflows.

Protos are template epics with the "template" label. They define a DAG of work
that can be spawned to create real issues (molecules).

The molecule metaphor:
  - A proto is an uninstantiated template (reusable work pattern)
  - Spawning creates a molecule (real issues) from the proto
  - Variables ({{key}}) are substituted during spawning
  - Bonding combines protos or molecules into compounds
  - Distilling extracts a proto from an ad-hoc epic

Commands:
  catalog  List available protos
  show     Show proto/molecule structure and variables
  spawn    Instantiate a proto → molecule
  bond     Polymorphic combine: proto+proto, proto+mol, mol+mol
  run      Spawn + assign + pin for durable execution
  distill  Extract proto from ad-hoc epic (reverse of spawn)`,
}

var molCatalogCmd = &cobra.Command{
	Use:     "catalog",
	Aliases: []string{"list", "ls"},
	Short:   "List available molecules",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		var molecules []*types.Issue

		if daemonClient != nil {
			resp, err := daemonClient.List(&rpc.ListArgs{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecules: %v\n", err)
				os.Exit(1)
			}
			var allIssues []*types.Issue
			if err := json.Unmarshal(resp.Data, &allIssues); err == nil {
				for _, issue := range allIssues {
					for _, label := range issue.Labels {
						if label == MoleculeLabel {
							molecules = append(molecules, issue)
							break
						}
					}
				}
			}
		} else if store != nil {
			var err error
			molecules, err = store.GetIssuesByLabel(ctx, MoleculeLabel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecules: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(molecules)
			return
		}

		if len(molecules) == 0 {
			fmt.Println("No protos available.")
			fmt.Println("\nTo create a proto:")
			fmt.Println("  1. Create an epic with child issues")
			fmt.Println("  2. Add the 'template' label: bd label add <epic-id> template")
			fmt.Println("  3. Use {{variable}} placeholders in titles/descriptions")
			fmt.Println("\nTo spawn (instantiate) a molecule from a proto:")
			fmt.Println("  bd mol spawn <id> --var key=value")
			return
		}

		fmt.Printf("%s\n", ui.RenderPass("Protos (for bd mol spawn):"))
		for _, mol := range molecules {
			vars := extractVariables(mol.Title + " " + mol.Description)
			varStr := ""
			if len(vars) > 0 {
				varStr = fmt.Sprintf(" (vars: %s)", strings.Join(vars, ", "))
			}
			fmt.Printf("  %s: %s%s\n", ui.RenderAccent(mol.ID), mol.Title, varStr)
		}
		fmt.Println()
	},
}

var molShowCmd = &cobra.Command{
	Use:   "show <molecule-id>",
	Short: "Show molecule details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// mol show requires direct store access for subgraph loading
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: mol show requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol show %s\n", args[0])
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}

		moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: molecule '%s' not found\n", args[0])
			os.Exit(1)
		}

		subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
			os.Exit(1)
		}

		showMolecule(subgraph)
	},
}

func showMolecule(subgraph *MoleculeSubgraph) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"root":         subgraph.Root,
			"issues":       subgraph.Issues,
			"dependencies": subgraph.Dependencies,
			"variables":    extractAllVariables(subgraph),
		})
		return
	}

	fmt.Printf("\n%s Molecule: %s\n", ui.RenderAccent("🧪"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", subgraph.Root.ID)
	fmt.Printf("   Steps: %d\n", len(subgraph.Issues))

	vars := extractAllVariables(subgraph)
	if len(vars) > 0 {
		fmt.Printf("\n%s Variables:\n", ui.RenderWarn("📝"))
		for _, v := range vars {
			fmt.Printf("   {{%s}}\n", v)
		}
	}

	fmt.Printf("\n%s Structure:\n", ui.RenderPass("🌲"))
	printMoleculeTree(subgraph, subgraph.Root.ID, 0, true)
	fmt.Println()
}

var molSpawnCmd = &cobra.Command{
	Use:   "spawn <proto-id>",
	Short: "Instantiate a proto into a molecule",
	Long: `Spawn a molecule by instantiating a proto template into real issues.

Variables are specified with --var key=value flags. The proto's {{key}}
placeholders will be replaced with the corresponding values.

Example:
  bd mol spawn mol-code-review --var pr=123 --var repo=myproject
  bd mol spawn bd-abc123 --var version=1.2.0 --assignee=worker-1`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("mol spawn")

		ctx := rootCtx

		// mol spawn requires direct store access for subgraph loading and cloning
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: mol spawn requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol spawn %s ...\n", args[0])
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		varFlags, _ := cmd.Flags().GetStringSlice("var")
		assignee, _ := cmd.Flags().GetString("assignee")

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

		// Resolve molecule ID
		moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving molecule ID %s: %v\n", args[0], err)
			os.Exit(1)
		}

		// Load the molecule subgraph
		subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
			os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error: missing required variables: %s\n", strings.Join(missingVars, ", "))
			fmt.Fprintf(os.Stderr, "Provide them with: --var %s=<value>\n", missingVars[0])
			os.Exit(1)
		}

		if dryRun {
			fmt.Printf("\nDry run: would create %d issues from molecule %s\n\n", len(subgraph.Issues), moleculeID)
			for _, issue := range subgraph.Issues {
				newTitle := substituteVariables(issue.Title, vars)
				suffix := ""
				if issue.ID == subgraph.Root.ID && assignee != "" {
					suffix = fmt.Sprintf(" (assignee: %s)", assignee)
				}
				fmt.Printf("  - %s (from %s)%s\n", newTitle, issue.ID, suffix)
			}
			if len(vars) > 0 {
				fmt.Printf("\nVariables:\n")
				for k, v := range vars {
					fmt.Printf("  {{%s}} = %s\n", k, v)
				}
			}
			return
		}

		// Clone the subgraph (spawn the molecule)
		result, err := spawnMolecule(ctx, store, subgraph, vars, assignee, actor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error spawning molecule: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(result)
			return
		}

		fmt.Printf("%s Spawned molecule: created %d issues\n", ui.RenderPass("✓"), result.Created)
		fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	},
}

var molBondCmd = &cobra.Command{
	Use:   "bond <A> <B>",
	Short: "Bond two protos or molecules together",
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

var molDistillCmd = &cobra.Command{
	Use:   "distill <epic-id>",
	Short: "Extract a reusable proto from an existing epic",
	Long: `Distill a molecule by extracting a reusable proto from an existing epic.

This is the reverse of spawn: instead of proto → molecule, it's molecule → proto.

The distill command:
  1. Loads the existing epic and all its children
  2. Clones the structure as a new proto (adds "template" label)
  3. Replaces concrete values with {{variable}} placeholders (via --var flags)

Use cases:
  - Team develops good workflow organically, wants to reuse it
  - Capture tribal knowledge as executable templates
  - Create starting point for similar future work

Examples:
  bd mol distill bd-o5xe --as release-workflow
  bd mol distill bd-abc --var title=feature_name --var version=1.0.0`,
	Args: cobra.ExactArgs(1),
	Run:  runMolDistill,
}

var molRunCmd = &cobra.Command{
	Use:   "run <proto-id>",
	Short: "Spawn proto and start execution (spawn + assign + pin)",
	Long: `Run a molecule by spawning a proto and setting up for durable execution.

This command:
  1. Spawns the molecule (creates issues from proto template)
  2. Assigns the root issue to the caller
  3. Sets root status to in_progress
  4. Pins the root issue for session recovery

After a crash or session reset, the pinned root issue ensures the agent
can resume from where it left off by checking 'bd ready'.

Example:
  bd mol run mol-version-bump --var version=1.2.0
  bd mol run bd-qqc --var version=0.32.0 --var date=2025-01-01`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("mol run")

		ctx := rootCtx

		// mol run requires direct store access
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: mol run requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol run %s ...\n", args[0])
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}

		varFlags, _ := cmd.Flags().GetStringSlice("var")

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

		// Resolve molecule ID
		moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving molecule ID %s: %v\n", args[0], err)
			os.Exit(1)
		}

		// Load the molecule subgraph
		subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
			os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error: missing required variables: %s\n", strings.Join(missingVars, ", "))
			fmt.Fprintf(os.Stderr, "Provide them with: --var %s=<value>\n", missingVars[0])
			os.Exit(1)
		}

		// Spawn the molecule with actor as assignee
		result, err := spawnMolecule(ctx, store, subgraph, vars, actor, actor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error spawning molecule: %v\n", err)
			os.Exit(1)
		}

		// Update root issue: set status=in_progress and pinned=true
		rootID := result.NewEpicID
		updates := map[string]interface{}{
			"status": string(types.StatusInProgress),
			"pinned": true,
		}
		if err := store.UpdateIssue(ctx, rootID, updates, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating root issue: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"root_id":   rootID,
				"created":   result.Created,
				"id_mapping": result.IDMapping,
				"pinned":    true,
				"status":    "in_progress",
				"assignee":  actor,
			})
			return
		}

		fmt.Printf("%s Molecule running: created %d issues\n", ui.RenderPass("✓"), result.Created)
		fmt.Printf("  Root issue: %s (pinned, in_progress)\n", rootID)
		fmt.Printf("  Assignee: %s\n", actor)
		fmt.Println("\nNext steps:")
		fmt.Printf("  bd ready                # Find unblocked work in this molecule\n")
		fmt.Printf("  bd show %s       # View molecule status\n", rootID[:8])
	},
}

func init() {
	molSpawnCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	molSpawnCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molSpawnCmd.Flags().String("assignee", "", "Assign the root issue to this agent/user")

	molRunCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")

	molBondCmd.Flags().String("type", types.BondTypeSequential, "Bond type: sequential, parallel, or conditional")
	molBondCmd.Flags().String("as", "", "Custom title for compound proto (proto+proto only)")
	molBondCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molBondCmd.Flags().StringSlice("var", []string{}, "Variable substitution for spawned protos (key=value)")

	molDistillCmd.Flags().String("as", "", "Custom title for the new proto")
	molDistillCmd.Flags().StringSlice("var", []string{}, "Replace value with {{variable}} placeholder (value=variable)")
	molDistillCmd.Flags().Bool("dry-run", false, "Preview what would be created")

	molCmd.AddCommand(molCatalogCmd)
	molCmd.AddCommand(molShowCmd)
	molCmd.AddCommand(molSpawnCmd)
	molCmd.AddCommand(molRunCmd)
	molCmd.AddCommand(molBondCmd)
	molCmd.AddCommand(molDistillCmd)
	rootCmd.AddCommand(molCmd)
}

// =============================================================================
// Molecule Helper Functions
// =============================================================================

// spawnMolecule creates new issues from the proto with variable substitution.
// This instantiates a proto (template) into a molecule (real issues).
// Wraps cloneSubgraph from template.go and returns SpawnResult.
func spawnMolecule(ctx context.Context, s storage.Storage, subgraph *MoleculeSubgraph, vars map[string]string, assignee string, actorName string) (*InstantiateResult, error) {
	return cloneSubgraph(ctx, s, subgraph, vars, assignee, actorName)
}

// printMoleculeTree prints the molecule structure as a tree
func printMoleculeTree(subgraph *MoleculeSubgraph, parentID string, depth int, isRoot bool) {
	printTemplateTree(subgraph, parentID, depth, isRoot)
}

// =============================================================================
// Bond Command Implementation
// =============================================================================

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

// BondResult holds the result of a bond operation
type BondResult struct {
	ResultID   string            `json:"result_id"`
	ResultType string            `json:"result_type"` // "compound_proto" or "compound_molecule"
	BondType   string            `json:"bond_type"`
	Spawned    int               `json:"spawned,omitempty"`    // Number of issues spawned (if proto was involved)
	IDMapping  map[string]string `json:"id_mapping,omitempty"` // Old ID -> new ID for spawned issues
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
func operandType(isProto bool) string {
	if isProto {
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
			Labels:      []string{MoleculeLabel}, // Mark as proto
			BondedFrom: []types.BondRef{
				{ProtoID: protoA.ID, BondType: bondType, BondPoint: ""},
				{ProtoID: protoB.ID, BondType: bondType, BondPoint: ""},
			},
		}
		if err := tx.CreateIssue(ctx, compound, actorName); err != nil {
			return fmt.Errorf("creating compound: %w", err)
		}
		compoundID = compound.ID

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

	// Spawn the proto
	spawnResult, err := spawnMolecule(ctx, s, subgraph, vars, "", actorName)
	if err != nil {
		return nil, fmt.Errorf("spawning proto: %w", err)
	}

	// Attach spawned molecule to existing molecule
	err = s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Add parent-child from spawned root to molecule
		dep := &types.Dependency{
			IssueID:     spawnResult.NewEpicID,
			DependsOnID: mol.ID,
			Type:        types.DepParentChild,
		}
		if err := tx.AddDependency(ctx, dep, actorName); err != nil {
			return fmt.Errorf("attaching to molecule: %w", err)
		}

		// For sequential, spawned work blocks on molecule completion
		if bondType == types.BondTypeSequential {
			seqDep := &types.Dependency{
				IssueID:     spawnResult.NewEpicID,
				DependsOnID: mol.ID,
				Type:        types.DepBlocks,
			}
			if err := tx.AddDependency(ctx, seqDep, actorName); err != nil {
				return fmt.Errorf("adding sequence dep: %w", err)
			}
		}

		// Update molecule with bond lineage
		updates := map[string]interface{}{
			"bonded_from": append(mol.BondedFrom, types.BondRef{
				ProtoID:   proto.ID,
				BondType:  bondType,
				BondPoint: mol.ID,
			}),
		}
		return tx.UpdateIssue(ctx, mol.ID, updates, actorName)
	})

	if err != nil {
		return nil, err
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
		// Add parent-child: B becomes child of A
		dep := &types.Dependency{
			IssueID:     molB.ID,
			DependsOnID: molA.ID,
			Type:        types.DepParentChild,
		}
		if err := tx.AddDependency(ctx, dep, actorName); err != nil {
			return fmt.Errorf("linking molecules: %w", err)
		}

		// For sequential, B blocks on A
		if bondType == types.BondTypeSequential {
			seqDep := &types.Dependency{
				IssueID:     molB.ID,
				DependsOnID: molA.ID,
				Type:        types.DepBlocks,
			}
			if err := tx.AddDependency(ctx, seqDep, actorName); err != nil {
				return fmt.Errorf("adding sequence dep: %w", err)
			}
		}

		// Update both with bond lineage
		updatesA := map[string]interface{}{
			"bonded_from": append(molA.BondedFrom, types.BondRef{
				ProtoID:   molB.ID,
				BondType:  bondType,
				BondPoint: "",
			}),
		}
		if err := tx.UpdateIssue(ctx, molA.ID, updatesA, actorName); err != nil {
			return err
		}

		updatesB := map[string]interface{}{
			"bonded_from": append(molB.BondedFrom, types.BondRef{
				ProtoID:   molA.ID,
				BondType:  bondType,
				BondPoint: molA.ID,
			}),
		}
		return tx.UpdateIssue(ctx, molB.ID, updatesB, actorName)
	})

	if err != nil {
		return nil, err
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

// =============================================================================
// Distill Command Implementation
// =============================================================================

// DistillResult holds the result of a distill operation
type DistillResult struct {
	ProtoID   string            `json:"proto_id"`
	IDMapping map[string]string `json:"id_mapping"` // old ID -> new ID
	Created   int               `json:"created"`    // number of issues created
	Variables []string          `json:"variables"`  // variables introduced
}

// runMolDistill implements the distill command
func runMolDistill(cmd *cobra.Command, args []string) {
	CheckReadonly("mol distill")

	ctx := rootCtx

	// mol distill requires direct store access
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: mol distill requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol distill %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	customTitle, _ := cmd.Flags().GetString("as")
	varFlags, _ := cmd.Flags().GetStringSlice("var")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Parse variable substitutions: value=variable means replace "value" with "{{variable}}"
	replacements := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'value=variable'\n", v)
			os.Exit(1)
		}
		replacements[parts[0]] = parts[1]
	}

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

	if dryRun {
		fmt.Printf("\nDry run: would distill %d issues from %s into a proto\n\n", len(subgraph.Issues), epicID)
		fmt.Printf("Source: %s\n", subgraph.Root.Title)
		if customTitle != "" {
			fmt.Printf("Proto title: %s\n", customTitle)
		}
		if len(replacements) > 0 {
			fmt.Printf("\nVariable substitutions:\n")
			for value, varName := range replacements {
				fmt.Printf("  \"%s\" → {{%s}}\n", value, varName)
			}
		}
		fmt.Printf("\nStructure:\n")
		for _, issue := range subgraph.Issues {
			title := issue.Title
			for value, varName := range replacements {
				title = strings.ReplaceAll(title, value, "{{"+varName+"}}")
			}
			prefix := "  "
			if issue.ID == subgraph.Root.ID {
				prefix = "→ "
			}
			fmt.Printf("%s%s\n", prefix, title)
		}
		return
	}

	// Distill the molecule into a proto
	result, err := distillMolecule(ctx, store, subgraph, customTitle, replacements, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error distilling molecule: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Distilled proto: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Proto ID: %s\n", result.ProtoID)
	if len(result.Variables) > 0 {
		fmt.Printf("  Variables: %s\n", strings.Join(result.Variables, ", "))
	}
	fmt.Printf("\nTo spawn this proto:\n")
	fmt.Printf("  bd mol spawn %s", result.ProtoID[:8])
	for _, v := range result.Variables {
		fmt.Printf(" --var %s=<value>", v)
	}
	fmt.Println()
}

// distillMolecule creates a new proto from an existing epic
func distillMolecule(ctx context.Context, s storage.Storage, subgraph *MoleculeSubgraph, customTitle string, replacements map[string]string, actorName string) (*DistillResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Build the reverse mapping for tracking variables introduced
	var variables []string
	for _, varName := range replacements {
		variables = append(variables, varName)
	}

	// Generate new IDs and create mapping
	idMapping := make(map[string]string)

	// Helper to apply replacements
	applyReplacements := func(text string) string {
		result := text
		for value, varName := range replacements {
			result = strings.ReplaceAll(result, value, "{{"+varName+"}}")
		}
		return result
	}

	// Use transaction for atomicity
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// First pass: create all issues with new IDs
		for _, oldIssue := range subgraph.Issues {
			// Determine title
			title := applyReplacements(oldIssue.Title)
			if oldIssue.ID == subgraph.Root.ID && customTitle != "" {
				title = customTitle
			}

			// Add template label to all issues
			labels := append([]string{}, oldIssue.Labels...)
			hasTemplateLabel := false
			for _, l := range labels {
				if l == MoleculeLabel {
					hasTemplateLabel = true
					break
				}
			}
			if !hasTemplateLabel {
				labels = append(labels, MoleculeLabel)
			}

			newIssue := &types.Issue{
				Title:              title,
				Description:        applyReplacements(oldIssue.Description),
				Design:             applyReplacements(oldIssue.Design),
				AcceptanceCriteria: applyReplacements(oldIssue.AcceptanceCriteria),
				Notes:              applyReplacements(oldIssue.Notes),
				Status:             types.StatusOpen, // Protos start fresh
				Priority:           oldIssue.Priority,
				IssueType:          oldIssue.IssueType,
				Labels:             labels,
				EstimatedMinutes:   oldIssue.EstimatedMinutes,
			}

			if err := tx.CreateIssue(ctx, newIssue, actorName); err != nil {
				return fmt.Errorf("failed to create proto issue from %s: %w", oldIssue.ID, err)
			}

			idMapping[oldIssue.ID] = newIssue.ID
		}

		// Second pass: recreate dependencies with new IDs
		for _, dep := range subgraph.Dependencies {
			newFromID, ok1 := idMapping[dep.IssueID]
			newToID, ok2 := idMapping[dep.DependsOnID]
			if !ok1 || !ok2 {
				continue // Skip if either end is outside the subgraph
			}

			newDep := &types.Dependency{
				IssueID:     newFromID,
				DependsOnID: newToID,
				Type:        dep.Type,
			}
			if err := tx.AddDependency(ctx, newDep, actorName); err != nil {
				return fmt.Errorf("failed to create dependency: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &DistillResult{
		ProtoID:   idMapping[subgraph.Root.ID],
		IDMapping: idMapping,
		Created:   len(subgraph.Issues),
		Variables: variables,
	}, nil
}
