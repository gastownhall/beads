package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// StatsRequest asks for the workspace's summary statistics. It carries no
// predicate at all, and that absence is the type's whole shape: this is a
// question about the WORKSPACE, not about a set the caller picked out.
//
// A caller that wants a number about a set it described calls Counter. The two
// roles are told apart by exactly this: CountRequest has thirty filter fields
// and no dependency-aware number in its answer; StatsRequest has one
// performance knob and every number in its answer is either a status tally of
// the whole durable plane or derived from the dependency graph.
type StatsRequest struct {
	// SkipBlocked asks the implementation to answer WITHOUT the blocked-set
	// scan, which is the expensive half of this query on a large workspace.
	//
	// It is a HINT, and the promise is stated from the answer's side rather
	// than the request's: see StatsReporter.Stats. When it is honored,
	// StatsResult.Summary.BlockedIssues and .ReadyIssues are both nil; when the
	// implementation has no cheaper path it answers exactly as if this field
	// were unset. Nothing else in the result changes either way, so a caller
	// that does not read the two pointers cannot tell the difference — which is
	// the point of it being a hint.
	SkipBlocked bool
}

// AssigneeStatsRequest asks for one actor's summary statistics.
//
// It is a separate request type, answered by a separate method, rather than an
// Assignee field on StatsRequest. That is not ceremony: the two questions
// return the same struct and fill FOUR of its fields by different definitions
// (see StatsReporter.AssigneeStats). A single request with an optional
// Assignee would have made those definitions depend on whether a string was
// empty, which is the shape that makes a reader trust a number that means
// something else.
type AssigneeStatsRequest struct {
	// Assignee is the actor to answer for, used AS WRITTEN: there is no
	// trimming, no case folding and no alias expansion, because an assignee is
	// an opaque identifier this layer has no vocabulary for.
	//
	// An empty or whitespace-only value is ErrValidation rather than a query.
	// An empty assignee would otherwise select the rows whose assignee column
	// is empty and report them as one actor's workload, and there is no caller
	// that means that: `bd status --assigned` resolves an actor before it asks,
	// and an HTTP caller that omits the parameter is asking the workspace-wide
	// question and gets Stats.
	Assignee string
}

// StatsResult carries the summary.
//
// It wraps the CANONICAL struct rather than redeclaring its ten fields, and
// that is load-bearing rather than lazy. types.Statistics is what `bd status
// --json` marshals under "summary" and what the HTTP operation's `summary`
// object is pinned to with x-go-type, so both front doors serialize ONE struct
// and neither performs a Go-to-Go mapping on the way to the wire. A result type
// of this role's own would have put a translation step between the role and
// both surfaces, which is precisely the second wire struct that
// internal/httpapi/pinning.go exists to forbid.
//
// The summary is a VALUE, not a pointer. A role that answered with a pointer
// would let a nil-with-nil-error reach a front door that dereferences it — the
// hazard internal/httpapi/roles.go wraps checkedReader around Get for — and
// there is nothing this role could mean by "no summary": an empty workspace has
// statistics, and they are zeros.
type StatsResult struct {
	Summary types.Statistics
}

