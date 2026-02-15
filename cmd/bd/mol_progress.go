package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molProgressCmd = &cobra.Command{
	Use:   "progress [molecule-id]",
	Short: "Show molecule progress summary",
	Long: `Show efficient progress summary for a molecule.

This command uses indexed queries to count progress without loading all steps,
making it suitable for very large molecules (millions of steps).

If no molecule-id is given, shows progress for any molecule you're working on.

Output includes:
  - Progress: completed / total (percentage)
  - Current step: the in-progress step (if any)
  - Rate: steps/hour based on closure times
  - ETA: estimated time to completion

Example:
  bd mol progress bd-hanoi-xyz`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// mol progress requires direct store access
		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		var moleculeID string
		if len(args) == 1 {
			// Explicit molecule ID given
			resolved, err := utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: molecule '%s' not found\n", args[0])
				os.Exit(1)
			}
			moleculeID = resolved
		} else {
			// Infer from in_progress work - use lightweight discovery
			moleculeIDs := findInProgressMoleculeIDs(ctx, store, actor)
			if len(moleculeIDs) == 0 {
				if jsonOutput {
					outputJSON([]interface{}{})
					return
				}
				fmt.Println("No molecules in progress.")
				fmt.Println("\nUse: bd mol progress <molecule-id>")
				return
			}
			// Show progress for first molecule
			moleculeID = moleculeIDs[0]
		}

		stats, err := store.GetMoleculeProgress(ctx, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			// Add computed fields for JSON output
			output := map[string]interface{}{
				"molecule_id":     stats.MoleculeID,
				"molecule_title":  stats.MoleculeTitle,
				"total":           stats.Total,
				"completed":       stats.Completed,
				"in_progress":     stats.InProgress,
				"current_step_id": stats.CurrentStepID,
			}
			if stats.Total > 0 {
				output["percent"] = float64(stats.Completed) * 100 / float64(stats.Total)
			}
			if stats.FirstClosed != nil && stats.LastClosed != nil && stats.Completed > 1 {
				duration := stats.LastClosed.Sub(*stats.FirstClosed)
				if duration > 0 {
					rate := float64(stats.Completed-1) / duration.Hours()
					output["rate_per_hour"] = rate
					remaining := stats.Total - stats.Completed
					if rate > 0 {
						etaHours := float64(remaining) / rate
						output["eta_hours"] = etaHours
					}
				}
			}
			outputJSON(output)
			return
		}

		// Human-readable output
		printMoleculeProgressStats(stats)
	},
}

// findInProgressMoleculeIDs finds molecule IDs with in_progress steps for an agent.
// This is a lightweight version that only returns IDs without loading subgraphs.
func findInProgressMoleculeIDs(ctx context.Context, s *dolt.DoltStore, agent string) []string {
	// Query for in_progress issues
	status := types.StatusInProgress
	filter := types.IssueFilter{Status: &status}
	if agent != "" {
		filter.Assignee = &agent
	}
	inProgressIssues, err := s.SearchIssues(ctx, "", filter)
	if err != nil || len(inProgressIssues) == 0 {
		return nil
	}

	// For each in_progress issue, find its parent molecule
	seen := make(map[string]bool)
	var moleculeIDs []string
	for _, issue := range inProgressIssues {
		moleculeID := findParentMolecule(ctx, s, issue.ID)
		if moleculeID != "" && !seen[moleculeID] {
			seen[moleculeID] = true
			moleculeIDs = append(moleculeIDs, moleculeID)
		}
	}

	return moleculeIDs
}

// printMoleculeProgressStats prints molecule progress in human-readable format
func printMoleculeProgressStats(stats *types.MoleculeProgressStats) {
	fmt.Printf("Molecule: %s (%s)\n", ui.RenderAccent(stats.MoleculeID), stats.MoleculeTitle)

	// Progress bar
	var percent float64
	if stats.Total > 0 {
		percent = float64(stats.Completed) * 100 / float64(stats.Total)
	}
	fmt.Printf("Progress: %s / %s (%.1f%%)\n",
		formatNumber(stats.Completed),
		formatNumber(stats.Total),
		percent)

	// Current step
	if stats.CurrentStepID != "" {
		fmt.Printf("Current step: %s\n", stats.CurrentStepID)
	} else if stats.InProgress > 0 {
		fmt.Printf("In progress: %d step(s)\n", stats.InProgress)
	}

	// Rate calculation
	if stats.FirstClosed != nil && stats.LastClosed != nil && stats.Completed > 1 {
		duration := stats.LastClosed.Sub(*stats.FirstClosed)
		if duration > 0 {
			// Rate is (completed - 1) because we need at least 2 points to measure rate
			rate := float64(stats.Completed-1) / duration.Hours()
			fmt.Printf("Rate: ~%.0f steps/hour\n", rate)

			// ETA
			remaining := stats.Total - stats.Completed
			if rate > 0 && remaining > 0 {
				etaHours := float64(remaining) / rate
				fmt.Printf("ETA: %s remaining\n", formatDuration(etaHours))
			}
		}
	}
}

// formatNumber formats large numbers with commas (handles millions)
func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}
	millions := n / 1000000
	thousands := (n % 1000000) / 1000
	ones := n % 1000
	return fmt.Sprintf("%d,%03d,%03d", millions, thousands, ones)
}

// formatDuration formats hours as a human-readable duration
func formatDuration(hours float64) string {
	if hours < 1 {
		minutes := hours * 60
		return fmt.Sprintf("~%.0f minutes", minutes)
	}
	if hours < 24 {
		return fmt.Sprintf("~%.1f hours", hours)
	}
	days := hours / 24
	if days < 7 {
		return fmt.Sprintf("~%.1f days", days)
	}
	weeks := days / 7
	return fmt.Sprintf("~%.1f weeks", weeks)
}

func init() {
	molCmd.AddCommand(molProgressCmd)
}
