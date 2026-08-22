package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/issueops"
)

var countCmd = &cobra.Command{
	Use:     "count",
	GroupID: "views",
	Short:   "Count issues matching filters",
	Long: `Count issues matching the specified filters.

By default, returns the total count of issues matching the filters.
Use --by-* flags to group counts by different attributes.

Examples:
  bd count                          # Count all issues
  bd count --status open            # Count open issues
  bd count --by-status              # Group count by status
  bd count --by-priority            # Group count by priority
  bd count --by-type                # Group count by issue type
  bd count --by-assignee            # Group count by assignee
  bd count --by-label               # Group count by label
  bd count --assignee alice --by-status  # Count alice's issues by status
  bd count --include-infra          # Count issues + wisps tier (matches 'bd list --include-infra --all' cardinality)
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("count")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		request, groupBy, err := parseCountRequest(cmd)
		if err != nil {
			return err
		}

		counter, err := openCounter()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return executeCount(rootCtx, counter, request, groupBy)
	},
}

// openCounter hands back the count role for whichever route this invocation is
// on, each through its own capability accessor. Neither branch builds a filter,
// loads config or opens a unit of work.
func openCounter() (issueops.Counter, error) {
	if usesProxiedServer() {
		return proxiedCounter()
	}
	return store.Counter()
}

// parseCountRequest turns the flag set into the role's request. Normalization
// of labels and ids, the wisp-tier policy and the workspace's infra vocabulary
// all live behind the role, so the two routes cannot come to disagree.
func parseCountRequest(cmd *cobra.Command) (issueops.CountRequest, issueops.CountGroup, error) {
	groupBy, err := countGroupFlag(cmd)
	if err != nil {
		return issueops.CountRequest{}, "", err
	}

	status, _ := cmd.Flags().GetString("status")
	assignee, _ := cmd.Flags().GetString("assignee")
	issueType, _ := cmd.Flags().GetString("type")
	labels, _ := cmd.Flags().GetStringSlice("label")
	labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
	titleSearch, _ := cmd.Flags().GetString("title")
	idFilter, _ := cmd.Flags().GetString("id")
	titleContains, _ := cmd.Flags().GetString("title-contains")
	descContains, _ := cmd.Flags().GetString("desc-contains")
	notesContains, _ := cmd.Flags().GetString("notes-contains")
	emptyDesc, _ := cmd.Flags().GetBool("empty-description")
	noAssignee, _ := cmd.Flags().GetBool("no-assignee")
	noLabels, _ := cmd.Flags().GetBool("no-labels")
	includeInfra, _ := cmd.Flags().GetBool("include-infra")

	request := issueops.CountRequest{
		Status:        status,
		IssueType:     issueType,
		Assignee:      assignee,
		Labels:        labels,
		LabelsAny:     labelsAny,
		TitleSearch:   titleSearch,
		IDFilter:      idFilter,
		TitleContains: titleContains,
		DescContains:  descContains,
		NotesContains: notesContains,
		EmptyDesc:     emptyDesc,
		NoAssignee:    noAssignee,
		NoLabels:      noLabels,
		IncludeInfra:  includeInfra,
	}

	if cmd.Flags().Changed("priority") {
		priority, _ := cmd.Flags().GetInt("priority")
		request.Priority = &priority
	}
	if cmd.Flags().Changed("priority-min") {
		priorityMin, _ := cmd.Flags().GetInt("priority-min")
		request.PriorityMin = &priorityMin
	}
	if cmd.Flags().Changed("priority-max") {
		priorityMax, _ := cmd.Flags().GetInt("priority-max")
		request.PriorityMax = &priorityMax
	}

	for _, bound := range []struct {
		flag string
		dest **time.Time
	}{
		{"created-after", &request.CreatedAfter},
		{"created-before", &request.CreatedBefore},
		{"updated-after", &request.UpdatedAfter},
		{"updated-before", &request.UpdatedBefore},
		{"closed-after", &request.ClosedAfter},
		{"closed-before", &request.ClosedBefore},
	} {
		raw, _ := cmd.Flags().GetString(bound.flag)
		if raw == "" {
			continue
		}
		parsed, err := parseTimeFlag(raw)
		if err != nil {
			return issueops.CountRequest{}, "", HandleErrorRespectJSON("parsing --%s: %v", bound.flag, err)
		}
		*bound.dest = &parsed
	}

	return request, groupBy, nil
}

// countGroupFlag resolves the five mutually exclusive --by-* flags to one
// dimension. The role refuses an unknown one, but it cannot refuse TWO — by
// the time a request reaches it only one dimension is left — so the exclusivity
// check stays here, with the flags it is about.
func countGroupFlag(cmd *cobra.Command) (issueops.CountGroup, error) {
	var group issueops.CountGroup
	set := 0
	for _, candidate := range []struct {
		flag  string
		group issueops.CountGroup
	}{
		{"by-status", issueops.CountGroupStatus},
		{"by-priority", issueops.CountGroupPriority},
		{"by-type", issueops.CountGroupType},
		{"by-assignee", issueops.CountGroupAssignee},
		{"by-label", issueops.CountGroupLabel},
	} {
		if on, _ := cmd.Flags().GetBool(candidate.flag); on {
			group = candidate.group
			set++
		}
	}
	if set > 1 {
		return "", HandleErrorRespectJSON("only one --by-* flag can be specified")
	}
	return group, nil
}

func executeCount(ctx context.Context, counter issueops.Counter, request issueops.CountRequest, groupBy issueops.CountGroup) error {
	if groupBy == "" {
		result, err := counter.Count(ctx, request)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			return outputJSON(struct {
				Count int64 `json:"count"`
			}{Count: result.Total})
		}
		fmt.Println(result.Total)
		return nil
	}

	result, err := counter.CountByGroup(ctx, issueops.CountByGroupRequest{Filter: request, GroupBy: groupBy})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	type GroupCount struct {
		Group string `json:"group"`
		Count int    `json:"count"`
	}

	groups := make([]GroupCount, 0, len(result.Groups))
	for group, count := range result.Groups {
		groups = append(groups, GroupCount{Group: group, Count: count})
	}
	slices.SortFunc(groups, func(a, b GroupCount) int {
		return cmp.Compare(a.Group, b.Group)
	})

	if jsonOutput {
		return outputJSON(struct {
			Total  int64        `json:"total"`
			Groups []GroupCount `json:"groups"`
		}{
			Total:  result.Total,
			Groups: groups,
		})
	}
	// The total is the role's scalar count, not the sum of the buckets:
	// --by-label buckets overlap, so a multi-label issue is one row in the
	// total and one row in each of its buckets.
	fmt.Printf("Total: %d\n\n", result.Total)
	for _, g := range groups {
		fmt.Printf("%s: %d\n", g.Group, g.Count)
	}
	return nil
}

func init() {
	registerCountFlags(countCmd)
	rootCmd.AddCommand(countCmd)
}

// registerCountFlags declares `bd count`'s flag set on cmd. It is a function
// rather than a block inside init so a test can stand up an INDEPENDENT
// command carrying the same flags: cobra's AddFlagSet shares the underlying
// *Flag values, so a test that set a flag on a copy would leak it into the
// real command and into the next test.
func registerCountFlags(cmd *cobra.Command) {
	// Filter flags (same as list command)
	cmd.Flags().StringP("status", "s", "", "Filter by stored status (open, in_progress, blocked, deferred, closed). Note: dependency-blocked issues use 'bd blocked'")
	cmd.Flags().IntP("priority", "p", 0, "Filter by priority (0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)")
	cmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	cmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate)")
	cmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	cmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	cmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	cmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated)")

	// Pattern matching
	cmd.Flags().String("title-contains", "", "Filter by title substring")
	cmd.Flags().String("desc-contains", "", "Filter by description substring")
	cmd.Flags().String("notes-contains", "", "Filter by notes substring")

	// Date ranges
	cmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Empty/null checks
	cmd.Flags().Bool("empty-description", false, "Filter issues with empty description")
	cmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	cmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Priority ranges
	cmd.Flags().Int("priority-min", 0, "Filter by minimum priority (inclusive)")
	cmd.Flags().Int("priority-max", 0, "Filter by maximum priority (inclusive)")

	// Wisps tier (GH#4387): mirrors bd list's flag of the same name so
	// `bd count --include-infra <filters>` returns exactly the cardinality of
	// `bd list --include-infra <filters> --all`.
	cmd.Flags().Bool("include-infra", false, "Include infrastructure beads and the wisps tier (matches 'bd list --include-infra --all' cardinality)")

	// Grouping flags
	cmd.Flags().Bool("by-status", false, "Group count by status")
	cmd.Flags().Bool("by-priority", false, "Group count by priority")
	cmd.Flags().Bool("by-type", false, "Group count by issue type")
	cmd.Flags().Bool("by-assignee", false, "Group count by assignee")
	cmd.Flags().Bool("by-label", false, "Group count by label")
}
