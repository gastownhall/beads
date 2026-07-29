package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Delete closed issues to reduce database size",
	Long: `Delete closed issues to reduce database size.

This command permanently removes closed issues from the database.

NOTE: This command only manages issue lifecycle (closed -> deleted). For general
health checks and automatic repairs, use 'bd doctor --fix' instead.

By default, deletes ALL closed issues. Use --older-than to only delete
issues closed before a certain date.

EXAMPLES:
  bd admin cleanup --force                          # Delete all closed issues
  bd admin cleanup --older-than 30 --force          # Only issues closed 30+ days ago
  bd admin cleanup --ephemeral --force              # Only closed wisps (transient molecules)
  bd admin cleanup --dry-run                        # Preview what would be deleted

SAFETY:
- Requires --force flag to actually delete (unless --dry-run)
- Supports --cascade to delete dependents
- Shows preview of what will be deleted
- Use --json for programmatic output

SEE ALSO:
  bd doctor --fix    Automatic health checks and repairs (recommended for routine maintenance)
  bd admin compact   Compact old closed issues to save space`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("admin cleanup is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("admin-cleanup")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := requireServerMode("cleanup"); err != nil {
			return HandleError("%v", err)
		}
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		cascade, _ := cmd.Flags().GetBool("cascade")
		olderThanDays, _ := cmd.Flags().GetInt("older-than")
		wispOnly, _ := cmd.Flags().GetBool("ephemeral")

		if store == nil {
			if err := ensureStoreActive(); err != nil {
				return HandleError("%v", err)
			}
		}

		ctx := rootCtx

		// Build filter for closed issues. Cleanup is a scripted sweep — opt out
		// of BEADS_MAX_ROWS (designer §4.1) so a misconfigured env doesn't abort
		// cleanup mid-run and leave the database in an unswept state.
		statusClosed := types.StatusClosed
		filter := types.IssueFilter{
			Status:        &statusClosed,
			MaxRows:       0,
			MaxRowsSource: "",
		}

		if olderThanDays > 0 {
			cutoffTime := time.Now().AddDate(0, 0, -olderThanDays)
			filter.ClosedBefore = &cutoffTime
		}

		if wispOnly {
			wispTrue := true
			filter.Ephemeral = &wispTrue
		}

		closedIssues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleError("listing issues: %v", err)
		}

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
				if err := outputJSON(result); err != nil {
					return err
				}
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
			return nil
		}

		issueIDs := make([]string, len(closedIssues))
		for i, issue := range closedIssues {
			issueIDs[i] = issue.ID
		}

		if !force && !dryRun {
			issueType := "closed"
			if wispOnly {
				issueType = "closed wisp"
			}
			return HandleErrorWithHint(
				fmt.Sprintf("would delete %d %s issue(s)", len(issueIDs), issueType),
				"Use --force to confirm or --dry-run to preview.")
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

		if err := deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, false, "cleanup"); err != nil {
			return HandleError("%v", err)
		}
		return nil
	},
}

func init() {
	cleanupCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows error)")
	cleanupCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	cleanupCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	cleanupCmd.Flags().Int("older-than", 0, "Only delete issues closed more than N days ago (0 = all closed issues)")
	cleanupCmd.Flags().Bool("ephemeral", false, "Only delete closed wisps (transient molecules)")
	// Note: cleanupCmd is added to adminCmd in admin.go
}
