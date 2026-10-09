package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

var searchCmd = &cobra.Command{
	Use:     "search [query]",
	GroupID: "issues",
	Short:   "Search issues by text query",
	Long: `Search issues across title and ID (all statuses, including closed).

ID-like queries (e.g., "bd-123", "hq-319") use fast exact/prefix matching.
Text queries search titles. Use --desc-contains for description search.
Use --status open (etc.) to narrow; closed issues are included by default
so "was this already filed/fixed?" cannot silently answer no. Matches
beyond --limit are dropped status-blind, so when hunting live work in a
large DB, narrow with --status open or raise --limit.

Examples:
  bd search "authentication bug"
  bd search "login" --status open
  bd search "database" --label backend --limit 10
  bd search --query "performance" --assignee alice
  bd search "bd-5q" # Search by partial ID (fast prefix match)
  bd search "security" --priority-min 0 --priority-max 2
  bd search "bug" --created-after 2025-01-01
  bd search "refactor" --status open  # Only open issues
  bd search "bug" --sort priority
  bd search "task" --sort created --reverse
  bd search "api" --desc-contains "endpoint"
  bd search "cleanup" --no-assignee --no-labels`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("search")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()
		return runSearch(cmd, args)
	},
}

// runSearch is `bd search` on every route — the direct store, the proxied
// server and a remote backend — and every route asks the same library entry:
// issueops.Reader.List with issueops.SearchListRequest (plus the narrowing
// flags), whose ListRequest.Query is SearchIssues' free text. The route only
// decides WHICH reader answers: the store's, or the proxied provider's. A
// remote backend's reader sends the text as listIssues' `q`, which the
// server's handler hands to the same role, and refuses before dialing against
// a server that does not advertise issues.list.search.
//
// The page is the first --limit rows in the store's default order (the
// request's SortBy); --sort/--reverse reorder that page here, which is what
// this command has always done.
func runSearch(cmd *cobra.Command, args []string) error {
	fail := HandleError
	if usesProxiedServer() {
		fail = HandleErrorRespectJSON
	}

	queryFlag, _ := cmd.Flags().GetString("query")
	query := queryFlag
	if len(args) > 0 {
		query = strings.Join(args, " ")
	}
	if query == "" {
		if err := cmd.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
		}
		return fail("search query is required")
	}

	req, err := searchListRequest(cmd, query)
	if err != nil {
		return fail("%v", err)
	}

	reader, err := searchReader()
	if err != nil {
		return fail("%v", err)
	}
	page, err := reader.List(rootCtx, req)
	if err != nil {
		var unsup *storage.ErrUnsupported
		if errors.As(err, &unsup) && unsup.Capability != "" {
			return fail("bd search needs a bd serve that advertises %s: %v", unsup.Capability, err)
		}
		return fail("%v", err)
	}

	sortBy, _ := cmd.Flags().GetString("sort")
	reverse, _ := cmd.Flags().GetBool("reverse")

	if jsonOutput {
		items := make([]*types.IssueWithCounts, 0, len(page.Items))
		for _, row := range page.Items {
			if row != nil && row.Issue != nil {
				items = append(items, row)
			}
		}
		workapi.SortIssuesWithCounts(items, sortBy, reverse)
		return outputJSON(items)
	}

	issues, _ := listPageIssues(page)
	workapi.SortIssues(issues, sortBy, reverse)
	longFormat, _ := cmd.Flags().GetBool("long")
	outputSearchResults(issues, query, longFormat)
	return nil
}

// searchReader is the Reader this invocation's route answers through, taken
// from the route's own accessor so its decorators are in place.
func searchReader() (issueops.Reader, error) {
	if usesProxiedServer() {
		return proxiedIssueReader()
	}
	if store == nil {
		return nil, errors.New("no storage available")
	}
	return store.IssueReader()
}

