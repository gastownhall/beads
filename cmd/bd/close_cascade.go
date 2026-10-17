package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// closeCascadeMaxIssues caps how many open descendants one --cascade
// expansion may discover, the same runaway guard the storage layer's delete
// cascade carries (issueops.maxRecursiveResults). A well-formed hierarchy
// cannot reach it; a corrupt one must not close unbounded rows, and the
// batch aborts with an error rather than closing a partial forest silently.
const closeCascadeMaxIssues = 10000

// cascadeItem is one open descendant a --cascade close closes before the
// ancestors that contain it. depth is the ancestor distance the expansion
// found it at, and is what orders the batch deepest-first; issue is the
// pre-close snapshot the audit entry and the report read; store is the
// handle the close lands through (nil on the proxied route, which holds no
// per-repo handles).
type cascadeItem struct {
	id     string
	reason string
	depth  int
	store  storage.DoltStorage
	issue  *types.Issue
}

// cascadeRefusal is one discovered descendant the CLI's own close policy
// refused. It stays open, and so — through the engine's open-children guard —
// does every ancestor of it that this cascade was trying to close.
type cascadeRefusal struct {
	id      string
	refusal string
}

// cascadeExpansion is the read-only result of expanding --cascade over the
// arguments that survived preflight.
type cascadeExpansion struct {
	// items are the open, policy-passing descendants in discovery order,
	// each carrying the reason of the typed root it was found under.
	items []cascadeItem
	// refusals are the discovered descendants that failed the same
	// close-policy check a typed argument gets.
	refusals []cascadeRefusal
	// depths maps every id in the closed set — typed or discovered — to its
	// longest root-to-node path over the recorded parent edges, settled by
	// settleDepths after the walks. Longest path is what the batch order
	// needs: a node must sort behind everything that can reach it, and a
	// node reachable from two roots at different distances takes the deeper
	// reading. A typed id that is also a descendant of another typed id
	// therefore sorts before that ancestor AND after its own children — its
	// subtree hangs off the edge set, not off any walk's level counter,
	// which cannot see depth another root contributed.
	depths map[string]int
	// parents records child → parent for every edge the walks touched,
	// including edges into already-claimed and closed nodes: depths settle
	// from this edge set, not from per-root level counters.
	parents map[string][]string
}

// cascadeRootSpec is one surviving typed argument as the expansion sees it:
// the resolved id, the reason its subtree closes with, and the route's read
// and policy halves behind closures.
type cascadeRootSpec struct {
	id     string
	reason string
	// store is the handle every descendant of this root closes through; nil
	// on the proxied route, which holds no per-repo handles.
	store storage.DoltStorage
	// childrenOf returns the one-level parent-child children of id — the
	// same hierarchy bd list --tree walks, both planes — unbounded. A
	// subtree reads entirely through its root's handle, because a
	// parent-child edge and the child row it points at live in the same
	// repo the root resolved to.
	childrenOf func(ctx context.Context, id string) ([]*types.Issue, error)
	// checkOne applies the route's own close policy to a discovered
	// descendant — the same fence and gate checks a typed argument got —
	// returning "" when it may join the cascade.
	checkOne func(id string, issue *types.Issue) string
}

