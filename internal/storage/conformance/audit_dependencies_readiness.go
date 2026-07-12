package conformance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// This file encodes strange/undertested dependency-and-readiness behaviors of the
// issueops reference (validated against the embedded-Dolt oracle). Every case here
// is deliberately absent from conformance.go/portable.go: self-dep and cycle
// rejection, the blocks-only cross-type guard, idempotency vs type-conflict,
// external targets, the blocks-only vs all-types count split, ready-work type/pinned/
// deferred exclusions, hybrid sort ordering, transitive ParentID descendants,
// inherited parent blocking, typed blocker descriptions, and hypothetical
// unblock-by-close. Ordering that the SQL leaves unspecified is asserted as a set.

// RunAudit_dependencies_readiness runs the dependencies-readiness audit cases.
func RunAudit_dependencies_readiness(t *testing.T, f Factory) {
	t.Helper()
	t.Run("SelfDependencyRejected", func(t *testing.T) { testAuditSelfDependencyRejected(t, f) })
	t.Run("CycleRejection", func(t *testing.T) { testAuditCycleRejection(t, f) })
	t.Run("CycleScopeNonBlocking", func(t *testing.T) { testAuditCycleScopeNonBlocking(t, f) })
	t.Run("IdempotencyVsTypeConflict", func(t *testing.T) { testAuditIdempotencyVsTypeConflict(t, f) })
	t.Run("CrossTypeEpicTaskBlocking", func(t *testing.T) { testAuditCrossTypeEpicTaskBlocking(t, f) })
	t.Run("MissingSourceTarget", func(t *testing.T) { testAuditMissingSourceTarget(t, f) })
	t.Run("ExternalTarget", func(t *testing.T) { testAuditExternalTarget(t, f) })
	t.Run("RemoveMissingAndUnblock", func(t *testing.T) { testAuditRemoveMissingAndUnblock(t, f) })
	t.Run("DependencyCountsBlocksOnly", func(t *testing.T) { testAuditDependencyCountsBlocksOnly(t, f) })
	t.Run("DetectCyclesBlocksOnly", func(t *testing.T) { testAuditDetectCyclesBlocksOnly(t, f) })
	t.Run("DependencyTree", func(t *testing.T) { testAuditDependencyTree(t, f) })
	t.Run("ReadyTypeAndPinnedExclusions", func(t *testing.T) { testAuditReadyTypeAndPinnedExclusions(t, f) })
	t.Run("ReadyDeferredExclusion", func(t *testing.T) { testAuditReadyDeferredExclusion(t, f) })
	t.Run("ReadyHybridSortAndOldest", func(t *testing.T) { testAuditReadyHybridSortAndOldest(t, f) })
	t.Run("ReadyParentTransitiveDescendants", func(t *testing.T) { testAuditReadyParentTransitiveDescendants(t, f) })
	t.Run("BlockedInheritedParent", func(t *testing.T) { testAuditBlockedInheritedParent(t, f) })
	t.Run("IsBlockedTypedDescriptions", func(t *testing.T) { testAuditIsBlockedTypedDescriptions(t, f) })
	t.Run("NewlyUnblockedByClose", func(t *testing.T) { testAuditNewlyUnblockedByClose(t, f) })
	t.Run("ReadyWorkWithCounts", func(t *testing.T) { testAuditReadyWorkWithCounts(t, f) })
	t.Run("RelatesToDoesNotBlock", func(t *testing.T) { testAuditRelatesToDoesNotBlock(t, f) })
}

// --- file-private helpers (audit-prefixed to avoid collisions) ---

