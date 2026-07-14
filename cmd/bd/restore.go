package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var restoreApply bool

var restoreCmd = &cobra.Command{
	Use:           "restore <issue-id>",
	GroupID:       "sync",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Restore the pre-compaction content of a compacted issue",
	Long: `Restore the pre-compaction content of a compacted issue.

When an issue is compacted, its description/design/notes/acceptance criteria
are summarized and the originals are archived to a compaction snapshot. This
command recovers that original content.

By default it is read-only: it displays the archived content without modifying
the database. Pass --apply to write the original content back into the issue
and step its compaction level back down.

If no archived snapshot exists (e.g. the issue was compacted by an older bd
before snapshot archiving), restore falls back to a best-effort reconstruction
from Dolt version history, which can only be displayed, not applied.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("restore is not supported in proxied-server mode")
		}
		issueID := args[0]
		ctx := rootCtx

		// Get the issue
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				fmt.Fprintf(os.Stderr, "Error: issue '%s' not found\n", issueID)
			} else {
				fmt.Fprintf(os.Stderr, "Error: issue '%s' not found: %v\n", issueID, err)
			}
			return SilentExit()
		}

		// Check if issue is compacted
		if issue.CompactionLevel == 0 {
			fmt.Fprintf(os.Stderr, "Error: issue %s is not compacted\n", issueID)
			fmt.Fprintf(os.Stderr, "Hint: only compacted issues need restoration\n")
			return SilentExit()
		}

		// Prefer the archived snapshot: it is the authoritative pre-compaction
		// copy and the only source that can be safely written back.
		snap, err := store.GetCompactionSnapshot(ctx, issueID)
		if err != nil {
			return HandleError("failed to read compaction snapshot: %v", err)
		}

		if restoreApply {
			if snap == nil {
				fmt.Fprintf(os.Stderr, "Error: no archived snapshot for %s; cannot safely restore content\n", issueID)
				fmt.Fprintf(os.Stderr, "Hint: this issue was compacted before snapshot archiving existed.\n")
				fmt.Fprintf(os.Stderr, "      Run 'bd restore %s' (without --apply) to view the best-effort\n", issueID)
				fmt.Fprintf(os.Stderr, "      version reconstructed from Dolt history.\n")
				return SilentExit()
			}
			applied, err := store.RestoreFromSnapshot(ctx, issueID)
			if err != nil {
				return HandleError("failed to restore issue: %v", err)
			}
			if applied == nil {
				return HandleError("no archived snapshot for %s", issueID)
			}
			restored, err := store.GetIssue(ctx, issueID)
			if err != nil {
				return HandleError("restored, but failed to re-read issue: %v", err)
			}
			if jsonOutput {
				if err := outputJSON(restored); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return nil
			}
			fmt.Printf("%s Restored %s from archived snapshot (compaction level %d → %d)\n",
				ui.RenderPass("✓"), issueID, issue.CompactionLevel, restored.CompactionLevel)
			return nil
		}

		// Read-only display path. Prefer the archived snapshot; fall back to the
		// Dolt-history heuristic when no snapshot exists.
		if snap != nil {
			view := snapshotView(issue, snap)
			if jsonOutput {
				if err := outputJSON(view); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
			} else {
				displayRestoredIssue(view, "archived snapshot")
				fmt.Printf("%s\n", ui.RenderMuted("Run 'bd restore "+issueID+" --apply' to write this content back."))
			}
			return nil
		}

		// Query Dolt history for the pre-compaction version
		history, err := store.History(ctx, issueID)
		if err != nil {
			return HandleError("failed to query history: %v", err)
		}

		if len(history) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no history found for issue %s\n", issueID)
			fmt.Fprintf(os.Stderr, "Hint: issue may have been compacted before Dolt history was available\n")
			return SilentExit()
		}

		// Find the pre-compaction version: the history entry with the most content.
		// History is ordered by commit_date DESC, so we scan all entries.
		var best *storage.HistoryEntry
		bestSize := 0
		for _, entry := range history {
			size := issueContentSize(entry.Issue)
			if size > bestSize {
				bestSize = size
				best = entry
			}
		}

		if best == nil || bestSize <= issueContentSize(issue) {
			fmt.Fprintf(os.Stderr, "Error: no pre-compaction version found in Dolt history\n")
			fmt.Fprintf(os.Stderr, "Hint: issue may have been compacted before Dolt history was available\n")
			return SilentExit()
		}

		if jsonOutput {
			if err := outputJSON(best.Issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		} else {
			hashDisplay := best.CommitHash
			if len(hashDisplay) > 8 {
				hashDisplay = hashDisplay[:8]
			}
			displayRestoredIssue(best.Issue, "Dolt commit "+hashDisplay)
		}
		return nil
	},
}

// snapshotView returns a copy of the current issue with its text content
// overlaid by the archived pre-compaction snapshot, for read-only display.
func snapshotView(issue *types.Issue, snap *types.IssueSnapshot) *types.Issue {
	view := *issue
	if snap.Title != "" {
		view.Title = snap.Title
	}
	view.Description = snap.Description
	view.Design = snap.Design
	view.Notes = snap.Notes
	view.AcceptanceCriteria = snap.AcceptanceCriteria
	return &view
}

// issueContentSize returns the total text content size of an issue.
func issueContentSize(issue *types.Issue) int {
	return len(issue.Description) + len(issue.Design) + len(issue.AcceptanceCriteria) + len(issue.Notes)
}

func init() {
	restoreCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output restore results in JSON format")
	restoreCmd.Flags().BoolVar(&restoreApply, "apply", false, "Write the restored content back into the issue (default: display only)")
	rootCmd.AddCommand(restoreCmd)
}

// displayRestoredIssue displays the restored issue in a readable format.
// provenance describes where the content came from (e.g. "archived snapshot"
// or "Dolt commit abc12345").
func displayRestoredIssue(issue *types.Issue, provenance string) {
	fmt.Printf("\n%s %s (restored from %s)\n", ui.RenderAccent("📜"), ui.RenderBold(issue.ID), ui.RenderWarn(provenance))
	fmt.Printf("%s\n\n", ui.RenderBold(issue.Title))

	if issue.Description != "" {
		fmt.Printf("%s\n%s\n\n", ui.RenderBold("Description:"), issue.Description)
	}

	if issue.Design != "" {
		fmt.Printf("%s\n%s\n\n", ui.RenderBold("Design:"), issue.Design)
	}

	if issue.AcceptanceCriteria != "" {
		fmt.Printf("%s\n%s\n\n", ui.RenderBold("Acceptance Criteria:"), issue.AcceptanceCriteria)
	}

	if issue.Notes != "" {
		fmt.Printf("%s\n%s\n\n", ui.RenderBold("Notes:"), issue.Notes)
	}

	fmt.Printf("%s %s | %s %d | %s %s\n",
		ui.RenderBold("Status:"), issue.Status,
		ui.RenderBold("Priority:"), issue.Priority,
		ui.RenderBold("Type:"), issue.IssueType,
	)

	if issue.Assignee != "" {
		fmt.Printf("%s %s\n", ui.RenderBold("Assignee:"), issue.Assignee)
	}

	if len(issue.Labels) > 0 {
		fmt.Printf("%s %s\n", ui.RenderBold("Labels:"), strings.Join(issue.Labels, ", "))
	}

	fmt.Printf("\n%s %s\n", ui.RenderBold("Created:"), issue.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("%s %s\n", ui.RenderBold("Updated:"), issue.UpdatedAt.Format("2006-01-02 15:04:05"))
	if issue.ClosedAt != nil {
		fmt.Printf("%s %s\n", ui.RenderBold("Closed:"), issue.ClosedAt.Format("2006-01-02 15:04:05"))
	}

	if len(issue.Dependencies) > 0 {
		fmt.Printf("\n%s\n", ui.RenderBold("Dependencies:"))
		for _, dep := range issue.Dependencies {
			fmt.Printf("  %s %s (%s)\n", ui.RenderPass("→"), dep.DependsOnID, dep.Type)
		}
	}

	if issue.CompactionLevel > 0 {
		fmt.Printf("\n%s Level %d", ui.RenderWarn("⚠️  This issue was compacted:"), issue.CompactionLevel)
		if issue.CompactedAt != nil {
			fmt.Printf(" at %s", issue.CompactedAt.Format("2006-01-02 15:04:05"))
		}
		if issue.OriginalSize > 0 {
			currentSize := len(issue.Description) + len(issue.Design) + len(issue.AcceptanceCriteria) + len(issue.Notes)
			reduction := 100 * (1 - float64(currentSize)/float64(issue.OriginalSize))
			fmt.Printf(" (%.1f%% size reduction)", reduction)
		}
		fmt.Println()
	}

	fmt.Println()
}
