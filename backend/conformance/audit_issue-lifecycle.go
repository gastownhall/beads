package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// This file encodes audit findings for the "issue-lifecycle" slice: batch delete
// (cascade/force/orphan-guard/dry-run), FK child-row cleanup, derived timestamps
// (closed_at, started_at), metadata JSON round-trip, and the external-ref wisp
// fallthrough. Every case is validated against the embedded-Dolt reference (the
// oracle); they pin real reference behavior so a SQL backend that diverges fails
// loudly.
//
// THE CLOSE/REOPEN CASES ARE GONE, and deliberately. Close, reopen and the
// is_blocked recompute they trigger reach ONE body from either seam
// (issueops.closeIssueInTx / ReopenIssueInTx; the store verbs and the Lifecycle
// role both land there), and lifecycle_close_reopen_contract.go pins each of the
// four promises they held with a strictly stronger observation:
//
//	first-close attribution wins   RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose
//	                               (raw closed_at/close_reason/session + a raw
//	                               event delta, where this file read GetIssue)
//	reopen is a strict no-op       RunLifecycleReopenLeavesNonDoneStatusesUnchanged
//	                               (three non-done statuses, whole-row compare)
//	reopen mints exactly one event RunLifecycleReopenRecordsItsReason
//	a missing id is ErrNotFound    RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps
//	                               (guarded and unguarded, and NOT ErrVersionMismatch)
//	the recompute leaves           RunLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers
//	updated_at alone               (raw is_blocked flip + raw updated_at, with a
//	                               falsifiability pre-check, a cross-plane wisp
//	                               depender, an inherited child and a control)
//
// and each runs on the unit-of-work leg as well, which this suite's Factory type
// cannot reach. Same for UpdateIssueTypeRejectsInvalid against
// RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary, which adds
// the CONFIGURED-vocabulary positive half a guard hardcoding the built-ins would
// pass here. The store verbs keep their seats in this suite through
// testCloseAndReopen, testUpdateIssueType and the ready/blocked cases.

// RunAudit_issue_lifecycle runs the issue-lifecycle audit cases.
func RunAudit_issue_lifecycle(t *testing.T, f Factory) {
	t.Helper()
	t.Run("DeleteIssuesBatchModes", func(t *testing.T) { testAuditDeleteIssuesBatchModes(t, f) })
	t.Run("DeleteIssuesDryRunCounts", func(t *testing.T) { testAuditDeleteIssuesDryRunCounts(t, f) })
	t.Run("DeleteIssueCascadesChildRows", func(t *testing.T) { testAuditDeleteIssueCascadesChildRows(t, f) })
	t.Run("CreateClosedDerivesClosedAt", func(t *testing.T) { testAuditCreateClosedDerivesClosedAt(t, f) })
	t.Run("MetadataJSONRoundTrip", func(t *testing.T) { testAuditMetadataJSONRoundTrip(t, f) })
	t.Run("StartedAtStampedOnceOnInProgress", func(t *testing.T) { testAuditStartedAtStampedOnceOnInProgress(t, f) })
	t.Run("ExternalRefResolvesWispTier", func(t *testing.T) { testAuditExternalRefResolvesWispTier(t, f) })
}

