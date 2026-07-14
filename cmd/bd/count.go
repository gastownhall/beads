package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
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
		if usesProxiedServer() {
			return HandleErrorRespectJSON("count is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("count")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		titleSearch, _ := cmd.Flags().GetString("title")
		idFilter, _ := cmd.Flags().GetString("id")

		// Pattern matching flags
		titleContains, _ := cmd.Flags().GetString("title-contains")
		descContains, _ := cmd.Flags().GetString("desc-contains")
		notesContains, _ := cmd.Flags().GetString("notes-contains")

		// Date range flags
		createdAfter, _ := cmd.Flags().GetString("created-after")
		createdBefore, _ := cmd.Flags().GetString("created-before")
		updatedAfter, _ := cmd.Flags().GetString("updated-after")
		updatedBefore, _ := cmd.Flags().GetString("updated-before")
		closedAfter, _ := cmd.Flags().GetString("closed-after")
		closedBefore, _ := cmd.Flags().GetString("closed-before")

		// Empty/null check flags
		emptyDesc, _ := cmd.Flags().GetBool("empty-description")
		noAssignee, _ := cmd.Flags().GetBool("no-assignee")
		noLabels, _ := cmd.Flags().GetBool("no-labels")

		// Priority range flags
		priorityMin, _ := cmd.Flags().GetInt("priority-min")
		priorityMax, _ := cmd.Flags().GetInt("priority-max")

		// Group by flags
		byStatus, _ := cmd.Flags().GetBool("by-status")
		byPriority, _ := cmd.Flags().GetBool("by-priority")
		byType, _ := cmd.Flags().GetBool("by-type")
		byAssignee, _ := cmd.Flags().GetBool("by-assignee")
		byLabel, _ := cmd.Flags().GetBool("by-label")

		// Determine groupBy value
		groupBy := ""
		groupCount := 0
		if byStatus {
			groupBy = "status"
			groupCount++
		}
		if byPriority {
			groupBy = "priority"
			groupCount++
		}
		if byType {
			groupBy = "type"
			groupCount++
		}
		if byAssignee {
			groupBy = "assignee"
			groupCount++
		}
		if byLabel {
			groupBy = "label"
			groupCount++
		}

		if groupCount > 1 {
			return HandleErrorRespectJSON("only one --by-* flag can be specified")
		}

		// Normalize labels
		labels = utils.NormalizeLabels(labels)
		labelsAny = utils.NormalizeLabels(labelsAny)

		ctx := rootCtx

		// Direct mode
		filter := types.IssueFilter{}
		if status != "" && status != "all" {
			s := types.Status(status)
			filter.Status = &s
		}
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			filter.Priority = &priority
		}
		if assignee != "" {
			filter.Assignee = &assignee
		}
		if issueType != "" {
			t := types.IssueType(issueType)
			filter.IssueType = &t
		}
		if len(labels) > 0 {
			filter.Labels = labels
		}
		if len(labelsAny) > 0 {
			filter.LabelsAny = labelsAny
		}
		if titleSearch != "" {
			filter.TitleSearch = titleSearch
		}
		if idFilter != "" {
			ids := utils.NormalizeLabels(strings.Split(idFilter, ","))
			if len(ids) > 0 {
				filter.IDs = ids
			}
		}

		// Pattern matching
		filter.TitleContains = titleContains
		filter.DescriptionContains = descContains
		filter.NotesContains = notesContains

		// Date ranges
		if createdAfter != "" {
			t, err := parseTimeFlag(createdAfter)
			if err != nil {
				return HandleErrorRespectJSON("parsing --created-after: %v", err)
			}
			filter.CreatedAfter = &t
		}
		if createdBefore != "" {
			t, err := parseTimeFlag(createdBefore)
			if err != nil {
				return HandleErrorRespectJSON("parsing --created-before: %v", err)
			}
			filter.CreatedBefore = &t
		}
		if updatedAfter != "" {
			t, err := parseTimeFlag(updatedAfter)
			if err != nil {
				return HandleErrorRespectJSON("parsing --updated-after: %v", err)
			}
			filter.UpdatedAfter = &t
		}
		if updatedBefore != "" {
			t, err := parseTimeFlag(updatedBefore)
			if err != nil {
				return HandleErrorRespectJSON("parsing --updated-before: %v", err)
			}
			filter.UpdatedBefore = &t
		}
		if closedAfter != "" {
			t, err := parseTimeFlag(closedAfter)
			if err != nil {
				return HandleErrorRespectJSON("parsing --closed-after: %v", err)
			}
			filter.ClosedAfter = &t
		}
		if closedBefore != "" {
			t, err := parseTimeFlag(closedBefore)
			if err != nil {
				return HandleErrorRespectJSON("parsing --closed-before: %v", err)
			}
			filter.ClosedBefore = &t
		}

		// Empty/null checks
		filter.EmptyDescription = emptyDesc
		filter.NoAssignee = noAssignee
		filter.NoLabels = noLabels

		// Priority range
		if cmd.Flags().Changed("priority-min") {
			filter.PriorityMin = &priorityMin
		}
		if cmd.Flags().Changed("priority-max") {
			filter.PriorityMax = &priorityMax
		}

		if includeInfra, _ := cmd.Flags().GetBool("include-infra"); includeInfra {
			cfg, err := loadDirectListFilterConfig(ctx, store)
			if err != nil {
				return HandleError("%v", err)
			}
			applyCountIncludeInfra(&filter, issueType, cfg)
		} else {
			filter.SkipWisps = true // durable tier only; bd count's historical default
		}

		if groupBy == "" {
			count, err := store.CountIssues(ctx, "", filter)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			if jsonOutput {
				return outputJSON(struct {
					Count int64 `json:"count"`
				}{Count: count})
			}
			fmt.Println(count)
			return nil
		}

		counts, err := store.CountIssuesByGroup(ctx, filter, groupBy)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		type GroupCount struct {
			Group string `json:"group"`
			Count int    `json:"count"`
		}

		groups := make([]GroupCount, 0, len(counts))
		for group, count := range counts {
			groups = append(groups, GroupCount{Group: group, Count: count})
		}

		// --by-label buckets are not mutually exclusive, so use CountIssues for the total
		// to avoid double-counting multi-label issues.
		total, err := store.CountIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		slices.SortFunc(groups, func(a, b GroupCount) int {
			return cmp.Compare(a.Group, b.Group)
		})

		if jsonOutput {
			return outputJSON(struct {
				Total  int64        `json:"total"`
				Groups []GroupCount `json:"groups"`
			}{
				Total:  total,
				Groups: groups,
			})
		}
		fmt.Printf("Total: %d\n\n", total)
		for _, g := range groups {
			fmt.Printf("%s: %d\n", g.Group, g.Count)
		}
		return nil
	},
}

