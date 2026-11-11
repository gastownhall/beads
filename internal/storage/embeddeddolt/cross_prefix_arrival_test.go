//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// These tests pin the arrival half of GH#8299. An edge's target is
// classified when the edge is written, so an edge onto an id of another
// prefix that this database does not hold yet is filed in
// depends_on_external, where readiness never sees it. Every write that brings
// such an id into this database files the edges onto it in the typed column
// a write made then would choose.

// TestEmbeddedCrossPrefixRepoSyncOrderFilesEdgesOntoALaterRepo: `bd repo
// sync` hydrates each configured repo in its own batch, in config order. An
// edge of the first repo's issue onto an issue of a repo synced after it is
// written before its target is here, so it is filed external; the later
// repo's batch brings the target in and files the edge local. The dependent
// is blocked from then on, and re-syncing the first repo, whose export
// carries the edge again, leaves it local.
func TestEmbeddedCrossPrefixRepoSyncOrderFilesEdgesOntoALaterRepo(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const dependent, blocker = "b-sync1", "c-sync1"
	syncRepo := func(repo string, issue *types.Issue) {
		t.Helper()
		issue.SourceRepo = repo
		if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "repo-sync", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		}); err != nil {
			t.Fatalf("sync %s: %v", repo, err)
		}
	}
	repoB := func() *types.Issue {
		return &types.Issue{ID: dependent, Title: dependent, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: blocker, Type: types.DepBlocks}}}
	}

	syncRepo("../repo-b", repoB())
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", dependent, blocker, "depends_on_external")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", dependent, 0)

	syncRepo("../repo-c", &types.Issue{ID: blocker, Title: blocker, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", dependent, blocker, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", dependent, 1)
	if crossPrefixReady(t, ctx, te, dependent) {
		t.Fatalf("%s is ready beside its open blocker %s, synced from a later repo", dependent, blocker)
	}

	syncRepo("../repo-b", repoB())
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", dependent, blocker, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", dependent, 1)
}

// TestEmbeddedCrossPrefixExplicitIDCreateFilesEdgesOntoIt: the documented
// "add now, route later" flow is `bd dep add a-1 gt-9`, then `bd create --id
// gt-9`, and that create reaches storage through IssueOperations.Create
// (ExecuteCreate), not the store's CreateIssue. The edge added first is filed
// external; the create that names the id, as an issue or as a wisp, files it
// into the typed column of the plane the id lands on, and blocks its source.
func TestEmbeddedCrossPrefixExplicitIDCreateFilesEdgesOntoIt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	for _, tc := range []struct {
		target, source, column string
		ephemeral              bool
	}{
		{"b-cid1", "a-cidsrc1", "depends_on_issue_id", false},
		{"b-cidw1", "a-cidsrc2", "depends_on_wisp_id", true},
	} {
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: tc.source, Title: tc.source, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", tc.source, err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: tc.source, DependsOnID: tc.target, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", tc.source, tc.target, err)
		}
		assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", tc.source, tc.target, "depends_on_external")

		if _, err := ops.Create(ctx, publicops.CreateRequest{
			Actor:         "tester",
			ForceIDPrefix: true,
			Issue:         &types.Issue{ID: tc.target, Title: tc.target, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: tc.ephemeral},
		}); err != nil {
			t.Fatalf("Create %s: %v", tc.target, err)
		}
		assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", tc.source, tc.target, tc.column)
		assertCrossPrefixIsBlocked(t, ctx, te, "issues", tc.source, 1)
		if crossPrefixReady(t, ctx, te, tc.source) {
			t.Errorf("%s is ready beside its open blocker %s, created with that id after the edge", tc.source, tc.target)
		}
	}
}

// TestEmbeddedCrossPrefixReAddFilesAnExternalEdgeOntoAHeldTarget: an edge row
// can hold, in depends_on_external, a target this database holds: a peer that
// wrote the edge before the target was synced there passes the row on in
// that shape with a merge. Adding the same edge again, through the dependency
// verb from either source plane or through an import that carries it, files
// it in the typed column a first add would choose, with its id kept, and its
// source is blocked by it. The add is still the change-free re-add it was:
// type, metadata and thread are unchanged.
func TestEmbeddedCrossPrefixReAddFilesAnExternalEdgeOntoAHeldTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const target, reAdded, reAddedWisp, imported = "b-held1", "a-readd1", "a-readdw1", "a-reimp1"
	for _, seed := range []struct {
		id        string
		ephemeral bool
	}{{target, false}, {reAdded, false}, {reAddedWisp, true}, {imported, false}} {
		issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.ephemeral}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", seed.id, err)
		}
	}
	seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", reAdded, target, types.DepBlocks)
	seedExternalCrossPrefixEdge(t, ctx, te, "wisp_dependencies", reAddedWisp, target, types.DepBlocks)
	seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", imported, target, types.DepBlocks)
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", reAdded, 0)

	for _, source := range []string{reAdded, reAddedWisp} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: source, DependsOnID: target, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("re-add %s -> %s: %v", source, target, err)
		}
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", reAdded, target, "depends_on_issue_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "wisp_dependencies", reAddedWisp, target, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", reAdded, 1)
	assertCrossPrefixIsBlocked(t, ctx, te, "wisps", reAddedWisp, 1)
	if crossPrefixReady(t, ctx, te, reAdded) {
		t.Fatalf("%s is ready after re-adding its edge onto the open %s", reAdded, target)
	}
	var id, createdBy string
	te.queryScalar(t, ctx, "SELECT id, created_by FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		[]any{reAdded, target}, &id, &createdBy)
	if id != depid.New(reAdded, target) || createdBy != "peer" {
		t.Errorf("re-filed row id = %s, created_by = %s, want the stored row (%s, peer)", id, createdBy, depid.New(reAdded, target))
	}

	// An import that carries the stored edge upserts its source and finds the
	// edge already there.
	if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
		ID: imported, Title: imported, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{DependsOnID: target, Type: types.DepBlocks}},
	}}, "importer", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("import %s: %v", imported, err)
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", imported, target, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", imported, 1)
}

