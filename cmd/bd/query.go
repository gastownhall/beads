package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/query"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
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

		in, err := gatherQueryInput(cmd, args)
		if err != nil {
			return err
		}
		if in.parseOnly {
			return printParsedQuery(in.expression)
		}
		if in.offset > 0 {
			return HandleErrorRespectJSON("--offset is only supported under --proxied-server")
		}

		querier, err := openQuerier()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return runQuery(rootCtx, querier, in)
	},
}

// openQuerier hands back the boolean-query role for whichever route this
// invocation is on. Neither branch parses the expression, builds a filter or
// opens a unit of work: that is what moved behind the role.
func openQuerier() (issueops.Querier, error) {
	if usesProxiedServer() {
		return proxiedQuerier()
	}
	if store == nil {
		return nil, errors.New("no storage available")
	}
	return store.Querier()
}

// queryInput is the flag set both routes read, gathered once.
type queryInput struct {
	expression string
	request    issueops.QueryRequest
	limit      int
	offset     int
	longFormat bool
	parseOnly  bool
}

// gatherQueryInput turns the flags into the role's request. It is flag parsing
// and nothing else: the expression is handed over verbatim.
//
// ONE REFUSAL IS LEFT HERE, and it is flag hygiene rather than semantics: a
// negative --offset is not a page request at all. The two that WERE here are
// gone — the role refuses an offset under a display order for every caller, and
// answers an offset over a predicate query correctly rather than refusing it.
func gatherQueryInput(cmd *cobra.Command, args []string) (queryInput, error) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: query expression is required\n\n")
		if err := cmd.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
		}
		return queryInput{}, SilentExit()
	}

	in := queryInput{expression: strings.Join(args, " ")}
	in.limit, _ = cmd.Flags().GetInt("limit")
	in.longFormat, _ = cmd.Flags().GetBool("long")
	in.parseOnly, _ = cmd.Flags().GetBool("parse-only")
	in.offset, _ = cmd.Flags().GetInt("offset")
	sortBy, _ := cmd.Flags().GetString("sort")
	allFlag, _ := cmd.Flags().GetBool("all")
	reverse, _ := cmd.Flags().GetBool("reverse")

	if in.offset < 0 {
		return queryInput{}, HandleErrorRespectJSON("--offset must be non-negative")
	}

	limit := in.limit
	in.request = issueops.QueryRequest{
		Expression:    in.expression,
		IncludeClosed: allFlag,
		SortBy:        sortBy,
		Reverse:       reverse,
		Limit:         &limit,
		Offset:        in.offset,
	}
	return in, nil
}

// printParsedQuery serves --parse-only, which opens no store: it is a debugging
// view of the AST, not a query, and asking for the role to print it would make
// a syntax check need a database. Its refusal is worded as the ROLE words the
// same fault, so a syntax error reads the same either way.
func printParsedQuery(expression string) error {
	node, err := query.Parse(expression)
	if err != nil {
		return HandleErrorRespectJSON("invalid query expression: %v", err)
	}
	fmt.Printf("Parsed query: %s\n", node.String())
	return nil
}

// runQuery asks the role and renders the page. Both routes call it, so the two
// surfaces differ by which accessor produced the role and by nothing else.
func runQuery(ctx context.Context, querier issueops.Querier, in queryInput) error {
	page, err := querier.Query(ctx, in.request)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if jsonOutput {
		if err := outputJSON(page.Items); err != nil {
			return err
		}
	} else {
		outputQueryResults(queryPageIssues(page.Items), in.expression, in.longFormat)
	}
	// The hint is on stderr and on BOTH routes now: the direct one had no
	// has-more verdict to print before, because the query it ran was the
	// truncating one.
	printTruncationHint(page.HasMore, in.limit)
	return nil
}

// queryPageIssues projects the counted page onto the rows the text renderings
// take. Both output modes now read ONE query — the counted one `--json` always
// used — because two calls that differed only in their projection are two
// chances for a predicate to be applied to different rows.
func queryPageIssues(items []*types.IssueWithCounts) []*types.Issue {
	out := make([]*types.Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		out = append(out, item.Issue)
	}
	return out
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
	queryCmd.Flags().IntP("limit", "n", workapi.DefaultQueryLimit, "Limit results (default: 50, 0 = unlimited)")
	queryCmd.Flags().Int("offset", 0, "Skip the first N matching results (0-based). Only supported under --proxied-server.")
	queryCmd.Flags().BoolP("all", "a", false, "Include closed issues (default: exclude closed)")
	queryCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	queryCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	queryCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")
	queryCmd.Flags().Bool("parse-only", false, "Only parse the query and show the AST (for debugging)")

	rootCmd.AddCommand(queryCmd)
}
