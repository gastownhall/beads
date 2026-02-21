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
	Long: `Promote a wisp (ephemeral issue) to a permanent bead (Level 1).

This sets ephemeral=false on the issue, making it persistent and exportable
to JSONL. The original ID is preserved so all links keep working.

A comment is added recording the promotion and optional reason.

Examples:
  bd promote bd-abc123
  bd promote bd-abc123 --reason "Worth tracking long-term"`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("promote")

		id := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		ctx := rootCtx

		// Build promotion comment
		comment := "Promoted from Level 0"
		if reason != "" {
			comment += ": " + reason
		}

		// Direct mode
		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd init' to create a database")
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

		// Set ephemeral=false
		updates := map[string]interface{}{
			"wisp": false,
		}
		if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
			FatalErrorRespectJSON("promoting %s: %v", fullID, err)
		}

		// Add promotion comment
		if err := store.AddComment(ctx, fullID, actor, comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, err)
		}

		if jsonOutput {
			updated, _ := store.GetIssue(ctx, fullID) // Best effort: nil issue handled by subsequent nil check
			if updated != nil {
				outputJSON(updated)
			}
		} else {
			fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
		}
	},
}

// promoteRouted handles promotion for cross-rig routed issues.
func promoteRouted(id, comment string) {
	result, err := resolveAndGetIssueWithRouting(rootCtx, store, id)
	if err != nil {
		FatalErrorRespectJSON("resolving %s: %v", id, err)
	}
	if result == nil || result.Issue == nil {
		if result != nil {
			result.Close()
		}
		FatalErrorRespectJSON("issue %s not found", id)
	}
	defer result.Close()

	if !result.Issue.Ephemeral {
		FatalErrorRespectJSON("%s is not a wisp (already persistent)", id)
	}

	updates := map[string]interface{}{
		"wisp": false,
	}
	if err := result.Store.UpdateIssue(rootCtx, result.ResolvedID, updates, actor); err != nil {
		FatalErrorRespectJSON("promoting %s: %v", id, err)
	}

	if err := result.Store.AddComment(rootCtx, result.ResolvedID, actor, comment); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", id, err)
	}

	if jsonOutput {
		updated, _ := result.Store.GetIssue(rootCtx, result.ResolvedID) // Best effort: nil issue handled by subsequent nil check
		if updated != nil {
			outputJSON(updated)
		}
	} else {
		fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), result.ResolvedID)
	}
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}
