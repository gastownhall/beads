package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

var assignCmd = &cobra.Command{
	Use:     "assign <id> <name>",
	GroupID: "issues",
	Short:   "Assign an issue to someone",
	Long: `Assign an issue to someone.

Shorthand for 'bd update <id> --assignee <name>'.

Refuses to overwrite another actor's live in_progress claim without --force
(bd-98s5c); issues assigned to a claim.pools alias are exempt, matching
--claim. For a holder-aware transfer prefer
'bd update <id> --if-assignee <holder> -a <new>'.

Examples:
  bd assign bd-123 alice
  bd assign bd-123 ""      # unassign`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("assign")

		evt := metrics.NewCommandEvent("assign")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		force, _ := cmd.Flags().GetBool("force")

		// A8 (beads#4682): assign takes exactly two positional args (id, name),
		// so "one id only" always holds; no separate check is needed the way
		// update/close/delete need one.
		ifRevision, err := parseIfRevisionFlag(cmd)
		if err != nil {
			return err
		}

		if usesProxiedServer() {
			return runAssignProxiedServer(rootCtx, args, force, ifRevision)
		}

		id := args[0]
		assignee := args[1]

		ctx := rootCtx

		result, err := resolveAndGetIssueForMutation(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue %s not found", id)
		}
		defer result.Close()

		issueStore := result.Store

		if err := validateIssueUpdatable(id, result.Issue); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		// bd-98s5c: bd assign is shorthand for an unguarded assignee update —
		// same live-claim fence as bd update -a.
		if err := validateIssueReassignable(id, result.Issue, actor, assignee,
			storeClaimPoolAliases(ctx, issueStore), force); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		// A8 (beads#4682): routed through issueops.Lifecycle.Update (via the
		// shared commandUpdateMutation helper update.go uses) rather than the
		// store's raw UpdateIssue, so --if-revision has a checked write to
		// guard — bd assign had no compare-and-set surface at all before this.
		ops, err := writeOps(issueStore)
		if err != nil {
			return HandleErrorRespectJSON("updating %s: %v", id, err)
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		mutationResult, err := runCommandUpdateMutation(opsCtx, ops, commandUpdateMutation{
			actor:   actor,
			issueID: result.ResolvedID,
			patch: issueops.IssuePatch{
				Assignee: issueops.Field[string]{Set: true, Value: assignee},
			},
			force:           force,
			expectedVersion: ifRevision,
		})
		if err != nil {
			if ifRevision != nil {
				if reported, ok := reportIfRevisionFailure("assigning", id, err); ok {
					return reported
				}
			}
			return HandleErrorRespectJSON("updating %s: %v", id, err)
		}

		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "assign",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		updatedIssue := mutationResult.Issue
		title := ""
		if updatedIssue != nil {
			title = updatedIssue.Title
		}
		if jsonOutput {
			if updatedIssue != nil {
				if err := outputJSON(updatedIssue); err != nil {
					return err
				}
			}
		} else {
			if assignee == "" {
				fmt.Printf("%s Unassigned %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title))
			} else {
				fmt.Printf("%s Assigned %s to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title), assignee)
			}
		}
		return nil
	},
}

func init() {
	assignCmd.Flags().Bool("force", false, "Allow overwriting another actor's live in_progress claim (use only for abandoned claims — crashed agent, expired lease; prefer bd reclaim)")
	// A8 (beads#4682)
	assignCmd.Flags().String("if-revision", "", ifRevisionFlagHelp)
	assignCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(assignCmd)
}
