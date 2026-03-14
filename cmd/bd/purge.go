package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/types"
	"github.com/gastownhall/beads/internal/ui"
)

var purgeCmd = &cobra.Command{
	Use:     "purge",
	GroupID: "maint",
	Short:   "Delete closed ephemeral beads to reclaim space",
	Long: `Permanently delete closed ephemeral beads and their associated data.

Closed ephemeral beads (wisps, transient molecules) accumulate rapidly and
have no value once closed. This command removes them to reclaim storage.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads (protected).

EXAMPLES:
  bd purge                           # Preview what would be purged
  bd purge --force                   # Delete all closed ephemeral beads
  bd purge --older-than 7d --force   # Only purge items closed 7+ days ago
  bd purge --pattern "*-wisp-*"      # Only purge matching ID pattern
  bd purge --dry-run                 # Detailed preview with stats`,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("purge")

		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		olderThan, _ := cmd.Flags().GetString("older-than")
		pattern, _ := cmd.Flags().GetString("pattern")

		if store == nil {
			if err := ensureStoreActive(); err != nil {
				FatalError("%v", err)
			}
		}

		ctx := rootCtx

		// Build filter: closed + ephemeral
		statusClosed := types.StatusClosed
		wispTrue := true
		filter := types.IssueFilter{
			Status:    &statusClosed,
			Ephemeral: &wispTrue,
		}

		// Parse --older-than duration (e.g., "7d", "30d", "24h", or just "30" for days)
		if olderThan != "" {
			days, err := parseHumanDuration(olderThan)
			if err != nil {
				FatalError("invalid --older-than value %q: %v", olderThan, err)
			}
			cutoff := time.Now().AddDate(0, 0, -days)
			filter.ClosedBefore = &cutoff
		}

		// Get matching issues
		closedIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			FatalError("listing issues: %v", err)
		}

		// Filter by ID pattern if specified
		if pattern != "" {
			var matched []*types.Issue
			for _, issue := range closedIssues {
				if ok, _ := filepath.Match(pattern, issue.ID); ok {
					matched = append(matched, issue)
				}
			}
			closedIssues = matched
		}

		// Filter out pinned beads
		pinnedCount := 0
		filtered := make([]*types.Issue, 0, len(closedIssues))
		for _, issue := range closedIssues {
			if issue.Pinned {
				pinnedCount++
				continue
			}
			filtered = append(filtered, issue)
		}
		closedIssues = filtered

		// Report
		if len(closedIssues) == 0 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"purged_count": 0,
					"message":      "No closed ephemeral beads to purge",
				})
			} else {
				msg := "No closed ephemeral beads to purge"
				if olderThan != "" {
					msg += fmt.Sprintf(" (older than %s)", olderThan)
				}
				if pattern != "" {
					msg += fmt.Sprintf(" (matching %q)", pattern)
				}
				fmt.Println(msg)
			}
			return
		}

		// Extract IDs
		issueIDs := make([]string, len(closedIssues))
		for i, issue := range closedIssues {
			issueIDs[i] = issue.ID
		}

		// Dry-run: show stats preview
		if dryRun {
			result, err := store.DeleteIssues(ctx, issueIDs, false, false, true)
			if jsonOutput {
				stats := map[string]interface{}{
					"dry_run":      true,
					"purge_count":  len(issueIDs),
					"dependencies": 0,
					"labels":       0,
					"events":       0,
				}
				if err == nil {
					stats["dependencies"] = result.DependenciesCount
					stats["labels"] = result.LabelsCount
					stats["events"] = result.EventsCount
				}
				if pinnedCount > 0 {
					stats["pinned_skipped"] = pinnedCount
				}
				outputJSON(stats)
			} else {
				fmt.Printf("Would purge %d closed ephemeral bead(s)\n", len(issueIDs))
				if err == nil {
					fmt.Printf("  Dependencies: %d\n", result.DependenciesCount)
					fmt.Printf("  Labels:       %d\n", result.LabelsCount)
					fmt.Printf("  Events:       %d\n", result.EventsCount)
				}
				if pinnedCount > 0 {
					fmt.Printf("  Pinned (skipped): %d\n", pinnedCount)
				}
				fmt.Printf("\n(Dry-run mode — no changes made)\n")
			}
			return
		}

		// Preview mode (no --force)
		if !force {
			fmt.Printf("Found %d closed ephemeral bead(s) to purge\n", len(issueIDs))
			if pinnedCount > 0 {
				fmt.Printf("Skipping %d pinned bead(s)\n", pinnedCount)
			}
			hint := "bd purge --force"
			if olderThan != "" {
				hint += " --older-than " + olderThan
			}
			if pattern != "" {
				hint += " --pattern " + pattern
			}
			FatalErrorWithHint(
				fmt.Sprintf("would purge %d bead(s)", len(issueIDs)),
				fmt.Sprintf("Use --force to confirm or --dry-run to preview.\n  %s", hint))
		}

		// Actually purge
		result, err := store.DeleteIssues(ctx, issueIDs, false, true, false)
		if err != nil {
			FatalError("purge failed: %v", err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			stats := map[string]interface{}{
				"purged_count": result.DeletedCount,
				"dependencies": result.DependenciesCount,
				"labels":       result.LabelsCount,
				"events":       result.EventsCount,
			}
			if pinnedCount > 0 {
				stats["pinned_skipped"] = pinnedCount
			}
			outputJSON(stats)
		} else {
			fmt.Printf("%s Purged %d closed ephemeral bead(s)\n", ui.RenderPass("✓"), result.DeletedCount)
			fmt.Printf("  Dependencies removed: %d\n", result.DependenciesCount)
			fmt.Printf("  Labels removed:       %d\n", result.LabelsCount)
			fmt.Printf("  Events removed:       %d\n", result.EventsCount)
			if pinnedCount > 0 {
				fmt.Printf("  Pinned (skipped):     %d\n", pinnedCount)
			}
		}
	},
}

// parseHumanDuration parses a human-friendly duration string into days.
// Accepts: "7d", "30d", "24h", "2w", or just a number (treated as days).
func parseHumanDuration(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Plain number = days
	if days, err := strconv.Atoi(s); err == nil {
		if days <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return days, nil
	}

	// Parse suffix
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", numStr)
	}
	if num <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}

	switch unit {
	case 'h', 'H':
		days := num / 24
		if days == 0 {
			days = 1 // minimum 1 day
		}
		return days, nil
	case 'd', 'D':
		return num, nil
	case 'w', 'W':
		return num * 7, nil
	default:
		return 0, fmt.Errorf("unknown unit %q (use h, d, or w)", string(unit))
	}
}

func init() {
	purgeCmd.Flags().BoolP("force", "f", false, "Actually purge (without this, shows preview)")
	purgeCmd.Flags().Bool("dry-run", false, "Preview what would be purged with stats")
	purgeCmd.Flags().String("older-than", "", "Only purge beads closed more than N ago (e.g., 7d, 2w, 30)")
	purgeCmd.Flags().String("pattern", "", "Only purge beads matching ID glob pattern (e.g., *-wisp-*)")
	rootCmd.AddCommand(purgeCmd)
}
