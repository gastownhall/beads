package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
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
				"run 'bd doctor' to diagnose, or 'bd init' to create a new database")
		}

		// Resolve with cross-rig routing (handles both local and remote)
		result, err := resolveAndGetIssueWithRouting(ctx, store, id)
		if result != nil {
			defer result.Close()
		}
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}

		fullID := result.ResolvedID
		issueStore := result.Store

		if !result.Issue.Ephemeral {
			FatalErrorRespectJSON("%s is not a wisp (already persistent)", fullID)
		}

		// Promote: copy from wisps to issues table, preserving labels/deps/events/comments
		if err := issueStore.PromoteFromEphemeral(ctx, fullID, actor); err != nil {
			FatalErrorRespectJSON("promoting %s: %v", fullID, err)
		}

		// Add promotion comment
		comment := "Promoted from wisp to permanent bead"
		if reason != "" {
			comment += ": " + reason
		}
		if err := issueStore.AddComment(ctx, fullID, actor, comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, err)
		}

		if jsonOutput {
			updated, _ := issueStore.GetIssue(ctx, fullID)
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
