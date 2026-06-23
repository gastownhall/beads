package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// purgeScope parameterizes the shared purge/prune implementation so both
// commands can share filter plumbing, preview/dry-run/force semantics, and
// messaging without copying 200 lines of boilerplate.
type purgeScope struct {
	// cmdName is the user-visible command name (e.g. "purge", "prune").
	// Used in messages and the suggested `--force` hint.
	cmdName string
	// pastTense is the user-visible completed action (e.g. "purged", "pruned").
	pastTense string
	// countKey is the JSON key used for the actual deletion count.
	countKey string
	// dryRunCountKey is the JSON key used for the dry-run deletion count.
	dryRunCountKey string
	// subjectNoun describes what's being purged, in singular form
	// (e.g. "closed ephemeral bead", "closed bead"). "(s)" is appended by
	// the printer when multiple items are involved.
	subjectNoun string
	// ephemeralOnly restricts the filter to ephemeral beads when true.
	// When false, restricts to non-ephemeral beads — the scopes are
	// deliberately disjoint so `prune` never touches wisps that `purge`
	// would handle, and vice versa.
	ephemeralOnly bool
	// requireFilter forces the user to pass --older-than or --pattern.
	// Without this gate, `bd prune --force` would silently delete every
	// closed non-ephemeral bead in the repo.
	requireFilter bool
}

var purgeCmd = &cobra.Command{
	Use:     "purge",
	GroupID: "maint",
	Short:   "Delete closed ephemeral beads to reclaim space",
	Long: `Permanently delete closed ephemeral beads and their associated data.

Closed ephemeral beads (wisps, transient molecules) accumulate rapidly and
have no value once closed. This command removes them to reclaim storage.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads (protected).

To delete closed non-ephemeral beads (regular tasks, features, bugs, etc.)
use ` + "`bd prune`" + ` instead.

For full Dolt storage reclaim after deleting many rows, follow with ` + "`bd flatten`" + `
so history can be collapsed and old chunks can be garbage-collected.

EXAMPLES:
  bd purge                           # Preview what would be purged
  bd purge --force                   # Delete all closed ephemeral beads
  bd purge --older-than 7d --force   # Only purge items closed 7+ days ago
  bd purge --pattern "*-wisp-*"      # Only purge matching ID pattern
  bd purge --dry-run                 # Detailed preview with stats`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("purge")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runPurgeOrPrune(cmd, purgeScope{
			cmdName:        "purge",
			pastTense:      "purged",
			countKey:       "purged_count",
			dryRunCountKey: "purge_count",
			subjectNoun:    "closed ephemeral bead",
			ephemeralOnly:  true,
			requireFilter:  false,
		})
	},
}

