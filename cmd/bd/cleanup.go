package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// CleanupEmptyResponse is returned when there are no closed issues to delete
type CleanupEmptyResponse struct {
	DeletedCount int    `json:"deleted_count"`
	Message      string `json:"message"`
	Filter       string `json:"filter,omitempty"`
	Ephemeral    bool   `json:"ephemeral,omitempty"`
	// RetentionRequired counts the closed issues kept because their type is
	// one the workspace retains (types.retained); absent when none were.
	RetentionRequired int `json:"retention_required,omitempty"`
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
- Skips pinned issues, and issues of a type the workspace's types.retained
  setting names (their ids are idempotency keys; only bd prune/purge
  --retain-ids deletes them); --json reports the latter as retention_required
- Supports --cascade to delete dependents (refused while types.retained names
  a type, since a cascade could reach a retained issue)
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
		warnIgnoredYAMLRetainedTypes()

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

		// The closed issues and types.retained come from one snapshot, so the
		// hold-back and the --cascade refusal use the setting it held.
		closedIssues, retainedSetting, err := storage.SearchIssuesWithConfig(ctx, store, "", filter, issueops.SettingKeyTypesRetained)
		if err != nil {
			return HandleError("listing issues: %v", err)
		}
		selection, err := selectCleanupCandidates(closedIssues, retainedSetting, cascade)
		if err != nil {
			return HandleError("%v", err)
		}

		if selection.Pinned > 0 && !jsonOutput {
			fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", selection.Pinned)
		}
		if selection.RetentionRequired > 0 && !jsonOutput {
			fmt.Println(retainedTypesKeptLine(selection.RetentionRequired, "closed issue(s)"))
		}

		if len(selection.IDs) == 0 {
			if jsonOutput {
				result := CleanupEmptyResponse{
					DeletedCount:      0,
					Message:           "No closed issues to delete",
					RetentionRequired: selection.RetentionRequired,
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

		issueIDs := selection.IDs

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
				fmt.Printf("Found %d %s issue(s) older than %d days\n", len(issueIDs), issueType, olderThanDays)
			} else {
				fmt.Printf("Found %d %s issue(s)\n", len(issueIDs), issueType)
			}
			if dryRun {
				fmt.Println(ui.RenderWarn("DRY RUN - no changes will be made"))
			}
			fmt.Println()
		}

		if err := deleteBatchReporting(issueIDs, force, dryRun, cascade, jsonOutput, nil, retentionReport(selection.RetentionRequired)); err != nil {
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

// cleanupSelection is what `bd admin cleanup` deletes and what it held back.
type cleanupSelection struct {
	IDs               []string
	Pinned            int
	RetentionRequired int
}

// selectCleanupCandidates narrows the closed issues `bd admin cleanup` found
// to the ones it may delete: pinned issues are protected, and so are issues
// of a type the workspace retains (retainedSetting is the stored value of
// types.retained). Cleanup deletes through the Deleter role, which writes no
// tombstone, so a retained issue is never its to take.
//
// --cascade is refused while anything is retained: the role expands a cascade
// to every dependent past this selection, so it could delete a retained issue
// the selection just held back.
func selectCleanupCandidates(closed []*types.Issue, retainedSetting string, cascade bool) (cleanupSelection, error) {
	var selection cleanupSelection
	if cascade && len(workapi.RetainedTypesFromSetting(retainedSetting)) > 0 {
		return selection, fmt.Errorf("--cascade is refused while %s names a type (%q): a cascade deletes dependents past the selection, "+
			"including issues of a retained type, without retiring their ids; run without --cascade",
			issueops.SettingKeyTypesRetained, retainedSetting)
	}
	unpinned := make([]*types.Issue, 0, len(closed))
	for _, issue := range closed {
		if issue.Pinned {
			selection.Pinned++
			continue
		}
		unpinned = append(unpinned, issue)
	}
	kept, retained := holdBackRetainedTypes(unpinned, retainedSetting)
	selection.RetentionRequired = retained
	selection.IDs = make([]string, len(kept))
	for i, issue := range kept {
		selection.IDs[i] = issue.ID
	}
	return selection, nil
}
