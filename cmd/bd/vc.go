package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

var vcCmd = &cobra.Command{
	Use:     "vc",
	GroupID: "sync",
	Short:   "Version control operations (requires Dolt backend)",
	Long: `Version control operations for the beads database.

These commands require the Dolt storage backend. They provide git-like
version control for your issue data, including branching, merging, and
viewing history.

Note: 'bd history', 'bd diff', and 'bd branch' also work for quick access.
This subcommand provides additional operations like merge and commit.`,
}

var vcMergeStrategy string

var vcMergeCmd = &cobra.Command{
	Use:   "merge <branch>",
	Short: "Merge a branch into the current branch",
	Long: `Merge the specified branch into the current branch.

If there are merge conflicts, they will be reported. You can resolve
conflicts with --strategy.

Examples:
  bd vc merge feature-xyz                    # Merge feature-xyz into current branch
  bd vc merge feature-xyz --strategy ours    # Merge, preferring our changes on conflict
  bd vc merge feature-xyz --strategy theirs  # Merge, preferring their changes on conflict`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		branchName := args[0]

		// Perform merge
		conflicts, err := store.Merge(ctx, branchName)
		if err != nil {
			FatalErrorRespectJSON("failed to merge branch: %v", err)
		}

		// Handle conflicts
		if len(conflicts) > 0 {
			if vcMergeStrategy != "" {
				// Auto-resolve conflicts with specified strategy
				for _, conflict := range conflicts {
					table := conflict.Field // Field contains table name from GetConflicts
					if table == "" {
						table = "issues" // Default to issues table
					}
					if err := store.ResolveConflicts(ctx, table, vcMergeStrategy); err != nil {
						FatalErrorRespectJSON("failed to resolve conflicts: %v", err)
					}
				}
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"merged":        branchName,
						"conflicts":     len(conflicts),
						"resolved_with": vcMergeStrategy,
					})
					return
				}
				fmt.Printf("Merged %s with %d conflicts resolved using '%s' strategy\n",
					ui.RenderAccent(branchName), len(conflicts), vcMergeStrategy)
				return
			}

			// Report conflicts without auto-resolution
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"merged":    branchName,
					"conflicts": conflicts,
				})
				return
			}

			fmt.Printf("\n%s Merge completed with conflicts:\n\n", ui.RenderAccent("!!"))
			for _, conflict := range conflicts {
				fmt.Printf("  - %s\n", conflict.Field)
			}
			fmt.Printf("\nResolve conflicts with: bd vc merge %s --strategy [ours|theirs]\n\n", branchName)
			return
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"merged":    branchName,
				"conflicts": 0,
			})
			return
		}

		fmt.Printf("Successfully merged %s\n", ui.RenderAccent(branchName))
	},
}

var vcCommitMessage string
var vcCommitStdin bool

var vcCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Create a commit with all staged changes",
	Long: `Create a new Dolt commit with all current changes.

Examples:
  bd vc commit -m "Added new feature issues"
  bd vc commit --message "Fixed priority on several issues"
  echo "Multi-line message" | bd vc commit --stdin`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		if vcCommitStdin {
			if vcCommitMessage != "" {
				FatalErrorRespectJSON("cannot specify both --stdin and -m/--message")
			}
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				FatalErrorRespectJSON("failed to read commit message from stdin: %v", err)
			}
			vcCommitMessage = strings.TrimRight(string(b), "\n")
		}

		if vcCommitMessage == "" {
			FatalErrorRespectJSON("commit message is required (use -m, --message, or --stdin)")
		}

		// We are explicitly creating a Dolt commit; avoid redundant auto-commit in PersistentPostRun.
		commandDidExplicitDoltCommit = true
		if err := store.Commit(ctx, vcCommitMessage); err != nil {
			FatalErrorRespectJSON("failed to commit: %v", err)
		}

		// Get the new commit hash
		hash, err := store.GetCurrentCommit(ctx)
		if err != nil {
			hash = "(unknown)"
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"committed": true,
				"hash":      hash,
				"message":   vcCommitMessage,
			})
			return
		}

		fmt.Printf("Created commit %s\n", ui.RenderMuted(hash[:8]))
	},
}

var vcStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current branch and uncommitted changes",
	Long: `Show the current branch, commit hash, and any uncommitted changes.

Examples:
  bd vc status`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		currentBranch, err := store.CurrentBranch(ctx)
		if err != nil {
			FatalErrorRespectJSON("failed to get current branch: %v", err)
		}

		currentCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			currentCommit = "(unknown)"
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"branch": currentBranch,
				"commit": currentCommit,
			})
			return
		}

		fmt.Printf("\n%s Version Control Status\n\n", ui.RenderAccent("📊"))
		fmt.Printf("  Branch: %s\n", ui.StatusInProgressStyle.Render(currentBranch))
		fmt.Printf("  Commit: %s\n", ui.RenderMuted(currentCommit[:8]))
		fmt.Println()
	},
}

func init() {
	vcMergeCmd.Flags().StringVar(&vcMergeStrategy, "strategy", "", "Conflict resolution strategy: 'ours' or 'theirs'")
	vcCommitCmd.Flags().StringVarP(&vcCommitMessage, "message", "m", "", "Commit message")
	vcCommitCmd.Flags().BoolVar(&vcCommitStdin, "stdin", false, "Read commit message from stdin")

	vcCmd.AddCommand(vcMergeCmd)
	vcCmd.AddCommand(vcCommitCmd)
	vcCmd.AddCommand(vcStatusCmd)
	rootCmd.AddCommand(vcCmd)
}
