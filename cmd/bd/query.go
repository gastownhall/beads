package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/query"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var queryCmd = &cobra.Command{
	Use:     "query [expression]",
	GroupID: "issues",
	Short:   "Query issues using a simple query language",
	Long: `Query issues using a simple query language that supports compound filters,
boolean operators, and date-relative expressions.

The query language enables complex filtering that would otherwise require
multiple flags or piping through jq.

Syntax:
  field=value       Equality comparison
  field!=value      Inequality comparison
  field>value       Greater than
  field>=value      Greater than or equal
  field<value       Less than
  field<=value      Less than or equal

Boolean operators (case-insensitive):
  expr AND expr     Both conditions must match
  expr OR expr      Either condition can match
  NOT expr          Negates the condition
  (expr)            Grouping with parentheses

Supported fields:
  status            Issue status (open, in_progress, blocked, deferred, closed)
  priority          Priority level (0-4)
  type              Issue type (bug, feature, task, epic, chore)
  assignee          Assigned user (use "none" for unassigned)
  owner             Issue owner
  label             Issue label (use "none" for unlabeled)
  title             Search in title (contains)
  description       Search in description (contains, "none" for empty)
  notes             Search in notes (contains)
  created           Creation date/time
  updated           Last update date/time
  closed            Close date/time
  id                Issue ID (supports wildcards: bd-*)
  spec              Spec ID (supports wildcards)
  pinned            Boolean (true/false)
  ephemeral         Boolean (true/false)
  template          Boolean (true/false)
  parent            Parent issue ID
  mol_type          Molecule type (swarm, patrol, work)

Date values:
  Relative durations: 7d (7 days ago), 24h (24 hours ago), 2w (2 weeks ago)
  Absolute dates: 2025-01-15, 2025-01-15T10:00:00Z
  Natural language: tomorrow, "next monday", "in 3 days"

Examples:
  bd query "status=open AND priority>1"
  bd query "status=open AND priority<=2 AND updated>7d"
  bd query "(status=open OR status=blocked) AND priority<2"
  bd query "type=bug AND label=urgent"
  bd query "NOT status=closed"
  bd query "assignee=none AND type=task"
  bd query "created>30d AND status!=closed"
  bd query "label=frontend OR label=backend"
  bd query "title=authentication AND priority=0"`,
	Run: func(cmd *cobra.Command, args []string) {
		// Get query from args
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Error: query expression is required\n\n")
			if err := cmd.Help(); err != nil {
				fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
			}
			os.Exit(1)
		}

		queryStr := strings.Join(args, " ")

		// Get option flags
		limit, _ := cmd.Flags().GetInt("limit")
		allFlag, _ := cmd.Flags().GetBool("all")
		longFormat, _ := cmd.Flags().GetBool("long")
		sortBy, _ := cmd.Flags().GetString("sort")
		reverse, _ := cmd.Flags().GetBool("reverse")
		parseOnly, _ := cmd.Flags().GetBool("parse-only")

		// Parse the query
		node, err := query.Parse(queryStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing query: %v\n", err)
			os.Exit(1)
		}

		// If --parse-only, just show the parsed AST
		if parseOnly {
			fmt.Printf("Parsed query: %s\n", node.String())
			return
		}

		// Evaluate the query to get filter and/or predicate
		eval := query.NewEvaluator(time.Now())
		result, err := eval.Evaluate(node)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error evaluating query: %v\n", err)
			os.Exit(1)
		}

		// Apply limit if specified
		if limit > 0 && !result.RequiresPredicate {
			result.Filter.Limit = limit
		}

		// By default exclude closed issues unless --all is specified or query explicitly filters by status
		if !allFlag && result.Filter.Status == nil && !hasExplicitStatusFilter(node) {
			result.Filter.ExcludeStatus = append(result.Filter.ExcludeStatus, types.StatusClosed)
		}

		ctx := rootCtx

		// Check database freshness before reading (skip when using daemon)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		var issues []*types.Issue

		// Execute query
		if daemonClient != nil {
			// Daemon mode: use RPC with filter
			// Convert filter to ListArgs
			listArgs := filterToListArgs(&result.Filter)

			resp, err := daemonClient.List(listArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			if store == nil {
				fmt.Fprintf(os.Stderr, "Error: no storage available\n")
				os.Exit(1)
			}

			// If we need predicate filtering, we may need to fetch more results
			// to ensure we get enough after filtering
			searchFilter := result.Filter
			if result.RequiresPredicate && limit > 0 {
				// Fetch more to account for predicate filtering
				searchFilter.Limit = limit * 3
				if searchFilter.Limit < 100 {
					searchFilter.Limit = 100
				}
			}

			issues, err = store.SearchIssues(ctx, "", searchFilter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// If no issues found, check if git has issues and auto-import
			if len(issues) == 0 {
				if checkAndAutoImport(ctx, store) {
					issues, err = store.SearchIssues(ctx, "", searchFilter)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error: %v\n", err)
						os.Exit(1)
					}
				}
			}
		}

		// Apply predicate filter if needed (for OR queries)
		if result.RequiresPredicate && result.Predicate != nil {
			// For predicate filtering, we need labels populated on issues
			if store != nil {
				issueIDs := make([]string, len(issues))
				for i, issue := range issues {
					issueIDs[i] = issue.ID
				}
				labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
				for _, issue := range issues {
					issue.Labels = labelsMap[issue.ID]
				}
			}

			filtered := make([]*types.Issue, 0, len(issues))
			for _, issue := range issues {
				if result.Predicate(issue) {
					filtered = append(filtered, issue)
				}
			}
			issues = filtered

			// Apply limit after predicate filtering
			if limit > 0 && len(issues) > limit {
				issues = issues[:limit]
			}
		}

		// Apply sorting
		sortIssues(issues, sortBy, reverse)

		// Output results
		if jsonOutput {
			// Get labels and dependency counts
			if store != nil {
				issueIDs := make([]string, len(issues))
				for i, issue := range issues {
					issueIDs[i] = issue.ID
				}
				labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
				depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)
				commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)

				for _, issue := range issues {
					issue.Labels = labelsMap[issue.ID]
				}

				issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
				for i, issue := range issues {
					counts := depCounts[issue.ID]
					if counts == nil {
						counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
					}
					issuesWithCounts[i] = &types.IssueWithCounts{
						Issue:           issue,
						DependencyCount: counts.DependencyCount,
						DependentCount:  counts.DependentCount,
						CommentCount:    commentCounts[issue.ID],
					}
				}
				outputJSON(issuesWithCounts)
			} else {
				outputJSON(issues)
			}
			return
		}

		// Load labels for display
		if store != nil && !result.RequiresPredicate {
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
			}
		}

		outputQueryResults(issues, queryStr, longFormat)
	},
}

