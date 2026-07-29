package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
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
	// ignoreReferences, when true, bypasses the reference-aware skip in prune.
	// Always false for purge — ephemeral beads' references are themselves transient.
	ignoreReferences bool
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

// buildReferencedSet scans every non-closed bead's description, notes, and
// comments for literal occurrences of any candidate ID and returns the set of
// candidate IDs that were found. Uses a Statuses filter (not ExcludeStatus)
// to avoid the PG ExcludeStatus coverage gap (be-jdeief).
func buildReferencedSet(ctx context.Context, st storage.DoltStorage, candidateIDs map[string]bool) (map[string]bool, error) {
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	matcher := newCandidateIDMatcher(candidateIDs)

	// Scan every non-done bead: built-in active statuses plus any configured
	// custom statuses whose category is not "done". A repo can define custom
	// statuses (status.custom) in active/wip/frozen categories; a bead in such
	// a status that cites a closed bead must protect it from prune exactly like
	// a built-in open bead does. Reading custom statuses is required, not
	// best-effort: if we cannot enumerate them we must not under-scan and risk
	// deleting a referenced bead, so the error propagates and aborts the prune.
	notClosedStatuses := []types.Status{
		types.StatusOpen,
		types.StatusInProgress,
		types.StatusBlocked,
		types.StatusDeferred,
		types.StatusPinned,
		types.StatusHooked,
	}
	customStatuses, err := st.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for reference scan: %w", err)
	}
	for _, cs := range customStatuses {
		if cs.Category != types.CategoryDone {
			notClosedStatuses = append(notClosedStatuses, types.Status(cs.Name))
		}
	}
	notClosed := types.IssueFilter{Statuses: notClosedStatuses}
	openBeads, err := st.SearchIssues(ctx, "", notClosed)
	if err != nil {
		return nil, err
	}

	refSet := make(map[string]bool)
	scanText := func(text string) {
		matcher.findAll(text, refSet)
	}

	for _, iss := range openBeads {
		scanText(iss.Description)
		scanText(iss.Notes)
		comments, err := st.GetIssueComments(ctx, iss.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			scanText(c.Text)
		}
	}
	return refSet, nil
}

type candidateIDMatcher struct {
	byFirstByte map[byte][]string
}

func newCandidateIDMatcher(candidateIDs map[string]bool) candidateIDMatcher {
	byFirstByte := make(map[byte][]string)
	for id := range candidateIDs {
		if id == "" {
			continue
		}
		byFirstByte[id[0]] = append(byFirstByte[id[0]], id)
	}
	for first := range byFirstByte {
		ids := byFirstByte[first]
		sort.Slice(ids, func(i, j int) bool {
			if len(ids[i]) == len(ids[j]) {
				return ids[i] < ids[j]
			}
			return len(ids[i]) > len(ids[j])
		})
		byFirstByte[first] = ids
	}
	return candidateIDMatcher{byFirstByte: byFirstByte}
}

func (m candidateIDMatcher) findAll(text string, found map[string]bool) {
	for i := 0; i < len(text); i++ {
		ids := m.byFirstByte[text[i]]
		if len(ids) == 0 || !isWordBoundaryAt(text, i) {
			continue
		}
		for _, id := range ids {
			end := i + len(id)
			if end <= len(text) && strings.HasPrefix(text[i:], id) && isWordBoundaryAt(text, end) {
				found[id] = true
				break
			}
		}
	}
}

func isWordBoundaryAt(s string, idx int) bool {
	var before, after byte
	if idx > 0 {
		before = s[idx-1]
	}
	if idx < len(s) {
		after = s[idx]
	}
	return isASCIIWordByte(before) != isASCIIWordByte(after)
}

func isASCIIWordByte(b byte) bool {
	return b == '_' ||
		('0' <= b && b <= '9') ||
		('A' <= b && b <= 'Z') ||
		('a' <= b && b <= 'z')
}

