package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// countGroupColumns is the closed set issueops.CountByGroupRequest.GroupBy
// promises, mapped to what the storage seam calls each dimension. It exists so
// an unknown value is refused HERE, as ErrValidation, rather than reaching
// storage and coming back as an unclassifiable "unsupported groupBy" string.
var countGroupColumns = map[issueops.CountGroup]string{
	issueops.CountGroupStatus:   "status",
	issueops.CountGroupPriority: "priority",
	issueops.CountGroupType:     "type",
	issueops.CountGroupAssignee: "assignee",
	issueops.CountGroupLabel:    "label",
}

// ValidateCountGroup resolves a grouping dimension to the name the storage
// seam takes, refusing anything outside the published set with ErrValidation.
func ValidateCountGroup(group issueops.CountGroup) (string, error) {
	column, ok := countGroupColumns[group]
	if !ok {
		return "", fmt.Errorf("invalid group '%s'. Valid values: status, priority, type, assignee, label%.0w",
			group, issueops.ErrValidation)
	}
	return column, nil
}

// BuildCountFilter turns a count request into the storage-level filter: the
// single definition of what `bd count` means, which every implementation of
// issueops.Counter builds through.
//
// It is a SEPARATE builder rather than a call into BuildListFilter with the
// paging fields blanked out, because the two commands disagree about the
// default answer: a listing hides closed, pinned, template and gate rows, and a
// count hides none of them. The one place they must agree is --include-infra,
// pinned by a golden-style test comparing this builder's output against
// BuildListFilter's for the same request (count_test.go, GH#4387).
//
// cfg supplies the workspace's infra vocabulary and is only read under
// IncludeInfra; a zero ListConfig falls back to the default infra set.
func BuildCountFilter(in issueops.CountRequest, cfg ListConfig) (types.IssueFilter, error) {
	filter := types.IssueFilter{
		TitleSearch:         in.TitleSearch,
		TitleContains:       in.TitleContains,
		DescriptionContains: in.DescContains,
		NotesContains:       in.NotesContains,
		CreatedAfter:        in.CreatedAfter,
		CreatedBefore:       in.CreatedBefore,
		UpdatedAfter:        in.UpdatedAfter,
		UpdatedBefore:       in.UpdatedBefore,
		ClosedAfter:         in.ClosedAfter,
		ClosedBefore:        in.ClosedBefore,
		EmptyDescription:    in.EmptyDesc,
		NoAssignee:          in.NoAssignee,
		NoLabels:            in.NoLabels,
		Priority:            in.Priority,
		PriorityMin:         in.PriorityMin,
		PriorityMax:         in.PriorityMax,
	}

	// Status and IssueType are taken as written. Neither is validated against
	// the workspace vocabulary: issueops.CountRequest promises an unrecognized
	// name matches nothing rather than failing.
	if in.Status != "" && in.Status != "all" {
		status := types.Status(in.Status)
		filter.Status = &status
	}
	if in.IssueType != "" {
		issueType := types.IssueType(in.IssueType)
		filter.IssueType = &issueType
	}
	if in.Assignee != "" {
		assignee := in.Assignee
		filter.Assignee = &assignee
	}

	// NormalizeLabels allocates its own slice, so the caller's Labels and
	// LabelsAny are read and never written — the snapshot promise on
	// issueops.Counter.
	if labels := utils.NormalizeLabels(in.Labels); len(labels) > 0 {
		filter.Labels = labels
	}
	if labelsAny := utils.NormalizeLabels(in.LabelsAny); len(labelsAny) > 0 {
		filter.LabelsAny = labelsAny
	}
	if ids := utils.NormalizeLabels(strings.Split(in.IDFilter, ",")); len(ids) > 0 {
		filter.IDs = ids
	}

	if in.IncludeInfra {
		applyCountIncludeInfra(&filter, in.IssueType, cfg)
	} else {
		filter.SkipWisps = true
	}
	return filter, nil
}

// applyCountIncludeInfra switches the count filter to the wisps-inclusive mode
// of `bd list --include-infra` (GH#4387). It mirrors the BuildListFilter
// defaults that determine list's cardinality so that, for any filter set,
// `bd count --include-infra <filters>` returns exactly the number of rows
// `bd list --include-infra <filters> --all` materializes:
//
//   - the wisps table is merged in (SkipWisps=false), picking up no_history
//     beads and ephemeral wisps, like list's merge path;
//   - template molecules are excluded (list's default);
//   - gate beads are excluded unless requested via --type gate;
//   - counting an infra type (agent/role/message, or the store-configured set)
//     routes to the ephemeral wisps tier, like list's infra-type listing.
//
// A count without IncludeInfra never calls this and keeps its historical
// durable-only semantics.
func applyCountIncludeInfra(filter *types.IssueFilter, issueType string, cfg ListConfig) {
	filter.SkipWisps = false

	isTemplate := false
	filter.IsTemplate = &isTemplate

	if issueType != "gate" {
		filter.ExcludeTypes = append(filter.ExcludeTypes, "gate")
	}

	if issueType != "" && cfg.IsInfra(issueType) {
		ephemeral := true
		filter.Ephemeral = &ephemeral
	}
}