// hasExplicitStatusFilter checks if the query contains an explicit status comparison
func hasExplicitStatusFilter(node query.Node) bool {
	switch n := node.(type) {
	case *query.ComparisonNode:
		return n.Field == "status"
	case *query.AndNode:
		return hasExplicitStatusFilter(n.Left) || hasExplicitStatusFilter(n.Right)
	case *query.OrNode:
		return hasExplicitStatusFilter(n.Left) || hasExplicitStatusFilter(n.Right)
	case *query.NotNode:
		return hasExplicitStatusFilter(n.Operand)
	default:
		return false
	}
}

// filterToListArgs converts an IssueFilter to RPC ListArgs
func filterToListArgs(filter *types.IssueFilter) *rpc.ListArgs {
	args := &rpc.ListArgs{
		Limit: filter.Limit,
	}

	if filter.Status != nil {
		args.Status = string(*filter.Status)
	}
	if filter.IssueType != nil {
		args.IssueType = string(*filter.IssueType)
	}
	if filter.Assignee != nil {
		args.Assignee = *filter.Assignee
	}
	if len(filter.Labels) > 0 {
		args.Labels = filter.Labels
	}
	if len(filter.LabelsAny) > 0 {
		args.LabelsAny = filter.LabelsAny
	}
	if filter.TitleContains != "" {
		args.TitleContains = filter.TitleContains
	}
	if filter.DescriptionContains != "" {
		args.DescriptionContains = filter.DescriptionContains
	}
	if filter.NotesContains != "" {
		args.NotesContains = filter.NotesContains
	}
	if len(filter.IDs) > 0 {
		args.IDs = filter.IDs
	}
	if filter.SpecIDPrefix != "" {
		args.SpecIDPrefix = filter.SpecIDPrefix
	}
	if filter.CreatedAfter != nil {
		args.CreatedAfter = filter.CreatedAfter.Format(time.RFC3339)
	}
	if filter.CreatedBefore != nil {
		args.CreatedBefore = filter.CreatedBefore.Format(time.RFC3339)
	}
	if filter.UpdatedAfter != nil {
		args.UpdatedAfter = filter.UpdatedAfter.Format(time.RFC3339)
	}
	if filter.UpdatedBefore != nil {
		args.UpdatedBefore = filter.UpdatedBefore.Format(time.RFC3339)
	}
	if filter.ClosedAfter != nil {
		args.ClosedAfter = filter.ClosedAfter.Format(time.RFC3339)
	}
	if filter.ClosedBefore != nil {
		args.ClosedBefore = filter.ClosedBefore.Format(time.RFC3339)
	}
	if filter.PriorityMin != nil {
		args.PriorityMin = filter.PriorityMin
	}
	if filter.PriorityMax != nil {
		args.PriorityMax = filter.PriorityMax
	}
	if filter.Priority != nil {
		args.Priority = filter.Priority
	}
	if filter.NoAssignee {
		args.NoAssignee = true
	}
	if filter.NoLabels {
		args.NoLabels = true
	}
	if filter.EmptyDescription {
		args.EmptyDescription = true
	}
	if filter.Pinned != nil {
		args.Pinned = filter.Pinned
	}
	if filter.ParentID != nil {
		args.ParentID = *filter.ParentID
	}
	if len(filter.ExcludeStatus) > 0 {
		for _, s := range filter.ExcludeStatus {
			args.ExcludeStatus = append(args.ExcludeStatus, string(s))
		}
	}
	if len(filter.ExcludeTypes) > 0 {
		for _, t := range filter.ExcludeTypes {
			args.ExcludeTypes = append(args.ExcludeTypes, string(t))
		}
	}

	return args
}

