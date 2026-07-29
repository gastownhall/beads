//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestReopenIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("close_then_reopen", func(t *testing.T) {
		te := newTestEnv(t, "ro")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ro-1",
			Title:     "Reopen test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Close it.
		if err := te.store.CloseIssue(ctx, "ro-1", "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}

		// Verify it is closed.
		got, err := te.store.GetIssue(ctx, "ro-1")
		if err != nil {
			t.Fatalf("GetIssue after close: %v", err)
		}
		if got.Status != types.StatusClosed {
			t.Fatalf("expected status closed, got %q", got.Status)
		}
		if got.ClosedAt == nil {
			t.Fatal("expected ClosedAt to be set after close")
		}

		// Reopen it.
		if err := te.store.ReopenIssue(ctx, "ro-1", "not actually done", "tester"); err != nil {
			t.Fatalf("ReopenIssue: %v", err)
		}

		got, err = te.store.GetIssue(ctx, "ro-1")
		if err != nil {
			t.Fatalf("GetIssue after reopen: %v", err)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open after reopen, got %q", got.Status)
		}
		if got.ClosedAt != nil {
			t.Errorf("expected ClosedAt nil after reopen, got %v", got.ClosedAt)
		}
	})
}

func TestUpdateIssueType(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("task_to_feature", func(t *testing.T) {
		te := newTestEnv(t, "ut")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ut-1",
			Title:     "Type change test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if err := te.store.UpdateIssueType(ctx, "ut-1", string(types.TypeFeature), "tester"); err != nil {
			t.Fatalf("UpdateIssueType: %v", err)
		}

		got, err := te.store.GetIssue(ctx, "ut-1")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.IssueType != types.TypeFeature {
			t.Errorf("IssueType: got %q, want %q", got.IssueType, types.TypeFeature)
		}
	})
}

func TestListWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("only_ephemeral_returned", func(t *testing.T) {
		te := newTestEnv(t, "lw")
		ctx := t.Context()

		// Create a regular (non-ephemeral) issue.
		regular := &types.Issue{
			ID:        "lw-regular",
			Title:     "Regular issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, regular, "tester"); err != nil {
			t.Fatalf("CreateIssue (regular): %v", err)
		}

		// Create an ephemeral issue (wisp).
		wisp := &types.Issue{
			ID:        "lw-wisp-1",
			Title:     "Ephemeral wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := te.store.CreateIssue(ctx, wisp, "tester"); err != nil {
			t.Fatalf("CreateIssue (wisp): %v", err)
		}

		// ListWisps with empty filter should return only the ephemeral issue.
		wisps, err := te.store.ListWisps(ctx, types.WispFilter{})
		if err != nil {
			t.Fatalf("ListWisps: %v", err)
		}

		// Verify only the ephemeral one is in results.
		found := false
		for _, w := range wisps {
			if w.ID == "lw-regular" {
				t.Errorf("ListWisps returned non-ephemeral issue %q", w.ID)
			}
			if w.ID == "lw-wisp-1" {
				found = true
			}
		}
		if !found {
			t.Errorf("ListWisps did not return ephemeral issue lw-wisp-1; got %d results", len(wisps))
		}
	})

	t.Run("empty_when_no_wisps", func(t *testing.T) {
		te := newTestEnv(t, "le")
		ctx := t.Context()

		// Create only a regular issue.
		regular := &types.Issue{
			ID:        "le-1",
			Title:     "Regular only",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, regular, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		wisps, err := te.store.ListWisps(ctx, types.WispFilter{})
		if err != nil {
			t.Fatalf("ListWisps: %v", err)
		}
		if len(wisps) != 0 {
			t.Errorf("expected 0 wisps, got %d", len(wisps))
		}
	})
}