func auditBlockedByID(blocked []*types.BlockedIssue, id string) *types.BlockedIssue {
	for _, b := range blocked {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// auditMetaN returns the integer under key "n" of a JSON metadata blob, coping
// with the whitespace normalization the JSON column type may apply on round-trip.
func auditMetaN(t *testing.T, raw string) int {
	t.Helper()
	var m map[string]int
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("metadata %q not valid JSON: %v", raw, err)
	}
	return m["n"]
}

// --- AddDependency guards ---

func testAuditSelfDependencyRejected(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "sd-a", Title: "A"}), "a"))

	errBlocks := s.AddDependency(ctx(), &types.Dependency{IssueID: "sd-a", DependsOnID: "sd-a", Type: types.DepBlocks}, "a")
	if errBlocks == nil || !strings.Contains(errBlocks.Error(), "self-dependency") {
		t.Errorf("blocks self-dep err = %v, want a 'self-dependency' error", errBlocks)
	}
	// The self-dep guard fires before the blocks/conditional-blocks early return,
	// so even a relates-to self-edge is rejected.
	errRelates := s.AddDependency(ctx(), &types.Dependency{IssueID: "sd-a", DependsOnID: "sd-a", Type: types.DepRelatesTo}, "a")
	if errRelates == nil || !strings.Contains(errRelates.Error(), "self-dependency") {
		t.Errorf("relates-to self-dep err = %v, want a 'self-dependency' error", errRelates)
	}

	deps, _ := s.GetDependencies(ctx(), "sd-a")
	if len(deps) != 0 {
		t.Errorf("GetDependencies after rejected self-deps = %v, want empty", issueIDs(deps))
	}
}

func testAuditCycleRejection(t *testing.T, f Factory) {
	s := f(t)
	for _, id := range []string{"c1", "c2", "c3"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "c1", DependsOnID: "c2", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "c2", DependsOnID: "c3", Type: types.DepBlocks}, "a"))

	// c3 -> c1 would close the c1->c2->c3 chain into a cycle.
	err := s.AddDependency(ctx(), &types.Dependency{IssueID: "c3", DependsOnID: "c1", Type: types.DepBlocks}, "a")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("closing edge err = %v, want a 'cycle' error", err)
	}
	// The rejected add left no edge: nobody depends on c1.
	dependents, _ := s.GetDependents(ctx(), "c1")
	if len(dependents) != 0 {
		t.Errorf("GetDependents(c1) after rejected add = %v, want empty", issueIDs(dependents))
	}

	// A diamond must NOT be misreported as a cycle (UNION-distinct termination).
	for _, id := range []string{"d1", "d2", "d3", "d4"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d1", DependsOnID: "d2", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d1", DependsOnID: "d3", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d2", DependsOnID: "d4", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "d3", DependsOnID: "d4", Type: types.DepBlocks}, "a"))
}

func testAuditCycleScopeNonBlocking(t *testing.T, f Factory) {
	s := f(t)
	for _, id := range []string{"ra", "rb", "pa", "pb"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	// relates-to and parent-child edges skip the reachability probe, so graph
	// cycles in those edge types form freely.
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ra", DependsOnID: "rb", Type: types.DepRelatesTo}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rb", DependsOnID: "ra", Type: types.DepRelatesTo}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "pa", DependsOnID: "pb", Type: types.DepParentChild}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "pb", DependsOnID: "pa", Type: types.DepParentChild}, "a"))

	raRecs, _ := s.GetDependencyRecords(ctx(), "ra")
	if got := depTargets(raRecs); !slices.Equal(got, []string{"rb"}) {
		t.Errorf("ra records = %v, want [rb]", got)
	}
	rbRecs, _ := s.GetDependencyRecords(ctx(), "rb")
	if got := depTargets(rbRecs); !slices.Equal(got, []string{"ra"}) {
		t.Errorf("rb records = %v, want [ra]", got)
	}
}

func testAuditIdempotencyVsTypeConflict(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ia", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ib", Title: "B"}), "a"))

	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ib", DependsOnID: "ia", Type: types.DepBlocks, Metadata: `{"n":1}`}, "a"))
	// Re-adding the same pair+type is idempotent: metadata is updated, no new row.
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ib", DependsOnID: "ia", Type: types.DepBlocks, Metadata: `{"n":2}`}, "a"))

	recs, _ := s.GetDependencyRecords(ctx(), "ib")
	if len(recs) != 1 {
		t.Fatalf("after idempotent re-add: %d records, want exactly 1", len(recs))
	}
	if recs[0].Type != types.DepBlocks {
		t.Errorf("record type = %q, want blocks", recs[0].Type)
	}
	if n := auditMetaN(t, recs[0].Metadata); n != 2 {
		t.Errorf("metadata n = %d, want 2 (updated, not duplicated)", n)
	}

	// Re-adding the same pair with a different type is a conflict.
	err := s.AddDependency(ctx(), &types.Dependency{IssueID: "ib", DependsOnID: "ia", Type: types.DepRelatesTo}, "a")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("type-conflict err = %v, want an 'already exists' error", err)
	}
}

