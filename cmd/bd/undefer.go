package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var undeferCmd = &cobra.Command{
	Use:   "undefer [id...]",
	Short: "Undefer one or more issues (restore to open)",
	Long: `Undefer issues to restore them to open status.

This brings issues back from the icebox so they can be worked on again.
Issues will appear in 'bd ready' if they have no blockers.

Examples:
  bd undefer bd-abc        # Undefer a single issue
  bd undefer bd-abc bd-def # Undefer multiple issues`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("undefer")

		ctx := rootCtx

		undeferredIssues := []*types.Issue{}

		// Direct storage access
		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd doctor' to diagnose, or 'bd init' to create a new database")
		}

		for _, id := range args {
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store
			issue := result.Issue

			// Skip if not deferred — avoid false "Undeferred" message
			if issue.Status != types.StatusDeferred {
				result.Close()
				fmt.Fprintf(os.Stderr, "%s is not deferred (status: %s)\n", fullID, string(issue.Status))
				continue
			}

			updates := map[string]interface{}{
				"status":      string(types.StatusOpen),
				"defer_until": nil, // Clear defer_until timestamp (GH#820)
			}

			if err := issueStore.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				result.Close()
				fmt.Fprintf(os.Stderr, "Error undeferring %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				updated, _ := issueStore.GetIssue(ctx, fullID)
				if updated != nil {
					undeferredIssues = append(undeferredIssues, updated)
				}
			} else {
				fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), fullID)
			}
			result.Close()
		}

		if jsonOutput && len(undeferredIssues) > 0 {
			outputJSON(undeferredIssues)
		}

		// Embedded mode: flush Dolt commit.
		if isEmbeddedDolt && len(args) > 0 && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalError("failed to commit: %v", err)
			}
		}
	},
}

func init() {
	undeferCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(undeferCmd)
}
