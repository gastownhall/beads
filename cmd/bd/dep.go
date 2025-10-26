// Package main implements the bd CLI dependency management commands.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
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

		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepAddArgs{
				FromID:  args[0],
				ToID:    args[1],
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
			IssueID:     args[0],
			DependsOnID: args[1],
			Type:        types.DependencyType(depType),
		}

		ctx := context.Background()
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
				"issue_id":      args[0],
				"depends_on_id": args[1],
				"type":          depType,
			})
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
			green("✓"), args[0], args[1], depType)
	},
}

var depRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id] [depends-on-id]",
	Short: "Remove a dependency",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepRemoveArgs{
				FromID: args[0],
				ToID:   args[1],
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
				green("✓"), args[0], args[1])
			return
		}

		// Direct mode
		ctx := context.Background()
		if err := store.RemoveDependency(ctx, args[0], args[1], actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "removed",
				"issue_id":      args[0],
				"depends_on_id": args[1],
			})
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
			green("✓"), args[0], args[1])
	},
}

var depTreeCmd = &cobra.Command{
	Use:   "tree [issue-id]",
	Short: "Show dependency tree",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		showAllPaths, _ := cmd.Flags().GetBool("show-all-paths")
		maxDepth, _ := cmd.Flags().GetInt("max-depth")

		if maxDepth < 1 {
			fmt.Fprintf(os.Stderr, "Error: --max-depth must be >= 1\n")
			os.Exit(1)
		}

		ctx := context.Background()
		tree, err := store.GetDependencyTree(ctx, args[0], maxDepth, showAllPaths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
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
			fmt.Printf("\n%s has no dependencies\n", args[0])
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s Dependency tree for %s:\n\n", cyan("🌲"), args[0])

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
			store, err = sqlite.New(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		ctx := context.Background()
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

func init() {
	depAddCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|related|parent-child|discovered-from)")
	depTreeCmd.Flags().Bool("show-all-paths", false, "Show all paths to nodes (no deduplication for diamond dependencies)")
	depTreeCmd.Flags().IntP("max-depth", "d", 50, "Maximum tree depth to display (safety limit)")
	depCmd.AddCommand(depAddCmd)
	depCmd.AddCommand(depRemoveCmd)
	depCmd.AddCommand(depTreeCmd)
	depCmd.AddCommand(depCyclesCmd)
	rootCmd.AddCommand(depCmd)
}