func runPurgeOrPrune(cmd *cobra.Command, scope purgeScope) error {
	CheckReadonly(scope.cmdName)

	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	olderThan, _ := cmd.Flags().GetString("older-than")
	pattern, _ := cmd.Flags().GetString("pattern")

	if scope.requireFilter && olderThan == "" && pattern == "" {
		return HandleErrorWithHint(
			fmt.Sprintf("bd %s requires --older-than or --pattern", scope.cmdName),
			"Protects against accidental bulk deletion. Use `--pattern '*'` to\n"+
				"  include all closed beads in this scope, or `--older-than 1d`\n"+
				"  / `--pattern '<glob>'` to narrow the deletion.")
	}

	if store == nil {
		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
	}

	ctx := rootCtx

	statusClosed := types.StatusClosed
	ephemeralFlag := scope.ephemeralOnly
	filter := types.IssueFilter{
		Status:    &statusClosed,
		Ephemeral: &ephemeralFlag,
	}

	var cutoff *time.Time
	if olderThan != "" {
		days, err := parseHumanDuration(olderThan)
		if err != nil {
			return HandleErrorRespectJSON("invalid --older-than value %q: %v", olderThan, err)
		}
		cutoffTime := time.Now().UTC().AddDate(0, 0, -days)
		cutoff = &cutoffTime
		filter.ClosedBefore = cutoff
	}

	closedIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("listing issues: %v", err)
	}

	if pattern != "" {
		var matched []*types.Issue
		for _, issue := range closedIssues {
			if ok, _ := filepath.Match(pattern, issue.ID); ok {
				matched = append(matched, issue)
			}
		}
		closedIssues = matched
	}

	var safetyStats closedDeletionCandidateStats
	closedIssues, safetyStats = filterClosedDeletionCandidates(closedIssues, cutoff)
	pinnedCount := safetyStats.PinnedSkipped
	warnClosedDeletionSafetySkips(safetyStats)

	if len(closedIssues) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				scope.countKey: 0,
				"message":      fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName),
			})
		}
		msg := fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName)
		if olderThan != "" {
			msg += fmt.Sprintf(" (older than %s)", olderThan)
		}
		if pattern != "" {
			msg += fmt.Sprintf(" (matching %q)", pattern)
		}
		fmt.Println(msg)
		return nil
	}

	issueIDs := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		issueIDs[i] = issue.ID
	}

	if dryRun {
		result, err := store.DeleteIssues(ctx, issueIDs, false, false, true)
		if jsonOutput {
			stats := map[string]interface{}{
				"dry_run":            true,
				scope.dryRunCountKey: len(issueIDs),
				"dependencies":       0,
				"labels":             0,
				"events":             0,
			}
			if err == nil {
				stats["dependencies"] = result.DependenciesCount
				stats["labels"] = result.LabelsCount
				stats["events"] = result.EventsCount
			}
			if pinnedCount > 0 {
				stats["pinned_skipped"] = pinnedCount
			}
			return outputJSON(stats)
		}
		fmt.Printf("Would %s %d %s(s)\n", scope.cmdName, len(issueIDs), scope.subjectNoun)
		if err == nil {
			fmt.Printf("  Dependencies: %d\n", result.DependenciesCount)
			fmt.Printf("  Labels:       %d\n", result.LabelsCount)
			fmt.Printf("  Events:       %d\n", result.EventsCount)
		}
		if pinnedCount > 0 {
			fmt.Printf("  Pinned (skipped): %d\n", pinnedCount)
		}
		fmt.Printf("\n(Dry-run mode — no changes made)\n")
		return nil
	}

	if !force {
		fmt.Printf("Found %d %s(s) to %s\n", len(issueIDs), scope.subjectNoun, scope.cmdName)
		if pinnedCount > 0 {
			fmt.Printf("Skipping %d pinned bead(s)\n", pinnedCount)
		}
		hint := fmt.Sprintf("bd %s --force", scope.cmdName)
		if olderThan != "" {
			hint += " --older-than " + olderThan
		}
		if pattern != "" {
			hint += " --pattern " + pattern
		}
		return HandleErrorWithHint(
			fmt.Sprintf("would %s %d bead(s)", scope.cmdName, len(issueIDs)),
			fmt.Sprintf("Use --force to confirm or --dry-run to preview.\n  %s", hint))
	}

	result, err := store.DeleteIssues(ctx, issueIDs, false, true, false)
	if err != nil {
		return HandleErrorRespectJSON("%s failed: %v", scope.cmdName, err)
	}

	commandDidWrite.Store(true)
	if result.DeletedCount > 0 {
		commandMayEmptyJSONLExport.Store(true)
	}

	if jsonOutput {
		stats := map[string]interface{}{
			scope.countKey: result.DeletedCount,
			"dependencies": result.DependenciesCount,
			"labels":       result.LabelsCount,
			"events":       result.EventsCount,
		}
		if pinnedCount > 0 {
			stats["pinned_skipped"] = pinnedCount
		}
		return outputJSON(stats)
	}
	fmt.Printf("%s %s %d %s(s)\n", ui.RenderPass("✓"), capitalize(scope.pastTense), result.DeletedCount, scope.subjectNoun)
	fmt.Printf("  Dependencies removed: %d\n", result.DependenciesCount)
	fmt.Printf("  Labels removed:       %d\n", result.LabelsCount)
	fmt.Printf("  Events removed:       %d\n", result.EventsCount)
	if pinnedCount > 0 {
		fmt.Printf("  Pinned (skipped):     %d\n", pinnedCount)
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
