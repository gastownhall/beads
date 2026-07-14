package conformance

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Audit cases for the molecule/wisp/batch/iter slice. Each case targets a
// reference behavior the portable contract left unexercised, and is validated
// against the embedded-Dolt oracle. Ordering the implementations leave
// unspecified (tie-broken current step, unordered dependency scans) is asserted
// as set membership, never positionally.

// RunAudit_molecule_wisp_batch_iter runs the slice's audit cases.
func RunAudit_molecule_wisp_batch_iter(t *testing.T, f Factory) {
	t.Helper()
	t.Run("MoleculeProgressTiebrokenCurrentStep", func(t *testing.T) { testAuditMoleculeProgressTiebrokenCurrentStep(t, f) })
	t.Run("MoleculeLastActivityStepClosed", func(t *testing.T) { testAuditMoleculeLastActivityStepClosed(t, f) })
	t.Run("MoleculeLastActivityWispRouting", func(t *testing.T) { testAuditMoleculeLastActivityWispRouting(t, f) })
	t.Run("PromoteAuxMigration", func(t *testing.T) { testAuditPromoteAuxMigration(t, f) })
	t.Run("PromoteInboundWispRetarget", func(t *testing.T) { testAuditPromoteInboundWispRetarget(t, f) })
	t.Run("UpdateIssueIDWispRename", func(t *testing.T) { testAuditUpdateIssueIDWispRename(t, f) })
	t.Run("UpdateIssueIDDurableSourceRename", func(t *testing.T) { testAuditUpdateIssueIDDurableSourceRename(t, f) })
	t.Run("UpdateIssueIDCollision", func(t *testing.T) { testAuditUpdateIssueIDCollision(t, f) })
	t.Run("UpdateIssueIDColumnSubset", func(t *testing.T) { testAuditUpdateIssueIDColumnSubset(t, f) })
	t.Run("DeleteBySourceRepoEmptyString", func(t *testing.T) { testAuditDeleteBySourceRepoEmptyString(t, f) })
	t.Run("DeleteBySourceRepoDependencyCascade", func(t *testing.T) { testAuditDeleteBySourceRepoDependencyCascade(t, f) })
	t.Run("CreateRejectStaleUpserts", func(t *testing.T) { testAuditCreateRejectStaleUpserts(t, f) })
	t.Run("CreateAllWispsFastPath", func(t *testing.T) { testAuditCreateAllWispsFastPath(t, f) })
	t.Run("CreateAllWispsInlineDependencies", func(t *testing.T) { testAuditCreateAllWispsInlineDependencies(t, f) })
	t.Run("CreateOrphanStrict", func(t *testing.T) { testAuditCreateOrphanStrict(t, f) })
	t.Run("CreateCrossBucketDependency", func(t *testing.T) { testAuditCreateCrossBucketDependency(t, f) })
	t.Run("CreateInBatchCycle", func(t *testing.T) { testAuditCreateInBatchCycle(t, f) })
	t.Run("ListWisps", func(t *testing.T) { testAuditListWisps(t, f) })
	t.Run("GetNextChildID", func(t *testing.T) { testAuditGetNextChildID(t, f) })
}

// auditDepTargets returns the sorted DependsOnID set for a dependency slice.
func auditDepTargets(deps []*types.Dependency) []string {
	out := make([]string, len(deps))
	for i, d := range deps {
		out[i] = d.DependsOnID
	}
	slices.Sort(out)
	return out
}

func auditHasRenamedEvent(events []*types.Event, oldID, newID string) bool {
	for _, e := range events {
		if string(e.EventType) == "renamed" && e.OldValue != nil && *e.OldValue == oldID && e.NewValue != nil && *e.NewValue == newID {
			return true
		}
	}
	return false
}

// --- Molecule ---

