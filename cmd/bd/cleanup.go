package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// CleanupEmptyResponse is returned when there are no closed issues to delete
type CleanupEmptyResponse struct {
	DeletedCount int    `json:"deleted_count"`
	Message      string `json:"message"`
	Filter       string `json:"filter,omitempty"`
	Ephemeral    bool   `json:"ephemeral,omitempty"`
}

// Hard delete mode: bypass tombstone TTL safety, use --older-than days directly

// showCleanupDeprecationHint shows a hint about bd doctor --fix
func showCleanupDeprecationHint() {
	fmt.Fprintln(os.Stderr, ui.RenderMuted("💡 Tip: 'bd doctor --fix' can now cleanup stale issues and prune tombstones"))
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Delete closed issues and prune expired tombstones",
	Long: `Delete closed issues and prune expired tombstones to reduce database size.

This command:
1. Converts closed issues to tombstones (soft delete)
2. Prunes expired tombstones (older than 30 days) from issues.jsonl

It does NOT remove temporary files - use 'bd clean' for that.

By default, deletes ALL closed issues. Use --older-than to only delete
issues closed before a certain date.

HARD DELETE MODE:
Use --hard to bypass the 30-day tombstone safety period. When combined with
--older-than, tombstones older than N days are permanently removed from JSONL.
This is useful for cleaning house when you know old clones won't resurrect issues.

WARNING: --hard bypasses sync safety. Deleted issues may resurrect if an old
clone syncs before you've cleaned up all clones.

EXAMPLES:
Delete all closed issues and prune tombstones:
  bd cleanup --force

Delete issues closed more than 30 days ago:
  bd cleanup --older-than 30 --force

Delete only closed wisps (transient molecules):
  bd cleanup --ephemeral --force

Preview what would be deleted/pruned:
  bd cleanup --dry-run
  bd cleanup --older-than 90 --dry-run

Hard delete: permanently remove issues/tombstones older than 3 days:
  bd cleanup --older-than 3 --hard --force

SAFETY:
- Requires --force flag to actually delete (unless --dry-run)
- Supports --cascade to delete dependents
- Shows preview of what will be deleted
- Use --json for programmatic output

SEE ALSO:
  bd clean      Remove temporary git merge artifacts
  bd compact    Run compaction on issues`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		cascade, _ := cmd.Flags().GetBool("cascade")
		olderThanDays, _ := cmd.Flags().GetInt("older-than")
		hardDelete, _ := cmd.Flags().GetBool("hard")
		wispOnly, _ := cmd.Flags().GetBool("ephemeral")

		// Calculate custom TTL for --hard mode
		// When --hard is set, use --older-than days as the tombstone TTL cutoff
		// This bypasses the default 30-day tombstone safety period
		var customTTL time.Duration
		if hardDelete {
			if olderThanDays > 0 {
				customTTL = time.Duration(olderThanDays) * 24 * time.Hour
			} else {
				// --hard without --older-than: prune ALL tombstones immediately
				// Negative TTL means "immediately expired" (bd-4q8 fix)
				customTTL = -1
			}
			if !jsonOutput && !dryRun {
				fmt.Println(ui.RenderWarn("⚠️  HARD DELETE MODE: Bypassing tombstone TTL safety"))
			}
		}

		// Ensure we have storage
		if daemonClient != nil {
			if err := ensureDirectMode("daemon does not support delete command"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store == nil {
			if err := ensureStoreActive(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		ctx := rootCtx

		// Build filter for closed issues
		statusClosed := types.StatusClosed
		filter := types.IssueFilter{
			Status: &statusClosed,
		}

		// Add age filter if specified
		if olderThanDays > 0 {
			cutoffTime := time.Now().AddDate(0, 0, -olderThanDays)
			filter.ClosedBefore = &cutoffTime
		}

		// Add wisp filter if specified (bd-kwro.9)
		if wispOnly {
			wispTrue := true
			filter.Ephemeral = &wispTrue
		}

		// Get all closed issues matching filter
		closedIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing issues: %v\n", err)
			os.Exit(1)
		}

		// Filter out pinned issues - they are protected from cleanup (bd-b2k)
		pinnedCount := 0
		filteredIssues := make([]*types.Issue, 0, len(closedIssues))
		for _, issue := range closedIssues {
			if issue.Pinned {
				pinnedCount++
				continue
			}
			filteredIssues = append(filteredIssues, issue)
		}
		closedIssues = filteredIssues

		if pinnedCount > 0 && !jsonOutput {
			fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", pinnedCount)
		}

		if len(closedIssues) == 0 {
			if jsonOutput {
				result := CleanupEmptyResponse{
					DeletedCount: 0,
					Message:      "No closed issues to delete",
				}
				if olderThanDays > 0 {
					result.Filter = fmt.Sprintf("older than %d days", olderThanDays)
				}
				if wispOnly {
					result.Ephemeral = true
				}
				outputJSON(result)
			} else {
				msg := "No closed issues to delete"
				if wispOnly && olderThanDays > 0 {
					msg = fmt.Sprintf("No closed wisps older than %d days to delete", olderThanDays)
				} else if wispOnly {
					msg = "No closed wisps to delete"
				} else if olderThanDays > 0 {
					msg = fmt.Sprintf("No closed issues older than %d days to delete", olderThanDays)
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

		// Show preview
		if !force && !dryRun {
			issueType := "closed"
			if wispOnly {
				issueType = "closed wisp"
			}
			fmt.Fprintf(os.Stderr, "Would delete %d %s issue(s). Use --force to confirm or --dry-run to preview.\n", len(issueIDs), issueType)
			os.Exit(1)
		}

		if !jsonOutput {
			issueType := "closed"
			if wispOnly {
				issueType = "closed wisp"
			}
			if olderThanDays > 0 {
				fmt.Printf("Found %d %s issue(s) older than %d days\n", len(closedIssues), issueType, olderThanDays)
			} else {
				fmt.Printf("Found %d %s issue(s)\n", len(closedIssues), issueType)
			}
			if dryRun {
				fmt.Println(ui.RenderWarn("DRY RUN - no changes will be made"))
			}
			fmt.Println()
		}

		// Use the existing batch deletion logic
		// Note: cleanup always creates tombstones first; --hard prunes them after
		deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, false, "cleanup")

		// Also prune expired tombstones
		// This runs after closed issues are converted to tombstones, cleaning up old ones
		// In --hard mode, customTTL overrides the default 30-day TTL
		if dryRun {
			// Preview what tombstones would be pruned
			tombstoneResult, err := previewPruneTombstones(customTTL)
			if err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to check tombstones: %v\n", err)
				}
			} else if tombstoneResult != nil && tombstoneResult.PrunedCount > 0 {
				if !jsonOutput {
					ttlMsg := fmt.Sprintf("older than %d days", tombstoneResult.TTLDays)
					if hardDelete && olderThanDays == 0 {
						ttlMsg = "all tombstones (--hard mode)"
					}
					fmt.Printf("\nExpired tombstones that would be pruned: %d (%s)\n",
						tombstoneResult.PrunedCount, ttlMsg)
				}
			}
		} else if force {
			// Actually prune expired tombstones
			tombstoneResult, err := pruneExpiredTombstones(customTTL)
			if err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to prune expired tombstones: %v\n", err)
				}
			} else if tombstoneResult != nil && tombstoneResult.PrunedCount > 0 {
				if !jsonOutput {
					ttlMsg := fmt.Sprintf("older than %d days", tombstoneResult.TTLDays)
					if hardDelete && olderThanDays == 0 {
						ttlMsg = "all tombstones (--hard mode)"
					}
					fmt.Printf("\n%s Pruned %d expired tombstone(s) (%s)\n",
						ui.RenderPass("✓"), tombstoneResult.PrunedCount, ttlMsg)
				}
			}
		}

		// bd-bqcc: Show hint about doctor --fix consolidation
		if !jsonOutput {
			showCleanupDeprecationHint()
		}
	},
}

func init() {
	cleanupCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows error)")
	cleanupCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	cleanupCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	cleanupCmd.Flags().Int("older-than", 0, "Only delete issues closed more than N days ago (0 = all closed issues)")
	cleanupCmd.Flags().Bool("hard", false, "Bypass tombstone TTL safety; use --older-than days as cutoff")
	cleanupCmd.Flags().Bool("ephemeral", false, "Only delete closed wisps (transient molecules)")
	// Note: cleanupCmd is added to adminCmd in admin.go
}