func TestGetReadyWorkMoleculeFilter(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("filter_by_molecule_id", func(t *testing.T) {
		te := newTestEnv(t, "gm")
		ctx := t.Context()

		// Create a molecule (parent).
		mol := &types.Issue{
			ID:        "gm-mol-1",
			Title:     "Test molecule",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeMolecule,
		}
		if err := te.store.CreateIssue(ctx, mol, "tester"); err != nil {
			t.Fatalf("CreateIssue (molecule): %v", err)
		}

		// Create child issues of this molecule.
		child1 := &types.Issue{
			ID:        "gm-child-1",
			Title:     "Child 1",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		child2 := &types.Issue{
			ID:        "gm-child-2",
			Title:     "Child 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, child1, "tester"); err != nil {
			t.Fatalf("CreateIssue (child1): %v", err)
		}
		if err := te.store.CreateIssue(ctx, child2, "tester"); err != nil {
			t.Fatalf("CreateIssue (child2): %v", err)
		}

		// Create an unrelated issue (not a child of the molecule).
		unrelated := &types.Issue{
			ID:        "gm-other",
			Title:     "Unrelated",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, unrelated, "tester"); err != nil {
			t.Fatalf("CreateIssue (unrelated): %v", err)
		}

		// Add parent-child dependencies: molecule -> child1, molecule -> child2.
		dep1 := &types.Dependency{
			IssueID:     "gm-child-1",
			DependsOnID: "gm-mol-1",
			Type:        types.DepParentChild,
		}
		dep2 := &types.Dependency{
			IssueID:     "gm-child-2",
			DependsOnID: "gm-mol-1",
			Type:        types.DepParentChild,
		}
		if err := te.store.AddDependency(ctx, dep1, "tester"); err != nil {
			t.Fatalf("AddDependency (child1): %v", err)
		}
		if err := te.store.AddDependency(ctx, dep2, "tester"); err != nil {
			t.Fatalf("AddDependency (child2): %v", err)
		}

		// GetReadyWork filtered by MoleculeID should return only children.
		filter := types.WorkFilter{
			MoleculeID: "gm-mol-1",
		}
		ready, err := te.store.GetReadyWork(ctx, filter)
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}

		ids := make(map[string]bool)
		for _, r := range ready {
			ids[r.ID] = true
		}

		if !ids["gm-child-1"] {
			t.Errorf("expected gm-child-1 in ready work, got %v", ids)
		}
		if !ids["gm-child-2"] {
			t.Errorf("expected gm-child-2 in ready work, got %v", ids)
		}
		if ids["gm-other"] {
			t.Errorf("unrelated issue gm-other should not appear in molecule-filtered ready work")
		}
		if ids["gm-mol-1"] {
			t.Errorf("molecule gm-mol-1 itself should not appear as ready work child")
		}
	})
}

