package main

import (
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// depRelation names one dependency type from both ends of its edge. A stored
// edge is (issue_id, depends_on_id, type): out* describes the edge as the
// issue_id side sees it, in* as the depends_on_id side sees it.
//
// Each type's direction comes from the command that writes it, NOT from reading
// the type name left to right — beads has no single convention there.
// `bd supersede old --with new` stores (old, new, supersedes), so the edge
// reads "issue_id is superseded by depends_on_id", while
// `bd duplicate dup --of canonical` stores (dup, canonical, duplicates), which
// reads the other way round. Getting this backwards prints a lie, so change an
// entry only alongside the command that creates that edge.
type depRelation struct {
	outHeading string // bd show section heading for the edge's source
	inHeading  string // bd show section heading for the edge's target
	outGlyph   string
	inGlyph    string
	phrase     string // reads "<source> <phrase> <target>", for dep add feedback
}

// depRelations covers the well-known types. Missing entries fall back to the
// type's own name (see depRelationFor), which is what custom types get.
var depRelations = map[types.DependencyType]depRelation{
	types.DepBlocks:            {"DEPENDS ON", "BLOCKS", "→", "←", "depends on"},
	types.DepParentChild:       {"PARENT", "CHILDREN", "↑", "↳", "is a child of"},
	types.DepConditionalBlocks: {"CONDITIONALLY DEPENDS ON", "CONDITIONALLY BLOCKS", "→", "←", "conditionally depends on"},
	types.DepWaitsFor:          {"WAITS FOR", "AWAITED BY", "→", "←", "waits for"},
	types.DepRelated:           {"RELATED", "RELATED", "↔", "↔", "is related to"},
	types.DepRelatesTo:         {"RELATED", "RELATED", "↔", "↔", "relates to"},
	types.DepDiscoveredFrom:    {"DISCOVERED FROM", "DISCOVERED", "◊", "◊", "was discovered from"},
	types.DepRepliesTo:         {"IN REPLY TO", "REPLIES", "→", "←", "replies to"},
	types.DepDuplicates:        {"DUPLICATE OF", "DUPLICATED BY", "→", "←", "duplicates"},
	types.DepSupersedes:        {"SUPERSEDED BY", "SUPERSEDES", "→", "←", "is superseded by"},
	types.DepAuthoredBy:        {"AUTHORED BY", "AUTHORED", "→", "←", "was authored by"},
	types.DepAssignedTo:        {"ASSIGNED TO", "ASSIGNED", "→", "←", "is assigned to"},
	types.DepApprovedBy:        {"APPROVED BY", "APPROVED", "→", "←", "was approved by"},
	types.DepAttests:           {"ATTESTS", "ATTESTED BY", "→", "←", "attests"},
	types.DepTracks:            {"TRACKS", "TRACKED BY", "→", "←", "tracks"},
	types.DepUntil:             {"ACTIVE UNTIL", "KEEPS ACTIVE", "→", "←", "is active until"},
	types.DepCausedBy:          {"CAUSED BY", "CAUSED", "→", "←", "was caused by"},
	types.DepValidates:         {"VALIDATES", "VALIDATED BY", "→", "←", "validates"},
	types.DepDelegatedFrom:     {"DELEGATED FROM", "DELEGATED TO", "→", "←", "was delegated from"},
}

// depRelationFor returns how to name an edge of this type. A custom type gets
// its own name rather than being described as a blocker it is not.
func depRelationFor(t types.DependencyType) depRelation {
	if rel, ok := depRelations[t]; ok {
		return rel
	}
	name := strings.ToUpper(string(t))
	return depRelation{
		outHeading: name,
		inHeading:  name + " (INBOUND)",
		outGlyph:   "→",
		inGlyph:    "←",
		phrase:     string(t),
	}
}

// depSectionOrder fixes the order dependency sections print in. It leads with
// the types bd show has always grouped, so an issue wired only with those
// renders exactly as it did before the other types got sections of their own.
// DepRelated stands for both symmetric spellings and sorts last, matching where
// bd show has always printed RELATED.
var depSectionOrder = []types.DependencyType{
	types.DepParentChild, types.DepBlocks,
	types.DepConditionalBlocks, types.DepWaitsFor,
	types.DepDiscoveredFrom,
	types.DepRepliesTo, types.DepDuplicates, types.DepSupersedes,
	types.DepAuthoredBy, types.DepAssignedTo, types.DepApprovedBy, types.DepAttests,
	types.DepTracks, types.DepUntil, types.DepCausedBy, types.DepValidates,
	types.DepDelegatedFrom,
	types.DepRelated,
}

// depSection is one printable group of dependency lines under one heading.
type depSection struct {
	Type    types.DependencyType
	Heading string
	Glyph   string
	Deps    []*types.IssueWithDependencyMetadata
}

// groupDepSections splits one direction's edges into printable sections, in
// display order. outgoing means issue is the edges' source (dependencies.
// issue_id).
//
// A non-nil relatedSeen diverts the symmetric related edges into it, keyed by
// issue ID, so a caller that passes the same map for both directions ends up
// with one RELATED section instead of two halves. A caller reading a single
// direction passes nil and gets RELATED as an ordinary trailing section; either
// way the two spellings share one section, because they read the same.
func groupDepSections(deps []*types.IssueWithDependencyMetadata, outgoing bool, relatedSeen map[string]*types.IssueWithDependencyMetadata) []depSection {
	byType := make(map[types.DependencyType][]*types.IssueWithDependencyMetadata)
	for _, dep := range deps {
		switch dep.DependencyType {
		case types.DepRelated, types.DepRelatesTo:
			if relatedSeen != nil {
				relatedSeen[dep.ID] = dep
				continue
			}
			byType[types.DepRelated] = append(byType[types.DepRelated], dep)
		default:
			byType[dep.DependencyType] = append(byType[dep.DependencyType], dep)
		}
	}

	sections := make([]depSection, 0, len(byType))
	emit := func(t types.DependencyType) {
		group, ok := byType[t]
		if !ok {
			return
		}
		delete(byType, t)
		rel := depRelationFor(t)
		heading, glyph := rel.outHeading, rel.outGlyph
		if !outgoing {
			heading, glyph = rel.inHeading, rel.inGlyph
		}
		sections = append(sections, depSection{Type: t, Heading: heading, Glyph: glyph, Deps: group})
	}
	for _, t := range depSectionOrder {
		emit(t)
	}
	// Custom types trail the built-ins, sorted by name so output is stable.
	custom := make([]types.DependencyType, 0, len(byType))
	for t := range byType {
		custom = append(custom, t)
	}
	slices.Sort(custom)
	for _, t := range custom {
		emit(t)
	}
	return sections
}