// applyCountIncludeInfra switches the count filter to the wisps-inclusive
// mode of `bd list --include-infra` (GH#4387). It mirrors the buildListFilter
// defaults that determine list's cardinality so that, for any filter set,
// `bd count --include-infra <filters>` returns exactly the number of rows
// `bd list --include-infra <filters> --all` materializes:
//
//   - the wisps table is merged into the count (SkipWisps=false), picking up
//     no_history beads (durable work stored in the wisps tier) and ephemeral
//     wisps, exactly like list's merge path;
//   - template molecules are excluded (list's default without
//     --include-templates);
//   - gate beads are excluded unless gates are explicitly requested via
//     --type gate (list's default without --include-gates);
//   - counting an infra type (agent/role/message, or the store-configured
//     set) routes to the ephemeral wisps tier, like list's infra-type listing.
//
// The non-flag path never calls this function: bd count without
// --include-infra keeps its historical durable-only semantics.
func applyCountIncludeInfra(filter *types.IssueFilter, issueType string, cfg listFilterConfig) {
	filter.SkipWisps = false

	isTemplate := false
	filter.IsTemplate = &isTemplate

	if issueType != "gate" {
		filter.ExcludeTypes = append(filter.ExcludeTypes, "gate")
	}

	if issueType != "" && cfg.isInfra(issueType) {
		ephemeral := true
		filter.Ephemeral = &ephemeral
	}
}

func init() {
	// Filter flags (same as list command)
	countCmd.Flags().StringP("status", "s", "", "Filter by stored status (open, in_progress, blocked, deferred, closed). Note: dependency-blocked issues use 'bd blocked'")
	countCmd.Flags().IntP("priority", "p", 0, "Filter by priority (0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)")
	countCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	countCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate)")
	countCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL)")
	countCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE)")
	countCmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	countCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated)")

	// Pattern matching
	countCmd.Flags().String("title-contains", "", "Filter by title substring")
	countCmd.Flags().String("desc-contains", "", "Filter by description substring")
	countCmd.Flags().String("notes-contains", "", "Filter by notes substring")

	// Date ranges
	countCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	countCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	countCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	countCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	countCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	countCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Empty/null checks
	countCmd.Flags().Bool("empty-description", false, "Filter issues with empty description")
	countCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	countCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Priority ranges
	countCmd.Flags().Int("priority-min", 0, "Filter by minimum priority (inclusive)")
	countCmd.Flags().Int("priority-max", 0, "Filter by maximum priority (inclusive)")

	// Wisps tier (GH#4387): mirrors bd list's flag of the same name so
	// `bd count --include-infra <filters>` returns exactly the cardinality of
	// `bd list --include-infra <filters> --all`.
	countCmd.Flags().Bool("include-infra", false, "Include infrastructure beads and the wisps tier (matches 'bd list --include-infra --all' cardinality)")

	// Grouping flags
	countCmd.Flags().Bool("by-status", false, "Group count by status")
	countCmd.Flags().Bool("by-priority", false, "Group count by priority")
	countCmd.Flags().Bool("by-type", false, "Group count by issue type")
	countCmd.Flags().Bool("by-assignee", false, "Group count by assignee")
	countCmd.Flags().Bool("by-label", false, "Group count by label")

	rootCmd.AddCommand(countCmd)
}
