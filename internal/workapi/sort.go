package workapi

import (
	"cmp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// CompareIssuesBy orders two issues by one of `bd list --sort`'s fields.
// An unknown field compares equal, which leaves the underlying order intact.
func CompareIssuesBy(a, b *types.Issue, sortBy string) int {
	switch sortBy {
	case "priority":
		return cmp.Compare(a.Priority, b.Priority)
	case "created":
		return b.CreatedAt.Compare(a.CreatedAt)
	case "updated":
		return b.UpdatedAt.Compare(a.UpdatedAt)
	case "closed":
		if a.ClosedAt == nil && b.ClosedAt == nil {
			return 0
		} else if a.ClosedAt == nil {
			return 1
		} else if b.ClosedAt == nil {
			return -1
		}
		return b.ClosedAt.Compare(*a.ClosedAt)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	case "id":
		return utils.NaturalCompareIDs(a.ID, b.ID)
	case "title":
		return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "type":
		return cmp.Compare(a.IssueType, b.IssueType)
	case "assignee":
		return cmp.Compare(a.Assignee, b.Assignee)
	}
	return 0
}

// ReadyFilterFromIssueFilter projects a list filter onto the ready-work
// filter, which is how `--ready` reuses the whole list vocabulary against the
// blocker-aware query. WorkFilter is the narrower of the two, so this is a
// deliberate lossy projection: what it does not carry, ready work does not
// filter on.
func ReadyFilterFromIssueFilter(filter types.IssueFilter) types.WorkFilter {
	wf := types.WorkFilter{
		Status:         types.StatusOpen,
		Limit:          filter.Limit,
		Offset:         filter.Offset,
		Labels:         filter.Labels,
		LabelsAny:      filter.LabelsAny,
		ExcludeLabels:  filter.ExcludeLabels,
		LabelPattern:   filter.LabelPattern,
		LabelRegex:     filter.LabelRegex,
		ParentID:       filter.ParentID,
		MolType:        filter.MolType,
		WispType:       filter.WispType,
		ExcludeTypes:   filter.ExcludeTypes,
		MetadataFields: filter.MetadataFields,
		HasMetadataKey: filter.HasMetadataKey,
		MaxRows:        filter.MaxRows,
		MaxRowsSource:  filter.MaxRowsSource,
	}
	if filter.IssueType != nil {
		wf.Type = string(*filter.IssueType)
	}
	if filter.Priority != nil {
		wf.Priority = filter.Priority
	}
	if filter.Assignee != nil {
		wf.Assignee = filter.Assignee
	}
	if filter.NoAssignee {
		wf.Unassigned = true
	}
	if filter.Ephemeral != nil && *filter.Ephemeral {
		wf.IncludeEphemeral = true
	}
	return wf
}

// WithFetchOneExtra bumps Limit by one so the caller can detect "more results
// than the limit" (GH#3212) by comparing the row count against the original
// limit.
//
// `--limit N --max-rows N` (equal) needs special handling so this bump doesn't
// collide with the MaxRows cap check: EffectiveSearchLimit treats
// Limit==MaxRows as "no overage sniff" (returns Limit verbatim, no +1 — see
// EffectiveSearchLimit's doc and the WithLimit_LimitAtCap_NoError storage
// test), but bumping Limit to N+1 alone flips that to Limit>MaxRows, which
// *does* sniff for overage — so the probe row itself would trip
// EnforceMaxRowsCap(N+1, N, ...) even though the delivered set is always
// trimmed back to N. Bumping MaxRows by one in lockstep, only in this exact
// N==M case, keeps the probe row inside the (temporarily N+1) cap so
// EnforceMaxRowsCap never fires on it while still fetching N+1 rows for the
// truncation check. This can't false-negative a real violation: the query's
// own LIMIT is capped at N+1 either way, so "more than N+1 matches exist" was
// already undetectable at Limit==MaxRows before this bump ever existed (the
// same "no overage detection above the equal cap" contract EffectiveSearchLimit
// documents for the unbumped case).
//
// This does not change the Limit>MaxRows (tighter-cap) or Limit<MaxRows cases:
// EffectiveSearchLimit's branch selection there is unaffected by a one-row bump
// to Limit, and MaxRows is left untouched, so a genuine tighter-cap violation
// still fires with the correct (unbumped) Cap value in the error message.
func WithFetchOneExtra(filter types.IssueFilter) types.IssueFilter {
	if filter.Limit > 0 {
		if filter.MaxRows > 0 && filter.Limit == filter.MaxRows {
			filter.MaxRows++
		}
		filter.Limit++
	}
	return filter
}