// CurrentStepID is set to the first in_progress child the unordered child scan
// yields (molecule.go: no ORDER BY). With more than one in_progress child the
// winner is backend-dependent, so it must be asserted as set membership.
func testAuditMoleculeProgressTiebrokenCurrentStep(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-m", Title: "Mol", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c1", Title: "c1", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c2", Title: "c2", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c3", Title: "c3", Status: types.StatusInProgress}), "a"))
	parentChild(t, s, "test-c1", "test-m")
	parentChild(t, s, "test-c2", "test-m")
	parentChild(t, s, "test-c3", "test-m")

	got, err := s.GetMoleculeProgress(c, "test-m")
	must(t, err)
	if got.Total != 3 || got.InProgress != 3 || got.Completed != 0 {
		t.Fatalf("progress = %+v, want total=3 inprogress=3 completed=0", got)
	}
	if !contains([]string{"test-c1", "test-c2", "test-c3"}, got.CurrentStepID) {
		t.Errorf("CurrentStepID = %q, want a member of {test-c1,test-c2,test-c3}", got.CurrentStepID)
	}
}

// step_closed fires only when max(closed_at) is STRICTLY after max(updated_at)
// across children (compaction.go:340). An equal timestamp yields step_updated.
func testAuditMoleculeLastActivityStepClosed(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	y2021 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	y2022 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

	// Case 1: closed_at (2022) strictly after every updated_at (2021) => step_closed.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-m1", Title: "M1", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-a", Title: "a", Status: types.StatusOpen, CreatedAt: y2021, UpdatedAt: y2021}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-b", Title: "b", Status: types.StatusClosed, CreatedAt: y2021, UpdatedAt: y2021, ClosedAt: &y2022}), "a"))
	parentChild(t, s, "test-a", "test-m1")
	parentChild(t, s, "test-b", "test-m1")
	la, err := s.GetMoleculeLastActivity(c, "test-m1")
	must(t, err)
	if la.Source != "step_closed" || !la.LastActivity.Equal(y2022) || la.SourceStepID != "test-b" {
		t.Fatalf("case1 = %+v, want step_closed at %v from test-b", la, y2022)
	}

	// Case 2: closed_at == max updated_at (both 2022) => strict After false => step_updated.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-m2", Title: "M2", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-c", Title: "c", Status: types.StatusClosed, CreatedAt: y2021, UpdatedAt: y2022, ClosedAt: &y2022}), "a"))
	parentChild(t, s, "test-c", "test-m2")
	la, err = s.GetMoleculeLastActivity(c, "test-m2")
	must(t, err)
	if la.Source != "step_updated" || !la.LastActivity.Equal(y2022) || la.SourceStepID != "test-c" {
		t.Fatalf("case2 = %+v, want step_updated at %v from test-c (equal ts, not step_closed)", la, y2022)
	}
}

// An active-wisp molecule rolls its activity up from the wisp tables
// (compaction.go:256-261), a distinct code path from the durable rollup.
func testAuditMoleculeLastActivityWispRouting(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	y2023 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	y2024 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Wisp molecule with a wisp child: step_updated from the child.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wm", Title: "WM", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wc", Title: "wc", Status: types.StatusOpen, Ephemeral: true, CreatedAt: y2023, UpdatedAt: y2023}), "a"))
	parentChild(t, s, "test-wc", "test-wm")
	la, err := s.GetMoleculeLastActivity(c, "test-wm")
	must(t, err)
	if la.Source != "step_updated" || !la.LastActivity.Equal(y2023) || la.SourceStepID != "test-wc" {
		t.Fatalf("wisp molecule = %+v, want step_updated at %v from test-wc", la, y2023)
	}

	// Childless wisp molecule: molecule_updated from its own updated_at.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wm0", Title: "WM0", Ephemeral: true, CreatedAt: y2024, UpdatedAt: y2024}), "a"))
	la, err = s.GetMoleculeLastActivity(c, "test-wm0")
	must(t, err)
	if la.Source != "molecule_updated" || !la.LastActivity.Equal(y2024) || la.SourceStepID != "" {
		t.Fatalf("childless wisp = %+v, want molecule_updated at %v", la, y2024)
	}
}

// --- Promote ---

