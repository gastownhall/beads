// Package main implements the bd CLI dependency management commands.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

var depCmd = &cobra.Command{
	Use:   "dep",
	Short: "Manage dependencies",
}

var depAddCmd = &cobra.Command{
	Use:   "add [issue-id] [depends-on-id]",
	Short: "Add a dependency",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		depType, _ := cmd.Flags().GetString("type")

		ctx := rootCtx
		
		// Resolve partial IDs first
		var fromID, toID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fromID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
			
			resolveArgs = &rpc.ResolveIDArgs{ID: args[1]}
			resp, err = daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &toID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
		} else {
			var err error
			fromID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			
			toID, err = utils.ResolvePartialID(ctx, store, args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepAddArgs{
				FromID:  fromID,
				ToID:    toID,
				DepType: depType,
			}

			resp, err := daemonClient.AddDependency(depArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
				return
			}

			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
				green("✓"), args[0], args[1], depType)
			return
		}

		// Direct mode
		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        types.DependencyType(depType),
		}

		if err := store.AddDependency(ctx, dep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		// Check for cycles after adding dependency
		cycles, err := store.DetectCycles(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to check for cycles: %v\n", err)
		} else if len(cycles) > 0 {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Fprintf(os.Stderr, "\n%s Warning: Dependency cycle detected!\n", yellow("⚠"))
			fmt.Fprintf(os.Stderr, "This can hide issues from the ready work list and cause confusion.\n\n")
			fmt.Fprintf(os.Stderr, "Cycle path:\n")
			for _, cycle := range cycles {
				for j, issue := range cycle {
					if j == 0 {
						fmt.Fprintf(os.Stderr, "  %s", issue.ID)
					} else {
						fmt.Fprintf(os.Stderr, " → %s", issue.ID)
					}
				}
				if len(cycle) > 0 {
					fmt.Fprintf(os.Stderr, " → %s", cycle[0].ID)
				}
				fmt.Fprintf(os.Stderr, "\n")
			}
			fmt.Fprintf(os.Stderr, "\nRun 'bd dep cycles' for detailed analysis.\n\n")
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          depType,
			})
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
			green("✓"), fromID, toID, depType)
	},
}

var depRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id] [depends-on-id]",
	Short: "Remove a dependency",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		
		// Resolve partial IDs first
		var fromID, toID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fromID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
			
			resolveArgs = &rpc.ResolveIDArgs{ID: args[1]}
			resp, err = daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &toID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
		} else {
			var err error
			fromID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			
			toID, err = utils.ResolvePartialID(ctx, store, args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepRemoveArgs{
				FromID: fromID,
				ToID:   toID,
			}

			resp, err := daemonClient.RemoveDependency(depArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
				return
			}

			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
				green("✓"), fromID, toID)
			return
		}

		// Direct mode
		fullFromID := fromID
		fullToID := toID
		
		if err := store.RemoveDependency(ctx, fullFromID, fullToID, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "removed",
				"issue_id":      fullFromID,
				"depends_on_id": fullToID,
			})
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
			green("✓"), fullFromID, fullToID)
	},
}

var depTreeCmd = &cobra.Command{
	Use:   "tree [issue-id]",
	Short: "Show dependency tree",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		
		// Resolve partial ID first
		var fullID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fullID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
		} else {
			var err error
			fullID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", args[0], err)
				os.Exit(1)
			}
		}
		
		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(rootCtx, dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		showAllPaths, _ := cmd.Flags().GetBool("show-all-paths")
		maxDepth, _ := cmd.Flags().GetInt("max-depth")
		reverse, _ := cmd.Flags().GetBool("reverse")
		formatStr, _ := cmd.Flags().GetString("format")

		if maxDepth < 1 {
			fmt.Fprintf(os.Stderr, "Error: --max-depth must be >= 1\n")
			os.Exit(1)
		}
		
		tree, err := store.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, reverse)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Handle mermaid format
		if formatStr == "mermaid" {
			outputMermaidTree(tree, args[0])
			return
		}

		if jsonOutput {
			// Always output array, even if empty
			if tree == nil {
				tree = []*types.TreeNode{}
			}
			outputJSON(tree)
			return
		}

		if len(tree) == 0 {
			if reverse {
				fmt.Printf("\n%s has no dependents\n", fullID)
			} else {
				fmt.Printf("\n%s has no dependencies\n", fullID)
			}
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		if reverse {
			fmt.Printf("\n%s Dependent tree for %s:\n\n", cyan("🌲"), fullID)
		} else {
			fmt.Printf("\n%s Dependency tree for %s:\n\n", cyan("🌲"), fullID)
		}

		hasTruncation := false
		for _, node := range tree {
			indent := ""
			for i := 0; i < node.Depth; i++ {
				indent += "  "
			}
			line := fmt.Sprintf("%s→ %s: %s [P%d] (%s)",
				indent, node.ID, node.Title, node.Priority, node.Status)
			if node.Truncated {
				line += " … [truncated]"
				hasTruncation = true
			}
			fmt.Println(line)
		}

		if hasTruncation {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("\n%s Warning: Tree truncated at depth %d (safety limit)\n",
				yellow("⚠"), maxDepth)
		}
		fmt.Println()
	},
}

