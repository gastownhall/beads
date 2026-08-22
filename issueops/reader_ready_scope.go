package issueops

import (
	"fmt"
	"strings"
)

// readyScopeField is one ListRequest field the ReadyFlag arm cannot carry,
// paired with the `bd list` flag that sets it. Naming both is the point of the
// refusal: a library caller looks for the struct field and a CLI caller for
// the flag, and neither should have to map one onto the other to learn which
// part of their request could not be honored.
type readyScopeField struct {
	name string
	flag string
	set  func(ListRequest) bool
}

// readyScopeFields is the drop set: every field a list request can set that
// does not survive the trip to the blocker-aware ready query.
//
// It was read off the two functions between this request and that query —
// internal/workapi.BuildListFilter, which turns the request into a storage
// filter, and internal/workapi.ReadyFilterFromIssueFilter, which projects that
// filter onto the narrower ready-work filter — and it is exactly the
// intersection: fields the builder copies onto the filter and the projection
// then discards.
//
// Four groups are deliberately ABSENT, because listing them here would refuse
// requests that are answered correctly today:
//
//   - What the projection carries: IssueType, all five label forms, Assignee,
//     NoAssignee, the exact Priority, ParentID, MolType, WispType,
//     MetadataFields, HasMetadataKey, ExcludeTypes (and with it IncludeGates
//     and IncludeInfra's type suppression), IncludeEphemeral (and with it
//     IncludeInfra's plane half, which the ready query admits through its own
//     ephemeral gate), Limit, Offset, and the MaxRows cap with its
//     attribution.
//
//   - THE HYDRATION KNOBS, SkipLabels and SkipCounts. They are the one group
//     the "exactly the intersection" rule above would otherwise sweep in: the
//     builder does copy them onto the filter and the projection does discard
//     them. They stay out because they select what is HYDRATED rather than
//     which rows match, so a ReadyFlag request that sets one is answered with
//     the rows it asked for and merely pays for a column it did not want.
//     Refusing that would be refusing a correct answer over its cost, and the
//     promise is already stated where a caller reads it (ListRequest.ReadyFlag).
//
//   - Status and AllFlag. The builder resolves both to "open" under ReadyFlag
//     before the projection ever runs, so IssueFilter.Statuses is never
//     populated on this path and there is nothing for the projection to drop.
//     Ready work is open work; that override is pinned by the builder's golden
//     file (internal/workapi/testdata/list_filter_golden.json,
//     ready_flag_overrides_status).
//
//   - NoPinnedFlag. Pinned is dropped by the projection, but the ready-work
//     WHERE clause excludes pinned rows unconditionally
//     (internal/storage/sqlbuild/ready.go), so a caller who asks for the
//     pinned rows to be left out gets exactly that. The opposite request,
//     PinnedFlag, asks for pinned rows only and would be answered with a set
//     that can never contain one — it is in the list below.
var readyScopeFields = []readyScopeField{
	{"IDFilter", "--id", func(r ListRequest) bool { return r.IDFilter != "" }},
	{"TitleSearch", "--title", func(r ListRequest) bool { return r.TitleSearch != "" }},
	{"SpecPrefix", "--spec", func(r ListRequest) bool { return r.SpecPrefix != "" }},

	{"TitleContains", "--title-contains", func(r ListRequest) bool { return r.TitleContains != "" }},
	{"DescContains", "--desc-contains", func(r ListRequest) bool { return r.DescContains != "" }},
	{"NotesContains", "--notes-contains", func(r ListRequest) bool { return r.NotesContains != "" }},
	{"ExternalContains", "--external-contains", func(r ListRequest) bool { return r.ExternalContains != "" }},
	{"ExternalRef", "--external-ref", func(r ListRequest) bool { return r.ExternalRef != "" }},

	{"CreatedAfter", "--created-after", func(r ListRequest) bool { return r.CreatedAfter != nil }},
	{"CreatedBefore", "--created-before", func(r ListRequest) bool { return r.CreatedBefore != nil }},
	{"UpdatedAfter", "--updated-after", func(r ListRequest) bool { return r.UpdatedAfter != nil }},
	{"UpdatedBefore", "--updated-before", func(r ListRequest) bool { return r.UpdatedBefore != nil }},
	{"ClosedAfter", "--closed-after", func(r ListRequest) bool { return r.ClosedAfter != nil }},
	{"ClosedBefore", "--closed-before", func(r ListRequest) bool { return r.ClosedBefore != nil }},
	{"DeferAfter", "--defer-after", func(r ListRequest) bool { return r.DeferAfter != nil }},
	{"DeferBefore", "--defer-before", func(r ListRequest) bool { return r.DeferBefore != nil }},
	{"DueAfter", "--due-after", func(r ListRequest) bool { return r.DueAfter != nil }},
	{"DueBefore", "--due-before", func(r ListRequest) bool { return r.DueBefore != nil }},

	{"DeferredFlag", "--deferred", func(r ListRequest) bool { return r.DeferredFlag }},
	{"OverdueFlag", "--overdue", func(r ListRequest) bool { return r.OverdueFlag }},
	{"EmptyDesc", "--empty-description", func(r ListRequest) bool { return r.EmptyDesc }},
	{"NoLabels", "--no-labels", func(r ListRequest) bool { return r.NoLabels }},
	{"NoParent", "--no-parent", func(r ListRequest) bool { return r.NoParent }},
	{"PinnedFlag", "--pinned", func(r ListRequest) bool { return r.PinnedFlag }},

	{"PriorityMin", "--priority-min", func(r ListRequest) bool { return r.PriorityMin != nil }},
	{"PriorityMax", "--priority-max", func(r ListRequest) bool { return r.PriorityMax != nil }},

	// The keyset position is a pair and only the timestamp half decides
	// whether one was supplied; AfterID alone is not a position.
	{"AfterCreatedAt/AfterID", "cursor", func(r ListRequest) bool { return r.AfterCreatedAt != nil }},
}

// ValidateReadyFlagScope refuses a list request that combines ReadyFlag with a
// filter the blocker-aware ready query cannot carry, naming every field it
// could not honor. A generic refusal would leave the caller to bisect their
// own request.
//
// This is the enforcement half of ListRequest.ReadyFlag's promise; the other
// half is the doc comment there. It lives beside that promise rather than in
// any implementation, and it is called from the single builder every
// implementation of Reader runs (internal/workapi.BuildListFilter), so no
// backend can be missing it — a per-backend copy is the drift this role exists
// to remove.
//
// A request without ReadyFlag is never refused here: every field below is
// honored by the ordinary listing.
func ValidateReadyFlagScope(req ListRequest) error {
	if !req.ReadyFlag {
		return nil
	}
	var named []string
	for _, f := range readyScopeFields {
		if f.set(req) {
			named = append(named, f.name+" ("+f.flag+")")
		}
	}
	if len(named) == 0 {
		return nil
	}
	return fmt.Errorf("%w: --ready cannot filter on %s; the blocker-aware ready query carries only part of the list vocabulary, so answering would return every ready issue rather than the ones asked for — drop --ready, or drop what it cannot carry",
		ErrValidation, strings.Join(named, ", "))
}