// Batch DeleteIssues: non-cascade/non-force with an external dependent is a guarded
// no-op error; cascade removes the whole subtree; force deletes the target and
// reports the orphaned dependents.
func testAuditDeleteIssuesBatchModes(t *testing.T, f Factory) {
	s := f(t)

	// Case 1: orphan guard. B depends-on A (blocks), so A has an external dependent.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d1-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d1-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d1-b", DependsOnID: "d1-a", Type: types.DepBlocks}, "a"))
	res1, err := s.DeleteIssues(ctx(), []string{"d1-a"}, false, false, false)
	if err == nil {
		t.Error("Case1: expected error deleting a blocker without cascade/force")
	}
	if res1 == nil || len(res1.OrphanedIssues) != 1 || res1.OrphanedIssues[0] != "d1-b" {
		t.Errorf("Case1: OrphanedIssues = %v, want [d1-b]", orphaned(res1))
	}
	if _, err := s.GetIssue(ctx(), "d1-a"); err != nil {
		t.Errorf("Case1: A should still exist (nothing deleted), got %v", err)
	}

	// Case 2: cascade removes A and its dependent B.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d2-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d2-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d2-b", DependsOnID: "d2-a", Type: types.DepBlocks}, "a"))
	if _, err := s.DeleteIssues(ctx(), []string{"d2-a"}, true, false, false); err != nil {
		t.Fatalf("Case2: cascade delete: %v", err)
	}
	if _, err := s.GetIssue(ctx(), "d2-a"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Case2: A after cascade = %v, want ErrNotFound", err)
	}
	if _, err := s.GetIssue(ctx(), "d2-b"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Case2: B after cascade = %v, want ErrNotFound", err)
	}

	// Case 3: force deletes A, leaves B orphaned and reports it.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d3-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d3-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d3-b", DependsOnID: "d3-a", Type: types.DepBlocks}, "a"))
	res3, err := s.DeleteIssues(ctx(), []string{"d3-a"}, false, true, false)
	if err != nil {
		t.Fatalf("Case3: force delete: %v", err)
	}
	if _, err := s.GetIssue(ctx(), "d3-a"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Case3: A after force = %v, want ErrNotFound", err)
	}
	if _, err := s.GetIssue(ctx(), "d3-b"); err != nil {
		t.Errorf("Case3: B should survive force delete of A, got %v", err)
	}
	if res3 == nil || len(res3.OrphanedIssues) != 1 || res3.OrphanedIssues[0] != "d3-b" {
		t.Errorf("Case3: OrphanedIssues = %v, want [d3-b]", orphaned(res3))
	}
}

// orphaned safely reads OrphanedIssues from a possibly-nil result.
func orphaned(r *types.DeleteIssuesResult) []string {
	if r == nil {
		return nil
	}
	return r.OrphanedIssues
}

// dryRun counts child rows without mutating anything.
func testAuditDeleteIssuesDryRunCounts(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dry-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dry-b", Title: "B"}), "a"))
	must(t, s.AddLabel(ctx(), "dry-a", "l1", "a"))
	must(t, s.AddLabel(ctx(), "dry-a", "l2", "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "dry-b", DependsOnID: "dry-a", Type: types.DepBlocks}, "a"))

	res, err := s.DeleteIssues(ctx(), []string{"dry-a"}, false, true, true)
	must(t, err)
	if res.DeletedCount != 1 {
		t.Errorf("DeletedCount = %d, want 1", res.DeletedCount)
	}
	if res.LabelsCount != 2 {
		t.Errorf("LabelsCount = %d, want 2", res.LabelsCount)
	}
	if res.DependenciesCount != 1 {
		t.Errorf("DependenciesCount = %d, want 1 (the B->A edge)", res.DependenciesCount)
	}
	// Dry run mutated nothing.
	if _, err := s.GetIssue(ctx(), "dry-a"); err != nil {
		t.Errorf("A should still exist after dry run, got %v", err)
	}
}

// DeleteIssue relies on FK ON DELETE CASCADE to clean up child dependency/label rows.
func testAuditDeleteIssueCascadesChildRows(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cc-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cc-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "cc-a", DependsOnID: "cc-b", Type: types.DepBlocks}, "a"))
	must(t, s.AddLabel(ctx(), "cc-a", "x", "a"))

	must(t, s.DeleteIssue(ctx(), "cc-a"))

	recs, err := s.GetAllDependencyRecords(ctx())
	must(t, err)
	if _, ok := recs["cc-a"]; ok {
		t.Errorf("GetAllDependencyRecords still has an entry for cc-a: %v", recs["cc-a"])
	}

	byLabel, err := s.GetIssuesByLabel(ctx(), "x")
	must(t, err)
	if contains(issueIDs(byLabel), "cc-a") {
		t.Errorf("GetIssuesByLabel(x) still returns cc-a: %v", issueIDs(byLabel))
	}
}

