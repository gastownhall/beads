package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molSpawnCmd = &cobra.Command{
	Use:   "spawn <proto-id>",
	Short: "Instantiate a proto into a molecule",
	Long: `Spawn a molecule by instantiating a proto template into real issues.

Variables are specified with --var key=value flags. The proto's {{key}}
placeholders will be replaced with the corresponding values.

Phase behavior:
  - By default, spawned molecules are WISPS (ephemeral, in .beads-wisp/)
  - Use --pour to create a persistent MOL (in .beads/)
  - Wisps are local-only, gitignored, and not synced
  - Mols are permanent, synced, and auditable

Chemistry shortcuts:
  bd pour <proto>    # Equivalent to: bd mol spawn <proto> --pour
  bd wisp <proto>    # Equivalent to: bd mol spawn <proto>

Use --attach to bond additional protos to the spawned molecule in a single
command. Each attached proto is spawned and bonded using the --attach-type
(default: sequential). This is equivalent to running spawn + multiple bond
commands, but more convenient for composing workflows.

Example:
  bd mol spawn mol-patrol                                  # Creates wisp (default)
  bd mol spawn mol-feature --pour --var name=auth          # Creates persistent mol
  bd mol spawn bd-abc123 --pour --var version=1.2.0        # Persistent with vars
  bd mol spawn mol-feature --attach mol-testing --var name=auth`,
	Args: cobra.ExactArgs(1),
	Run:  runMolSpawn,
}

func runMolSpawn(cmd *cobra.Command, args []string) {
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
	attachFlags, _ := cmd.Flags().GetStringSlice("attach")
	attachType, _ := cmd.Flags().GetString("attach-type")
	pour, _ := cmd.Flags().GetBool("pour")
	persistent, _ := cmd.Flags().GetBool("persistent")

	// Handle deprecated --persistent flag
	if persistent {
		fmt.Fprintf(os.Stderr, "Warning: --persistent is deprecated, use --pour instead\n")
		pour = true
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

	// Resolve and load attached protos
	type attachmentInfo struct {
		id       string
		issue    *types.Issue
		subgraph *MoleculeSubgraph
	}
	var attachments []attachmentInfo
	for _, attachArg := range attachFlags {
		attachID, err := utils.ResolvePartialID(ctx, store, attachArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving attachment ID %s: %v\n", attachArg, err)
			os.Exit(1)
		}
		attachIssue, err := store.GetIssue(ctx, attachID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading attachment %s: %v\n", attachID, err)
			os.Exit(1)
		}
		// Verify it's a proto (has template label)
		isProtoIssue := false
		for _, label := range attachIssue.Labels {
			if label == MoleculeLabel {
				isProtoIssue = true
				break
			}
		}
		if !isProtoIssue {
			fmt.Fprintf(os.Stderr, "Error: %s is not a proto (missing '%s' label)\n", attachID, MoleculeLabel)
			os.Exit(1)
		}
		attachSubgraph, err := loadTemplateSubgraph(ctx, store, attachID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading attachment subgraph %s: %v\n", attachID, err)
			os.Exit(1)
		}
		attachments = append(attachments, attachmentInfo{
			id:       attachID,
			issue:    attachIssue,
			subgraph: attachSubgraph,
		})
	}

	// Check for missing variables (primary + all attachments)
	requiredVars := extractAllVariables(subgraph)
	for _, attach := range attachments {
		attachVars := extractAllVariables(attach.subgraph)
		for _, v := range attachVars {
			// Dedupe: only add if not already in requiredVars
			found := false
			for _, rv := range requiredVars {
				if rv == v {
					found = true
					break
				}
			}
			if !found {
				requiredVars = append(requiredVars, v)
			}
		}
	}
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
		if len(attachments) > 0 {
			fmt.Printf("\nAttachments (%s bonding):\n", attachType)
			for _, attach := range attachments {
				fmt.Printf("  + %s (%d issues)\n", attach.issue.Title, len(attach.subgraph.Issues))
				for _, issue := range attach.subgraph.Issues {
					newTitle := substituteVariables(issue.Title, vars)
					fmt.Printf("    - %s (from %s)\n", newTitle, issue.ID)
				}
			}
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
	// Spawned molecules are wisps by default (vapor phase) - use --pour for persistent mol (liquid phase)
	wisp := !pour
	result, err := spawnMolecule(ctx, store, subgraph, vars, assignee, actor, wisp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error spawning molecule: %v\n", err)
		os.Exit(1)
	}

	// Attach bonded protos to the spawned molecule
	totalAttached := 0
	if len(attachments) > 0 {
		// Get the spawned molecule issue for bonding
		spawnedMol, err := store.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading spawned molecule: %v\n", err)
			os.Exit(1)
		}

		for _, attach := range attachments {
			bondResult, err := bondProtoMol(ctx, store, attach.issue, spawnedMol, attachType, vars, "", actor)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error attaching %s: %v\n", attach.id, err)
				os.Exit(1)
			}
			totalAttached += bondResult.Spawned
		}
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		// Enhance result with attachment info
		type spawnWithAttach struct {
			*InstantiateResult
			Attached int `json:"attached"`
		}
		outputJSON(spawnWithAttach{result, totalAttached})
		return
	}

	fmt.Printf("%s Spawned molecule: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	if totalAttached > 0 {
		fmt.Printf("  Attached: %d issues from %d protos\n", totalAttached, len(attachments))
	}
}

func init() {
	molSpawnCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	molSpawnCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	molSpawnCmd.Flags().String("assignee", "", "Assign the root issue to this agent/user")
	molSpawnCmd.Flags().StringSlice("attach", []string{}, "Proto to attach after spawning (repeatable)")
	molSpawnCmd.Flags().String("attach-type", types.BondTypeSequential, "Bond type for attachments: sequential, parallel, or conditional")
	molSpawnCmd.Flags().Bool("pour", false, "Create persistent mol in .beads/ (default: wisp in .beads-wisp/)")
	molSpawnCmd.Flags().Bool("persistent", false, "Deprecated: use --pour instead")
	_ = molSpawnCmd.Flags().MarkDeprecated("persistent", "use --pour instead") // Only fails if flag missing

	molCmd.AddCommand(molSpawnCmd)
}
