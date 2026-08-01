package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

func runListProxiedServer(cmd *cobra.Command, ctx context.Context, in listInput) error {
	if in.repoOverrideSet {
		return errors.New("--repo is not supported with --proxied-server")
	}
	switch {
	case in.watchMode:
		return runListProxiedWatch(cmd, ctx, in)
	case in.ReadyFlag:
		return runListProxiedReady(cmd, ctx, in)
	default:
		return runListProxiedSearch(cmd, ctx, in)
	}
}

func openProxiedListUOW(ctx context.Context) (uow.UnitOfWork, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return nil, fmt.Errorf("open unit of work: %w", err)
	}
	return uw, nil
}

func runListProxiedSearch(_ *cobra.Command, ctx context.Context, in listInput) error {
	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if in.prettyFormat && in.ParentID != "" {
		if in.Offset > 0 {
			return fmt.Errorf("--offset is not supported with hierarchical --parent + pretty/tree")
		}
		return runListProxiedHierarchicalParent(ctx, uw, in, filter)
	}

	if jsonOutput {
		page, err := uw.IssueUseCase().SearchIssuesWithCounts(ctx, "", filter)
		if err != nil {
			return err
		}
		return emitProxiedListJSONResult(page.Items, in, page.HasMore)
	}

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return err
	}

	issues, hasMore := workapi.FinishPage(page.Items, in.SortBy, in.Reverse, in.effectiveLimit, page.HasMore)

	return renderProxiedListText(ctx, uw, issues, in, hasMore)
}

func runListProxiedReady(_ *cobra.Command, ctx context.Context, in listInput) error {
	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	wf := workapi.ReadyFilterFromIssueFilter(filter)

	if jsonOutput {
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, wf)
		if err != nil {
			return err
		}
		return emitProxiedListJSONResult(page.Items, in, page.HasMore)
	}

	page, err := uw.IssueUseCase().GetReadyWork(ctx, wf)
	if err != nil {
		return err
	}

	issues, hasMore := workapi.FinishPage(page.Items, in.SortBy, in.Reverse, in.effectiveLimit, page.HasMore)

	return renderProxiedListText(ctx, uw, issues, in, hasMore)
}

func runListProxiedWatch(_ *cobra.Command, ctx context.Context, in listInput) error {
	if in.formatStr != "" {
		return errors.New("--format under --proxied-server --watch is not supported")
	}

	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	uw.Close(ctx)

	load := func() ([]*types.Issue, bool, map[string][]*types.Dependency, error) {
		uw, err := openProxiedListUOW(ctx)
		if err != nil {
			return nil, false, nil, err
		}
		defer uw.Close(ctx)

		var issues []*types.Issue
		var hasMore bool
		switch {
		case in.ReadyFlag:
			wf := workapi.ReadyFilterFromIssueFilter(filter)
			page, perr := uw.IssueUseCase().GetReadyWork(ctx, wf)
			if perr != nil {
				return nil, false, nil, perr
			}
			issues, hasMore = workapi.FinishPage(page.Items, in.SortBy, in.Reverse, in.effectiveLimit, page.HasMore)
		case in.ParentID != "":
			issues, err = gatherProxiedHierarchical(ctx, uw, in.ParentID, filter)
			if err != nil {
				return nil, false, nil, err
			}
			// The tree is gathered, not queried, so no seam reported a
			// has-more: the cut is the only thing that can say the limit hid
			// a descendant. Its order is the tree's own, not --sort's.
			issues, hasMore = workapi.FinishPage(issues, "id", false, in.effectiveLimit, false)
		default:
			page, perr := uw.IssueUseCase().SearchIssues(ctx, "", filter)
			if perr != nil {
				return nil, false, nil, perr
			}
			issues, hasMore = workapi.FinishPage(page.Items, in.SortBy, in.Reverse, in.effectiveLimit, page.HasMore)
		}

		deps, err := loadDepsForIssues(ctx, uw, issues)
		if err != nil {
			return nil, false, nil, err
		}
		return issues, hasMore, deps, nil
	}

	issues, hasMore, deps, err := load()
	if err != nil {
		return fmt.Errorf("initial query: %w", err)
	}
	displayPrettyListWithDeps(issues, true, deps)
	printTruncationHint(hasMore, in.effectiveLimit)
	lastSnapshot := issueSnapshot(issues)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return nil
		case <-ticker.C:
			issues, hasMore, deps, err := load()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error refreshing issues: %v\n", err)
				continue
			}
			snap := issueSnapshot(issues)
			if snap != lastSnapshot {
				lastSnapshot = snap
				displayPrettyListWithDeps(issues, true, deps)
				printTruncationHint(hasMore, in.effectiveLimit)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
}

