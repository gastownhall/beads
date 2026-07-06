package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/query"
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
  status            Stored status (open, in_progress, blocked, deferred, closed). Note: dependency-blocked issues stay "open"; use 'bd blocked' to find them
  priority          Priority level (0-4)
  type              Issue type (bug, feature, task, epic, chore, decision)
  assignee          Assigned user (use "none" for unassigned)
  owner             Issue owner
  label             Issue label (use "none" for unlabeled)
  title             Search in title (contains)
  description       Search in description (contains, "none" for empty)
  notes             Search in notes (contains)
  created           Creation date/time
  updated           Last update date/time
  started           Date/time issue first transitioned to in_progress
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("query")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runQueryProxiedServer(cmd, rootCtx, args)
		}

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
		if offset > 0 {
			return HandleErrorRespectJSON("--offset is only supported under --proxied-server")
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

		if limit > 0 && !result.RequiresPredicate {
			result.Filter.Limit = limit
		}

		if !allFlag && result.Filter.Status == nil && !hasExplicitStatusFilter(node) {
			result.Filter.ExcludeStatus = append(result.Filter.ExcludeStatus, types.StatusClosed)
		}

		ctx := rootCtx

		if store == nil {
			return HandleErrorRespectJSON("no storage available")
		}

		searchFilter := result.Filter
		if result.RequiresPredicate && limit > 0 {
			searchFilter.Limit = limit * 3
			if searchFilter.Limit < 100 {
				searchFilter.Limit = 100
			}
		}

		if jsonOutput {
			iwc, err := store.SearchIssuesWithCounts(ctx, "", searchFilter)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
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
				if limit > 0 && len(iwc) > limit {
					iwc = iwc[:limit]
				}
			}
			sortIssuesWithCounts(iwc, sortBy, reverse)
			if iwc == nil {
				iwc = []*types.IssueWithCounts{}
			}
			return outputJSON(iwc)
		}

		issues, err := store.SearchIssues(ctx, "", searchFilter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if result.RequiresPredicate && result.Predicate != nil {
			filtered := make([]*types.Issue, 0, len(issues))
			for _, issue := range issues {
				if result.Predicate(issue) {
					filtered = append(filtered, issue)
				}
			}
			issues = filtered
			if limit > 0 && len(issues) > limit {
				issues = issues[:limit]
			}
		}

		sortIssues(issues, sortBy, reverse)

		outputQueryResults(issues, queryStr, longFormat)
		return nil
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
	queryCmd.Flags().Int("offset", 0, "Skip the first N matching results (0-based). Only supported under --proxied-server.")
	queryCmd.Flags().BoolP("all", "a", false, "Include closed issues (default: exclude closed)")
	queryCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	queryCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	queryCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")
	queryCmd.Flags().Bool("parse-only", false, "Only parse the query and show the AST (for debugging)")

	rootCmd.AddCommand(queryCmd)
}
