package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// promotionComment is the audit comment both routes record on the promoted
// bead. Shared by the classic and proxied-server paths so the text cannot
// drift.
func promotionComment(reason string) string {
	comment := "Promoted from Level 0"
	if reason != "" {
		comment += ": " + reason
	}
	return comment
}

// printPromoteResult renders the `bd promote` success output. updated is the
// best-effort post-promote re-read used only by --json (it may be nil, in
// which case the JSON path prints nothing, matching the classic behavior).
// Shared by the classic and proxied-server paths so the output shape cannot
// drift.
func printPromoteResult(fullID string, updated *types.Issue) error {
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
	return nil
}

var promoteCmd = &cobra.Command{
	Use:     "promote <wisp-id>",
	GroupID: "issues",
	Short:   "Promote a wisp to a permanent bead",
	Long: `Promote a wisp (ephemeral issue) to a permanent bead.

This copies the issue from the wisps table (dolt_ignored) to the permanent
issues table (Dolt-versioned), preserving labels, dependencies, events, and
comments. The original ID is preserved so all links keep working.

A comment is added recording the promotion and optional reason.

Examples:
  bd promote bd-wisp-abc123
  bd promote bd-wisp-abc123 --reason "Worth tracking long-term"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("promote")

		evt := metrics.NewCommandEvent("promote")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		id := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		if usesProxiedServer() {
			return runPromoteProxiedServer(rootCtx, id, reason)
		}

		ctx := rootCtx

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		fullID, err := utils.ResolvePartialID(ctx, store, id)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}

		issue, err := store.GetIssue(ctx, fullID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return HandleErrorRespectJSON("issue %s not found", fullID)
			}
			return HandleErrorRespectJSON("getting issue %s: %v", fullID, err)
		}
		if !issue.Ephemeral {
			return HandleErrorRespectJSON("%s is not a wisp (already persistent)", fullID)
		}

		if err := store.PromoteFromEphemeral(ctx, fullID, actor); err != nil {
			return HandleErrorRespectJSON("promoting %s: %v", fullID, err)
		}

		// Add promotion comment (issue is now in permanent table, AddComment routes correctly
		// via GetIssue fallback)
		if err := store.AddComment(ctx, fullID, actor, promotionComment(reason)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, err)
		}

		commandDidWrite.Store(true)

		var updated *types.Issue
		if jsonOutput {
			updated, _ = store.GetIssue(ctx, fullID)
		}
		return printPromoteResult(fullID, updated)
	},
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}