// searchListRequest maps `bd search`'s flags onto issueops.SearchListRequest.
// Flags a backend's Reader cannot carry (over http: --updated-*, --closed-*,
// --priority-min/max, the text-contains and empty/absent filters) are put on
// the request anyway, so that Reader refuses them rather than answering a
// wider search than was asked for.
func searchListRequest(cmd *cobra.Command, query string) (issueops.ListRequest, error) {
	flags := cmd.Flags()
	str := func(name string) string { v, _ := flags.GetString(name); return v }
	boolean := func(name string) bool { v, _ := flags.GetBool(name); return v }
	limit, _ := flags.GetInt("limit")
	labels, _ := flags.GetStringSlice("label")
	labelsAny, _ := flags.GetStringSlice("label-any")

	req := issueops.SearchListRequest(query)
	req.Limit = &limit
	if status := str("status"); status != "all" {
		req.Status = status
	}
	req.Assignee = str("assignee")
	req.IssueType = str("type")
	req.Labels = utils.NormalizeLabels(labels)
	req.LabelsAny = utils.NormalizeLabels(labelsAny)
	req.DescContains = str("desc-contains")
	req.NotesContains = str("notes-contains")
	req.ExternalContains = str("external-contains")
	req.EmptyDesc = boolean("empty-description")
	req.NoAssignee = boolean("no-assignee")
	req.NoLabels = boolean("no-labels")

	for _, d := range []struct {
		flag string
		dst  **time.Time
	}{
		{"created-after", &req.CreatedAfter},
		{"created-before", &req.CreatedBefore},
		{"updated-after", &req.UpdatedAfter},
		{"updated-before", &req.UpdatedBefore},
		{"closed-after", &req.ClosedAfter},
		{"closed-before", &req.ClosedBefore},
	} {
		raw := str(d.flag)
		if raw == "" {
			continue
		}
		t, err := parseTimeFlag(raw)
		if err != nil {
			return req, fmt.Errorf("parsing --%s: %w", d.flag, err)
		}
		*d.dst = &t
	}

	for _, p := range []struct {
		flag string
		dst  **int
	}{
		{"priority-min", &req.PriorityMin},
		{"priority-max", &req.PriorityMax},
	} {
		if !flags.Changed(p.flag) {
			continue
		}
		v, err := validation.ValidatePriority(str(p.flag))
		if err != nil {
			return req, fmt.Errorf("parsing --%s: %w", p.flag, err)
		}
		*p.dst = &v
	}

	if fields, _ := flags.GetStringArray("metadata-field"); len(fields) > 0 {
		req.MetadataFields = make(map[string]string, len(fields))
		for _, mf := range fields {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return req, fmt.Errorf("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return req, fmt.Errorf("invalid --metadata-field key: %w", err)
			}
			req.MetadataFields[k] = v
		}
	}
	if key := str("has-metadata-key"); key != "" {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return req, fmt.Errorf("invalid --has-metadata-key: %w", err)
		}
		req.HasMetadataKey = key
	}
	return req, nil
}

// outputSearchResults formats and displays search results
func outputSearchResults(issues []*types.Issue, query string, longFormat bool) {
	if len(issues) == 0 {
		fmt.Printf("No issues found matching '%s'\n", query)
		return
	}

	if longFormat {
		// Long format: multi-line with details
		fmt.Printf("\nFound %d issues matching '%s':\n\n", len(issues), query)
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
		// Compact format: one line per issue
		fmt.Printf("Found %d issues matching '%s':\n", len(issues), query)
		for _, issue := range issues {
			labelsStr := ""
			if len(issue.Labels) > 0 {
				labelsStr = fmt.Sprintf(" %v", issue.Labels)
			}
			assigneeStr := ""
			if issue.Assignee != "" {
				assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
			}
			fmt.Printf("%s [P%d] [%s] %s%s%s - %s\n",
				issue.ID, issue.Priority, issue.IssueType, issue.Status,
				assigneeStr, labelsStr, issue.Title)
		}
	}
}

func init() {
	searchCmd.Flags().String("query", "", "Search query (alternative to positional argument)")
	searchCmd.Flags().StringP("status", "s", "", "Filter by stored status (comma-separated for OR; open, in_progress, blocked, deferred, closed, all). Default searches all statuses including closed. Note: dependency-blocked issues use 'bd blocked'")
	searchCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	searchCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate)")
	searchCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	searchCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	searchCmd.Flags().IntP("limit", "n", 50, "Limit results (default: 50)")
	searchCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	searchCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	searchCmd.Flags().BoolP("reverse", "r", false, "Invert the sort field's default direction (created/updated/closed default to newest-first, so --sort updated --reverse is oldest-first)")

	// Date range flags
	searchCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	searchCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Priority range flags
	searchCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	searchCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")

	// Pattern matching flags
	searchCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	searchCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")
	searchCmd.Flags().String("external-contains", "", "Filter by external ref substring (case-insensitive)")

	// Empty/null check flags
	searchCmd.Flags().Bool("empty-description", false, "Filter issues with empty or missing description")
	searchCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	searchCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Metadata filtering (GH#1406)
	searchCmd.Flags().StringArray("metadata-field", nil, "Filter by metadata field (key=value, repeatable)")
	searchCmd.Flags().String("has-metadata-key", "", "Filter issues that have this metadata key set")

	rootCmd.AddCommand(searchCmd)
}