// Promotion migrates the wisp's own comments, events, and outbound dependencies
// into the durable tables (promote.go:55-86), not just labels.
func testAuditPromoteAuxMigration(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-t", Title: "target"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "Wisp", Ephemeral: true, Labels: []string{"x"}}), "a"))
	if _, err := s.AddIssueComment(c, "test-w", "alice", "hello wisp"); err != nil {
		t.Fatalf("AddIssueComment on wisp: %v", err)
	}
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-w", DependsOnID: "test-t", Type: types.DepBlocks}, "a"))

	must(t, s.PromoteFromEphemeral(c, "test-w", "a"))

	got, err := s.GetIssue(c, "test-w")
	must(t, err)
	if got.Ephemeral {
		t.Error("promoted issue still Ephemeral")
	}
	comments, err := s.GetIssueComments(c, "test-w")
	must(t, err)
	if len(comments) != 1 || comments[0].Text != "hello wisp" {
		t.Errorf("comments after promote = %v, want the migrated wisp comment", commentTexts(comments))
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-w"]); !slices.Equal(got, []string{"test-t"}) {
		t.Errorf("outbound dep after promote = %v, want [test-t]", got)
	}
	evs, err := s.GetEvents(c, "test-w", 0)
	must(t, err)
	if len(evs) == 0 {
		t.Error("no events after promote; wisp events should have migrated")
	}
}

// A wisp that depended on the promoted wisp keeps a resolvable edge after the
// target leaves the wisp bucket (dependencies.go:597-614 retarget).
func testAuditPromoteInboundWispRetarget(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "W", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w2", Title: "W2", Ephemeral: true}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-w2", DependsOnID: "test-w", Type: types.DepBlocks}, "a"))

	must(t, s.PromoteFromEphemeral(c, "test-w", "a"))

	if got, err := s.GetIssue(c, "test-w"); err != nil || got.Ephemeral {
		t.Fatalf("test-w after promote = (%+v,%v), want durable", got, err)
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-w2"]); !slices.Equal(got, []string{"test-w"}) {
		t.Errorf("inbound wisp dep after promote = %v, want [test-w] (edge still resolves)", got)
	}
}

// --- UpdateIssueID ---

// Renaming an active wisp routes to updateWispIDInTx (bulk_ops.go:250-272):
// wisp tables, wisp_events, and dependency retarget for both target and source.
func testAuditUpdateIssueIDWispRename(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Part A: rename a wisp that is a dependency TARGET.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wa1", Title: "WA1", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wa2", Title: "WA2", Ephemeral: true}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-wa2", DependsOnID: "test-wa1", Type: types.DepBlocks}, "a"))
	must(t, s.UpdateIssueID(c, "test-wa1", "test-wa9", &types.Issue{Title: "WA9"}, "a"))
	if got, err := s.GetIssue(c, "test-wa9"); err != nil || !got.Ephemeral {
		t.Fatalf("test-wa9 after rename = (%+v,%v), want ephemeral", got, err)
	}
	if _, err := s.GetIssue(c, "test-wa1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("old wisp id = %v, want ErrNotFound", err)
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-wa2"]); !slices.Equal(got, []string{"test-wa9"}) {
		t.Errorf("wisp target rename: dep = %v, want [test-wa9]", got)
	}

	// Part B: rename a wisp that is a dependency SOURCE.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wb1", Title: "WB1", Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wb2", Title: "WB2", Ephemeral: true}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-wb2", DependsOnID: "test-wb1", Type: types.DepBlocks}, "a"))
	must(t, s.UpdateIssueID(c, "test-wb2", "test-wb8", &types.Issue{Title: "WB8"}, "a"))
	recs, _ = s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-wb8"]); !slices.Equal(got, []string{"test-wb1"}) {
		t.Errorf("wisp source rename: dep = %v, want [test-wb1]", got)
	}
	if len(recs["test-wb2"]) != 0 {
		t.Errorf("old source id still has edges: %v", auditDepTargets(recs["test-wb2"]))
	}
}

// Renaming a durable issue that is the SOURCE of an outbound edge moves the edge
// to the new id (bulk_ops FK cascade + rekeyDependencySourceInTx).
func testAuditUpdateIssueIDDurableSourceRename(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-0", Title: "Zero"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "One"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-1", DependsOnID: "test-0", Type: types.DepBlocks}, "a"))

	must(t, s.UpdateIssueID(c, "test-1", "test-9", &types.Issue{Title: "One"}, "a"))

	if _, err := s.GetIssue(c, "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("old id = %v, want ErrNotFound", err)
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-9"]); !slices.Equal(got, []string{"test-0"}) {
		t.Errorf("outbound edge after source rename = %v, want [test-0]", got)
	}
	if len(recs["test-1"]) != 0 {
		t.Errorf("old source id still has edges: %v", auditDepTargets(recs["test-1"]))
	}
}