// runPurgeOrPrune implements the shared delete-closed-beads flow used by
// both `bd purge` (ephemeral scope) and `bd prune` (non-ephemeral scope).
// The caller's scope controls the filter, messaging, and safety gate.
func runPurgeOrPrune(cmd *cobra.Command, scope purgeScope) error {
	if usesProxiedServer() {
		return runPurgeOrPruneProxied(cmd, scope)
	}

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

	// Reference-aware skip (prune only): filter closed beads cited by open beads.
	referencedCount := 0
	var referencedSample []string
	if scope.cmdName == "prune" && !scope.ignoreReferences {
		candidateIDs := make(map[string]bool, len(closedIssues))
		for _, iss := range closedIssues {
			candidateIDs[iss.ID] = true
		}
		refSet, err := buildReferencedSet(ctx, store, candidateIDs)
		if err != nil {
			return HandleErrorRespectJSON("scanning open beads for references: %v", err)
		}
		nonReferenced := closedIssues[:0]
		for _, iss := range closedIssues {
			if refSet[iss.ID] {
				referencedCount++
				if len(referencedSample) < 100 {
					referencedSample = append(referencedSample, iss.ID)
				}
			} else {
				nonReferenced = append(nonReferenced, iss)
			}
		}
		closedIssues = nonReferenced
	}

	if len(closedIssues) == 0 {
		if jsonOutput {
			stats := map[string]interface{}{
				scope.countKey: 0,
				"message":      fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName),
			}
			if scope.cmdName == "prune" {
				stats["referenced_skipped"] = referencedCount
				stats["referenced_count"] = referencedCount
				if len(referencedSample) > 0 {
					stats["referenced_ids_sample"] = referencedSample
				}
			}
			return outputJSON(stats)
		}
		msg := fmt.Sprintf("No %ss to %s", scope.subjectNoun, scope.cmdName)
		if olderThan != "" {
			msg += fmt.Sprintf(" (older than %s)", olderThan)
		}
		if pattern != "" {
			msg += fmt.Sprintf(" (matching %q)", pattern)
		}
		fmt.Println(msg)
		if referencedCount > 0 {
			fmt.Println(ui.MutedStyle.Render(fmt.Sprintf(
				"  (%d closed bead(s) protected by open-bead references — use --ignore-references to override)",
				referencedCount)))
		}
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
			if scope.cmdName == "prune" {
				stats["referenced_skipped"] = referencedCount
				stats["referenced_count"] = referencedCount
				if len(referencedSample) > 0 {
					stats["referenced_ids_sample"] = referencedSample
				}
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
		if referencedCount > 0 {
			fmt.Printf("  %s   %d\n", ui.MutedStyle.Render("Referenced (skipped):"), referencedCount)
			sample := referencedSample
			if len(sample) > 5 {
				sample = sample[:5]
			}
			idStrs := make([]string, len(sample))
			for i, id := range sample {
				idStrs[i] = ui.IDStyle.Render(id)
			}
			suffix := ""
			if referencedCount > 5 {
				suffix = ui.MutedStyle.Render(", ...")
			}
			fmt.Printf("  %s %s%s\n", ui.MutedStyle.Render("Referenced IDs (sample):"), strings.Join(idStrs, ", "), suffix)
		}
		fmt.Printf("\n(Dry-run mode — no changes made)\n")
		return nil
	}

	if !force {
		fmt.Printf("Found %d %s(s) to %s\n", len(issueIDs), scope.subjectNoun, scope.cmdName)
		if pinnedCount > 0 {
			fmt.Printf("Skipping %d pinned bead(s)\n", pinnedCount)
		}
		if referencedCount > 0 {
			fmt.Println(ui.MutedStyle.Render(fmt.Sprintf("Skipping %d referenced bead(s)", referencedCount)))
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
		if scope.cmdName == "prune" {
			stats["referenced_skipped"] = referencedCount
			stats["referenced_count"] = referencedCount
			if len(referencedSample) > 0 {
				stats["referenced_ids_sample"] = referencedSample
			}
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
	if referencedCount > 0 {
		fmt.Printf("  %s %d\n", ui.MutedStyle.Render("Referenced (skipped):"), referencedCount)
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