func testAuditCrossTypeEpicTaskBlocking(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ep", Title: "Epic", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "tk", Title: "Task", IssueType: types.TypeTask}), "a"))

	if err := s.AddDependency(ctx(), &types.Dependency{IssueID: "ep", DependsOnID: "tk", Type: types.DepBlocks}, "a"); err == nil || !strings.Contains(err.Error(), "can only block") {
		t.Errorf("epic->task blocks err = %v, want a 'can only block' error", err)
	}
	if err := s.AddDependency(ctx(), &types.Dependency{IssueID: "tk", DependsOnID: "ep", Type: types.DepBlocks}, "a"); err == nil || !strings.Contains(err.Error(), "can only block") {
		t.Errorf("task->epic blocks err = %v, want a 'can only block' error", err)
	}
	// The cross-type guard is scoped to DepBlocks only; conditional-blocks is allowed.
	if err := s.AddDependency(ctx(), &types.Dependency{IssueID: "ep", DependsOnID: "tk", Type: types.DepConditionalBlocks}, "a"); err != nil {
		t.Errorf("epic->task conditional-blocks err = %v, want nil (guard not applied)", err)
	}
}

func testAuditMissingSourceTarget(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "me", Title: "Me"}), "a"))

	errTarget := s.AddDependency(ctx(), &types.Dependency{IssueID: "me", DependsOnID: "ghost", Type: types.DepBlocks}, "a")
	if errTarget == nil || !strings.Contains(errTarget.Error(), "ghost") || !strings.Contains(errTarget.Error(), "not found") {
		t.Errorf("missing-target err = %v, want to mention 'ghost' and 'not found'", errTarget)
	}
	errSource := s.AddDependency(ctx(), &types.Dependency{IssueID: "ghost", DependsOnID: "me", Type: types.DepBlocks}, "a")
	if errSource == nil || !strings.Contains(errSource.Error(), "ghost") || !strings.Contains(errSource.Error(), "not found") {
		t.Errorf("missing-source err = %v, want to mention 'ghost' and 'not found'", errSource)
	}
	deps, _ := s.GetDependencies(ctx(), "me")
	if len(deps) != 0 {
		t.Errorf("GetDependencies(me) = %v, want empty", issueIDs(deps))
	}
}

func testAuditExternalTarget(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ex1", Title: "Ext"}), "a"))

	// An external: target skips existence validation and is written to depends_on_external.
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ex1", DependsOnID: "external:PROJ-9", Type: types.DepBlocks}, "a"))

	// GetDependencies hydrates issues/wisps only, so the external target is dropped.
	deps, _ := s.GetDependencies(ctx(), "ex1")
	if len(deps) != 0 {
		t.Errorf("GetDependencies(ex1) = %v, want empty (external target is not an issue)", issueIDs(deps))
	}
	// GetDependencyRecords surfaces the raw edge via the COALESCE target expression.
	recs, _ := s.GetDependencyRecords(ctx(), "ex1")
	if got := depTargets(recs); !slices.Equal(got, []string{"external:PROJ-9"}) {
		t.Errorf("GetDependencyRecords(ex1) targets = %v, want [external:PROJ-9]", got)
	}
}

func testAuditRemoveMissingAndUnblock(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb1", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb2", Title: "Blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rb2", DependsOnID: "rb1", Type: types.DepBlocks}, "a"))

	// Removing a non-existent edge is a silent no-op.
	must(t, s.RemoveDependency(ctx(), "rb2", "nope", "a"))
	// Removing the real sole blocks edge recomputes is_blocked; rb2 becomes ready.
	must(t, s.RemoveDependency(ctx(), "rb2", "rb1", "a"))

	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if !contains(issueIDs(ready), "rb2") {
		t.Errorf("ready after remove = %v, want to contain rb2", issueIDs(ready))
	}
	blocked, _, err := s.IsBlocked(ctx(), "rb2")
	must(t, err)
	if blocked {
		t.Error("IsBlocked(rb2) = true after removing sole blocker, want false")
	}
}