// Renaming onto an already-existing id violates the PK and must error, leaving
// both rows unchanged (bulk_ops.go:227-231).
func testAuditUpdateIssueIDCollision(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "One"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "Two"}), "a"))

	if err := s.UpdateIssueID(c, "test-1", "test-2", &types.Issue{Title: "One"}, "a"); err == nil {
		t.Fatal("rename onto existing id: want error, got nil")
	}
	if got, err := s.GetIssue(c, "test-1"); err != nil || got.Title != "One" {
		t.Errorf("test-1 after failed rename = (%+v,%v), want unchanged", got, err)
	}
	if got, err := s.GetIssue(c, "test-2"); err != nil || got.Title != "Two" {
		t.Errorf("test-2 after failed rename = (%+v,%v), want original", got, err)
	}
}

// updateIssueIDInTx writes only id,title,description,design,acceptance_criteria,
// notes,updated_at (bulk_ops.go:227-231); status/priority are preserved from the
// stored row, and a 'renamed' event is recorded keyed to the new id.
func testAuditUpdateIssueIDColumnSubset(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "Old", Description: "olddesc", Priority: 3, Status: types.StatusInProgress}), "a"))

	must(t, s.UpdateIssueID(c, "test-1", "test-9", &types.Issue{Title: "New", Description: "D", Priority: 0, Status: types.StatusOpen}, "a"))

	got, err := s.GetIssue(c, "test-9")
	must(t, err)
	if got.Title != "New" || got.Description != "D" {
		t.Errorf("text fields = title=%q desc=%q, want New/D", got.Title, got.Description)
	}
	if got.Priority != 3 {
		t.Errorf("Priority = %d, want 3 (not overwritten from passed issue)", got.Priority)
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want in_progress (not overwritten)", got.Status)
	}
	evs, err := s.GetEvents(c, "test-9", 0)
	must(t, err)
	if !auditHasRenamedEvent(evs, "test-1", "test-9") {
		t.Errorf("no 'renamed' event with old=test-1 new=test-9; events=%+v", evs)
	}
}

// --- DeleteIssuesBySourceRepo ---

// source_repo defaults to ” for issues created without one, so deleting by ""
// removes every such durable issue (WHERE source_repo = ”); wisps are untouched.
func testAuditDeleteBySourceRepoEmptyString(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "A"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "B"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-3", Title: "C", SourceRepo: "repoY"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "W", Ephemeral: true}), "a"))

	n, err := s.DeleteIssuesBySourceRepo(c, "")
	must(t, err)
	if n != 2 {
		t.Errorf("deleted = %d, want 2 (both empty-source durable issues)", n)
	}
	if _, err := s.GetIssue(c, "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("test-1 = %v, want ErrNotFound", err)
	}
	if _, err := s.GetIssue(c, "test-2"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("test-2 = %v, want ErrNotFound", err)
	}
	if _, err := s.GetIssue(c, "test-3"); err != nil {
		t.Errorf("test-3 (repoY) wrongly deleted: %v", err)
	}
	if _, err := s.GetIssue(c, "test-w"); err != nil {
		t.Errorf("wisp wrongly deleted: %v", err)
	}
}

// Deleting by source_repo cascades dependency edges where the deleted issue is a
// source or a target (bulk_ops.go:200 FK ON DELETE CASCADE).
func testAuditDeleteBySourceRepoDependencyCascade(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	// test-del is both a dependency TARGET (of test-keep) and a SOURCE (of
	// test-other); both edges must cascade-delete with test-del. Distinct
	// targets avoid a blocks-cycle rejection at AddDependency time.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-keep", Title: "keep", SourceRepo: "repoY"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-other", Title: "other", SourceRepo: "repoY"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-del", Title: "del", SourceRepo: "repoX"}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-keep", DependsOnID: "test-del", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "test-del", DependsOnID: "test-other", Type: types.DepBlocks}, "a"))

	n, err := s.DeleteIssuesBySourceRepo(c, "repoX")
	must(t, err)
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	if _, err := s.GetIssue(c, "test-keep"); err != nil {
		t.Errorf("test-keep wrongly deleted: %v", err)
	}
	if _, err := s.GetIssue(c, "test-other"); err != nil {
		t.Errorf("test-other wrongly deleted: %v", err)
	}
	recs, _ := s.GetAllDependencyRecords(c)
	for src, deps := range recs {
		if src == "test-del" && len(deps) > 0 {
			t.Errorf("outbound edges from test-del survive: %v", auditDepTargets(deps))
		}
		if contains(auditDepTargets(deps), "test-del") {
			t.Errorf("edge still targets deleted test-del from %q", src)
		}
	}
}

