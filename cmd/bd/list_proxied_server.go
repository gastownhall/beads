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
)

func runListProxiedServer(cmd *cobra.Command, ctx context.Context, in listInput) error {
	if in.repoOverrideSet {
		return errors.New("--repo is not supported with --proxied-server")
	}
	switch {
	case in.watchMode:
		return runListProxiedWatch(cmd, ctx, in)
	case in.readyFlag:
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

func openAndPrepare(ctx context.Context, in listInput) (uow.UnitOfWork, types.IssueFilter, error) {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return nil, types.IssueFilter{}, err
	}
	cfg, err := loadProxiedListFilterConfig(ctx, uw)
	if err != nil {
		uw.Close(ctx)
		return nil, types.IssueFilter{}, err
	}
	filter, err := buildListFilter(in, cfg)
	if err != nil {
		uw.Close(ctx)
		return nil, types.IssueFilter{}, err
	}
	return uw, filter, nil
}

func runListProxiedSearch(_ *cobra.Command, ctx context.Context, in listInput) error {
	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if in.prettyFormat && in.parentID != "" {
		if in.offset > 0 {
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

	sortIssues(page.Items, in.sortBy, in.reverse)

	return renderProxiedListText(ctx, uw, page.Items, in, page.HasMore)
}

func runListProxiedHierarchicalParent(ctx context.Context, uw uow.UnitOfWork, in listInput, filter types.IssueFilter) error {
	treeIssues, err := gatherProxiedHierarchical(ctx, uw, in.parentID, filter)
	if err != nil {
		return err
	}
	if len(treeIssues) == 0 {
		fmt.Printf("Issue '%s' has no children\n", in.parentID)
		return nil
	}

	depsByIssueID, err := loadDepsForIssues(ctx, uw, treeIssues)
	if err != nil {
		return err
	}

	displayPrettyListWithDeps(treeIssues, false, depsByIssueID)
	printSkipLabelsFooter(in.skipLabels)
	return nil
}

func gatherProxiedHierarchical(ctx context.Context, uw uow.UnitOfWork, parentID string, baseFilter types.IssueFilter) ([]*types.Issue, error) {
	parent, err := uw.IssueUseCase().GetIssue(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("error checking parent issue: %w", err)
	}
	if parent == nil {
		return nil, fmt.Errorf("parent issue %q not found", parentID)
	}

	descendants, err := uw.IssueUseCase().GetDescendants(ctx, parentID, baseFilter)
	if err != nil {
		return nil, fmt.Errorf("error finding descendants: %w", err)
	}
	if len(descendants) == 0 {
		return nil, nil
	}

	out := make([]*types.Issue, 0, len(descendants)+1)
	out = append(out, parent)
	out = append(out, descendants...)
	return out, nil
}

func runListProxiedReady(_ *cobra.Command, ctx context.Context, in listInput) error {
	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	wf := readyWorkFilterFromIssueFilter(filter)

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

	sortIssues(page.Items, in.sortBy, in.reverse)

	return renderProxiedListText(ctx, uw, page.Items, in, page.HasMore)
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
		case in.readyFlag:
			wf := readyWorkFilterFromIssueFilter(filter)
			page, perr := uw.IssueUseCase().GetReadyWork(ctx, wf)
			if perr != nil {
				return nil, false, nil, perr
			}
			issues, hasMore = page.Items, page.HasMore
			sortIssues(issues, in.sortBy, in.reverse)
		case in.parentID != "":
			issues, err = gatherProxiedHierarchical(ctx, uw, in.parentID, filter)
			if err != nil {
				return nil, false, nil, err
			}
			sortIssues(issues, "id", false)
		default:
			page, perr := uw.IssueUseCase().SearchIssues(ctx, "", filter)
			if perr != nil {
				return nil, false, nil, perr
			}
			issues, hasMore = page.Items, page.HasMore
			sortIssues(issues, in.sortBy, in.reverse)
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
	sortIssuesWithCounts(iwc, in.sortBy, in.reverse)
	if iwc == nil {
		iwc = []*types.IssueWithCounts{}
	}
	var err error
	if in.skipLabels {
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
		displayPrettyListWithDeps(issues, false, depsByIssueID)
		printTruncationHint(truncated, in.effectiveLimit)
		printSkipLabelsFooter(in.skipLabels)
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
			formatIssueLong(&buf, issue, labelsMap[issue.ID], in.skipLabels)
		}
	default:
		for _, issue := range issues {
			formatIssueCompact(&buf, issue, labelsMap[issue.ID], blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
	}

	if in.skipLabels && !isQuiet() {
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