func testAuditDependencyCountsBlocksOnly(t *testing.T, f Factory) {
	s := f(t)
	for _, id := range []string{"t", "c", "r", "b"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "c", DependsOnID: "t", Type: types.DepParentChild}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "r", DependsOnID: "t", Type: types.DepRelatesTo}, "a"))

	// CountDependents counts ALL edge types.
	if n, _ := s.CountDependents(ctx(), "t"); n != 2 {
		t.Errorf("CountDependents(t) = %d, want 2 (all types)", n)
	}
	// GetDependencyCounts counts ONLY blocks edges.
	counts, _ := s.GetDependencyCounts(ctx(), []string{"t"})
	if counts["t"] == nil || counts["t"].DependentCount != 0 {
		t.Errorf("GetDependencyCounts[t].DependentCount = %v, want 0 (blocks-only)", counts["t"])
	}

	// Add a blocks edge and re-check: both APIs move, but by different amounts.
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "b", DependsOnID: "t", Type: types.DepBlocks}, "a"))
	if n, _ := s.CountDependents(ctx(), "t"); n != 3 {
		t.Errorf("CountDependents(t) = %d, want 3 after blocks edge", n)
	}
	counts, _ = s.GetDependencyCounts(ctx(), []string{"t"})
	if counts["t"] == nil || counts["t"].DependentCount != 1 {
		t.Errorf("GetDependencyCounts[t].DependentCount = %v, want 1 (blocks-only)", counts["t"])
	}
}

func testAuditDetectCyclesBlocksOnly(t *testing.T, f Factory) {
	s := f(t)
	// An acyclic blocks chain a->b->c yields no cycles.
	for _, id := range []string{"a", "b", "c"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "a", DependsOnID: "b", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "b", DependsOnID: "c", Type: types.DepBlocks}, "a"))

	// A relates-to 2-cycle is invisible to DetectCycles (only blocks/cond-blocks form the graph).
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "x", Title: "x"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "y", Title: "y"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "x", DependsOnID: "y", Type: types.DepRelatesTo}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "y", DependsOnID: "x", Type: types.DepRelatesTo}, "a"))

	cycles, err := s.DetectCycles(ctx())
	must(t, err)
	if len(cycles) != 0 {
		t.Errorf("DetectCycles = %v, want empty (acyclic blocks chain + relates-to cycle)", cycles)
	}
}

func testAuditDependencyTree(t *testing.T, f Factory) {
	s := f(t)
	for _, id := range []string{"g1", "g2", "g3", "g4"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id}), "a"))
	}
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "g1", DependsOnID: "g2", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "g2", DependsOnID: "g3", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "g1", DependsOnID: "g4", Type: types.DepRelatesTo}, "a"))

	nodes, err := s.GetDependencyTree(ctx(), "g1", 10, false, false)
	must(t, err)
	byID := make(map[string]*types.TreeNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	ids := make([]string, 0, len(nodes))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"g1", "g2", "g3"}) {
		t.Errorf("tree ids = %v, want [g1 g2 g3] (relates-to g4 excluded)", ids)
	}
	if byID["g1"] == nil || byID["g1"].Depth != 0 {
		t.Errorf("g1 depth = %v, want 0", byID["g1"])
	}
	if byID["g2"] == nil || byID["g2"].Depth != 1 || byID["g2"].EdgeFromParent != types.DepBlocks {
		t.Errorf("g2 node = %+v, want depth 1 edge blocks", byID["g2"])
	}
	if byID["g3"] == nil || byID["g3"].Depth != 2 {
		t.Errorf("g3 depth = %v, want 2", byID["g3"])
	}

	// maxDepth=1 stops before expanding g1's children.
	shallow, err := s.GetDependencyTree(ctx(), "g1", 1, false, false)
	must(t, err)
	if len(shallow) != 1 || shallow[0].ID != "g1" {
		t.Errorf("maxDepth=1 tree = %v, want just [g1]", orderedIDs(treeIssues(shallow)))
	}
}

// treeIssues extracts the embedded Issues from tree nodes for orderedIDs reuse.
func treeIssues(nodes []*types.TreeNode) []*types.Issue {
	out := make([]*types.Issue, len(nodes))
	for i, n := range nodes {
		iss := n.Issue
		out[i] = &iss
	}
	return out
}

// --- Ready / blocked ---

func testAuditReadyTypeAndPinnedExclusions(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rk1", Title: "task", IssueType: types.TypeTask, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rk2", Title: "gate", IssueType: types.TypeGate, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rk3", Title: "mol", IssueType: types.TypeMolecule, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rk4", Title: "pinned", IssueType: types.TypeTask, Status: types.StatusOpen, Pinned: true}), "a"))

	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if got := issueIDs(ready); !slices.Equal(got, []string{"rk1"}) {
		t.Errorf("ready = %v, want [rk1] (gate/molecule/pinned excluded)", got)
	}
}

