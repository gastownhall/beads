package issueops

import (
	"context"
	"time"
)

// CountGroup names a bucketing dimension for a grouped count. The set is
// closed: a value outside it is ErrValidation rather than an empty answer,
// because a caller that misspelled a dimension and got zero buckets back has
// no way to tell that from a store with nothing in it.
type CountGroup string

const (
	CountGroupStatus   CountGroup = "status"
	CountGroupPriority CountGroup = "priority"
	CountGroupType     CountGroup = "type"
	CountGroupAssignee CountGroup = "assignee"
	CountGroupLabel    CountGroup = "label"
)

// CountRequest describes one issue-count query: the predicate, and nothing
// about a page.
//
// It is a HIGH-LEVEL request in the sense ReadyRequest and ListRequest are —
// label normalization, id splitting and the wisp-tier policy all happen inside
// — but it is NOT a ListRequest with the paging fields removed, and the
// difference is the whole reason this is a separate type. A default `bd list`
// hides closed, pinned, template and gate rows; a default `bd count` hides
// none of them and answers for every durable row that matches. Sharing
// ListRequest would have made the two questions look interchangeable at the
// call site while their answers differed by every one of those exclusions.
//
// THERE IS NO Limit AND NO Offset, deliberately. A count is a cardinality:
// bounding the scan would answer "how many of the first N" and there is no
// front door that wants that. The storage seam ignores both fields on the
// count path, so accepting them here would be accepting two knobs that do
// nothing.
//
// THERE IS NO FREE-TEXT QUERY either. The count seam takes one — the same
// full-text argument search does — and both front doors have always passed the
// empty string. A field no caller sets is a field whose behavior nothing
// checks, so it is left off rather than published untested; TitleSearch and
// the three *Contains fields are the substring matches that ARE reachable.
type CountRequest struct {
	// Status restricts to one stored status. Empty and the literal "all" both
	// mean every status, which is what makes a bare count answer for closed
	// rows as well as open ones.
	//
	// It is NOT validated against the workspace vocabulary and NOT a
	// comma-separated OR set: an unrecognized name matches nothing and returns
	// zero rather than failing, exactly as ReadyRequest.IssueType does. That is
	// the shipped behavior of both front doors and it is stated here rather
	// than quietly tightened, because a scripted caller that counts a status
	// its workspace has since dropped currently reads 0 and would start reading
	// an error.
	Status string
	// IssueType restricts the type, with the same match-nothing-rather-than-fail
	// treatment Status gets and no alias expansion at all.
	//
	// It has a SECOND effect under IncludeInfra: an infra type routes the count
	// to the ephemeral tier. See that field.
	IssueType string
	// Assignee restricts to one actor's rows; NoAssignee restricts to rows with
	// none. Setting both is not refused — they are handed to the filter as
	// written and answer with the empty intersection.
	Assignee string

	// Priority is an exact priority; PriorityMin and PriorityMax bound a range
	// inclusively. All three are pointers because 0 is a real priority, for the
	// reason ReadyRequest.Priority gives.
	Priority    *int
	PriorityMin *int
	PriorityMax *int

	// Labels must ALL be present; LabelsAny requires at least one. Both are
	// raw: entries are trimmed and de-duplicated INSIDE, and a slice whose
	// entries are all blank is the same as an unset one.
	Labels    []string
	LabelsAny []string

	// TitleSearch is a case-insensitive substring match on the title.
	TitleSearch string
	// IDFilter is a comma-separated id set. Splitting, trimming and
	// de-duplication happen inside, so a caller passes the string it was given
	// rather than a slice it had to prepare.
	IDFilter string

	// TitleContains, DescContains and NotesContains are substring matches on
	// the three long fields, spelled as ListRequest spells them so the two
	// requests cannot drift apart in naming while meaning the same thing.
	TitleContains string
	DescContains  string
	NotesContains string

	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	ClosedAfter   *time.Time
	ClosedBefore  *time.Time

	// EmptyDesc, NoAssignee and NoLabels restrict to rows missing each of those.
	EmptyDesc  bool
	NoAssignee bool
	NoLabels   bool

	// IncludeInfra switches the count from the durable plane to the cardinality
	// of `bd list --include-infra --all`, which is FOUR changes at once and not
	// one (GH#4387):
	//
	//   - the ephemeral wisps tier is merged in, picking up both wisps and the
	//     no_history beads that are durable work stored in that tier;
	//   - template molecules are excluded, which a default count includes;
	//   - gate beads are excluded, unless IssueType asks for gates by name;
	//   - an IssueType the workspace calls infra routes the count to the
	//     ephemeral tier instead of the durable one.
	//
	// The infra vocabulary is the WORKSPACE's, read from configuration inside
	// the implementation. A caller does not supply it and cannot: that is the
	// config load this role exists to keep off both front doors.
	//
	// Unset, the count is durable-plane only and applies none of those four —
	// the historical `bd count` answer, kept exactly so a scripted caller reads
	// the same number it read yesterday.
	IncludeInfra bool
}

// CountResult is the cardinality of the matching set.
type CountResult struct {
	// Total is the number of matching rows. It is an int64 because the storage
	// seam counts in one, not because a workspace is expected to hold four
	// billion beads.
	Total int64
}

