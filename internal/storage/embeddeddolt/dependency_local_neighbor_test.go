//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedHydratedListingsKeepTheLocalNeighborContract pins the storage
// decision the bd-hkpas7 (#5272) design rests on: the hydrated metadata
// listings still DROP every edge whose far end has no row in this database,
// exactly as before. The display closes the count/listing skew OUTSIDE these
// listings — GetDependencyRecords publishes the raw rows `bd show` renders as
// raw edge lines and unresolvable_dependency_edges carries — so every graph
// consumer downstream (gating, parent, readiness, epic progress, Relations,
// dep-list) keeps seeing only real, local, hydrated neighbors and never an
// invented issue.
//
// The alternative — hydrating placeholder rows so the listings stop dropping
// non-local targets — was considered and rejected by the approved design
// (C2). The triage repro for this issue asked for exactly that keep-non-local
// behavior from GetDependenciesWithMetadata; this test is its deliberate
// inverse, and the documented reason it is not the contract.
func TestEmbeddedHydratedListingsKeepTheLocalNeighborContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lnc")
	ctx := t.Context()

	create := func(id, title string) {
		t.Helper()
		iss := &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	addDep := func(source, target string, typ types.DependencyType) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: source, DependsOnID: target, Type: typ}, "tester"); err != nil {
			t.Fatalf("add edge %s -> %s (%s): %v", source, target, typ, err)
		}
	}

	// lnc-s carries the full skew shape: one local blocker, one `external:`
	// reference, one bare cross-prefix target. lnc-d makes lnc-s a local
	// dependent. lnc-g carries ONLY a cross-repo blocking edge, so a phantom
	// target is the only thing that could gate it.
	create("lnc-s", "subject with mixed edges")
	create("lnc-l", "local blocker")
	create("lnc-d", "local dependent")
	create("lnc-g", "gated only by a phantom")
	addDep("lnc-s", "lnc-l", types.DepBlocks)
	addDep("lnc-s", "external:liveop:lnc-e", types.DepTracks)
	addDep("lnc-s", "liveop-lnc-c", types.DepBlocks)
	addDep("lnc-d", "lnc-s", types.DepBlocks)
	addDep("lnc-g", "liveop-lnc-c", types.DepBlocks)

	// The design decision itself: the hydrated OUTBOUND listing drops the two
	// non-local targets and hydrates only the local one.
	deps, err := te.store.GetDependenciesWithMetadata(ctx, "lnc-s")
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "lnc-l" || deps[0].DependencyType != types.DepBlocks {
		t.Fatalf("hydrated dependencies = %+v, want only the local lnc-l blocks row — "+
			"non-local targets must stay dropped (bd-hkpas7 design rejects placeholder hydration)", deps)
	}

	// The INBOUND listing is untouched by the change: the local dependent
	// hydrates exactly as it always did.
	dependents, err := te.store.GetDependentsWithMetadata(ctx, "lnc-s")
	if err != nil {
		t.Fatalf("GetDependentsWithMetadata: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != "lnc-d" {
		t.Fatalf("hydrated dependents = %+v, want only the local lnc-d row", dependents)
	}

	// The counts keep what the listings drop — the skew itself, unchanged.
	n, err := te.store.CountDependencies(ctx, "lnc-s")
	if err != nil {
		t.Fatalf("CountDependencies: %v", err)
	}
	if n != 3 {
		t.Errorf("CountDependencies = %d, want 3 — every stored edge counts, local or not", n)
	}

	// The display source: the raw read hands back ALL three rows, including
	// the two the hydrated listing dropped. This is what `bd show` renders
	// as raw edge lines and what unresolvable_dependency_edges publishes.
	raw, err := te.store.GetDependencyRecords(ctx, "lnc-s")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("raw records = %+v, want all 3 stored rows", raw)
	}
	gotTargets := map[string]bool{}
	for _, row := range raw {
		gotTargets[row.DependsOnID] = true
	}
	for _, want := range []string{"lnc-l", "external:liveop:lnc-e", "liveop-lnc-c"} {
		if !gotTargets[want] {
			t.Errorf("raw records missing the %q edge: %+v", want, raw)
		}
	}

	// A graph consumer reading the same data: a blocking edge to a target with
	// no row cannot gate — the phantom stays a display fact, never a blocker.
	blocked, blockers, err := te.store.IsBlocked(ctx, "lnc-g")
	if err != nil {
		t.Fatalf("IsBlocked(lnc-g): %v", err)
	}
	if blocked || len(blockers) != 0 {
		t.Errorf("lnc-g is blocked by %v — a target with no row in this database must not gate", blockers)
	}
	// And the local blocker still does, exactly as before the change.
	blocked, blockers, err = te.store.IsBlocked(ctx, "lnc-s")
	if err != nil {
		t.Fatalf("IsBlocked(lnc-s): %v", err)
	}
	if !blocked || len(blockers) != 1 || blockers[0] != "lnc-l" {
		t.Errorf("lnc-s must stay blocked by exactly the local lnc-l, got %v", blockers)
	}
}
