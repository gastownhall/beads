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

type purgeProxiedTxResult struct {
	empty        bool
	dryRun       bool
	needsConfirm bool

	issueIDs         []string
	deleteResult     domain.DeleteIssuesResult
	deleteErr        error
	pinnedCount      int
	referencedCount  int
	referencedSample []string
	safetyStats      closedDeletionCandidateStats
}

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

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (purgeProxiedTxResult, string, error) {
		var result purgeProxiedTxResult

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
				return result, "", fmt.Errorf("invalid --older-than value %q: %w", olderThan, err)
			}
			cutoffTime := time.Now().UTC().AddDate(0, 0, -days)
			cutoff = &cutoffTime
			filter.ClosedBefore = cutoff
		}

		page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
		if err != nil {
			return result, "", fmt.Errorf("listing issues: %w", err)
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

		closedIssues, result.safetyStats = filterClosedDeletionCandidates(closedIssues, cutoff)
		result.pinnedCount = result.safetyStats.PinnedSkipped

		if scope.cmdName == "prune" && !scope.ignoreReferences {
			candidateIDs := make(map[string]bool, len(closedIssues))
			for _, iss := range closedIssues {
				candidateIDs[iss.ID] = true
			}
			refSet, err := buildReferencedSetProxied(ctx, uw, candidateIDs)
			if err != nil {
				return result, "", fmt.Errorf("scanning open beads for references: %w", err)
			}
			nonReferenced := closedIssues[:0]
			for _, iss := range closedIssues {
				if refSet[iss.ID] {
					result.referencedCount++
					if len(result.referencedSample) < 100 {
						result.referencedSample = append(result.referencedSample, iss.ID)
					}
				} else {
					nonReferenced = append(nonReferenced, iss)
				}
			}
			closedIssues = nonReferenced
		}

		if len(closedIssues) == 0 {
			result.empty = true
			return result, "", nil
		}

		result.issueIDs = make([]string, len(closedIssues))
		for i, issue := range closedIssues {
			result.issueIDs[i] = issue.ID
		}

		if dryRun {
			result.dryRun = true
			result.deleteResult, result.deleteErr = uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
				IDs:    result.issueIDs,
				DryRun: true,
			}, actor)
			return result, "", nil
		}

		if !force {
			result.needsConfirm = true
			return result, "", nil
		}

		deleteResult, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs: result.issueIDs,
		}, actor)
		if err != nil {
			return result, "", fmt.Errorf("%s failed: %w", scope.cmdName, err)
		}
		result.deleteResult = deleteResult

		return result, fmt.Sprintf("bd: %s %d bead(s)", scope.cmdName, deleteResult.DeletedCount), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	warnClosedDeletionSafetySkips(res.safetyStats)

	switch {
	case res.empty:
		return emitProxiedPruneEmpty(scope, olderThan, pattern, res.referencedCount, res.referencedSample)
	case res.dryRun:
		return emitProxiedPruneDryRun(scope, res.issueIDs, res.deleteResult, res.deleteErr, res.pinnedCount, res.referencedCount, res.referencedSample)
	case res.needsConfirm:
		return emitProxiedPruneConfirm(scope, res.issueIDs, olderThan, pattern, res.pinnedCount, res.referencedCount)
	default:
		return emitProxiedPruneResult(scope, res.deleteResult, res.pinnedCount, res.referencedCount, res.referencedSample)
	}
}

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