func testAuditReadyDeferredExclusion(t *testing.T, f Factory) {
	s := f(t)
	future := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "df1", Title: "deferred", Status: types.StatusOpen, DeferUntil: &future}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "df2", Title: "ready", Status: types.StatusOpen}), "a"))

	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if got := issueIDs(ready); !slices.Equal(got, []string{"df2"}) {
		t.Errorf("ready = %v, want [df2] (df1 deferred out)", got)
	}
	withDeferred, _ := s.GetReadyWork(ctx(), types.WorkFilter{IncludeDeferred: true})
	if got := issueIDs(withDeferred); !slices.Equal(got, []string{"df1", "df2"}) {
		t.Errorf("ready(IncludeDeferred) = %v, want [df1 df2]", got)
	}
}

func testAuditReadyHybridSortAndOldest(t *testing.T, f Factory) {
	s := f(t)
	// Three recent (<48h) tasks created in this wall order, with distinct
	// whole-second created_at so the oldest policy has an unambiguous order.
	base := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "so_p2", Title: "p2", Priority: 2, Status: types.StatusOpen, CreatedAt: base, UpdatedAt: base}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "so_p0", Title: "p0", Priority: 0, Status: types.StatusOpen, CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "so_p1", Title: "p1", Priority: 1, Status: types.StatusOpen, CreatedAt: base.Add(2 * time.Second), UpdatedAt: base.Add(2 * time.Second)}), "a"))

	// Hybrid: all recent -> priority ASC.
	hybrid, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if got := orderedIDs(hybrid); !slices.Equal(got, []string{"so_p0", "so_p1", "so_p2"}) {
		t.Errorf("hybrid order = %v, want [so_p0 so_p1 so_p2]", got)
	}
	// Oldest: created_at ASC -> creation order.
	oldest, _ := s.GetReadyWork(ctx(), types.WorkFilter{SortPolicy: types.SortPolicyOldest})
	if got := orderedIDs(oldest); !slices.Equal(got, []string{"so_p2", "so_p0", "so_p1"}) {
		t.Errorf("oldest order = %v, want [so_p2 so_p0 so_p1]", got)
	}
}

func testAuditReadyParentTransitiveDescendants(t *testing.T, f Factory) {
	s := f(t)
	// Dotted ids satisfy both the recursive-CTE and the id-LIKE descendant paths.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "pp-1", Title: "parent", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "pp-1.1", Title: "child", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "pp-1.1.1", Title: "grandchild", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "pu-1", Title: "unrelated", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "pp-1.1", DependsOnID: "pp-1", Type: types.DepParentChild}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "pp-1.1.1", DependsOnID: "pp-1.1", Type: types.DepParentChild}, "a"))

	parent := "pp-1"
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{ParentID: &parent})
	ids := issueIDs(ready)
	if !contains(ids, "pp-1.1") || !contains(ids, "pp-1.1.1") {
		t.Errorf("ready(ParentID) = %v, want to include child and grandchild", ids)
	}
	if contains(ids, "pu-1") {
		t.Errorf("ready(ParentID) = %v, want to exclude unrelated pu-1", ids)
	}
}

func testAuditBlockedInheritedParent(t *testing.T, f Factory) {
	s := f(t)
	// A blocked epic (blocked by an open epic) propagates is_blocked to its child;
	// the child has no direct active blocker, so GetBlockedIssues substitutes the
	// parent epic as the inherited blocker. Order of the two adds matters: the epic
	// must already be is_blocked when the parent-child edge is added.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bblk", Title: "epic blocker", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bep", Title: "epic", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "bep", DependsOnID: "bblk", Type: types.DepBlocks}, "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bch", Title: "child", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "bch", DependsOnID: "bep", Type: types.DepParentChild}, "a"))

	blocked, err := s.GetBlockedIssues(ctx(), types.WorkFilter{})
	must(t, err)
	child := auditBlockedByID(blocked, "bch")
	if child == nil {
		t.Fatalf("bch not in blocked set = %v", blocked)
	}
	if !slices.Equal(child.BlockedBy, []string{"bep"}) || child.BlockedByCount != 1 {
		t.Errorf("bch blockers = %v (count %d), want [bep] count 1 (inherited parent)", child.BlockedBy, child.BlockedByCount)
	}
}