// --- CreateIssuesWithFullOptions ---

// RejectStaleUpserts: a strictly-older incoming row is rejected, a strictly-newer
// one overwrites, and an equal-timestamp row leaves the stored columns (the local
// row wins the tie) — create.go:531-560.
func testAuditCreateRejectStaleUpserts(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	y2020 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	y2021 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	y2022 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	opts := storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true, RejectStaleUpserts: true}

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-r", Title: "Orig", CreatedAt: y2021, UpdatedAt: y2021}), "a"))

	// (a) Older incoming row rejected: stored 'Orig' kept.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-r", Title: "Stale", CreatedAt: y2020, UpdatedAt: y2020}),
	}, "a", opts))
	if got, _ := s.GetIssue(c, "test-r"); got.Title != "Orig" {
		t.Errorf("(a) title = %q, want Orig (older rejected)", got.Title)
	}

	// (b) Newer incoming row overwrites.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-r", Title: "Fresh", CreatedAt: y2022, UpdatedAt: y2022}),
	}, "a", opts))
	if got, _ := s.GetIssue(c, "test-r"); got.Title != "Fresh" {
		t.Errorf("(b) title = %q, want Fresh (newer overwrites)", got.Title)
	}

	// (c) Equal timestamp: stored column kept (local wins the same-second tie).
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-r", Title: "Tie", CreatedAt: y2022, UpdatedAt: y2022}),
	}, "a", opts))
	if got, _ := s.GetIssue(c, "test-r"); got.Title != "Fresh" {
		t.Errorf("(c) title = %q, want Fresh (equal ts => stored column kept)", got.Title)
	}
}

// An all-wisp batch runs in a single batch transaction and forces Ephemeral; the
// wisps route to the wisp tables and are not visible as durable issues.
func testAuditCreateAllWispsFastPath(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-w1", Title: "W1", Ephemeral: true}),
		withDefaults(&types.Issue{ID: "test-w2", Title: "W2", Ephemeral: true, Labels: []string{"x"}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))

	for _, id := range []string{"test-w1", "test-w2"} {
		got, err := s.GetIssue(c, id)
		must(t, err)
		if !got.Ephemeral {
			t.Errorf("%s Ephemeral = false, want true", id)
		}
	}
	if labels, _ := s.GetLabels(c, "test-w2"); !contains(labels, "x") {
		t.Errorf("labels of test-w2 = %v, want to include x", labels)
	}
	wisps, err := s.ListWisps(c, types.WispFilter{})
	must(t, err)
	if got := issueIDs(wisps); !slices.Equal(got, []string{"test-w1", "test-w2"}) {
		t.Errorf("ListWisps = %v, want [test-w1 test-w2]", got)
	}
}