func emitProxiedListJSONResult(iwc []*types.IssueWithCounts, in listInput, hasMore bool) error {
	iwc, hasMore = workapi.FinishPage(iwc, in.SortBy, in.Reverse, in.effectiveLimit, hasMore)
	var err error
	if in.SkipLabels {
		err = outputJSON(newSkipLabelsListJSONResponse(iwc))
	} else {
		err = outputJSON(iwc)
	}
	if err != nil {
		return err
	}
	printTruncationHint(hasMore, in.effectiveLimit)
	return nil
}

func loadDepsForIssues(ctx context.Context, uw uow.UnitOfWork, issues []*types.Issue) (map[string][]*types.Dependency, error) {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return uw.DependencyUseCase().GetForIssueIDs(ctx, ids)
}

func renderProxiedListText(ctx context.Context, uw uow.UnitOfWork, issues []*types.Issue, in listInput, truncated bool) error {
	if in.formatStr != "" {
		depsByIssueID, err := loadDepsForIssues(ctx, uw, issues)
		if err != nil {
			return err
		}
		if err := outputFormattedList(issues, depsByIssueID, in.formatStr); err != nil {
			return err
		}
		printTruncationHint(truncated, in.effectiveLimit)
		return nil
	}

	if in.prettyFormat {
		depsByIssueID, err := loadDepsForIssues(ctx, uw, issues)
		if err != nil {
			return err
		}
		displayPrettyListWithDepsMode(issues, false, depsByIssueID, in.depsMode)
		printTruncationHint(truncated, in.effectiveLimit)
		printSkipLabelsFooter(in.SkipLabels)
		return nil
	}

	issueIDs := make([]string, len(issues))
	labelsMap := make(map[string][]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
		if len(issue.Labels) > 0 {
			labelsMap[issue.ID] = issue.Labels
		}
	}

	info, err := uw.DependencyUseCase().GetBlockingInfo(ctx, issueIDs)
	if err != nil {
		return fmt.Errorf("load blocking info: %w", err)
	}
	blockedByMap := info.BlockedBy
	blocksMap := info.Blocks
	parentMap := info.Parent

	var buf strings.Builder
	switch {
	case ui.IsAgentMode():
		for _, issue := range issues {
			formatAgentIssue(&buf, issue, blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
		fmt.Print(buf.String())
		printTruncationHint(truncated, in.effectiveLimit)
		return nil
	case in.longFormat:
		buf.WriteString(fmt.Sprintf("\nFound %d issues:\n\n", len(issues)))
		for _, issue := range issues {
			formatIssueLong(&buf, issue, labelsMap[issue.ID], in.SkipLabels)
		}
	default:
		for _, issue := range issues {
			formatIssueCompact(&buf, issue, labelsMap[issue.ID], blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
	}

	if in.SkipLabels && !isQuiet() {
		buf.WriteString(skipLabelsFooterText())
	}

	if err := ui.ToPager(buf.String(), ui.PagerOptions{NoPager: in.noPager}); err != nil {
		if _, werr := fmt.Fprint(os.Stdout, buf.String()); werr != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", werr)
		}
	}
	printTruncationHint(truncated, in.effectiveLimit)
	return nil
}
