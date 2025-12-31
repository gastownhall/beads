package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var refileCmd = &cobra.Command{
	Use:     "refile <source-id> <target-rig>",
	GroupID: "issues",
	Short:   "Move an issue to a different rig",
	Long: `Move an issue from one rig to another.

This creates a new issue in the target rig with the same content,
then closes the source issue with a reference to the new location.

The target rig can be specified as:
  - A rig name: beads, gastown
  - A prefix: bd-, gt-
  - A prefix without hyphen: bd, gt

Examples:
  bd refile bd-8hea gastown     # Move to gastown by rig name
  bd refile bd-8hea gt-         # Move to gastown by prefix
  bd refile bd-8hea gt          # Move to gastown (prefix without hyphen)`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("refile")

		sourceID := args[0]
		targetRig := args[1]

		keepOpen, _ := cmd.Flags().GetBool("keep-open")

		ctx := rootCtx

		// Step 1: Get the source issue (via routing if needed)
		result, err := resolveAndGetIssueWithRouting(ctx, store, sourceID)
		if err != nil {
			FatalError("failed to find source issue: %v", err)
		}
		if result == nil || result.Issue == nil {
			FatalError("source issue %s not found", sourceID)
		}
		defer result.Close()

		sourceIssue := result.Issue
		resolvedSourceID := result.ResolvedID

		// Warn if source issue is already closed
		if sourceIssue.Status == types.StatusClosed {
			fmt.Fprintf(os.Stderr, "%s Source issue %s is already closed\n", ui.RenderWarn("⚠"), resolvedSourceID)
		}

		// Step 2: Find the town-level beads directory
		townBeadsDir, err := findTownBeadsDir()
		if err != nil {
			FatalError("cannot refile: %v", err)
		}

		// Step 3: Resolve the target rig's beads directory
		targetBeadsDir, targetPrefix, err := routing.ResolveBeadsDirForRig(targetRig, townBeadsDir)
		if err != nil {
			FatalError("%v", err)
		}

		// Check we're not refiling to the same rig
		sourcePrefix := routing.ExtractPrefix(resolvedSourceID)
		if sourcePrefix == targetPrefix {
			FatalError("source issue %s is already in rig %q", resolvedSourceID, targetRig)
		}

		// Step 4: Open storage for the target rig
		targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
		targetStore, err := sqlite.New(ctx, targetDBPath)
		if err != nil {
			FatalError("failed to open target rig database: %v", err)
		}
		defer func() {
			if err := targetStore.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to close target rig database: %v\n", err)
			}
		}()

		// Step 5: Create the new issue in target rig (copy all fields)
		newIssue := &types.Issue{
			// Don't copy ID - let target rig generate new one
			Title:              sourceIssue.Title,
			Description:        sourceIssue.Description,
			Design:             sourceIssue.Design,
			AcceptanceCriteria: sourceIssue.AcceptanceCriteria,
			Status:             types.StatusOpen, // Always start as open
			Priority:           sourceIssue.Priority,
			IssueType:          sourceIssue.IssueType,
			Assignee:           sourceIssue.Assignee,
			ExternalRef:        sourceIssue.ExternalRef,
			EstimatedMinutes:   sourceIssue.EstimatedMinutes,
			SourceRepo:         sourceIssue.SourceRepo,
			Ephemeral:          sourceIssue.Ephemeral,
			MolType:            sourceIssue.MolType,
			RoleType:           sourceIssue.RoleType,
			Rig:                sourceIssue.Rig,
			CreatedBy:          actor,
		}

		// Append refiled note to description
		if newIssue.Description != "" {
			newIssue.Description += "\n\n"
		}
		newIssue.Description += fmt.Sprintf("(Refiled from %s)", resolvedSourceID)

		if err := targetStore.CreateIssue(ctx, newIssue, actor); err != nil {
			FatalError("failed to create issue in target rig: %v", err)
		}

		// Step 6: Copy labels if any
		labels, err := result.Store.GetLabels(ctx, resolvedSourceID)
		if err == nil && len(labels) > 0 {
			for _, label := range labels {
				if err := targetStore.AddLabel(ctx, newIssue.ID, label, actor); err != nil {
					WarnError("failed to copy label %s: %v", label, err)
				}
			}
		}

		// Step 7: Close the source issue (unless --keep-open)
		if !keepOpen {
			closeReason := fmt.Sprintf("Refiled to %s", newIssue.ID)
			if err := result.Store.CloseIssue(ctx, resolvedSourceID, closeReason, actor); err != nil {
				WarnError("failed to close source issue: %v", err)
			}
			// Schedule auto-flush if source was local store
			if !result.Routed {
				markDirtyAndScheduleFlush()
			}
		}

		// Output
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"source": resolvedSourceID,
				"target": newIssue.ID,
				"closed": !keepOpen,
			})
		} else {
			fmt.Printf("%s Refiled %s → %s\n", ui.RenderPass("✓"), resolvedSourceID, newIssue.ID)
			if !keepOpen {
				fmt.Printf("  Source issue closed\n")
			}
		}
	},
}

func init() {
	refileCmd.Flags().Bool("keep-open", false, "Keep the source issue open (don't close it)")
	rootCmd.AddCommand(refileCmd)
}