func TestGetReadyWorkIncludeEphemeralAppliesWispFilters(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rwf")
	ctx := t.Context()

	createIssue := func(issue *types.Issue) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	addDep := func(issueID, dependsOnID string, depType types.DependencyType) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", issueID, dependsOnID, err)
		}
	}
	readyIDs := func(filter types.WorkFilter) map[string]bool {
		t.Helper()
		filter.IncludeEphemeral = true
		ready, err := te.store.GetReadyWork(ctx, filter)
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make(map[string]bool, len(ready))
		for _, issue := range ready {
			ids[issue.ID] = true
		}
		return ids
	}
	assertReady := func(ids map[string]bool, want string, rejects ...string) {
		t.Helper()
		if !ids[want] {
			t.Fatalf("matching wisp %s missing from ready work: %v", want, ids)
		}
		for _, reject := range rejects {
			if ids[reject] {
				t.Fatalf("non-matching wisp %s leaked into ready work: %v", reject, ids)
			}
		}
	}

	matching := &types.Issue{
		ID:        "rwf-wisp-filter-match",
		Title:     "Matching routed wisp",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Metadata:  []byte(`{"gc.routed_to":"beads/workflows.kimi"}`),
	}
	assigned := &types.Issue{
		ID:        "rwf-wisp-filter-assigned",
		Title:     "Assigned routed wisp",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Assignee:  "beads--control-dispatcher",
		Ephemeral: true,
		Metadata:  []byte(`{"gc.routed_to":"beads/workflows.kimi"}`),
	}
	wrongRoute := &types.Issue{
		ID:        "rwf-wisp-filter-wrong-route",
		Title:     "Wrong routed wisp",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Metadata:  []byte(`{"gc.routed_to":"beads/workflows.codex-max"}`),
	}
	for _, issue := range []*types.Issue{matching, assigned, wrongRoute} {
		createIssue(issue)
	}

	ids := readyIDs(types.WorkFilter{
		Status:     types.StatusOpen,
		Unassigned: true,
		MetadataFields: map[string]string{
			"gc.routed_to": "beads/workflows.kimi",
		},
	})
	assertReady(ids, matching.ID, assigned.ID, wrongRoute.ID)

	labelsMatch := &types.Issue{ID: "rwf-labels-match", Title: "Labels match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, Labels: []string{"route", "keep"}}
	labelsWrong := &types.Issue{ID: "rwf-labels-wrong", Title: "Labels wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, Labels: []string{"other", "keep"}}
	labelsExcluded := &types.Issue{ID: "rwf-labels-excluded", Title: "Labels excluded", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, Labels: []string{"route", "skip"}}
	for _, issue := range []*types.Issue{labelsMatch, labelsWrong, labelsExcluded} {
		createIssue(issue)
	}
	ids = readyIDs(types.WorkFilter{
		Status:        types.StatusOpen,
		LabelsAny:     []string{"route"},
		ExcludeLabels: []string{"skip"},
	})
	assertReady(ids, labelsMatch.ID, labelsWrong.ID, labelsExcluded.ID)

	alice := "alice"
	metaAssigneeMatch := &types.Issue{ID: "rwf-meta-assignee-match", Title: "Metadata assignee match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: alice, Ephemeral: true, Metadata: []byte(`{"flag":"yes"}`)}
	metaAssigneeMissing := &types.Issue{ID: "rwf-meta-assignee-missing", Title: "Metadata missing", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: alice, Ephemeral: true}
	metaAssigneeWrong := &types.Issue{ID: "rwf-meta-assignee-wrong", Title: "Assignee wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "bob", Ephemeral: true, Metadata: []byte(`{"flag":"yes"}`)}
	for _, issue := range []*types.Issue{metaAssigneeMatch, metaAssigneeMissing, metaAssigneeWrong} {
		createIssue(issue)
	}
	ids = readyIDs(types.WorkFilter{
		Status:         types.StatusOpen,
		Assignee:       &alice,
		HasMetadataKey: "flag",
	})
	assertReady(ids, metaAssigneeMatch.ID, metaAssigneeMissing.ID, metaAssigneeWrong.ID)

	typeMatch := &types.Issue{ID: "rwf-type-match", Title: "Type match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, Ephemeral: true}
	typeWrong := &types.Issue{ID: "rwf-type-wrong", Title: "Type wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	excludeTypeMatch := &types.Issue{ID: "rwf-exclude-type-match", Title: "Exclude type match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	excludeTypeWrong := &types.Issue{ID: "rwf-exclude-type-wrong", Title: "Exclude type wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, Ephemeral: true}
	for _, issue := range []*types.Issue{typeMatch, typeWrong, excludeTypeMatch, excludeTypeWrong} {
		createIssue(issue)
	}
	ids = readyIDs(types.WorkFilter{Status: types.StatusOpen, Type: string(types.TypeBug)})
	assertReady(ids, typeMatch.ID, typeWrong.ID)
	ids = readyIDs(types.WorkFilter{Status: types.StatusOpen, ExcludeTypes: []types.IssueType{types.TypeBug}})
	assertReady(ids, excludeTypeMatch.ID, excludeTypeWrong.ID)

	swarm := types.MolTypeSwarm
	errorWisp := types.WispTypeError
	typeClassMatch := &types.Issue{ID: "rwf-class-match", Title: "Class match", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, MolType: swarm, WispType: errorWisp}
	typeClassWrong := &types.Issue{ID: "rwf-class-wrong", Title: "Class wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, MolType: swarm, WispType: types.WispTypePing}
	for _, issue := range []*types.Issue{typeClassMatch, typeClassWrong} {
		createIssue(issue)
	}
	ids = readyIDs(types.WorkFilter{Status: types.StatusOpen, MolType: &swarm, WispType: &errorWisp})
	assertReady(ids, typeClassMatch.ID, typeClassWrong.ID)

	molecule := &types.Issue{ID: "rwf-molecule", Title: "Molecule", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	moleculeMatch := &types.Issue{ID: "rwf-molecule-match", Title: "Molecule child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	moleculeWrong := &types.Issue{ID: "rwf-molecule-wrong", Title: "Molecule non-child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{molecule, moleculeMatch, moleculeWrong} {
		createIssue(issue)
	}
	addDep(moleculeMatch.ID, molecule.ID, types.DepParentChild)
	ids = readyIDs(types.WorkFilter{Status: types.StatusOpen, MoleculeID: molecule.ID})
	assertReady(ids, moleculeMatch.ID, moleculeWrong.ID)

	root := &types.Issue{ID: "rwf-root", Title: "Root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	child := &types.Issue{ID: "rwf-child", Title: "Child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	recursiveMatch := &types.Issue{ID: "rwf-recursive-match", Title: "Recursive wisp child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	recursiveWrong := &types.Issue{ID: "rwf-recursive-wrong", Title: "Recursive wisp wrong", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{root, child, recursiveMatch, recursiveWrong} {
		createIssue(issue)
	}
	addDep(child.ID, root.ID, types.DepParentChild)
	addDep(recursiveMatch.ID, child.ID, types.DepParentChild)
	parentID := root.ID
	ids = readyIDs(types.WorkFilter{Status: types.StatusOpen, ParentID: &parentID})
	assertReady(ids, recursiveMatch.ID, recursiveWrong.ID)
}

func TestGetReadyWorkIncludeEphemeralExcludesNonReadyWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rwn")
	ctx := t.Context()
	future := time.Now().UTC().Add(24 * time.Hour)

	createIssue := func(issue *types.Issue) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	addDep := func(issueID, dependsOnID string, depType types.DependencyType) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", issueID, dependsOnID, err)
		}
	}
	readyIDs := func(includeDeferred bool) map[string]bool {
		t.Helper()
		ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{
			Status:           types.StatusOpen,
			IncludeEphemeral: true,
			IncludeDeferred:  includeDeferred,
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make(map[string]bool, len(ready))
		for _, issue := range ready {
			ids[issue.ID] = true
		}
		return ids
	}

	ready := &types.Issue{ID: "rwn-ready", Title: "Ready wisp", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	pinned := &types.Issue{ID: "rwn-pinned", Title: "Pinned wisp", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, Pinned: true}
	deferred := &types.Issue{ID: "rwn-deferred", Title: "Deferred wisp", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true, DeferUntil: &future}
	deferredParent := &types.Issue{ID: "rwn-deferred-parent", Title: "Deferred parent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, DeferUntil: &future}
	childOfDeferred := &types.Issue{ID: "rwn-child-of-deferred", Title: "Child of deferred", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	blocked := &types.Issue{ID: "rwn-blocked", Title: "Blocked wisp", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
	blocker := &types.Issue{ID: "rwn-blocker", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{ready, pinned, deferred, deferredParent, childOfDeferred, blocked, blocker} {
		createIssue(issue)
	}
	addDep(childOfDeferred.ID, deferredParent.ID, types.DepParentChild)
	addDep(blocked.ID, blocker.ID, types.DepBlocks)

	ids := readyIDs(false)
	if !ids[ready.ID] {
		t.Fatalf("ready wisp missing from ready work: %v", ids)
	}
	for _, reject := range []string{pinned.ID, deferred.ID, childOfDeferred.ID, blocked.ID} {
		if ids[reject] {
			t.Fatalf("non-ready wisp %s leaked into ready work: %v", reject, ids)
		}
	}

	ids = readyIDs(true)
	if !ids[deferred.ID] {
		t.Fatalf("deferred wisp should be included when IncludeDeferred=true: %v", ids)
	}
	if !ids[childOfDeferred.ID] {
		t.Fatalf("child of deferred parent should be included when IncludeDeferred=true: %v", ids)
	}
	if ids[pinned.ID] {
		t.Fatalf("pinned wisp leaked with IncludeDeferred=true: %v", ids)
	}
	if ids[blocked.ID] {
		t.Fatalf("blocked wisp leaked with IncludeDeferred=true: %v", ids)
	}
}