// expandCloseCascade walks each root's subtree breadth-first, one level of
// parent-child edges at a time, and returns the open descendants that may
// close with the batch.
//
// The walk descends through closed nodes — a closed child with stranded open
// grandchildren is exactly the recovery case that asked for this flag — but
// adds only open ones as items: a closed node has no close to perform, and
// listing its re-close would pollute the report as a fake landing. A node
// the policy refuses is recorded and NOT descended into: its subtree closes
// around it, and its open presence is what refuses its ancestors through the
// engine's guard, which --force overrides. Typed ids are seeded visited —
// they are already items — so a typed id inside another root's subtree is
// never discovered twice; the edge that reaches it is recorded all the same,
// and depths settle from the whole edge set once every walk is done.
//
// Expansion never writes: the batch below is the only writer, so the guard
// that actually decides each close still runs inside the close's own
// transaction. A descendant added between this walk and the batch makes its
// ancestor's guard refuse — a safe, visible failure, never a silently
// orphaned child.
func expandCloseCascade(ctx context.Context, roots []cascadeRootSpec) (*cascadeExpansion, error) {
	exp := &cascadeExpansion{
		depths:  make(map[string]int, len(roots)),
		parents: make(map[string][]string),
	}
	visited := make(map[string]bool, len(roots))

	// node is one queued subtree position: the id to read children from,
	// the snapshot a discovered item keeps, and the reason the root this
	// subtree hangs from closes with.
	type node struct {
		id     string
		issue  *types.Issue
		reason string
	}

	// Every root is seeded visited BEFORE any walk: one typed id can be
	// both a root in its own right and a descendant of an earlier root, and
	// the walk that reaches it first must not turn it into a second item —
	// it is already an item, at its own argument slot.
	for _, root := range roots {
		if !visited[root.id] {
			visited[root.id] = true
			exp.depths[root.id] = 0
		}
	}
	for _, root := range roots {
		frontier := []node{{id: root.id, reason: root.reason}}
		for len(frontier) > 0 {
			var next []node
			for _, current := range frontier {
				children, err := root.childrenOf(ctx, current.id)
				if err != nil {
					return nil, fmt.Errorf("cascade: children of %s: %w", current.id, err)
				}
				for _, child := range children {
					if child == nil {
						continue
					}
					// The edge is recorded whether or not the child was
					// already claimed by an earlier root: a typed id reached
					// through another root still hangs from this edge, and
					// its whole subtree orders through it. Depths themselves
					// settle from the edge set once every walk is done.
					exp.parents[child.ID] = append(exp.parents[child.ID], current.id)
					if visited[child.ID] {
						continue
					}
					visited[child.ID] = true
					if child.Status == types.StatusClosed {
						// Nothing to close here, but the levels below may
						// hold the stranded open descendants this flag is
						// for; keep walking through the closed node.
						next = append(next, node{id: child.ID, reason: current.reason})
						continue
					}
					if len(exp.items)+len(exp.refusals) >= closeCascadeMaxIssues {
						return nil, fmt.Errorf("cascade traversal discovered over %d open descendants; aborting to prevent a runaway close", closeCascadeMaxIssues)
					}
					if refusal := root.checkOne(child.ID, child); refusal != "" {
						exp.refusals = append(exp.refusals, cascadeRefusal{id: child.ID, refusal: refusal})
						continue // pruned: not an item, and not descended into
					}
					exp.items = append(exp.items, cascadeItem{
						id:     child.ID,
						reason: current.reason,
						store:  root.store,
						issue:  child,
					})
					next = append(next, node{id: child.ID, reason: current.reason})
				}
			}
			frontier = next
		}
	}
	exp.settleDepths()
	for i := range exp.items {
		exp.items[i].depth = exp.depths[exp.items[i].id]
	}
	return exp, nil
}

