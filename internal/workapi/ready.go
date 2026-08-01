package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// DefaultReadyLimit is the default number of rows a ready-work query returns
// when the caller does not ask for a specific limit. Every frontend registers
// its limit knob from this constant so the surfaces cannot drift apart; 0
// still means unlimited.
const DefaultReadyLimit = 100

// LimitOr resolves a request's optional limit: nil takes the surface's shared
// default, and an explicit 0 stays 0, which means unlimited at both storage
// seams. The pointer is what keeps "unset" and "explicitly unlimited"
// distinguishable — collapsing them would make one constant unable to serve
// both surfaces.
func LimitOr(limit *int, fallback int) int {
	if limit == nil {
		return fallback
	}
	return *limit
}

// BuildReadyFilter turns a ready request into the storage-level work filter.
// It is the single definition of what `bd ready` means: open issues only (an
// empty WorkFilter would default to open plus in_progress), issue-type alias
// expansion, label and exclude-type normalization, limit defaulting, and the
// assignee rule that lets Unassigned win over a stale Assignee.
//
// Labels, LabelsAny and ExcludeLabels are normalized here, so a caller can
// pass raw user input. A caller that has to decide something from the label
// sets - the CLI's directory-label default is the one such case - reads them
// back off the returned filter, where they are already normalized, rather than
// normalizing its own copy: a value it then puts on the filter itself is its
// own to shape, and running it through here would change it.
func BuildReadyFilter(in issueops.ReadyRequest) (types.WorkFilter, error) {
	filter := types.WorkFilter{
		// Open only, not in_progress - the same set `bd list --ready` shows.
		Status:           types.StatusOpen,
		Type:             utils.NormalizeIssueType(in.IssueType),
		Limit:            LimitOr(in.Limit, DefaultReadyLimit),
		Offset:           in.Offset,
		Unassigned:       in.Unassigned,
		SortPolicy:       types.SortPolicy(in.Sort),
		Labels:           utils.NormalizeLabels(in.Labels),
		LabelsAny:        utils.NormalizeLabels(in.LabelsAny),
		ExcludeLabels:    utils.NormalizeLabels(in.ExcludeLabels),
		LabelPattern:     in.LabelPattern,
		LabelRegex:       in.LabelRegex,
		IncludeDeferred:  in.IncludeDeferred,
		IncludeEphemeral: in.IncludeEphemeral,
		ExcludeTypes:     normalizeExcludeTypes(in.ExcludeTypes),
		HasMetadataKey:   in.HasMetadataKey,
	}

	if in.Priority != nil {
		p := *in.Priority
		filter.Priority = &p
	}
	if in.Assignee != "" && !in.Unassigned {
		a := in.Assignee
		filter.Assignee = &a
	}
	if in.ParentID != "" {
		pid := in.ParentID
		filter.ParentID = &pid
	}
	if in.MolType != nil {
		filter.MolType = in.MolType
	}
	if len(in.MetadataFields) > 0 {
		filter.MetadataFields = in.MetadataFields
	}

	if err := ValidateMetadataFilters(in.MetadataFields, in.HasMetadataKey); err != nil {
		return filter, err
	}

	if !filter.SortPolicy.IsValid() {
		return filter, fmt.Errorf("invalid sort policy '%s'. Valid values: hybrid, priority, oldest", in.Sort)
	}
	return filter, nil
}

// normalizeExcludeTypes splits comma-separated exclusions and expands type
// aliases, so --exclude-type=mr,epic and --exclude-type mr --exclude-type epic
// mean the same thing.
func normalizeExcludeTypes(raw []string) []types.IssueType {
	var out []types.IssueType
	for _, group := range raw {
		for _, t := range strings.Split(group, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				out = append(out, types.IssueType(utils.NormalizeIssueType(t)))
			}
		}
	}
	return out
}
