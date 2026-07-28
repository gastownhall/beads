package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// depGlyph marks a dependency/relationship edge in the --deps tree view. It is
// deliberately distinct from the parent-child connectors (├── └──) so a
// dependency can never be mistaken for hierarchy (the confusion GH#4686 fixed).
const depGlyph = "╌╌▷"

// depRender carries the state needed to annotate and order a --deps tree.
// A nil *depRender means the feature is off; its methods are nil-safe.
type depRender struct {
	mode    string                         // "scheduling" or "all"
	allDeps map[string][]*types.Dependency // outgoing edges keyed by issue_id
	inView  map[string]*types.Issue        // displayed issues, for titles + in-view test
}

// depEdgeDisplay classifies a dependency edge for the --deps view.
//   - label: how the edge reads from the source node's perspective. A stored
//     "blocks" edge (issue_id depends on depends_on_id) reads as "depends-on",
//     matching `bd show`.
//   - scheduling: true for edges that constrain execution order (depends-on,
//     conditional-blocks, waits-for). Only these drive sibling ordering.
//   - ok: false for parent-child, which is hierarchy and never appears here.
func depEdgeDisplay(t types.DependencyType) (label string, scheduling, ok bool) {
	switch t {
	case types.DepParentChild:
		return "", false, false
	case types.DepBlocks:
		return "depends-on", true, true
	case types.DepConditionalBlocks:
		return "conditional-blocks", true, true
	case types.DepWaitsFor:
		return "waits-for", true, true
	case types.DepRelated, types.DepRelatesTo:
		return "related", false, true
	case types.DepDiscoveredFrom:
		return "discovered-from", false, true
	case types.DepDuplicates:
		return "duplicates", false, true
	case types.DepSupersedes:
		return "supersedes", false, true
	case types.DepRepliesTo:
		return "replies-to", false, true
	default:
		return string(t), false, true
	}
}

// orderSiblingsByDeps reorders a sibling group so that, within the group, an
// issue that depends on another (via a scheduling edge) sorts after it — giving
// a top-to-bottom reading that is a valid execution order. Ties and dependency
// cycles fall back to compareIssuesByPriority (priority, then natural ID), so
// the result is always total and never hangs on a cycle. Ordering is driven by
// scheduling edges only, regardless of --deps mode: knowledge-graph edges
// (related, discovered-from, ...) carry no ordering meaning.
func orderSiblingsByDeps(siblings []*types.Issue, allDeps map[string][]*types.Dependency) []*types.Issue {
	if len(siblings) < 2 || allDeps == nil {
		return siblings
	}

	byID := make(map[string]*types.Issue, len(siblings))
	for _, s := range siblings {
		byID[s.ID] = s
	}

	// indeg[N] = count of in-group scheduling targets N must come after.
	// dependents[T] = in-group issues that depend on T (T unblocks them).
	indeg := make(map[string]int, len(siblings))
	dependents := make(map[string][]string)
	for _, s := range siblings {
		seen := make(map[string]bool)
		for _, dep := range allDeps[s.ID] {
			if _, scheduling, ok := depEdgeDisplay(dep.Type); !ok || !scheduling {
				continue
			}
			t := dep.DependsOnID
			if t == s.ID || byID[t] == nil || seen[t] {
				continue
			}
			seen[t] = true
			indeg[s.ID]++
			dependents[t] = append(dependents[t], s.ID)
		}
	}

	// Kahn's algorithm with a priority-ordered ready set for a stable result.
	ready := make([]*types.Issue, 0, len(siblings))
	for _, s := range siblings {
		if indeg[s.ID] == 0 {
			ready = append(ready, s)
		}
	}
	slices.SortFunc(ready, compareIssuesByPriority)

	out := make([]*types.Issue, 0, len(siblings))
	emitted := make(map[string]bool, len(siblings))
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		out = append(out, n)
		emitted[n.ID] = true
		grew := false
		for _, dID := range dependents[n.ID] {
			indeg[dID]--
			if indeg[dID] == 0 {
				ready = append(ready, byID[dID])
				grew = true
			}
		}
		if grew {
			slices.SortFunc(ready, compareIssuesByPriority)
		}
	}

	// Cycle fallback: emit any remaining nodes in priority/ID order.
	if len(out) < len(siblings) {
		rest := make([]*types.Issue, 0, len(siblings)-len(out))
		for _, s := range siblings {
			if !emitted[s.ID] {
				rest = append(rest, s)
			}
		}
		slices.SortFunc(rest, compareIssuesByPriority)
		out = append(out, rest...)
	}
	return out
}

// annotationsFor prints the dependency-edge annotation rows for a node, indented
// to childPrefix so they align with (and sit just above) the node's children.
// No-op when the receiver is nil (--deps off).
func (dr *depRender) annotationsFor(nodeID, childPrefix string) {
	if dr == nil {
		return
	}

	type row struct{ label, target, title string }
	var inView []row
	var outView []string
	seen := make(map[string]bool)
	for _, dep := range dr.allDeps[nodeID] {
		label, scheduling, ok := depEdgeDisplay(dep.Type)
		if !ok {
			continue // parent-child: hierarchy, not a dependency
		}
		if dr.mode != "all" && !scheduling {
			continue // scheduling mode hides knowledge-graph edges
		}
		key := string(dep.Type) + "\x00" + dep.DependsOnID
		if seen[key] {
			continue
		}
		seen[key] = true
		if issue := dr.inView[dep.DependsOnID]; issue != nil {
			inView = append(inView, row{label: label, target: dep.DependsOnID, title: issue.Title})
		} else {
			outView = append(outView, dep.DependsOnID)
		}
	}
	if len(inView) == 0 && len(outView) == 0 {
		return
	}

	// In-view edges: one legible, greppable row each (label, then natural ID).
	slices.SortFunc(inView, func(a, b row) int {
		if a.label != b.label {
			return cmp.Compare(a.label, b.label)
		}
		return utils.NaturalCompareIDs(a.target, b.target)
	})
	for _, r := range inView {
		tag := ui.RenderMuted(fmt.Sprintf("%s %-20s", depGlyph, "["+r.label+"]"))
		fmt.Println(childPrefix + tag + " " + r.target + " " + r.title)
	}

	// Out-of-view edges: collapse to a single summary line so a filtered view
	// (--parent/--type) isn't buried under rows whose targets aren't on screen,
	// while still signaling that cross-view dependencies exist and naming them.
	if len(outView) > 0 {
		slices.SortFunc(outView, utils.NaturalCompareIDs)
		const maxNamed = 4
		named, suffix := outView, ""
		if len(outView) > maxNamed {
			named = outView[:maxNamed]
			suffix = fmt.Sprintf(", +%d more", len(outView)-maxNamed)
		}
		summary := fmt.Sprintf("%s ↗ %d outside this view: %s%s",
			depGlyph, len(outView), strings.Join(named, ", "), suffix)
		fmt.Println(childPrefix + ui.RenderMuted(summary))
	}
}