// settleDepths rewrites depths as each id's longest root-to-node path over
// the recorded parent edges: roots sit at 0, everything else one past its
// deepest parent. Memoized, with a visiting guard for a corrupt edge set —
// parent-child writes refuse ancestor relationships, so a cycle cannot be
// reached from a well-formed store.
func (exp *cascadeExpansion) settleDepths() {
	memo := make(map[string]int, len(exp.depths)+len(exp.parents))
	visiting := make(map[string]bool)
	var depthOf func(id string) int
	depthOf = func(id string) int {
		if d, ok := memo[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		d := 0
		for _, parent := range exp.parents[id] {
			if pd := depthOf(parent) + 1; pd > d {
				d = pd
			}
		}
		delete(visiting, id)
		memo[id] = d
		return d
	}
	for id := range exp.depths {
		exp.depths[id] = depthOf(id)
	}
	for id := range exp.parents {
		if _, ok := exp.depths[id]; !ok {
			exp.depths[id] = depthOf(id)
		}
	}
}

// cascadeCount is how many argument-equivalent ids a cascade expansion adds —
// discovered items plus policy refusals — for outcome sizing and the
// partial-failure denominator.
func (exp *cascadeExpansion) cascadeCount() int {
	if exp == nil {
		return 0
	}
	return len(exp.items) + len(exp.refusals)
}

// expandCloseCascadeForResults is the direct route's expansion: every
// surviving argument's subtree reads through the store the argument resolved
// to, and every descendant passes the same close-policy check a typed
// argument got before it joins the cascade.
func expandCloseCascadeForResults(ctx context.Context, plan closeDirectPlan, force bool) (*cascadeExpansion, error) {
	roots := make([]cascadeRootSpec, 0, len(plan.items))
	for _, item := range plan.items {
		itemStore := item.store
		roots = append(roots, cascadeRootSpec{
			id:     item.id,
			reason: item.reason,
			store:  itemStore,
			childrenOf: func(ctx context.Context, id string) ([]*types.Issue, error) {
				return itemStore.SearchIssues(ctx, "", types.IssueFilter{ParentID: &id, Limit: 0})
			},
			checkOne: func(id string, issue *types.Issue) string {
				return closeDirectCheckOne(id, issue, itemStore, force)
			},
		})
	}
	return expandCloseCascade(ctx, roots)
}

// orderCloseCascade merges the surviving typed items with the expansion's
// discovered descendants into one batch, ordered deepest first so every
// descendant closes before the ancestors that count it against the engine's
// open-children guard inside the shared transaction. Typed items keep their
// argument slots and their typed order among equals; discovered items take
// the slots after EVERY typed argument — argBase is len(resolvedIDs), not
// the survivor count, because a typed argument the preflight refused keeps
// its (nil-outcome) slot and numbering from the survivors would collide a
// discovered item with the surviving typed one that follows it. The proxied
// route reorders its own parallel slices with orderCloseCascadePerm.
func orderCloseCascade(typed []closeDirectItem, argBase int, exp *cascadeExpansion) []closeDirectItem {
	// Sort whenever a cascade ran, not only when it discovered items: a
	// typed id that is also another typed id's descendant was deduped into
	// the typed list and still needs the depth ordering.
	if exp == nil {
		return typed
	}
	items := make([]closeDirectItem, 0, len(typed)+len(exp.items))
	items = append(items, typed...)
	for i, ci := range exp.items {
		items = append(items, closeDirectItem{
			arg:    argBase + i,
			id:     ci.id,
			reason: ci.reason,
			store:  ci.store,
		})
	}
	sort.SliceStable(items, func(a, b int) bool {
		return exp.depths[items[a].id] > exp.depths[items[b].id]
	})
	return items
}

// orderCloseCascadePerm applies the same deepest-first ordering to the
// proxied route's parallel items/itemArgs slices. itemArgs carries the
// argument slot each item folds its outcome back onto, with -1 marking a
// cascade-discovered item that has no typed slot.
func orderCloseCascadePerm(exp *cascadeExpansion, items []issueops.BatchCloseItem, itemArgs []int) {
	// Same rule as orderCloseCascade: a cascade with zero discovered items
	// can still have re-depthed a typed id that sits under another one.
	if exp == nil {
		return
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return exp.depths[items[order[a]].IssueID] > exp.depths[items[order[b]].IssueID]
	})
	sortedItems := make([]issueops.BatchCloseItem, len(items))
	sortedArgs := make([]int, len(itemArgs))
	for dst, src := range order {
		sortedItems[dst] = items[src]
		sortedArgs[dst] = itemArgs[src]
	}
	copy(items, sortedItems)
	copy(itemArgs, sortedArgs)
}
