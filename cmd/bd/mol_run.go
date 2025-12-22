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
	Run:  runMolRun,
}

func runMolRun(cmd *cobra.Command, args []string) {
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

	// Spawn the molecule with actor as assignee (wisp for cleanup - bd-2vh3)
	result, err := spawnMolecule(ctx, store, subgraph, vars, actor, actor, true)
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
			"root_id":    rootID,
			"created":    result.Created,
			"id_mapping": result.IDMapping,
			"pinned":     true,
			"status":     "in_progress",
			"assignee":   actor,
		})
		return
	}

	fmt.Printf("%s Molecule running: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s (pinned, in_progress)\n", rootID)
	fmt.Printf("  Assignee: %s\n", actor)
	fmt.Println("\nNext steps:")
	fmt.Printf("  bd ready                # Find unblocked work in this molecule\n")
	fmt.Printf("  bd show %s       # View molecule status\n", rootID)
}

func init() {
	molRunCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")

	molCmd.AddCommand(molRunCmd)
}
