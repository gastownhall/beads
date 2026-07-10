package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// runPurgeOrPruneProxied is the proxied-server counterpart of runPurgeOrPrune.
// It drives the same delete-closed-beads flow through a UnitOfWork and the
// domain use cases instead of the embedded store.
func runPurgeOrPruneProxied(cmd *cobra.Command, scope purgeScope) error {
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

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	ctx := rootCtx
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

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

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("listing issues: %v", err)
	}
	closedIssues := page.Items

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

	referencedCount := 0
	var referencedSample []string
	if scope.cmdName == "prune" && !scope.ignoreReferences {
		candidateIDs := make(map[string]bool, len(closedIssues))
		for _, iss := range closedIssues {
			candidateIDs[iss.ID] = true
		}
		refSet, err := buildReferencedSetProxied(ctx, uw, candidateIDs)
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
		return emitProxiedPruneEmpty(scope, olderThan, pattern, referencedCount, referencedSample)
	}

	issueIDs := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		issueIDs[i] = issue.ID
	}

	if dryRun {
		result, derr := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs:    issueIDs,
			DryRun: true,
		}, actor)
		return emitProxiedPruneDryRun(scope, issueIDs, result, derr, pinnedCount, referencedCount, referencedSample)
	}

	if !force {
		return emitProxiedPruneConfirm(scope, issueIDs, olderThan, pattern, pinnedCount, referencedCount)
	}

	result, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs: issueIDs,
	}, actor)
	if err != nil {
		return HandleErrorRespectJSON("%s failed: %v", scope.cmdName, err)
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: %s %d bead(s)", scope.cmdName, result.DeletedCount)); err != nil && !isDoltNothingToCommit(err) {
		return HandleErrorRespectJSON("commit: %v", err)
	}

	return emitProxiedPruneResult(scope, result, pinnedCount, referencedCount, referencedSample)
}

// buildReferencedSetProxied mirrors buildReferencedSet using the domain use
// cases behind a UnitOfWork. It scans every non-done bead's description,
// notes, and comments for literal occurrences of any candidate ID, using the
// same linear candidateIDMatcher as the embedded path (the regexp-alternation
// scan it replaces was the ~15s-on-10K-beads profile behind the
// TestPruneLargeFixture CI failures).
func buildReferencedSetProxied(ctx context.Context, uw uow.UnitOfWork, candidateIDs map[string]bool) (map[string]bool, error) {
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	matcher := newCandidateIDMatcher(candidateIDs)

	notClosedStatuses := []types.Status{
		types.StatusOpen,
		types.StatusInProgress,
		types.StatusBlocked,
		types.StatusDeferred,
		types.StatusPinned,
		types.StatusHooked,
	}
	customStatuses, err := uw.ConfigUseCase().GetCustomStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for reference scan: %w", err)
	}
	for _, cs := range customStatuses {
		if cs.Category != types.CategoryDone {
			notClosedStatuses = append(notClosedStatuses, types.Status(cs.Name))
		}
	}
	notClosed := types.IssueFilter{Statuses: notClosedStatuses}
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", notClosed)
	if err != nil {
		return nil, err
	}
	openBeads := page.Items

	openIDs := make([]string, len(openBeads))
	for i, iss := range openBeads {
		openIDs[i] = iss.ID
	}
	commentsByIssue, err := uw.CommentUseCase().GetCommentsForIssues(ctx, openIDs)
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
		for _, c := range commentsByIssue[iss.ID] {
			scanText(c.Text)
		}
	}
	return refSet, nil
}

func emitProxiedPruneEmpty(scope purgeScope, olderThan, pattern string, referencedCount int, referencedSample []string) error {
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

func emitProxiedPruneDryRun(scope purgeScope, issueIDs []string, result domain.DeleteIssuesResult, resultErr error, pinnedCount, referencedCount int, referencedSample []string) error {
	if jsonOutput {
		stats := map[string]interface{}{
			"dry_run":            true,
			scope.dryRunCountKey: len(issueIDs),
			"dependencies":       0,
			"labels":             0,
			"events":             0,
		}
		if resultErr == nil {
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
	if resultErr == nil {
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

func emitProxiedPruneConfirm(scope purgeScope, issueIDs []string, olderThan, pattern string, pinnedCount, referencedCount int) error {
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

func emitProxiedPruneResult(scope purgeScope, result domain.DeleteIssuesResult, pinnedCount, referencedCount int, referencedSample []string) error {
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
