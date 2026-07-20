package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var unclaimCmd = &cobra.Command{
	Use:           "unclaim [id...]",
	GroupID:       "issues",
	Short:         "Release a claimed issue",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Release a claimed issue by clearing the assignee and resetting status to 'open'.

Use this when an agent crashes mid-work or you need to abandon a claimed task.
The issue becomes available for re-claiming by other agents.

Only the current assignee can release its own claim. Releasing another
actor's claim requires --force and should be coordinated with the holder
first — their claim may be live even if the issue looks idle. Prefer
letting lease expiry reclaim genuinely abandoned work.

With --if-assignee, the release is an atomic compare-and-swap (the inverse of
claim): the issue is released only while it is still assigned to the given
assignee. If the holder differs — e.g. the claim was already reclaimed and
re-taken by another worker — nothing is changed and bd exits nonzero with an
error naming the current holder. Use this from supervisors that must return a
specific worker's issue without ever clobbering someone else's live claim.
--if-assignee requires a non-empty assignee and cannot be combined with --force
(they encode contradictory intent).

Exit status: 0 when every issue was released; 1 when any release failed
(including an --if-assignee mismatch).

Examples:
  bd unclaim bd-123
  bd unclaim bd-123 --reason "Agent crashed"
  bd unclaim bd-123 bd-456
  bd unclaim bd-123 --if-assignee worker-7   # only if still held by worker-7`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("unclaim is not supported in proxied-server mode")
		}
		CheckReadonly("unclaim")
		reason, _ := cmd.Flags().GetString("reason")
		force, _ := cmd.Flags().GetBool("force")
		ifAssignee, _ := cmd.Flags().GetString("if-assignee")
		// A conditional release is selected by the presence of --if-assignee, not
		// by a non-empty value. An explicitly empty --if-assignee "" is almost
		// always an unset variable that expanded into the flag; treating it as an
		// omitted flag would silently downgrade the compare-and-swap to an
		// unconditional release, so reject it before touching any issue. (--force
		// and --if-assignee are mutually exclusive at the flag-group level; this
		// guards the remaining empty-value case.)
		conditional := cmd.Flags().Changed("if-assignee")
		if conditional && ifAssignee == "" {
			return HandleErrorRespectJSON("--if-assignee requires a non-empty assignee; it releases the issue only while that assignee still holds it")
		}
		ctx := rootCtx

		unclaimedIssues := []*types.Issue{}
		hasError := false
		if store == nil {
			return HandleErrorWithHint("database not initialized",
				diagHint())
		}
		for _, id := range args {
			// Resolve with prefix routing
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hasError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store

			var unclaimErr error
			if conditional {
				unclaimErr = issueStore.UnclaimIssueIfAssignee(ctx, fullID, actor, ifAssignee)
			} else {
				unclaimErr = issueStore.UnclaimIssue(ctx, fullID, actor, force)
			}
			if unclaimErr != nil {
				fmt.Fprintf(os.Stderr, "Error unclaiming %s: %v\n", fullID, unclaimErr)
				hasError = true
				result.Close()
				continue
			}

			if reason != "" {
				if _, err := issueStore.AddIssueComment(ctx, fullID, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add reason comment: %v\n", err)
				}
			}

			if jsonOutput {
				updated, _ := issueStore.GetIssue(ctx, fullID)
				if updated != nil {
					unclaimedIssues = append(unclaimedIssues, updated)
				}
			} else {
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Unclaimed %s%s\n", ui.RenderPass("✓"), fullID, reasonMsg)
			}
			result.Close()
		}

		commandDidWrite.Store(true)

		if jsonOutput && len(unclaimedIssues) > 0 {
			if err := outputJSON(unclaimedIssues); err != nil {
				return HandleError("%v", err)
			}
		}

		if hasError {
			return SilentExit()
		}
		return nil
	},
}

func init() {
	unclaimCmd.Flags().StringP("reason", "r", "", "Reason for unclaiming")
	unclaimCmd.Flags().Bool("force", false, "Release the claim even if held by a different actor (admin/reaper use)")
	unclaimCmd.Flags().String("if-assignee", "", "Only release if still assigned to this assignee (atomic compare-and-swap; exits nonzero without changing the issue when the holder differs)")
	// --force (unconditional bypass of the ownership check) and --if-assignee
	// (release only while a specific assignee still holds it) encode
	// contradictory intent. Rejecting the combination stops a reaper script that
	// habitually passes --force from silently dropping it when it also passes
	// --if-assignee for one case.
	unclaimCmd.MarkFlagsMutuallyExclusive("force", "if-assignee")
	unclaimCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(unclaimCmd)
}