func testAuditIsBlockedTypedDescriptions(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ib_t", Title: "target", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ib_s", Title: "source", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ib_s", DependsOnID: "ib_t", Type: types.DepConditionalBlocks}, "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "jb_t", Title: "btarget", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "jb_s", Title: "bsource", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "jb_s", DependsOnID: "jb_t", Type: types.DepBlocks}, "a"))

	// conditional-blocks blocker renders as "<id> (<type>)".
	blocked, blockers, err := s.IsBlocked(ctx(), "ib_s")
	must(t, err)
	if !blocked || !slices.Equal(blockers, []string{"ib_t (conditional-blocks)"}) {
		t.Errorf("IsBlocked(ib_s) = (%v,%v), want (true,[ib_t (conditional-blocks)])", blocked, blockers)
	}
	// plain blocks blocker renders as bare id.
	blocked, blockers, err = s.IsBlocked(ctx(), "jb_s")
	must(t, err)
	if !blocked || !slices.Equal(blockers, []string{"jb_t"}) {
		t.Errorf("IsBlocked(jb_s) = (%v,%v), want (true,[jb_t])", blocked, blockers)
	}
	// The target itself is not blocked.
	blocked, _, err = s.IsBlocked(ctx(), "ib_t")
	must(t, err)
	if blocked {
		t.Error("IsBlocked(ib_t) = true, want false (target has no blocker)")
	}
}

func testAuditNewlyUnblockedByClose(t *testing.T, f Factory) {
	s := f(t)
	for _, id := range []string{"nt", "other", "w1", "w2", "cb"} {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: id, Title: id, Status: types.StatusOpen}), "a"))
	}
	// w1 depends on nt AND other (both blocks); w2 depends only on nt (blocks);
	// cb depends on nt via conditional-blocks (not counted).
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "w1", DependsOnID: "nt", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "w1", DependsOnID: "other", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "w2", DependsOnID: "nt", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "cb", DependsOnID: "nt", Type: types.DepConditionalBlocks}, "a"))

	// Hypothetically closing nt: only w2 becomes unblocked. w1 still blocked by
	// 'other'; cb's conditional-blocks edge is not a candidate.
	unblocked, err := s.GetNewlyUnblockedByClose(ctx(), "nt")
	must(t, err)
	if got := issueIDs(unblocked); !slices.Equal(got, []string{"w2"}) {
		t.Errorf("GetNewlyUnblockedByClose(nt) = %v, want [w2]", got)
	}
}

func testAuditReadyWorkWithCounts(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rc_t", Title: "target", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rc_a", Title: "a", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rc_b", Title: "b", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rc_a", DependsOnID: "rc_t", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rc_b", DependsOnID: "rc_t", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rc_a", DependsOnID: "rc_b", Type: types.DepRelatesTo}, "a"))

	// rc_a and rc_b are blocked by open rc_t, so only rc_t is ready. Its counts
	// are blocks-only: two blocks dependents, zero dependencies.
	rows, err := s.GetReadyWorkWithCounts(ctx(), types.WorkFilter{})
	must(t, err)
	var target *types.IssueWithCounts
	for _, r := range rows {
		if r.ID == "rc_t" {
			target = r
		}
	}
	if target == nil {
		t.Fatalf("rc_t not in ready-with-counts set")
	}
	if target.DependentCount != 2 {
		t.Errorf("rc_t DependentCount = %d, want 2 (blocks only)", target.DependentCount)
	}
	if target.DependencyCount != 0 {
		t.Errorf("rc_t DependencyCount = %d, want 0", target.DependencyCount)
	}
}

func testAuditRelatesToDoesNotBlock(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "re_a", Title: "A", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "re_b", Title: "B", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "re_a", DependsOnID: "re_b", Type: types.DepRelatesTo}, "a"))

	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	ids := issueIDs(ready)
	if !contains(ids, "re_a") || !contains(ids, "re_b") {
		t.Errorf("ready = %v, want to contain both re_a and re_b (relates-to does not gate)", ids)
	}
	blocked, _, err := s.IsBlocked(ctx(), "re_a")
	must(t, err)
	if blocked {
		t.Error("IsBlocked(re_a) = true, want false (relates-to is informational)")
	}
	recs, _ := s.GetDependencyRecords(ctx(), "re_a")
	if got := depTargets(recs); !slices.Equal(got, []string{"re_b"}) {
		t.Errorf("GetDependencyRecords(re_a) = %v, want [re_b]", got)
	}
}
