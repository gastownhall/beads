package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("promote")

		id := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		ctx := rootCtx

		if store == nil {
			FatalErrorWithHint("database not initialized",
				diagHint())
		}

		fullID, err := utils.ResolvePartialID(ctx, store, id)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}

		// Verify the issue is actually a wisp
		issue, err := store.GetIssue(ctx, fullID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				FatalErrorRespectJSON("issue %s not found", fullID)
			}
			FatalErrorRespectJSON("getting issue %s: %v", fullID, err)
		}
		if !issue.Ephemeral {
			FatalErrorRespectJSON("%s is not a wisp (already persistent)", fullID)
		}

		// Promote: copy from wisps to issues table, preserving labels/deps/events/comments
		if err := store.PromoteFromEphemeral(ctx, fullID, actor); err != nil {
			FatalErrorRespectJSON("promoting %s: %v", fullID, err)
		}

		// Add promotion comment (issue is now in permanent table, AddComment routes correctly
		// via GetIssue fallback)
		comment := "Promoted from wisp to permanent bead"
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
				outputJSON(updated)
			}
		} else {
			fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
		}
	},
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}