// CountByGroupRequest describes one bucketed count.
type CountByGroupRequest struct {
	// Filter is the same predicate a scalar count takes, so the two questions
	// cannot be asked of different sets. It is embedded by NAME rather than
	// anonymously: a grouped count is a scalar count plus a dimension, and
	// spelling the dimension beside the whole filter says that, where promoted
	// fields would have made GroupBy look like one more predicate.
	Filter CountRequest
	// GroupBy is the bucketing dimension and must be one of the five
	// CountGroup constants. An empty or unknown value is ErrValidation, not a
	// scalar count in disguise: a caller that wanted a number calls Count.
	GroupBy CountGroup
}

// CountByGroupResult is one bucketed count.
type CountByGroupResult struct {
	// Groups maps each bucket's DISPLAY key to its cardinality. The keys are
	// normalized, and the normalization is part of this contract because both
	// front doors print them unmodified:
	//
	//   - a priority bucket is "P" followed by the number, so priority 1 is
	//     "P1";
	//   - the assignee bucket for unassigned rows is "(unassigned)", never the
	//     empty string, which would be indistinguishable from a store whose
	//     assignee column holds one;
	//   - the label bucket for rows carrying no label at all is "(no labels)",
	//     and it is ABSENT rather than zero when every matching row has one;
	//   - status and type buckets are the stored value verbatim.
	//
	// A dimension with no matching rows yields an empty map, never nil.
	Groups map[string]int
	// Total is the cardinality of the whole matching set — the SAME number a
	// scalar Count of Filter returns, and NOT the sum of Groups.
	//
	// The two differ for exactly one dimension, and it is the reason this field
	// exists rather than being left to the caller to add up: label buckets
	// OVERLAP. An issue carrying three labels is one row in Total and one row
	// in each of three buckets, so a caller that summed them would report a
	// workspace three times its size. Every front door that prints a grouped
	// count prints a total above it, and deriving that total is a decision this
	// role makes once instead of each surface making it again.
	//
	// It is NOT promised to be one snapshot with Groups. The store-backed
	// implementation runs the scalar and the grouped query separately, so a
	// concurrent write between them can leave Total disagreeing with the
	// buckets by that write. Nothing here is transactional across the two, and
	// a caller reconciling them under load should not expect it to be.
	Total int64
}

// Counter describes counting issues: the operation `bd count` performs, and —
// like Lifecycle, Reader, ReadyClaimer, BatchCloser, DependencyEditor,
// Commenter and Relations — a role with its own accessor. A new capability
// gets a new role interface and its own accessor; never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A COUNTED VARIANT OF Reader because it
// answers a different question. Reader answers with PAGES OF ISSUES: its
// requests carry a limit, an offset, a keyset position, a sort and a has-more
// verdict, and every one of those describes which rows come back and in what
// order. A count has no rows and therefore no order, no page and no cursor —
// it is a number about a set. Folding it into Reader would have meant a
// request type carrying paging fields that a count must ignore, which is the
// shape that makes a caller believe `--limit 10` bounded the answer.
//
// BOTH METHODS BELONG TO THE ONE ROLE, and that is not the rule bending. The
// governing rule forbids APPENDING to an existing role, not a role being born
// with the two shapes of one question: a scalar count and a bucketed count ask
// the same predicate of the same set and differ only in whether the answer is
// one number or a number per bucket. They also cannot be separated in
// practice — a grouped answer carries the scalar Total, because label buckets
// overlap and the sum is wrong (see CountByGroupResult.Total) — so splitting
// them would put one role's promise inside another role's result.
//
// WHAT IS NOT HERE. `bd status` and its `bd stats` alias look like counts and
// are not: their numbers are dependency-aware (ready work, blocked work,
// staleness) and are not expressible as a predicate over one table, so they
// get a role of their own rather than a sixth CountGroup. The count of READY
// work is likewise a different question — the ready predicate is not a filter
// this request can describe — and is its own role for the same reason.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply normalization only to attempt-local
// clones. That promise is load-bearing here and not merely conventional:
// CountRequest carries two slices, and label normalization is the step that
// would otherwise write through them.
//
// Counting is a READ. Nothing here records a history entry, fires a completion
// hook or changes a row, and a refusal changes nothing either. Deterministic
// request-validation failures match ErrValidation; result values are
// unspecified when error is non-nil.
//
// THE PER-ROLE COST of adding a role like this one — the accessor, the two
// decorator wrappers, the unit-of-work source and the three conformance
// wirings — is written down in engdocs/ADDING_AN_ISSUEOPS_ROLE.md. Counter is
// the role that checklist was derived from.
type Counter interface {
	// Count returns how many issues match the request. An empty request counts
	// every durable row in the workspace, including closed ones: a count
	// applies none of the listing's default exclusions.
	//
	// A predicate that matches nothing is 0 and a nil error. That is the whole
	// of the "not found" story for this role — there is no ErrNotFound here,
	// because a question about a set has an answer even when the set is empty,
	// and a caller polling for work would otherwise have to classify an error
	// to read a zero.
	Count(ctx context.Context, req CountRequest) (CountResult, error)

	// CountByGroup returns the same count bucketed by one dimension, plus the
	// scalar Total of the whole matching set.
	//
	// An unknown GroupBy is ErrValidation. Buckets with no rows are absent
	// rather than present at zero: the dimensions are open-ended (any assignee,
	// any label, any custom status), so there is no closed set of keys to
	// enumerate and a caller reads absence as zero.
	CountByGroup(ctx context.Context, req CountByGroupRequest) (CountByGroupResult, error)
}
