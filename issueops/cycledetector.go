package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// DetectCyclesRequest asks for the whole blocking graph's cycles.
//
// It carries NOTHING, and the empty struct is the honest shape rather than a
// placeholder. A cycle is a property of the graph as a whole: there is no
// predicate that narrows it, because narrowing the EDGE set would delete
// cycles that exist and narrowing the reported set would need an anchor this
// question does not have. `bd dep cycles` takes no flags for the same reason.
//
// It exists as a type so that a later option — a bound on how much of the graph
// is walked, say — is a new field rather than a new method or a changed
// signature, which is what every other role's request buys.
type DetectCyclesRequest struct{}

// CycleMember is one node on a detected cycle.
//
// ID IS ALWAYS SET; Issue may be nil. The two are separate fields because they
// come from different places and can disagree in exactly one direction: the id
// is read off the dependency edge, which is what proves the node is on the
// cycle, and the issue is a SECOND lookup that can find nothing — a target in
// another repository's namespace, an "external:" reference, or a row whose edges
// outlived it.
//
// A nil Issue therefore means "this node is on the cycle and this database
// cannot describe it", never "this node is not really on the cycle". A caller
// rendering a path prints ID for every member and reaches for Issue only to
// decorate it.
type CycleMember struct {
	// ID is the node's id as the dependency edge spells it.
	ID string `json:"id"`
	// Issue is the hydrated row, or nil when this database holds none. It is
	// ABSENT from the JSON encoding rather than null in that case, which is the
	// same "absent means not set" rule every other surface in this tree reads.
	//
	// Callers MUST NOT mutate it: implementations do not promise a fresh copy
	// per member, and a node appearing on two cycles may be the same pointer in
	// both.
	Issue *types.Issue `json:"issue,omitempty"`
}

// Cycle is one detected cycle: its members in EDGE ORDER, so member[i] blocks
// on member[i+1] and the last member blocks on the first. The closing edge is
// implied and is not repeated as a final member.
//
// IT IS CANONICAL. Members are rotated so the LOWEST id comes first, and the
// report's cycles are sorted, so two runs against an unchanged database produce
// the same bytes. That was not true before: the detector walked a Go map, so
// both the order of the cycles and the starting point of each one moved between
// runs — including under `--json`, where a caller diffing two snapshots saw
// changes that were not changes. Rotation is safe because the members of a
// cycle are DISTINCT, so the lowest id is unique and names one rotation.
type Cycle struct {
	// Members are the nodes, in edge order, starting at the lowest id. Never
	// empty.
	Members []CycleMember `json:"members"`
	// Partial is true when at least one member has a nil Issue. It is emitted
	// even when false: a consumer must be able to read "this path is complete"
	// from the answer rather than from the absence of a key.
	//
	// It is a field rather than something a caller derives because it is the
	// thing a caller must not silently miss. The previous behavior DROPPED
	// unhydratable members from the path, so a three-node cycle rendered as a
	// two-node one and looked complete; a wholly unhydratable cycle disappeared
	// from the report and from its count. Members is complete now, and this
	// flag says the descriptions beside it are not.
	Partial bool `json:"partial"`
}

// CycleReport is every cycle the detector found.
type CycleReport struct {
	// Cycles is the canonical, sorted set. It is empty — never nil — when the
	// graph is acyclic.
	//
	// ITS LENGTH IS THE TOTAL, and that is the point of carrying unhydratable
	// members rather than dropping them: a cycle nothing can describe is still
	// counted here, so "found N cycles" cannot shrink because a row went
	// missing. There is no separate count field, because there is no cycle this
	// slice omits.
	//
	// IT IS NOT PROMISED TO BE EVERY SIMPLE CYCLE IN THE GRAPH. Enumerating
	// those is exponential in the worst case; the detector records one cycle per
	// back edge of a depth-first walk, so two cycles sharing every node but one
	// edge may be reported as one. What IS promised is that the slice is empty
	// exactly when the graph is acyclic, and that the same graph always yields
	// the same slice.
	Cycles []Cycle `json:"cycles"`
}

// CycleDetector describes finding circular dependencies: the operation
// `bd dep cycles` performs, and the sweep both `bd dep add` routes run to warn
// after an edge lands. Like Lifecycle, Reader, ReadyClaimer, BatchCloser,
// DependencyEditor, Commenter, Counter and Relations it is a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A THIRD Relations METHOD because it answers a
// question with no ANCHOR. Relations answers about the edges around one issue —
// every request it takes names a source — and DependencyEditor refuses a cycle
// it is about to create. This one is asked of the whole graph and hands back
// paths rather than neighbors, so a Relations request type would have carried an
// anchor field a cycle sweep must ignore.
//
// WHAT COUNTS AS AN EDGE HERE is narrower than what counts when an edge is
// ADDED, and the two must not be confused. This walk follows `blocks` and
// `conditional-blocks` only. `waits-for` is excluded — gate semantics, so a
// mutual wait is not a deadlock — and so is `parent-child`, which the
// add-time gate DOES walk (a chain mixing blocking and hierarchy is a
// scheduling livelock the editor refuses). A graph this role calls acyclic can
// therefore still refuse an edge, and that is not a contradiction: the editor
// answers "would this be a livelock", this answers "is there a blocking cycle
// now".
//
// BOTH PLANES ARE ONE GRAPH. Durable and ephemeral edges are merged before the
// walk, so a cycle that runs issue → wisp → issue is found; it is the reason
// this cannot be answered by reading one table.
//
// HOW CYCLES GET THERE AT ALL, since the editor refuses them: not through it.
// They arrive by import, by bulk writes that skipped the per-edge probe, by
// concurrent adds that each saw an acyclic graph, and from rows written before
// the gate existed. That is why this role is a REPORT and not an assertion —
// finding one is not a bug in the writer that is running.
//
// Detecting is a READ. Nothing here records a history entry, fires a completion
// hook or changes a row. Result values are unspecified when error is non-nil.
type CycleDetector interface {
	// DetectCycles reports every cycle in the blocking graph.
	//
	// An acyclic graph is an empty report and a nil error. There is no
	// ErrNotFound and no refusal to classify: a question about a graph has an
	// answer even when the answer is "none", and a caller warning after a write
	// would otherwise have to tell an empty answer from a failure.
	//
	// A MEMBER THAT CANNOT BE HYDRATED DOES NOT FAIL THE CALL and does not
	// shrink the path. Its CycleMember carries the id with a nil Issue and its
	// Cycle is marked Partial. A report that failed outright on one unreadable
	// row would tell a caller nothing about the other nine cycles, and a report
	// that quietly dropped the row would tell it something false.
	DetectCycles(ctx context.Context, req DetectCyclesRequest) (CycleReport, error)
}
