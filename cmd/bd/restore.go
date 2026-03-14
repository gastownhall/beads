package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/storage"
	"github.com/gastownhall/beads/internal/types"
	"github.com/gastownhall/beads/internal/ui"
)

var restoreCmd = &cobra.Command{
	Use:     "restore <issue-id>",
	GroupID: "sync",
	Short:   "Restore full history of a compacted issue from Dolt history",
	Long: `Restore full history of a compacted issue from Dolt version history.

When an issue is compacted, its description and notes are truncated.
This command queries Dolt's history tables to find the pre-compaction
version and displays the full issue content.

This is read-only and does not modify the database.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
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
			os.Exit(1)
		}

		// Check if issue is compacted
		if issue.CompactionLevel == 0 {
			fmt.Fprintf(os.Stderr, "Error: issue %s is not compacted\n", issueID)
			fmt.Fprintf(os.Stderr, "Hint: only compacted issues need restoration\n")
			os.Exit(1)
		}

		// Query Dolt history for the pre-compaction version
		history, err := store.History(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to query history: %v\n", err)
			os.Exit(1)
		}

		if len(history) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no history found for issue %s\n", issueID)
			fmt.Fprintf(os.Stderr, "Hint: issue may have been compacted before Dolt history was available\n")
			os.Exit(1)
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
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(best.Issue)
		} else {
			displayRestoredIssue(best.Issue, best.CommitHash)
		}
	},
}

// issueContentSize returns the total text content size of an issue.
func issueContentSize(issue *types.Issue) int {
	return len(issue.Description) + len(issue.Design) + len(issue.AcceptanceCriteria) + len(issue.Notes)
}

func init() {
	restoreCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output restore results in JSON format")
	rootCmd.AddCommand(restoreCmd)
}

// displayRestoredIssue displays the restored issue in a readable format
func displayRestoredIssue(issue *types.Issue, commitHash string) {
	hashDisplay := commitHash
	if len(hashDisplay) > 8 {
		hashDisplay = hashDisplay[:8]
	}
	fmt.Printf("\n%s %s (restored from Dolt commit %s)\n", ui.RenderAccent("📜"), ui.RenderBold(issue.ID), ui.RenderWarn(hashDisplay))
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
