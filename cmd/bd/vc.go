package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var vcCmd = &cobra.Command{
	Use:     "vc",
	GroupID: "sync",
	Short:   "Version control operations",
	Long: `Version control operations for the beads database.

These commands provide git-like version control for your issue data, including branching, merging, and
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("vc merge is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("vc-merge")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		branchName := args[0]

		// Pre-merge HEAD scopes the post-resolution is_blocked recompute
		// (bd-578h9.11); empty degrades to a full-graph pass.
		preHead, _ := store.GetCurrentCommit(ctx)

		// Perform merge
		conflicts, err := store.Merge(ctx, branchName)
		if err != nil {
			return HandleErrorRespectJSON("failed to merge branch: %v", err)
		}

		if len(conflicts) > 0 {
			if vcMergeStrategy != "" {
				for _, conflict := range conflicts {
					table := conflict.Field
					if table == "" {
						table = "issues"
					}
					if err := store.ResolveConflicts(ctx, table, vcMergeStrategy); err != nil {
						return HandleErrorRespectJSON("failed to resolve conflicts: %v", err)
					}
				}
				// Conclude the merge: an unresolved-then-resolved working set
				// stays uncommitted otherwise, and the merged-in writes
				// bypassed every is_blocked hook (bd-578h9.11). Use
				// CommitMergeResolution, not Commit: server-mode Commit excludes
				// config (GH#2455), so a resolved config conflict — routine now
				// that kv.* user data syncs through config — would be silently
				// dropped, leaving the merge unconcluded and re-wedging the next
				// pull/sync (GH#2474).
				if err := store.CommitMergeResolution(ctx, fmt.Sprintf("Resolve merge conflicts from %s using %s strategy", branchName, vcMergeStrategy)); err != nil {
					return HandleErrorRespectJSON("conflicts resolved but commit failed: %v", err)
				}
				if rs, ok := store.(interface {
					RecomputeBlockedAfterMerge(ctx context.Context, fromCommit string) error
				}); ok {
					if err := rs.RecomputeBlockedAfterMerge(ctx, preHead); err != nil {
						return HandleErrorRespectJSON("conflicts resolved but is_blocked recompute failed: %v", err)
					}
				}
				if jsonOutput {
					return outputJSON(map[string]interface{}{
						"merged":        branchName,
						"conflicts":     len(conflicts),
						"resolved_with": vcMergeStrategy,
					})
				}
				fmt.Printf("Merged %s with %d conflicts resolved using '%s' strategy\n",
					ui.RenderAccent(branchName), len(conflicts), vcMergeStrategy)
				return nil
			}

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"merged":    branchName,
					"conflicts": conflicts,
				})
			}

			fmt.Printf("\n%s Merge completed with conflicts:\n\n", ui.RenderAccent("!!"))
			for _, conflict := range conflicts {
				fmt.Printf("  - %s\n", conflict.Field)
			}
			fmt.Printf("\nResolve conflicts with: bd vc merge %s --strategy [ours|theirs]\n\n", branchName)
			return nil
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"merged":    branchName,
				"conflicts": 0,
			})
		}

		fmt.Printf("Successfully merged %s\n", ui.RenderAccent(branchName))
		return nil
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("vc commit is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("vc-commit")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		if vcCommitStdin {
			if vcCommitMessage != "" {
				return HandleErrorRespectJSON("cannot specify both --stdin and -m/--message")
			}
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return HandleErrorRespectJSON("failed to read commit message from stdin: %v", err)
			}
			vcCommitMessage = strings.TrimRight(string(b), "\n")
		}

		if vcCommitMessage == "" {
			return HandleErrorRespectJSON("commit message is required (use -m, --message, or --stdin)")
		}

		if err := errExplicitCommitUnsupported(store, "vc commit"); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		commandDidExplicitDoltCommit = true
		if err := store.Commit(ctx, vcCommitMessage); err != nil {
			if isDoltNothingToCommit(err) {
				if jsonOutput {
					return outputJSON(map[string]interface{}{"committed": false, "message": "nothing to commit"})
				}
				fmt.Println("Nothing to commit")
				return nil
			}
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		hash, err := store.GetCurrentCommit(ctx)
		if err != nil {
			hash = "(unknown)"
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"committed": true,
				"hash":      hash,
				"message":   vcCommitMessage,
			})
		}

		fmt.Printf("Created commit %s\n", ui.RenderMuted(hash[:8]))
		return nil
	},
}

var vcStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current branch and uncommitted changes",
	Long: `Show the current branch, commit hash, and any uncommitted changes.

Examples:
  bd vc status`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("vc status is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("vc-status")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		currentBranch, err := store.CurrentBranch(ctx)
		if err != nil {
			return HandleErrorRespectJSON("failed to get current branch: %v", err)
		}

		currentCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			currentCommit = "(unknown)"
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"branch": currentBranch,
				"commit": currentCommit,
			})
		}

		fmt.Printf("\n%s Version Control Status\n\n", ui.RenderAccent("📊"))
		fmt.Printf("  Branch: %s\n", ui.StatusInProgressStyle.Render(currentBranch))
		fmt.Printf("  Commit: %s\n", ui.RenderMuted(currentCommit[:8]))
		fmt.Println()
		return nil
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
