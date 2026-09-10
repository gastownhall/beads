package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// RelatedIssue is one issue on the far end of an edge, carrying the edge's
// type alongside the issue's own fields.
//
// It is an ISSUE and can be nothing else, which settles what an edge whose
// target is neither issue nor wisp answers with. A dependency target may be an
// "external:" reference or an issue belonging to ANOTHER repository — see
// DependencyEditor, which accepts both — and this database holds no row for
// either. Such an edge has no far end to describe here, so it is not a neighbor:
// Related leaves it out, with no placeholder row and no error.
//
// The consequence is worth stating because it is not visible in the answer: the
// length of a result is the anchor's neighbor count and NOT its edge count, and
// the two differ by however many of its edges point outside this database. A
// caller that needs the edges themselves needs a role that answers with edges.
type RelatedIssue = types.IssueWithDependencyMetadata

// RelationDirection selects which way the anchor's edges are walked.
//
// Its ZERO VALUE is invalid, and that is the point. A silent default direction
// is how a caller asks for "what blocks this" and is handed "what this blocks"
// — the exact inverse of the graph it wanted, with the same shape, the same
// field names and no error to notice.
type RelationDirection string

const (
	// RelationOut walks OUTGOING edges: the issues the anchor depends on.
	RelationOut RelationDirection = "out"
	// RelationIn walks INCOMING edges: the issues that depend on the anchor.
	RelationIn RelationDirection = "in"
)

// RelatedRequest describes one neighbor query.
type RelatedRequest struct {
	// ID is the anchor's exact canonical id, and EXACT is a promise the role
	// keeps rather than an obligation it puts on the caller: a spelling that is
	// not the stored one resolves to nothing. A case variant, an id carrying
	// surrounding whitespace, a prefix of a real id and a real id with a suffix
	// are all misses, for the same reason GetRequest.ID has no fuzzy resolution
	// — an affordance that can answer ABOUT A DIFFERENT ISSUE than the caller
	// named has no place on a contract an unattended client calls.
	//
	// Both planes are searched, and a miss on both is ErrNotFound rather than an
	// empty answer: "this issue has no dependencies" and "there is no such
	// issue" are different facts, and a caller that cannot tell them apart
	// reports a typo as a clean graph.
	ID string
	// Direction is required. The zero value is ErrValidation; there is no
	// implicit "both", and no direction is the default.
	Direction RelationDirection
	// Types restricts the answer to these edge types. Empty means every type.
	//
	// An entry is checked for being a value at all — non-empty, within the
	// column's length — and NEVER for membership of a known-types list: the
	// vocabulary is OPEN (see the Dep* constants), so a workspace's own type
	// has to be able to filter. An unusable entry is ErrValidation rather than
	// a filter that quietly matches nothing.
	Types []DependencyType
}

// Relations describes reading an issue's neighbors: the read side of `bd dep
// list`, and — like Reader — a role with its own accessor. A new capability
// gets a new role interface and its own accessor; never append a method here.
//
// It is its own role rather than a fourth Reader method because it answers a
// question about EDGES. Reader's three methods all answer with pages of
// issues, selected by the issues' own fields; this one is anchored on a
// specific issue and its answer is shaped by the edges reaching it, which is
// why its result carries the edge type and its request carries a direction.
//
// The dependency TREE is deliberately not here. A recursive walk has a depth,
// a cycle policy and a node shape of its own, and it answers with a forest
// rather than a list — a different question, so a different role when
// something needs it.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. RelatedRequest travels by value, so Types is the one
// member a body could write through to the caller at all: reading it into a
// set is fine, sorting or de-duplicating it in place is not.
type Relations interface {
	// Related returns the anchor's neighbors in the requested direction.
	//
	// THE ORDER IS PINNED: ascending by the neighbor's id, with the edge type
	// breaking a tie. It is pinned rather than left to the query because the
	// rows come from two dependency tables read in sequence, so the natural
	// order is an artifact of which plane a neighbor happens to live on —
	// stable enough to look deliberate and not stable enough to rely on.
	//
	// The result is never nil for a successful call.
	Related(ctx context.Context, req RelatedRequest) ([]*RelatedIssue, error)
}
