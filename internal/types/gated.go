package types

import "strings"

// This file holds the ONE rule by which an issue counts as GATED, and the
// projection of a gate onto the wire.
//
// GATED IS DERIVED, NEVER STORED. `bd gate create --blocks X` writes a gate
// issue plus an ordinary blocking dependency X -> gate; nothing on X changes.
// `bd ready` then drops X through the denormalized is_blocked column, and the
// rule here is the subset of that column's recompute
// (internal/storage/issueops/blocked_consistency.go) a gate can account for:
//
//   - the EDGE clause, from the recompute's first two union legs: the edge is
//     'blocks' or 'conditional-blocks' and the target is neither closed nor
//     pinned (IsGateEdge + the status test in GateIsHolding);
//   - the SUBJECT clause, from unmarkAllBlockedSQL, which forces is_blocked=0
//     for a closed or pinned subject whatever its edges say (SubjectCanBeGated).
//
// Both halves live here, in one predicate every surface asks, so a glyph, a
// header and a gated_by field cannot disagree with each other or with `bd
// ready` about the same bead.
//
// WAITS-FOR IS DELIBERATELY OUT. The recompute's waits-for leg does not apply
// the closed/pinned status test at all: it goes through waitsForGateBlockedSQL
// (internal/storage/issueops/blocked_state.go), which blocks only when the
// target has an OPEN parent-child child, or metadata.also_blocks names an open
// spawner. A plain `bd dep add X G -t waits-for` onto an open childless gate
// therefore leaves X in `bd ready`, and decorating it would be exactly the
// drift this file exists to prevent. Mirroring that leg in Go would be a
// second implementation of a rule the database already owns; a decoration that
// covers the fanout case is its own bead, not this one.

// GateRef is a gate projected for a caller: the id to resolve, what kind of
// wait it is, and why. It is what `gated_by` carries on the detail view.
type GateRef struct {
	ID string `json:"id"`
	// Type is the gate's await condition (human, timer, gh:run, gh:pr, bead);
	// "gate" when the issue carries no await type, which is what a gate made
	// with a bare `bd create --type gate` looks like.
	Type string `json:"type,omitempty"`
	// Reason is the free text `bd gate create --reason` recorded, empty when
	// the gate was created without one.
	Reason string `json:"reason,omitempty"`
}

// IsGateEdge reports whether a dependency of this type is one whose target's
// status alone decides blockedness — the two legs of the is_blocked recompute
// that carry "target not closed and not pinned". It is NOT
// DependencyType.IsBlockingEdge, which also admits waits-for; see the file
// comment.
func IsGateEdge(depType DependencyType) bool {
	return depType == DepBlocks || depType == DepConditionalBlocks
}

// SubjectCanBeGated reports whether an issue's OWN status admits the gate
// decoration. A closed or pinned issue never does: unmarkAllBlockedSQL clears
// is_blocked for those two statuses unconditionally, so `bd ready` withholds
// them for reasons that have nothing to do with a gate, and a "GATED" marker
// on such a row would claim a causation that is not there.
//
// The listing route asks this without hydrated dependencies, which is why the
// clause is exported rather than buried inside GatesHolding.
func SubjectCanBeGated(subject *Issue) bool {
	if subject == nil {
		return false
	}
	return subject.Status != StatusClosed && subject.Status != StatusPinned
}

// GateIsHolding reports whether target, reached over a dependency of type
// depType, is a gate that is currently holding its dependent back. It is the
// EDGE half of the rule; the caller still owes the subject half, which is what
// GatesHolding exists to keep it from forgetting.
func GateIsHolding(depType DependencyType, target *Issue) bool {
	if target == nil || target.IssueType != TypeGate {
		return false
	}
	if !IsGateEdge(depType) {
		return false
	}
	return target.Status != StatusClosed && target.Status != StatusPinned
}

// GatesHolding selects, from subject's hydrated dependencies, the gates that
// are actively holding subject back, in the order the dependencies arrived.
//
// THE one predicate: every gated decoration — `bd show`'s header and meta
// lines, `bd list`'s glyph, the agent line, the detail view's gated_by — comes
// from this function or from the pair it is built out of, so the result is
// nonempty exactly when `bd ready` withholds subject on a gate's account.
func GatesHolding(subject *Issue, deps []*IssueWithDependencyMetadata) []*Issue {
	if !SubjectCanBeGated(subject) {
		return nil
	}
	var gates []*Issue
	for _, dep := range deps {
		if dep == nil {
			continue
		}
		issue := dep.Issue
		if GateIsHolding(dep.DependencyType, &issue) {
			gate := issue
			gates = append(gates, &gate)
		}
	}
	return gates
}

// GateKind names a gate's await condition for display, falling back to the
// issue type when the gate carries none.
func GateKind(gate *Issue) string {
	if gate == nil {
		return ""
	}
	if gate.AwaitType != "" {
		return gate.AwaitType
	}
	return string(TypeGate)
}

// NewGateRef projects one gate issue onto the wire shape.
func NewGateRef(gate *Issue) GateRef {
	if gate == nil {
		return GateRef{}
	}
	return GateRef{
		ID:     gate.ID,
		Type:   GateKind(gate),
		Reason: GateReason(gate.Description),
	}
}

// GateRefs projects a gate set, returning nil for an empty one so the field
// stays absent rather than serializing an empty array.
func GateRefs(gates []*Issue) []GateRef {
	if len(gates) == 0 {
		return nil
	}
	refs := make([]GateRef, 0, len(gates))
	for _, gate := range gates {
		refs = append(refs, NewGateRef(gate))
	}
	return refs
}

// ReasonMarker separates a generated description from the free-text reason its
// author gave. The reason has no column of its own, so the description is
// where it lives.
//
// It is SHARED, not gate-private: `bd gate create` writes it through
// GateDescription, and `bd state`'s state-change EVENT descriptions write the
// same marker (cmd/bd/state.go). One exported constant so the spelling cannot
// drift between them — and so the sharing is visible rather than a coincidence
// two files have to keep on purpose.
const ReasonMarker = "\n\nReason: "

// GateDescription builds an ad-hoc gate's description. `bd gate create` is its
// only writer.
func GateDescription(targetID, reason string) string {
	desc := "Ad-hoc gate blocking " + targetID
	if reason != "" {
		desc += ReasonMarker + reason
	}
	return desc
}

// GateReason recovers the reason GateDescription recorded, or "" when the
// description carries no marker.
//
// It is a TEXT read-back, not proof of provenance: ReasonMarker is shared (see
// its doc), so any description containing it yields a reason here — a gate
// written by hand with that spelling included. What the pair does guarantee is
// the round trip: what GateDescription wrote, this returns. `bd gate create`
// (cmd/bd/gate.go) is the only GateDescription caller today, so every gate
// whose reason this reports is one it recorded.
func GateReason(description string) string {
	idx := strings.Index(description, ReasonMarker)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(description[idx+len(ReasonMarker):])
}