// TestEmbeddedCrossPrefixArrivalLeavesAHierarchyConflictExternal: a blocking
// edge onto an issue's own ancestor is a gate that never clears, which every
// write path refuses for a target this database holds. One written before
// the ancestor arrived could not be checked, so when the ancestor arrives the
// parent-child edge that makes it an ancestor is filed local and the
// blocking edge stays external, where it gates nothing. A synced edge is
// judged on the synced hierarchy, though: an issue whose parent is a wisp of
// this clone does not have that wisp's parent as an ancestor there, so its
// edge onto that issue is filed local, as another clone's merge of the
// arriving issue would file it.
func TestEmbeddedCrossPrefixArrivalLeavesAHierarchyConflictExternal(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const grand, parent, child = "gt-hgrand1", "a-hpar1", "a-hchild1"
	const wispParent, belowWisp = "a-hwpar1", "a-hwkid1"
	for _, seed := range []struct {
		id        string
		ephemeral bool
	}{{parent, false}, {child, false}, {belowWisp, false}, {wispParent, true}} {
		issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.ephemeral}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", seed.id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: child, DependsOnID: parent, Type: types.DepParentChild},
		{IssueID: parent, DependsOnID: grand, Type: types.DepParentChild},
		{IssueID: child, DependsOnID: grand, Type: types.DepBlocks},
		{IssueID: belowWisp, DependsOnID: wispParent, Type: types.DepParentChild},
		{IssueID: wispParent, DependsOnID: grand, Type: types.DepParentChild},
		{IssueID: belowWisp, DependsOnID: grand, Type: types.DepBlocks},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", child, grand, "depends_on_external")
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", belowWisp, grand, "depends_on_external")

	if err := te.store.CreateIssue(ctx, &types.Issue{ID: grand, Title: grand, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", grand, err)
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", parent, grand, "depends_on_issue_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", child, grand, "depends_on_external")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", child, 0)
	assertCrossPrefixEdgeColumn(t, ctx, te, "wisp_dependencies", wispParent, grand, "depends_on_issue_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", belowWisp, grand, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", belowWisp, 1)
}

// TestEmbeddedCrossPrefixWispArrivalFilesEdgesOntoIt: a wisp created under an
// id an edge already names is a held target like an issue: the edge, from a
// durable source as from a wisp, files into depends_on_wisp_id, the column a
// write made now would choose, and its source is blocked by the open wisp.
func TestEmbeddedCrossPrefixWispArrivalFilesEdgesOntoIt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const arriving, issueSource, wispSource = "b-warr1", "a-warrsrc1", "a-warrwsrc1"
	for _, seed := range []struct {
		id        string
		ephemeral bool
	}{{issueSource, false}, {wispSource, true}} {
		issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.ephemeral}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", seed.id, err)
		}
	}
	for _, source := range []string{issueSource, wispSource} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: source, DependsOnID: arriving, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", source, arriving, err)
		}
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", issueSource, arriving, "depends_on_external")

	if err := te.store.CreateIssue(ctx, &types.Issue{ID: arriving, Title: arriving, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", arriving, err)
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", issueSource, arriving, "depends_on_wisp_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "wisp_dependencies", wispSource, arriving, "depends_on_wisp_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", issueSource, 1)
	assertCrossPrefixIsBlocked(t, ctx, te, "wisps", wispSource, 1)
}

// TestEmbeddedCrossPrefixPromotionFilesEdgesOntoThePromotedIssue: a durable
// edge filed external onto a clone-local wisp, the shape the upgrade repair
// leaves since a synced row must not bind to a wisp, becomes local when the
// wisp is promoted: from then on the issues plane holds the id, so the edge
// files into depends_on_issue_id and gates its source. Both promotions re-file
// it: the store's PromoteFromEphemeral, and the persistence move `bd promote`
// makes.
func TestEmbeddedCrossPrefixPromotionFilesEdgesOntoThePromotedIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	for i, promotion := range []struct {
		name    string
		promote func(id string) error
	}{
		{"PromoteFromEphemeral", func(id string) error { return te.store.PromoteFromEphemeral(ctx, id, "tester") }},
		{"persistence move", func(id string) error {
			_, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "tester", IssueID: id, Patch: publicops.IssuePatch{
				Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModePersistent},
			}})
			return err
		}},
	} {
		t.Run(promotion.name, func(t *testing.T) {
			wisp, source := fmt.Sprintf("b-pwisp%d", i), fmt.Sprintf("a-psrc%d", i)
			for _, seed := range []struct {
				id        string
				ephemeral bool
			}{{wisp, true}, {source, false}} {
				issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.ephemeral}
				if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
					t.Fatalf("CreateIssue %s: %v", seed.id, err)
				}
			}
			seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", source, wisp, types.DepBlocks)
			if err := promotion.promote(wisp); err != nil {
				t.Fatalf("promote %s: %v", wisp, err)
			}
			assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, wisp, "depends_on_issue_id")
			assertCrossPrefixIsBlocked(t, ctx, te, "issues", source, 1)
		})
	}
}
