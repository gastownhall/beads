package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
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

// blockedAfterMergeRecomputer is the narrow store surface that repairs the
// denormalized is_blocked column for the rows a merge brought in
// (bd-578h9.11). Only the concrete stores implement it
// (internal/storage/dolt/store.go, internal/storage/embeddeddolt/version_control.go);
// it is NOT part of storage.DoltStorage.
type blockedAfterMergeRecomputer interface {
	RecomputeBlockedAfterMerge(ctx context.Context, fromCommit string) error
}

// blockedAfterMergeRecomputerFor peels the storage decorator chain before
// asserting, so the optional interface is looked up on the concrete store.
//
// getStore() hands back the wireStorageDecorators chain — caller →
// HookFiringStore → InstrumentedStorage → concrete store
// (cmd/bd/storage_chain.go) — and both decorators embed the storage.DoltStorage
// INTERFACE, so their promoted method sets are exactly DoltStorage's.
// RecomputeBlockedAfterMerge is not in that set, so asserting on the chain
// itself always failed and the post-merge recompute was silently skipped on
// every rig carrying a hook layer, which is essentially all of them
// (main.go builds a hook runner whenever dbPath != ""; only no-hooks:true /
// BD_NO_HOOKS=1 leaves it off). Same defect class as wy-xtv17's
// persistedRemoteProber; wy-163oy.
func blockedAfterMergeRecomputerFor(st storage.DoltStorage) (blockedAfterMergeRecomputer, bool) {
	if st == nil {
		return nil, false
	}
	rs, ok := storage.UnwrapStore(st).(blockedAfterMergeRecomputer)
	return rs, ok
}

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

		if vcMergeStrategy != "" {
			// #4992: a bare CALL DOLT_MERGE under autocommit rejects any real
			// conflict before --strategy could ever be applied. MergeWithStrategy
			// runs the whole merge/resolve/repair/commit sequence on a pinned
			// session with Dolt's conflict-tolerant flags set, so the strategy
			// actually reaches DOLT_CONFLICTS_RESOLVE.
			merger, ok := storage.UnwrapStore(store).(storage.StrategicMerger)
			if !ok {
				return HandleErrorRespectJSON("storage backend %T does not support --strategy merges", storage.UnwrapStore(store))
			}
			conflicts, err := merger.MergeWithStrategy(ctx, branchName, vcMergeStrategy)
			if err != nil {
				return HandleErrorRespectJSON("failed to merge branch: %v", err)
			}

			if len(conflicts) > 0 {
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
					"conflicts": 0,
				})
			}
			fmt.Printf("Successfully merged %s\n", ui.RenderAccent(branchName))
			return nil
		}

		// No --strategy: a real conflict makes store.Merge return an error —
		// it still runs as a bare DOLT_MERGE under autocommit, which Dolt
		// rejects on conflict (Error 1105), same as plain `dolt merge` with no
		// further flags. versioncontrolops.Merge appends the --strategy escape
		// hatch to that error's message.
		conflicts, err := store.Merge(ctx, branchName)
		if err != nil {
			return HandleErrorRespectJSON("failed to merge branch: %v", err)
		}

		if len(conflicts) > 0 {
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

		commandDidExplicitDoltCommit = true
		// CommitAll, not Commit: the explicit command promises "all current
		// changes", so it must sweep in out-of-band writes (config above all,
		// which server-mode Commit excludes per GH#2455) and must report an
		// honest no-op instead of printing "Created commit" against the
		// unchanged HEAD. Its committed bool is the atomic signal the old
		// HEAD-before/HEAD-after comparison approximated (the mybd-z9h7j
		// threading: CommitPending already had the shape), so the concurrent-
		// writer misattribution race that comparison carried is gone.
		committed, err := store.CommitAll(ctx, vcCommitMessage)
		if err != nil {
			if isDoltNothingToCommit(err) {
				committed = false
			} else {
				return HandleErrorRespectJSON("failed to commit: %v", err)
			}
		}
		if !committed {
			if jsonOutput {
				return outputJSON(map[string]interface{}{"committed": false, "message": "nothing to commit"})
			}
			fmt.Println("Nothing to commit")
			return nil
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
