package createbatchequiv

import (
	"fmt"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The apply scenario drives the guarded single-verb bodies an apply-batch
// composes — ExecuteCreate (and its BatchContext reuse), dependency adds
// with their per-edge checks and blocked-state marking, updates — over rows
// the seed already holds.

func seedApply() []*types.Issue {
	root := issue("r", "root")
	parent := issue("ap", "apply parent")
	blocker := issue("ab", "apply blocker")
	closed := issue("ac", "apply closed blocker")
	closed.Status = types.StatusClosed
	return []*types.Issue{root, parent, blocker, closed}
}

func applyRequest() publicops.ApplyBatchRequest {
	var items []publicops.ApplyItem
	const creates = 12
	for i := 1; i <= creates; i++ {
		is := &types.Issue{
			ID: id(fmt.Sprintf("a%d", i)), Title: fmt.Sprintf("apply %d", i), Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, CreatedAt: fixedAt,
		}
		if i%3 == 0 {
			is.Labels = []string{"apply", fmt.Sprintf("n%d", i)}
		}
		if i == creates {
			is.Ephemeral = true
		}
		items = append(items, publicops.ApplyItem{Kind: publicops.ItemCreate,
			Create: &publicops.CreateItem{Key: fmt.Sprintf("k%d", i), Issue: is}})
	}
	edge := func(source, target publicops.Ref, t publicops.DependencyType) publicops.ApplyItem {
		return publicops.ApplyItem{Kind: publicops.ItemDepAdd, DepAdd: &publicops.DepAddItem{Source: source, Target: target, Type: t}}
	}
	key := func(i int) publicops.Ref { return publicops.Ref{Key: fmt.Sprintf("k%d", i)} }
	ref := func(suffix string) publicops.Ref { return publicops.Ref{ID: id(suffix)} }
	for i := 2; i < creates; i++ {
		items = append(items, edge(key(i), key(i-1), publicops.DepBlocks))
	}
	for i := 1; i < creates; i += 2 {
		items = append(items, edge(key(i), ref("ap"), publicops.DepParentChild))
	}
	items = append(items,
		edge(key(1), ref("ab"), publicops.DepBlocks),
		edge(key(4), ref("ac"), publicops.DepBlocks),
		edge(key(5), ref("r"), publicops.DepTracks),
		publicops.ApplyItem{Kind: publicops.ItemUpdate, Update: &publicops.UpdateItem{
			Target: key(2), Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "agent-2"}}}},
	)
	return publicops.ApplyBatchRequest{Actor: "apply-writer", Items: items, ForceIDPrefix: true}
}
