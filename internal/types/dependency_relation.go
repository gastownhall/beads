package types

import "strings"

// DependencyRelation names one dependency type from both ends of its edge. A
// stored edge is (issue_id, depends_on_id, type): Out* describes the edge as
// the issue_id side sees it, In* as the depends_on_id side sees it.
//
// Each type's direction comes from the command that writes it, NOT from reading
// the type name left to right — beads has no single convention there.
// `bd supersede old --with new` stores (old, new, supersedes), so the edge
// reads "issue_id is superseded by depends_on_id", while
// `bd duplicate dup --of canonical` stores (dup, canonical, duplicates), which
// reads the other way round. Getting this backwards prints a lie, so change an
// entry only alongside the command that creates that edge.
type DependencyRelation struct {
	OutHeading string // bd show section heading for the edge's source
	InHeading  string // bd show section heading for the edge's target
	OutGlyph   string
	InGlyph    string
	Phrase     string // reads "<source> <phrase> <target>", for dep add feedback
}

// dependencyRelations covers the well-known types. Missing entries fall back to
// the type's own name (see DependencyRelationFor), which is what custom types
// get. Unexported so no caller can rewrite an entry in place.
var dependencyRelations = map[DependencyType]DependencyRelation{
	DepBlocks:            {"DEPENDS ON", "BLOCKS", "→", "←", "depends on"},
	DepParentChild:       {"PARENT", "CHILDREN", "↑", "↳", "is a child of"},
	DepConditionalBlocks: {"CONDITIONALLY DEPENDS ON", "CONDITIONALLY BLOCKS", "→", "←", "conditionally depends on"},
	DepWaitsFor:          {"WAITS FOR", "AWAITED BY", "→", "←", "waits for"},
	DepRelated:           {"RELATED", "RELATED", "↔", "↔", "is related to"},
	DepRelatesTo:         {"RELATED", "RELATED", "↔", "↔", "relates to"},
	DepDiscoveredFrom:    {"DISCOVERED FROM", "DISCOVERED", "◊", "◊", "was discovered from"},
	DepRepliesTo:         {"IN REPLY TO", "REPLIES", "→", "←", "replies to"},
	DepDuplicates:        {"DUPLICATE OF", "DUPLICATED BY", "→", "←", "duplicates"},
	DepSupersedes:        {"SUPERSEDED BY", "SUPERSEDES", "→", "←", "is superseded by"},
	DepAuthoredBy:        {"AUTHORED BY", "AUTHORED", "→", "←", "was authored by"},
	DepAssignedTo:        {"ASSIGNED TO", "ASSIGNED", "→", "←", "is assigned to"},
	DepApprovedBy:        {"APPROVED BY", "APPROVED", "→", "←", "was approved by"},
	DepAttests:           {"ATTESTS", "ATTESTED BY", "→", "←", "attests"},
	DepTracks:            {"TRACKS", "TRACKED BY", "→", "←", "tracks"},
	DepUntil:             {"ACTIVE UNTIL", "KEEPS ACTIVE", "→", "←", "is active until"},
	DepCausedBy:          {"CAUSED BY", "CAUSED", "→", "←", "was caused by"},
	DepValidates:         {"VALIDATES", "VALIDATED BY", "→", "←", "validates"},
	DepDelegatedFrom:     {"DELEGATED FROM", "DELEGATED TO", "→", "←", "was delegated from"},
}

// DependencyRelationFor returns how to name an edge of this type. A custom
// type gets its own name rather than being described as a blocker it is not.
func DependencyRelationFor(t DependencyType) DependencyRelation {
	if rel, ok := dependencyRelations[t]; ok {
		return rel
	}
	name := strings.ToUpper(string(t))
	return DependencyRelation{
		OutHeading: name,
		InHeading:  name + " (INBOUND)",
		OutGlyph:   "→",
		InGlyph:    "←",
		Phrase:     string(t),
	}
}

// Relation returns the role word for an edge of this type, read from the shown
// issue's end: the bd show heading lower-cased with spaces as underscores, e.g.
// "duplicate_of" / "duplicated_by". outgoing means the shown issue is the
// edge's source (issue_id), i.e. the row sits under dependencies; otherwise it
// is the target and the row sits under dependents.
//
// A custom type reads as its own name outgoing and "<type>_inbound" incoming.
func (t DependencyType) Relation(outgoing bool) string {
	rel, ok := dependencyRelations[t]
	if !ok {
		if outgoing {
			return string(t)
		}
		return string(t) + "_inbound"
	}
	heading := rel.OutHeading
	if !outgoing {
		heading = rel.InHeading
	}
	return strings.ReplaceAll(strings.ToLower(heading), " ", "_")
}