// StatsReporter describes the summary statistics `bd status` prints and its
// `bd stats` alias reprints — and, like Lifecycle, Reader, ReadyClaimer,
// BatchCloser, DependencyEditor, Commenter, Relations and Counter, a role with
// its own accessor. A new capability gets a new role interface and its own
// accessor; never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A SIXTH CountGroup because its numbers are
// DEPENDENCY-AWARE. A grouped count partitions rows by a column; two of the
// numbers here — the blocked count and the ready count derived from it — come
// from the denormalized transitive is_blocked flag the dependency graph
// maintains, and no predicate over the issues table can describe them. Counter
// says the same thing from its side, and this is the role it points at.
//
// WHAT THE NUMBERS ARE, and they are not all the same kind of number. Reading
// them as "six counts of one set" is the mistake this doc is written to
// prevent:
//
//   - TotalIssues, OpenIssues, InProgressIssues, ClosedIssues, DeferredIssues
//     and PinnedIssues are one scan of the DURABLE issues plane. Every row is
//     in Total, including closed and pinned ones; the status tallies are exact
//     equality against the stored status, so a workspace with a custom status
//     has rows that appear in Total and in none of the four buckets, and the
//     buckets do not sum to Total. PinnedIssues counts the pinned FLAG and
//     overlaps every status bucket.
//   - BlockedIssues counts rows whose transitive is_blocked flag is set,
//     excluding those whose STATUS is "closed" or "pinned". The exclusion is
//     by status, NOT by the pinned flag the bullet above describes, and the
//     two are different rows: a flag-pinned OPEN row with an unfinished
//     blocker IS counted here. It is also not the count of rows whose status
//     is "blocked" — an open row with an unfinished blocker is counted here
//     and its status is still "open".
//   - ReadyIssues is ARITHMETIC, not a query: OpenIssues minus BlockedIssues,
//     clamped at zero. It is therefore not the cardinality of `bd ready`, which
//     applies type exclusions, the deferral window, assignee filters and a
//     limit that none of this touches. A caller that needs the ready-work count
//     asks the ready-work question; this field is the headline number
//     `bd status` prints and nothing more.
//   - EpicsEligibleForClosure and AverageLeadTime are ALWAYS ZERO. No shipped
//     implementation computes either, on any backend or either front door.
//     They are carried because types.Statistics is the struct both surfaces
//     serialize and dropping a field from it is a wire change; they are named
//     here so a caller does not read a zero as an answer — and so a reader
//     knows why `bd status`'s "Extended:" branch has never once printed.
//
// THE WISP TIER IS NOT MERGED into the workspace-wide answer: Stats counts the
// durable plane only, which is why an empty workspace full of wisps reports
// zero. AssigneeStats does merge it, and says so — one of the four places the
// two methods differ.
//
// Reporting is a READ. Nothing here records a history entry, fires a completion
// hook or changes a row, and a refusal changes nothing either. Deterministic
// request-validation failures match ErrValidation; result values are
// unspecified when error is non-nil. Implementations never mutate caller-owned
// request values.
//
// THE PER-ROLE COST of adding a role like this one is written down in
// engdocs/ADDING_AN_ISSUEOPS_ROLE.md.
type StatsReporter interface {
	// Stats returns the workspace summary.
	//
	// EMPTY IS ZEROS, NOT AN ERROR. A workspace with no issues answers with a
	// zero-valued summary and a nil error; there is no ErrNotFound on this
	// role, for the reason Counter gives — a question about a set has an
	// answer when the set is empty, and a caller polling a fresh workspace
	// would otherwise have to classify an error to read a zero.
	//
	// SkipBlocked IS A HINT, and this is the whole of its promise:
	//
	//   - BlockedIssues and ReadyIssues are nil TOGETHER or populated
	//     TOGETHER, never one of each. Readiness is derived from the blocked
	//     count, so there is no state in which one is knowable and the other
	//     is not, and every front door renders on exactly that pairing.
	//   - Nothing else in the summary changes. The other counts are the same
	//     numbers a full call returns.
	//   - Whether the hint was TAKEN is reported by BlockedIssues being nil,
	//     and that is the only report. An implementation with no cheaper path
	//     answers with the full numbers rather than discarding two it already
	//     computed: nilling them to match a stricter contract would trade a
	//     real answer for a slower lie.
	//
	// The store-backed implementations take the hint; the unit-of-work seam has
	// no no-blocked variant and does not. That difference is deliberate,
	// observable and stated here rather than left for a caller to discover.
	Stats(ctx context.Context, req StatsRequest) (StatsResult, error)

	// AssigneeStats returns the summary restricted to one actor's rows.
	//
	// IT IS THE SAME STRUCT WITH FOUR FIELDS DEFINED DIFFERENTLY, and the
	// differences are the shipped behavior of `bd status --assigned` rather
	// than a design anyone would choose fresh:
	//
	//   - THE SET IS WIDER. It is the actor's rows across the durable plane AND
	//     the ephemeral wisps tier, because the search seam merges the two
	//     unless told not to, where the workspace-wide answer counts the
	//     durable plane only. So this Total is NOT a subset of that one: an
	//     actor holding wisps can report more rows here than Stats reports for
	//     the entire workspace.
	//   - BlockedIssues COUNTS STATUS. Here it is the number of the actor's
	//     rows whose stored status is "blocked" — not the is_blocked flag Stats
	//     reports. The two answer different questions and can disagree in both
	//     directions.
	//   - ReadyIssues IS THE READY-WORK QUERY. Here it is the cardinality of
	//     the actor's ready work — the real dependency-aware question, with its
	//     exclusions — where Stats subtracts two numbers. This one is the
	//     stronger answer, and it is the narrower one.
	//   - PinnedIssues IS ALWAYS ZERO, along with the two fields that are
	//     always zero everywhere. The fold that produces this summary tallies
	//     the five statuses and nothing else.
	//
	// A FAILED READY-WORK QUERY IS REPORTED AS ZERO READY WORK, not as an
	// error: the other five numbers are still right and the summary is still
	// returned. That is the shipped behavior of both `bd status --assigned`
	// routes, published here rather than quietly tightened, and it is the one
	// place this role can hand back a number that is not an answer. A caller
	// that must not confuse "none ready" with "could not tell" asks the
	// ready-work question directly, where the failure is its own.
	//
	// An empty or whitespace-only Assignee is ErrValidation. An assignee that
	// matches nothing is a zero-valued summary and a nil error, not a miss:
	// "this actor has no work" is an answer.
	AssigneeStats(ctx context.Context, req AssigneeStatsRequest) (StatsResult, error)
}
