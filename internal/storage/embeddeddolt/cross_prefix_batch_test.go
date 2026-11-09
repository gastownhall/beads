//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCrossPrefixBatchCreate pins the create/import half of GH#8299:
// a batch whose issues carry edges on local cross-prefix targets files those
// edges as local, including a target created within the batch itself — the
// dependency pass runs after every row of the batch is written, so the
// batch's presence reads see both the pre-existing and the in-batch target.
// Import-shaped input names explicit foreign-prefix ids (SkipPrefixValidation
// is the import mode), which is exactly the shape that used to file external.
func TestEmbeddedCrossPrefixBatchCreate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	// A cross-prefix target that already exists when the batch runs.
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "b-pre", Title: "pre-existing cross-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue b-pre: %v", err)
	}

	batch := []*types.Issue{
		{
			ID:        "a-bsrc1",
			Title:     "batch source one",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "b-pre",
				Type:        types.DepBlocks,
			}},
		},
		{
			ID:        "a-bsrc2",
			Title:     "batch source two",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "b-btgt",
				Type:        types.DepBlocks,
			}},
		},
		{
			ID:        "b-btgt",
			Title:     "cross-prefix target created in the batch",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
	}
	if err := te.store.CreateIssuesWithFullOptions(ctx, batch, "tester", storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}

	assertDepTargetColumn(t, te, "dependencies", "a-bsrc1", "b-pre", "depends_on_issue_id")
	assertDepTargetColumn(t, te, "dependencies", "a-bsrc2", "b-btgt", "depends_on_issue_id")
}
