//go:build cgo

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestServedTreeCarriesTheStoredBlockedFlag pins the served read of #6630's
// fix across the wire: the dependency-tree walk stamps every node with the
// stored transitive is_blocked flag — the same projection bd ready and bd
// blocked read — so a child of a blocked epic and the epic itself come back
// blocked while the open blocker stays unblocked. Seeding goes through the
// reference store, never the client, so a pass proves the flag crossed the
// wire rather than the client agreeing with itself. The httpapi tree tests
// pin the raw JSON member this decode rides on.
func TestServedTreeCarriesTheStoredBlockedFlag(t *testing.T) {
	env := newServedEnv(t, "tib")
	ctx := t.Context()

	seed := func(id string) *types.Issue {
		id = env.prefix + "-" + id
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := env.createIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
		return issue
	}
	blocker := seed("blocker")
	epic := seed("epic")
	child := seed("child")
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID: epic.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed epic -> blocker edge: %v", err)
	}
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "seed"); err != nil {
		t.Fatalf("seed child -> epic edge: %v", err)
	}

	walker, err := env.subject.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}
	result, err := walker.WalkTree(ctx, issueops.WalkTreeRequest{RootID: child.ID, MaxDepth: 5})
	if err != nil {
		t.Fatalf("WalkTree over http: %v", err)
	}
	byID := make(map[string]*types.TreeNode, len(result.Nodes))
	for _, node := range result.Nodes {
		byID[node.ID] = node
	}
	for _, id := range []string{child.ID, epic.ID} {
		if !byID[id].IsBlocked {
			t.Errorf("node %s IsBlocked = false, want true: blocking inherited through the parent chain must reach the served answer", id)
		}
	}
	if byID[blocker.ID].IsBlocked {
		t.Errorf("node %s IsBlocked = true, want false: the open blocker is not blocked", blocker.ID)
	}
}

// TestServedTreeStampsEveryOccurrenceAcrossPlanes pins the occurrence half of
// #6630's fix on the served read: a diamond repeats shared — once expanded,
// once as a Deduped stub — and the walk also reaches a wisp node, each with a
// true stored flag backed by an own ready-affecting edge in its own family's
// table (shared on the durable plane, the wisp on the wisp plane). Every
// occurrence must come back blocked, the stub's copy included, so the
// assertion walks the node list rather than a by-id map, which would collapse
// the diamond and hide exactly the row this test exists for.
func TestServedTreeStampsEveryOccurrenceAcrossPlanes(t *testing.T) {
	env := newServedEnv(t, "tib6")
	ctx := t.Context()

	seed := func(id string) *types.Issue {
		id = env.prefix + "-" + id
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := env.createIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
		return issue
	}
	edge := func(from, to *types.Issue) {
		if err := env.addDependency(ctx, &types.Dependency{
			IssueID: from.ID, DependsOnID: to.ID, Type: types.DepBlocks,
		}, "seed"); err != nil {
			t.Fatalf("seed %s -> %s edge: %v", from.ID, to.ID, err)
		}
	}

	root := seed("root")
	mid := seed("mid")
	shared := seed("shared")
	sharedBlocker := seed("shared-blocker")
	wispBlocker := seed("wisp-blocker")
	wisp := &types.Issue{ID: env.prefix + "-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createWisp(ctx, wisp, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", wisp.ID, err)
	}

	// The diamond: root -> mid -> shared with root -> shared as the second
	// edge in; both mid and shared own a blocks edge to an open issue, so
	// their stored flags are true with an edge to qualify against, and the
	// wisp hangs off a durable row targeting it while owning its own edge on
	// the wisp plane.
	edge(root, mid)
	edge(root, shared)
	edge(root, wisp)
	edge(mid, shared)
	edge(shared, sharedBlocker)
	edge(wisp, wispBlocker)

	walker, err := env.subject.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}
	result, err := walker.WalkTree(ctx, issueops.WalkTreeRequest{RootID: root.ID, MaxDepth: 5})
	if err != nil {
		t.Fatalf("WalkTree over http: %v", err)
	}

	sharedOccurrences := 0
	for _, node := range result.Nodes {
		switch node.ID {
		case shared.ID:
			sharedOccurrences++
			if !node.IsBlocked {
				t.Errorf("shared occurrence (Deduped=%v) IsBlocked = false, want the flag on every copy", node.Deduped)
			}
		case wisp.ID:
			if !node.IsBlocked {
				t.Errorf("wisp node %s IsBlocked = false, want its own-plane flag qualified by wisp_dependencies", wisp.ID)
			}
		}
	}
	if sharedOccurrences != 2 {
		t.Errorf("shared occurs %d times, want the diamond's expanded row and its Deduped stub", sharedOccurrences)
	}
}