package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

func runSearchProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	queryFlag, _ := cmd.Flags().GetString("query")
	var query string
	if len(args) > 0 {
		query = strings.Join(args, " ")
	} else if queryFlag != "" {
		query = queryFlag
	}

	if query == "" {
		if err := cmd.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "Error displaying help: %v\n", err)
		}
		return HandleErrorRespectJSON("search query is required")
	}

	status, _ := cmd.Flags().GetString("status")
	assignee, _ := cmd.Flags().GetString("assignee")
	issueType, _ := cmd.Flags().GetString("type")
	limit, _ := cmd.Flags().GetInt("limit")
	labels, _ := cmd.Flags().GetStringSlice("label")
	labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
	longFormat, _ := cmd.Flags().GetBool("long")
	sortBy, _ := cmd.Flags().GetString("sort")
	reverse, _ := cmd.Flags().GetBool("reverse")

	createdAfter, _ := cmd.Flags().GetString("created-after")
	createdBefore, _ := cmd.Flags().GetString("created-before")
	updatedAfter, _ := cmd.Flags().GetString("updated-after")
	updatedBefore, _ := cmd.Flags().GetString("updated-before")
	closedAfter, _ := cmd.Flags().GetString("closed-after")
	closedBefore, _ := cmd.Flags().GetString("closed-before")

	priorityMinStr, _ := cmd.Flags().GetString("priority-min")
	priorityMaxStr, _ := cmd.Flags().GetString("priority-max")

	descContains, _ := cmd.Flags().GetString("desc-contains")
	notesContains, _ := cmd.Flags().GetString("notes-contains")
	externalContains, _ := cmd.Flags().GetString("external-contains")

	emptyDesc, _ := cmd.Flags().GetBool("empty-description")
	noAssignee, _ := cmd.Flags().GetBool("no-assignee")
	noLabels, _ := cmd.Flags().GetBool("no-labels")

	labels = utils.NormalizeLabels(labels)
	labelsAny = utils.NormalizeLabels(labelsAny)

	filter := types.IssueFilter{
		Limit: limit,
	}

	if status == "" {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
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

	if descContains != "" {
		filter.DescriptionContains = descContains
	}
	if notesContains != "" {
		filter.NotesContains = notesContains
	}
	if externalContains != "" {
		filter.ExternalRefContains = externalContains
	}

	if emptyDesc {
		filter.EmptyDescription = true
	}
	if noAssignee {
		filter.NoAssignee = true
	}
	if noLabels {
		filter.NoLabels = true
	}

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

	if cmd.Flags().Changed("priority-min") {
		priorityMin, err := validation.ValidatePriority(priorityMinStr)
		if err != nil {
			return HandleErrorRespectJSON("parsing --priority-min: %v", err)
		}
		filter.PriorityMin = &priorityMin
	}
	if cmd.Flags().Changed("priority-max") {
		priorityMax, err := validation.ValidatePriority(priorityMaxStr)
		if err != nil {
			return HandleErrorRespectJSON("parsing --priority-max: %v", err)
		}
		filter.PriorityMax = &priorityMax
	}

	metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
	if len(metadataFieldFlags) > 0 {
		filter.MetadataFields = make(map[string]string, len(metadataFieldFlags))
		for _, mf := range metadataFieldFlags {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return HandleErrorRespectJSON("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return HandleErrorRespectJSON("invalid --metadata-field key: %v", err)
			}
			filter.MetadataFields[k] = v
		}
	}
	hasMetadataKey, _ := cmd.Flags().GetString("has-metadata-key")
	if hasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(hasMetadataKey); err != nil {
			return HandleErrorRespectJSON("invalid --has-metadata-key: %v", err)
		}
		filter.HasMetadataKey = hasMetadataKey
	}

	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	defer uw.Close(ctx)

	if status != "" && status != "all" {
		cfg, err := loadProxiedListFilterConfig(ctx, uw)
		if err != nil {
			return HandleErrorRespectJSON("loading status configuration: %v", err)
		}
		if err := applyStatusFilter(&filter, status, cfg.customStatusNames()); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
	}

	if jsonOutput {
		page, err := uw.IssueUseCase().SearchIssuesWithCounts(ctx, query, filter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		items := page.Items
		sortIssuesWithCounts(items, sortBy, reverse)
		if items == nil {
			items = []*types.IssueWithCounts{}
		}
		return outputJSON(items)
	}

	page, err := uw.IssueUseCase().SearchIssues(ctx, query, filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	issues := page.Items
	sortIssues(issues, sortBy, reverse)
	outputSearchResults(issues, query, longFormat)
	return nil
}