// outputQueryResults formats and displays query results
func outputQueryResults(issues []*types.Issue, queryStr string, longFormat bool) {
	if len(issues) == 0 {
		fmt.Printf("No issues found matching query: %s\n", queryStr)
		return
	}

	if longFormat {
		fmt.Printf("\nFound %d issues:\n\n", len(issues))
		for _, issue := range issues {
			fmt.Printf("%s [P%d] [%s] %s\n", issue.ID, issue.Priority, issue.IssueType, issue.Status)
			fmt.Printf("  %s\n", issue.Title)
			if issue.Assignee != "" {
				fmt.Printf("  Assignee: %s\n", issue.Assignee)
			}
			if len(issue.Labels) > 0 {
				fmt.Printf("  Labels: %v\n", issue.Labels)
			}
			fmt.Println()
		}
	} else {
		// Use same compact format as list command
		fmt.Printf("Found %d issues:\n", len(issues))
		var buf strings.Builder
		for _, issue := range issues {
			formatQueryIssue(&buf, issue)
		}
		fmt.Print(buf.String())
	}
}

// formatQueryIssue formats a single issue in compact format
func formatQueryIssue(buf *strings.Builder, issue *types.Issue) {
	labelsStr := ""
	if len(issue.Labels) > 0 {
		labelsStr = fmt.Sprintf(" %v", issue.Labels)
	}
	assigneeStr := ""
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
	}

	// Get styled status icon
	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	if issue.Status == types.StatusClosed {
		line := fmt.Sprintf("%s %s [P%d] [%s]%s%s - %s",
			statusIcon, issue.ID, issue.Priority,
			issue.IssueType, assigneeStr, labelsStr, issue.Title)
		buf.WriteString(ui.RenderClosedLine(line))
		buf.WriteString("\n")
	} else {
		buf.WriteString(fmt.Sprintf("%s %s [%s] [%s]%s%s - %s\n",
			statusIcon,
			ui.RenderID(issue.ID),
			ui.RenderPriority(issue.Priority),
			ui.RenderType(string(issue.IssueType)),
			assigneeStr, labelsStr, issue.Title))
	}
}

func init() {
	queryCmd.Flags().IntP("limit", "n", 50, "Limit results (default: 50, 0 = unlimited)")
	queryCmd.Flags().BoolP("all", "a", false, "Include closed issues (default: exclude closed)")
	queryCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	queryCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	queryCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")
	queryCmd.Flags().Bool("parse-only", false, "Only parse the query and show the AST (for debugging)")

	rootCmd.AddCommand(queryCmd)
}
