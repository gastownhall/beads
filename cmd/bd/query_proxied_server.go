package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/query"
	"github.com/steveyegge/beads/internal/types"
)

func runQueryProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: query expression is required\n\n")
		if err := cmd.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
		}
		return SilentExit()
	}

	queryStr := strings.Join(args, " ")

	limit, _ := cmd.Flags().GetInt("limit")
	allFlag, _ := cmd.Flags().GetBool("all")
	longFormat, _ := cmd.Flags().GetBool("long")
	sortBy, _ := cmd.Flags().GetString("sort")
	reverse, _ := cmd.Flags().GetBool("reverse")
	parseOnly, _ := cmd.Flags().GetBool("parse-only")
	offset, _ := cmd.Flags().GetInt("offset")
	if offset < 0 {
		return HandleErrorRespectJSON("--offset must be non-negative")
	}

	node, err := query.Parse(queryStr)
	if err != nil {
		return HandleErrorRespectJSON("parsing query: %v", err)
	}

	if parseOnly {
		fmt.Printf("Parsed query: %s\n", node.String())
		return nil
	}

	eval := query.NewEvaluator(time.Now())
	result, err := eval.Evaluate(node)
	if err != nil {
		return HandleErrorRespectJSON("evaluating query: %v", err)
	}

	if result.RequiresPredicate && offset > 0 {
		return HandleErrorRespectJSON("--offset is not supported with OR/predicate queries")
	}

	if limit > 0 && !result.RequiresPredicate {
		result.Filter.Limit = limit
	}

	if !allFlag && result.Filter.Status == nil && !hasExplicitStatusFilter(node) {
		result.Filter.ExcludeStatus = append(result.Filter.ExcludeStatus, types.StatusClosed)
	}

	if offset > 0 && sortBy != "" {
		return HandleErrorRespectJSON("--offset is not supported with --sort (query applies --sort client-side, which cannot be paginated)")
	}

	searchFilter := result.Filter
	searchFilter.Offset = offset
	if result.RequiresPredicate && limit > 0 {
		searchFilter.Limit = limit * 3
		if searchFilter.Limit < 100 {
			searchFilter.Limit = 100
		}
	}

	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer uw.Close(ctx)

	if jsonOutput {
		page, err := uw.IssueUseCase().SearchIssuesWithCounts(ctx, "", searchFilter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		iwc := page.Items
		truncated := page.HasMore
		if result.RequiresPredicate && result.Predicate != nil {
			filtered := make([]*types.IssueWithCounts, 0, len(iwc))
			for _, item := range iwc {
				if item == nil || item.Issue == nil {
					continue
				}
				if result.Predicate(item.Issue) {
					filtered = append(filtered, item)
				}
			}
			iwc = filtered
			truncated = limit > 0 && len(iwc) > limit
			if truncated {
				iwc = iwc[:limit]
			}
		}
		sortIssuesWithCounts(iwc, sortBy, reverse)
		if iwc == nil {
			iwc = []*types.IssueWithCounts{}
		}
		if err := outputJSON(iwc); err != nil {
			return err
		}
		printTruncationHint(truncated, limit)
		return nil
	}

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", searchFilter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	issues := page.Items
	truncated := page.HasMore

	if result.RequiresPredicate && result.Predicate != nil {
		filtered := make([]*types.Issue, 0, len(issues))
		for _, issue := range issues {
			if result.Predicate(issue) {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
		truncated = limit > 0 && len(issues) > limit
		if truncated {
			issues = issues[:limit]
		}
	}

	sortIssues(issues, sortBy, reverse)

	outputQueryResults(issues, queryStr, longFormat)
	printTruncationHint(truncated, limit)
	return nil
}