var depCyclesCmd = &cobra.Command{
	Use:   "cycles",
	Short: "Detect dependency cycles",
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(rootCtx, dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		ctx := rootCtx
		cycles, err := store.DetectCycles(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			// Always output array, even if empty
			if cycles == nil {
				cycles = [][]*types.Issue{}
			}
			outputJSON(cycles)
			return
		}

		if len(cycles) == 0 {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("\n%s No dependency cycles detected\n\n", green("✓"))
			return
		}

		red := color.New(color.FgRed).SprintFunc()
		fmt.Printf("\n%s Found %d dependency cycles:\n\n", red("⚠"), len(cycles))
		for i, cycle := range cycles {
			fmt.Printf("%d. Cycle involving:\n", i+1)
			for _, issue := range cycle {
				fmt.Printf("   - %s: %s\n", issue.ID, issue.Title)
			}
			fmt.Println()
		}
	},
}

// outputMermaidTree outputs a dependency tree in Mermaid.js flowchart format
func outputMermaidTree(tree []*types.TreeNode, rootID string) {
	if len(tree) == 0 {
		fmt.Println("flowchart TD")
		fmt.Printf("  %s[\"No dependencies\"]\n", rootID)
		return
	}

	fmt.Println("flowchart TD")

	// Output nodes
	nodesSeen := make(map[string]bool)
	for _, node := range tree {
		if !nodesSeen[node.ID] {
			emoji := getStatusEmoji(node.Status)
			label := fmt.Sprintf("%s %s: %s", emoji, node.ID, node.Title)
			// Escape quotes and backslashes in label
			label = strings.ReplaceAll(label, "\\", "\\\\")
			label = strings.ReplaceAll(label, "\"", "\\\"")
			fmt.Printf("  %s[\"%s\"]\n", node.ID, label)

			nodesSeen[node.ID] = true
		}
	}

	fmt.Println()

	// Output edges - use explicit parent relationships from ParentID
	for _, node := range tree {
		if node.ParentID != "" && node.ParentID != node.ID {
			fmt.Printf("  %s --> %s\n", node.ParentID, node.ID)
		}
	}
}

// getStatusEmoji returns a symbol indicator for a given status
func getStatusEmoji(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "☐" // U+2610 Ballot Box
	case types.StatusInProgress:
		return "◧" // U+25E7 Square Left Half Black
	case types.StatusBlocked:
		return "⚠" // U+26A0 Warning Sign
	case types.StatusClosed:
		return "☑" // U+2611 Ballot Box with Check
	default:
		return "?"
	}
}

func init() {
	depAddCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|related|parent-child|discovered-from)")
	// Note: --json flag is defined as a persistent flag in main.go, not here

	// Note: --json flag is defined as a persistent flag in main.go, not here

	depTreeCmd.Flags().Bool("show-all-paths", false, "Show all paths to nodes (no deduplication for diamond dependencies)")
	depTreeCmd.Flags().IntP("max-depth", "d", 50, "Maximum tree depth to display (safety limit)")
	depTreeCmd.Flags().Bool("reverse", false, "Show dependent tree (what was discovered from this) instead of dependency tree (what blocks this)")
	depTreeCmd.Flags().String("format", "", "Output format: 'mermaid' for Mermaid.js flowchart")
	// Note: --json flag is defined as a persistent flag in main.go, not here

	// Note: --json flag is defined as a persistent flag in main.go, not here

	depCmd.AddCommand(depAddCmd)
	depCmd.AddCommand(depRemoveCmd)
	depCmd.AddCommand(depTreeCmd)
	depCmd.AddCommand(depCyclesCmd)
	rootCmd.AddCommand(depCmd)
}