// An all-wisp batch carrying inline dependencies persists those edges in the same
// batch transaction (via the batch dependency-persist pass) and reports a
// genuinely-unresolvable edge through OnSkippedDependency instead of dropping it
// silently. Regression guard: the sqlkit all-wisps arm previously looped the
// single-issue create, which runs no dependency-persist pass, so every inline edge
// was silently lost with an empty skipped-dependency report. Mirrors the reachable
// `bd import` options (RejectStaleUpserts off here; skip-on-validation + skip
// callback on).
func testAuditCreateAllWispsInlineDependencies(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	var skipped [][2]string
	opts := storage.BatchCreateOptions{
		OrphanHandling:                 storage.OrphanAllow,
		SkipPrefixValidation:           true,
		SkipDependencyValidationErrors: true,
		OnSkippedDependency: func(issueID, dependsOnID, _ string) {
			skipped = append(skipped, [2]string{issueID, dependsOnID})
		},
	}

	// test-w2 -> test-w1 is a valid in-batch inter-wisp edge (test-w1 is inserted
	// before the dependency pass, so it resolves to the wisp bucket). test-w2 ->
	// test-missing targets a nonexistent issue and must be reported skipped.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-w1", Title: "W1", Ephemeral: true}),
		withDefaults(&types.Issue{ID: "test-w2", Title: "W2", Ephemeral: true, Dependencies: []*types.Dependency{
			{IssueID: "test-w2", DependsOnID: "test-w1", Type: types.DepBlocks},
			{IssueID: "test-w2", DependsOnID: "test-missing", Type: types.DepBlocks},
		}}),
	}, "a", opts))

	// Both wisps persisted to the wisp bucket.
	for _, id := range []string{"test-w1", "test-w2"} {
		if got, err := s.GetIssue(c, id); err != nil || !got.Ephemeral {
			t.Fatalf("%s after all-wisp batch = (%+v,%v), want ephemeral", id, got, err)
		}
	}

	// The valid inter-wisp edge survives — this is exactly what the per-issue loop
	// dropped on the SQL backends.
	recs, _ := s.GetAllDependencyRecords(c)
	if got := auditDepTargets(recs["test-w2"]); !slices.Equal(got, []string{"test-w1"}) {
		t.Errorf("test-w2 deps = %v, want [test-w1] (inline all-wisp edge persisted)", got)
	}

	// The unresolvable edge was reported, not silently dropped, so the skipped
	// accounting still reflects reality.
	sawMissing := false
	for _, e := range skipped {
		if e[0] == "test-w2" && e[1] == "test-missing" {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Errorf("skipped deps = %v, want to include (test-w2 -> test-missing)", skipped)
	}
}

// OrphanStrict rejects an issue whose hierarchical parent is missing
// (create.go:490-515), inserting nothing.
//
// NOTE: the finding's companion claim — that OrphanSkip *silently* drops the
// same orphan — does NOT hold in the batch path on the Dolt reference: the
// skipped orphan is still handed to ReconcileChildCounters, which FK-violates
// on the missing parent (child_counters.fk_counter_parent). So a single
// missing-parent orphan under OrphanSkip errors on Dolt, not skips; only the
// Strict branch is a stable, reproducible contract here.
func testAuditCreateOrphanStrict(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	if err := s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-p.2", Title: "Orphan2"}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanStrict, SkipPrefixValidation: true}); err == nil {
		t.Error("OrphanStrict: want error, got nil")
	}
	if _, err := s.GetIssue(c, "test-p.2"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("OrphanStrict inserted the orphan: %v", err)
	}
}

// A mixed regular+wisp batch with a cross-bucket edge is rejected before insert;
// with SkipDependencyValidationErrors the edge is dropped and both issues persist
// (create.go:288-361).
func testAuditCreateCrossBucketDependency(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// (a) Rejected: neither issue persists.
	err := s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-a", Title: "A"}),
		withDefaults(&types.Issue{ID: "test-b", Title: "B", Ephemeral: true, Dependencies: []*types.Dependency{{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks}}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true})
	if err == nil {
		t.Fatal("(a) cross-bucket batch: want error, got nil")
	}
	if _, err := s.GetIssue(c, "test-a"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("(a) test-a persisted despite rejected batch: %v", err)
	}

	// (b) With skip: both persist, edge dropped.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-c", Title: "C"}),
		withDefaults(&types.Issue{ID: "test-d", Title: "D", Ephemeral: true, Dependencies: []*types.Dependency{{IssueID: "test-d", DependsOnID: "test-c", Type: types.DepBlocks}}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true, SkipDependencyValidationErrors: true}))
	if _, err := s.GetIssue(c, "test-c"); err != nil {
		t.Errorf("(b) test-c missing: %v", err)
	}
	if _, err := s.GetIssue(c, "test-d"); err != nil {
		t.Errorf("(b) test-d missing: %v", err)
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if len(recs["test-d"]) != 0 {
		t.Errorf("(b) cross-bucket edge not dropped: %v", auditDepTargets(recs["test-d"]))
	}
}

