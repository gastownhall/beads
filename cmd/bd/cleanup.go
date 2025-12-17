package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// Hard delete mode: bypass tombstone TTL safety, use --older-than days directly

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

Delete only closed ephemeral issues (transient messages):
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
		ephemeralOnly, _ := cmd.Flags().GetBool("ephemeral")

		// Calculate custom TTL for --hard mode
		// When --hard is set, use --older-than days as the tombstone TTL cutoff
		// This bypasses the default 30-day tombstone safety period
		var customTTL time.Duration
		if hardDelete {
			if olderThanDays > 0 {
				customTTL = time.Duration(olderThanDays) * 24 * time.Hour
			} else {
				// --hard without --older-than: prune ALL tombstones (use 1 second TTL)
				customTTL = time.Second
			}
			if !jsonOutput && !dryRun {
				fmt.Println(color.YellowString("⚠️  HARD DELETE MODE: Bypassing tombstone TTL safety"))
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

		// Add ephemeral filter if specified (bd-kwro.9)
		if ephemeralOnly {
			ephemeralTrue := true
			filter.Ephemeral = &ephemeralTrue
		}

		// Get all closed issues matching filter
		closedIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing issues: %v\n", err)
			os.Exit(1)
		}

		if len(closedIssues) == 0 {
			if jsonOutput {
				result := map[string]interface{}{
					"deleted_count": 0,
					"message":       "No closed issues to delete",
				}
				if olderThanDays > 0 {
					result["filter"] = fmt.Sprintf("older than %d days", olderThanDays)
				}
				if ephemeralOnly {
					result["ephemeral"] = true
				}
				output, _ := json.MarshalIndent(result, "", "  ")
				fmt.Println(string(output))
			} else {
				msg := "No closed issues to delete"
				if ephemeralOnly && olderThanDays > 0 {
					msg = fmt.Sprintf("No closed ephemeral issues older than %d days to delete", olderThanDays)
				} else if ephemeralOnly {
					msg = "No closed ephemeral issues to delete"
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
			if ephemeralOnly {
				issueType = "closed ephemeral"
			}
			fmt.Fprintf(os.Stderr, "Would delete %d %s issue(s). Use --force to confirm or --dry-run to preview.\n", len(issueIDs), issueType)
			os.Exit(1)
		}

		if !jsonOutput {
			issueType := "closed"
			if ephemeralOnly {
				issueType = "closed ephemeral"
			}
			if olderThanDays > 0 {
				fmt.Printf("Found %d %s issue(s) older than %d days\n", len(closedIssues), issueType, olderThanDays)
			} else {
				fmt.Printf("Found %d %s issue(s)\n", len(closedIssues), issueType)
			}
			if dryRun {
				fmt.Println(color.YellowString("DRY RUN - no changes will be made"))
			}
			fmt.Println()
		}

		// Use the existing batch deletion logic
		deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, "cleanup")

		// Also prune expired tombstones (bd-08ea)
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
					green := color.New(color.FgGreen).SprintFunc()
					ttlMsg := fmt.Sprintf("older than %d days", tombstoneResult.TTLDays)
					if hardDelete && olderThanDays == 0 {
						ttlMsg = "all tombstones (--hard mode)"
					}
					fmt.Printf("\n%s Pruned %d expired tombstone(s) (%s)\n",
						green("✓"), tombstoneResult.PrunedCount, ttlMsg)
				}
			}
		}
	},
}

func init() {
	cleanupCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows error)")
	cleanupCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	cleanupCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	cleanupCmd.Flags().Int("older-than", 0, "Only delete issues closed more than N days ago (0 = all closed issues)")
	cleanupCmd.Flags().Bool("hard", false, "Bypass tombstone TTL safety; use --older-than days as cutoff")
	cleanupCmd.Flags().Bool("ephemeral", false, "Only delete closed ephemeral issues (transient messages)")
	rootCmd.AddCommand(cleanupCmd)
}
