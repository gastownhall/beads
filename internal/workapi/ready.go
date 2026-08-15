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
		Status: types.StatusOpen,
		Type:   utils.NormalizeIssueType(in.IssueType),
		Limit:  LimitOr(in.Limit, DefaultReadyLimit),
		// Carried for the callers that consume this filter as a VALUE and run
		// their own query (cmd/bd's proxied `bd ready`), where the seam beneath
		// them renders it. Both implementations of issueops.Reader take it back
		// off and skip in the shared page epilogue; see BuildListFilter and
		// FinishPageAt.
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
		Lite:             in.Brief,
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
		// A deterministic request-validation failure, so it matches
		// ErrValidation: every role whose filter vocabulary this builds —
		// Reader.Ready, ReadyClaimer.ClaimNext and BatchCloser's ClaimNext —
		// promises a caller can classify one with errors.Is rather than by
		// reading prose. The wrap is %.0w rather than a "%w: " prefix because
		// this text is what `bd ready --sort bogus` prints verbatim behind
		// "Error: "; prefixing it would change user-visible copy to say
		// something the reader already knows.
		return filter, fmt.Errorf("invalid sort policy '%s'. Valid values: hybrid, priority, oldest%.0w", in.Sort, issueops.ErrValidation)
	}
	return filter, nil
}

// BuildReadyCountFilter turns a ready request into the storage-level filter a
// COUNT of the ready set runs against: BuildReadyFilter's filter with the page
// removed, and the single definition of what `bd ready`'s published total means.
//
// It refuses a request carrying a page. issueops.ReadyCounter.CountReady
// promises its answer equals len(Reader.Ready(r with Limit=0).Items), and a
// Limit would make that "how many of the first N" while an Offset would
// subtract the rows it skipped from the size of a set that still holds them.
//
// The zeroed limit is set on a LOCAL copy: a nil Limit means the shared ready
// default at BuildReadyFilter, so an unlimited count has to say so explicitly.
func BuildReadyCountFilter(in issueops.ReadyRequest) (types.WorkFilter, error) {
	if in.Limit != nil {
		return types.WorkFilter{}, fmt.Errorf("%w: a ready count does not take a limit", issueops.ErrValidation)
	}
	if in.Offset != 0 {
		return types.WorkFilter{}, fmt.Errorf("%w: a ready count does not take an offset", issueops.ErrValidation)
	}
	unlimited := 0
	counted := in
	counted.Limit = &unlimited
	// Brief is CARRIED, not refused and not cleared, which is the opposite of
	// what ClaimNext does with it. A count reads no field of any row, so the
	// projection cannot make the number wrong; and the count is not always
	// cheap enough for that to be the end of it. The unit-of-work seam has no
	// COUNT(*) over the ready predicate and sizes the set by running the
	// unbounded page and taking its length (uow/ready_counter.go), so clearing
	// the field here would hydrate every heavy column of the whole ready set to
	// answer `bd ready --brief`, which is the cost the projection exists to
	// avoid and larger than the page it was asked for. Carrying it also keeps
	// the count filter what this builder says it is: the listing's filter with
	// the PAGE removed, and nothing else removed.
	return BuildReadyFilter(counted)
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