// An in-batch dependency cycle is rejected (create.go:703-709); with
// SkipDependencyValidationErrors the offending edge is dropped and issues persist.
func testAuditCreateInBatchCycle(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// (a) Rejected.
	err := s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-a", Title: "A", Dependencies: []*types.Dependency{{IssueID: "test-a", DependsOnID: "test-b", Type: types.DepBlocks}}}),
		withDefaults(&types.Issue{ID: "test-b", Title: "B", Dependencies: []*types.Dependency{{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks}}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true})
	if err == nil {
		t.Fatal("(a) cyclic batch: want error, got nil")
	}
	recs, _ := s.GetAllDependencyRecords(c)
	if contains(auditDepTargets(recs["test-a"]), "test-b") || contains(auditDepTargets(recs["test-b"]), "test-a") {
		t.Errorf("(a) cyclic edge persisted: %v", recs)
	}

	// (b) With skip: both persist, cyclic edge dropped.
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{ID: "test-c", Title: "C", Dependencies: []*types.Dependency{{IssueID: "test-c", DependsOnID: "test-d", Type: types.DepBlocks}}}),
		withDefaults(&types.Issue{ID: "test-d", Title: "D", Dependencies: []*types.Dependency{{IssueID: "test-d", DependsOnID: "test-c", Type: types.DepBlocks}}}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true, SkipDependencyValidationErrors: true}))
	if _, err := s.GetIssue(c, "test-c"); err != nil {
		t.Errorf("(b) test-c missing: %v", err)
	}
	if _, err := s.GetIssue(c, "test-d"); err != nil {
		t.Errorf("(b) test-d missing: %v", err)
	}
}

// --- ListWisps / IterWisps ---

// ListWisps hides closed wisps by default and orders by priority ASC; IterWisps
// returns the identical ordered set (wisp_filter_convert.go, sqlbuild/sort.go).
func testAuditListWisps(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-whi", Title: "hi", Priority: 0, Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wlo", Title: "lo", Priority: 4, Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wmid", Title: "mid", Priority: 2, Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wclosed", Title: "closed", Priority: 1, Status: types.StatusClosed, Ephemeral: true}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-d1", Title: "durable"}), "a"))

	def, err := s.ListWisps(c, types.WispFilter{})
	must(t, err)
	if got := orderedIDs(def); !slices.Equal(got, []string{"test-whi", "test-wmid", "test-wlo"}) {
		t.Errorf("default ListWisps = %v, want [test-whi test-wmid test-wlo] (priority ASC, closed+durable excluded)", got)
	}

	withClosed, err := s.ListWisps(c, types.WispFilter{IncludeClosed: true})
	must(t, err)
	if !contains(orderedIDs(withClosed), "test-wclosed") {
		t.Errorf("IncludeClosed ListWisps = %v, want to include test-wclosed", orderedIDs(withClosed))
	}

	it, err := s.IterWisps(c, types.WispFilter{})
	must(t, err)
	iterated, err := storage.Collect(c, it)
	must(t, err)
	if got := orderedIDs(iterated); !slices.Equal(got, orderedIDs(def)) {
		t.Errorf("IterWisps order = %v, want same as ListWisps %v", got, orderedIDs(def))
	}
}

// GetNextChildID advances a counter without creating an issue and self-heals to
// max direct child (grandchildren excluded); active-wisp parents route to the
// wisp tables (child_id.go:11-58).
func testAuditGetNextChildID(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-p", Title: "P"}), "a"))

	id1, err := s.GetNextChildID(c, "test-p")
	must(t, err)
	if id1 != "test-p.1" {
		t.Errorf("first = %q, want test-p.1", id1)
	}
	id2, err := s.GetNextChildID(c, "test-p")
	must(t, err)
	if id2 != "test-p.2" {
		t.Errorf("second = %q, want test-p.2", id2)
	}

	// Seed a direct child (5) and a grandchild (5.1); the scan self-heals to 5,
	// excluding the grandchild.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-p.5", Title: "c5"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-p.5.1", Title: "gc"}), "a"))
	id3, err := s.GetNextChildID(c, "test-p")
	must(t, err)
	if id3 != "test-p.6" {
		t.Errorf("after seeding child 5 = %q, want test-p.6 (grandchild excluded)", id3)
	}

	// Wisp parent routes to wisp tables.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wp", Title: "WP", Ephemeral: true}), "a"))
	wid, err := s.GetNextChildID(c, "test-wp")
	must(t, err)
	if wid != "test-wp.1" {
		t.Errorf("wisp parent = %q, want test-wp.1", wid)
	}
}