// Creating an issue Status=closed with no ClosedAt derives closed_at = max(created,updated)+1s.
func testAuditCreateClosedDerivesClosedAt(t *testing.T, f Factory) {
	s := f(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{
		ID:        "clx-1",
		Title:     "T",
		Status:    types.StatusClosed,
		CreatedAt: base,
		UpdatedAt: base,
	}), "a"))

	got, err := s.GetIssue(ctx(), "clx-1")
	must(t, err)
	if got.ClosedAt == nil {
		t.Fatal("ClosedAt nil, want max(created,updated)+1s")
	}
	want := base.Add(time.Second)
	if !got.ClosedAt.Equal(want) {
		t.Errorf("ClosedAt = %v, want %v", got.ClosedAt, want)
	}
}

// Metadata survives a create/update round-trip through the JSON column (compare
// parsed maps, not bytes: JSON columns may reformat/reorder).
func testAuditMetadataJSONRoundTrip(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{
		ID:       "md-1",
		Title:    "T",
		Metadata: json.RawMessage(`{"team":"eng"}`),
	}), "a"))

	got, err := s.GetIssue(ctx(), "md-1")
	must(t, err)
	if m := auditParseMeta(t, got.Metadata); m["team"] != "eng" {
		t.Errorf("after create, metadata = %v, want team=eng", m)
	}

	// NormalizeMetadataValue accepts string/[]byte/json.RawMessage (not a map).
	must(t, s.UpdateIssue(ctx(), "md-1", map[string]interface{}{"metadata": []byte(`{"team":"ops"}`)}, "a"))
	got, err = s.GetIssue(ctx(), "md-1")
	must(t, err)
	if m := auditParseMeta(t, got.Metadata); m["team"] != "ops" {
		t.Errorf("after update, metadata = %v, want team=ops", m)
	}
}

func auditParseMeta(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("metadata not valid JSON: %v (%s)", err, string(raw))
	}
	return m
}

// Transitioning to in_progress stamps started_at once; a later re-transition
// preserves the original stamp.
func testAuditStartedAtStampedOnceOnInProgress(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "st-1", Title: "T", Status: types.StatusOpen}), "a"))

	must(t, s.UpdateIssue(ctx(), "st-1", map[string]interface{}{"status": string(types.StatusInProgress)}, "a"))
	got, err := s.GetIssue(ctx(), "st-1")
	must(t, err)
	if got.StartedAt == nil {
		t.Fatal("StartedAt nil after first in_progress transition")
	}
	first := *got.StartedAt

	must(t, s.UpdateIssue(ctx(), "st-1", map[string]interface{}{"status": string(types.StatusOpen)}, "a"))
	must(t, s.UpdateIssue(ctx(), "st-1", map[string]interface{}{"status": string(types.StatusInProgress)}, "a"))
	got, err = s.GetIssue(ctx(), "st-1")
	must(t, err)
	if got.StartedAt == nil || !got.StartedAt.Equal(first) {
		t.Errorf("StartedAt = %v, want preserved %v", got.StartedAt, first)
	}
}

// GetIssueByExternalRef resolves an ephemeral bead via the wisp tier fallthrough.
func testAuditExternalRefResolvesWispTier(t *testing.T, f Factory) {
	s := f(t)
	ref := "gh-77"
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "wr-1", Title: "Wisp", Ephemeral: true, ExternalRef: &ref}), "a"))

	got, err := s.GetIssueByExternalRef(ctx(), "gh-77")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef(gh-77): %v", err)
	}
	if got.ID != "wr-1" {
		t.Errorf("ID = %q, want wr-1", got.ID)
	}
	if !got.Ephemeral {
		t.Errorf("Ephemeral = false, want true")
	}

	_, err = s.GetIssueByExternalRef(ctx(), "gh-absent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing ref: %v, want ErrNotFound", err)
	}
}
