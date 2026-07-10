package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

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
		comment := "Promoted from Level 0"
		if reason != "" {
			comment += ": " + reason
		}
		if err := store.AddComment(ctx, fullID, actor, comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			updated, _ := store.GetIssue(ctx, fullID)
			if updated != nil {
				return outputJSON(updated)
			}
			return nil
		}
		fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
		return nil
	},
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}
