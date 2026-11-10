//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyHidesWispAndIssueChildrenOfUndatedDeferredParents walks all four
// legs the deferred-child scan can take: a durable-issue parent and a wisp
// parent, each deferred with plain `bd defer` (status deferred, no
// defer_until, GH#7300), each with a durable child and a wisp child. Every
// child stays off the ready set while the ready controls stay on it, so a
// fix that covers only one parent family or one edge table fails here.
func TestReadyHidesWispAndIssueChildrenOfUndatedDeferredParents(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "uc")

	for _, iss := range []*types.Issue{
		{ID: "uc-iparent", Title: "issue deferred parent", Status: types.StatusDeferred, IssueType: types.TypeEpic},
		{ID: "uc-wparent", Title: "wisp deferred parent", Status: types.StatusDeferred, IssueType: types.TypeEpic, Ephemeral: true},
		{ID: "uc-ichild", Title: "issue child of issue parent", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "uc-wchild", Title: "wisp child of issue parent", Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "uc-ichild2", Title: "issue child of wisp parent", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "uc-wchild2", Title: "wisp child of wisp parent", Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "uc-free", Title: "free", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "uc-wfree", Title: "free wisp", Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	for _, d := range []*types.Dependency{
		// The edge's write table follows the CHILD: dependencies for the
		// durable children, wisp_dependencies for the wisp children.
		{IssueID: "uc-ichild", DependsOnID: "uc-iparent", Type: types.DepParentChild},
		{IssueID: "uc-wchild", DependsOnID: "uc-iparent", Type: types.DepParentChild},
		{IssueID: "uc-ichild2", DependsOnID: "uc-wparent", Type: types.DepParentChild},
		{IssueID: "uc-wchild2", DependsOnID: "uc-wparent", Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", d.IssueID, d.DependsOnID, err)
		}
	}

	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{IncludeEphemeral: true})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	hidden := map[string]bool{
		"uc-ichild":  true,
		"uc-wchild":  true,
		"uc-ichild2": true,
		"uc-wchild2": true,
		"uc-iparent": true,
		"uc-wparent": true,
	}
	var ids []string
	sawFree, sawWfree := false, false
	for _, iss := range ready {
		ids = append(ids, iss.ID)
		switch iss.ID {
		case "uc-free":
			sawFree = true
		case "uc-wfree":
			sawWfree = true
		}
		if hidden[iss.ID] {
			t.Errorf("%s listed as ready although it (or its parent) is deferred: %v", iss.ID, ids)
		}
	}
	if !sawFree || !sawWfree {
		t.Fatalf("ready controls missing (free=%v, wfree=%v): %v", sawFree, sawWfree, ids)
	}
}
