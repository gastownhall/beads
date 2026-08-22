package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/steveyegge/beads/issueops"
)

func runListProxiedServer(cmd *cobra.Command, ctx context.Context, out io.Writer, in listInput) error {
	if in.repoOverrideSet {
		return errors.New("--repo is not supported with --proxied-server")
	}
	switch {
	case in.watchMode:
		return runListProxiedWatch(cmd, ctx, in)
	case !in.ReadyFlag && in.prettyFormat && in.ParentID != "":
		return runListProxiedTree(ctx, in)
	default:
		// The --ready arm is not a case of its own any more: it is
		// ListRequest.ReadyFlag, and choosing the ready query from it is the
		// ROLE's job on both routes.
		return runListProxiedPage(ctx, out, in)
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

// runListProxiedTree serves the ONE mode that is deliberately off the role: the
// hierarchical --parent walk under pretty output. It consumes the FILTER as a
// value, re-parenting a copy of it at every level, and it reaches no page
// epilogue on either route.
func runListProxiedTree(ctx context.Context, in listInput) error {
	uw, filter, err := openAndPrepare(ctx, in)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if in.Offset > 0 {
		return fmt.Errorf("--offset is not supported with hierarchical --parent + pretty/tree")
	}
	return runListProxiedHierarchicalParent(ctx, uw, in, filter)
}

// runListProxiedPage is the proxied twin of the direct route's two flips, and
// it is the same two in the same order: --json first, then every text
// rendering, both over issueops.Reader.
//
// It opens no unit of work of its own — the role opens one per call, which is
// one more than this route used to for a listing that also loads dependency
// records. Nothing here was atomic across those reads before either: the
// renderings ran after the query returned.
func runListProxiedPage(ctx context.Context, out io.Writer, in listInput) error {
	rd, err := proxiedIssueReader()
	if err != nil {
		return err
	}

	if jsonOutput {
		page, err := rd.List(ctx, in.ListRequest)
		if err != nil {
			return err
		}
		return emitProxiedListJSONResult(page.Items, in, page.HasMore)
	}

	// SkipCounts for the reason the direct route sets it: no text rendering
	// prints a cardinality, and the query this route used to run projected
	// none.
	textRequest := in.ListRequest
	textRequest.SkipCounts = true
	page, err := rd.List(ctx, textRequest)
	if err != nil {
		return err
	}
	issues, hasMore := listPageIssues(page)
	return renderProxiedListText(ctx, out, issues, in, hasMore)
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
	displayPrettyListWithDeps(issues, true, deps, hasMore, in.ReadyFlag)
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
				displayPrettyListWithDeps(issues, true, deps, hasMore, in.ReadyFlag)
				printTruncationHint(hasMore, in.effectiveLimit)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
}

// emitProxiedListJSONResult writes the page the role already finished. It runs
// no epilogue of its own: the sort, the trim and the has-more verdict are
// workapi.FinishPage's, inside issueops.Reader.List, where the direct route's
// are too.
func emitProxiedListJSONResult(iwc []*types.IssueWithCounts, in listInput, hasMore bool) error {
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

func renderProxiedListText(ctx context.Context, out io.Writer, issues []*types.Issue, in listInput, truncated bool) error {
	// --format and the pretty tree want the WHOLE dependency record set for the
	// page — every edge type, no status rule — which is neither role's
	// question. They open their own unit of work for it, which is what lets
	// every other rendering below reach its roles without one.
	if in.formatStr != "" || in.prettyFormat {
		uw, err := openProxiedListUOW(ctx)
		if err != nil {
			return err
		}
		defer uw.Close(ctx)
		depsByIssueID, err := loadDepsForIssues(ctx, uw, issues)
		if err != nil {
			return err
		}
		if in.formatStr != "" {
			if err := outputFormattedList(out, issues, depsByIssueID, in.formatStr); err != nil {
				return err
			}
			printTruncationHint(truncated, in.effectiveLimit)
			return nil
		}
		displayPrettyListWithDepsMode(issues, false, depsByIssueID, in.depsMode, truncated, in.ReadyFlag)
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

	// The decoration, onto issueops.BlockingAnnotator. This route FAILS on the
	// read where the direct one swallows it, and both are kept exactly as they
	// were: converging them is a behavior decision recorded for the owner
	// (AMBIGUITIES.md, A-blk-1) rather than taken here.
	annotator, err := proxiedBlockingAnnotator()
	if err != nil {
		return err
	}
	result, err := annotator.AnnotateBlocking(ctx, issueops.BlockingRequest{IDs: issueIDs})
	if err != nil {
		return fmt.Errorf("load blocking info: %w", err)
	}
	blocking := newListBlocking(result)

	var buf strings.Builder
	switch {
	case ui.IsAgentMode():
		for _, issue := range issues {
			formatAgentIssue(&buf, issue, blocking.blockedBy[issue.ID], blocking.blocks[issue.ID], blocking.parent[issue.ID])
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
			formatIssueCompact(&buf, issue, labelsMap[issue.ID], blocking.blockedBy[issue.ID], blocking.blocks[issue.ID], blocking.parent[issue.ID])
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
