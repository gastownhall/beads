package main

import (
	"slices"

	"github.com/steveyegge/beads/internal/types"
)

// depRelation names one dependency type from both ends of its edge, for the
// text views. The table and its direction notes live in
// types.DependencyRelationFor, which bd show --json reads too, so the two
// views cannot disagree about which end is which.
type depRelation struct {
	outHeading string // bd show section heading for the edge's source
	inHeading  string // bd show section heading for the edge's target
	outGlyph   string
	inGlyph    string
	phrase     string // reads "<source> <phrase> <target>", for dep add feedback
}

// depRelationFor returns how to name an edge of this type. A custom type gets
// its own name rather than being described as a blocker it is not.
func depRelationFor(t types.DependencyType) depRelation {
	rel := types.DependencyRelationFor(t)
	return depRelation{
		outHeading: rel.OutHeading,
		inHeading:  rel.InHeading,
		outGlyph:   rel.OutGlyph,
		inGlyph:    rel.InGlyph,
		phrase:     rel.Phrase,
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
