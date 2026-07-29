package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestCountAndIterIncludeWispDependencies is the regression guard for the
// PR #4010 review (be-xbl83). The Count*/Iter* paths that feed
// `bd show --json` (DependentCount/DependencyCount and --include-dependents)
// must include `wisp_dependencies` edges, matching the slice-path relationship
// APIs (GetDependentsWithMetadata / GetDependenciesWithMetadata) that the
// human-readable `bd show` already uses.
//
// Before the fix, CountDependents, CountDependencies, CountDependentsByStatus,
// and the streaming IterDependentsWithMetadata queried only the `dependencies`
// table. A permanent issue with a wisp dependent therefore reported a
// too-small dependent_count and omitted the wisp from --include-dependents,
// contradicting the `dependents` slice in the same output.
func TestCountAndIterIncludeWispDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build:  wc-perm-dep --blocks--> wc-target   (edge in `dependencies`)
	//         wc-wisp-dep --blocks--> wc-target   (edge in `wisp_dependencies`)
	// so wc-target has one permanent and one wisp dependent.
	createPerm(t, ctx, store, "wc-target")
	createPerm(t, ctx, store, "wc-perm-dep")
	createWisp(t, ctx, store, "wc-wisp-dep")

	addBlocks := func(src, tgt string) {
		t.Helper()
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID:     src,
			DependsOnID: tgt,
			Type:        types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("add dependency %s -> %s: %v", src, tgt, err)
		}
	}
	addBlocks("wc-perm-dep", "wc-target")
	addBlocks("wc-wisp-dep", "wc-target")

	// Precondition: the slice path bd show's human output uses already counts
	// both dependents. The new count/iter paths must agree with it.
	deps, err := store.GetDependentsWithMetadata(ctx, "wc-target")
	if err != nil {
		t.Fatalf("GetDependentsWithMetadata: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("precondition: GetDependentsWithMetadata(wc-target) = %d, want 2", len(deps))
	}

	// CountDependents must include the wisp dependent.
	if n, err := store.CountDependents(ctx, "wc-target"); err != nil {
		t.Fatalf("CountDependents: %v", err)
	} else if n != 2 {
		t.Errorf("CountDependents(wc-target) = %d, want 2 (1 perm + 1 wisp); wisp_dependencies edge dropped", n)
	}

	// CountDependentsByStatus must include the wisp dependent (both are open).
	if n, err := store.CountDependentsByStatus(ctx, "wc-target", types.StatusOpen); err != nil {
		t.Fatalf("CountDependentsByStatus: %v", err)
	} else if n != 2 {
		t.Errorf("CountDependentsByStatus(wc-target, open) = %d, want 2", n)
	}

	// CountDependencies on the wisp source must count its wisp_dependencies edge.
	if n, err := store.CountDependencies(ctx, "wc-wisp-dep"); err != nil {
		t.Fatalf("CountDependencies: %v", err)
	} else if n != 1 {
		t.Errorf("CountDependencies(wc-wisp-dep) = %d, want 1 (edge lives in wisp_dependencies)", n)
	}

	// IterDependentsWithMetadata must stream BOTH dependents.
	iter, err := store.IterDependentsWithMetadata(ctx, "wc-target")
	if err != nil {
		t.Fatalf("IterDependentsWithMetadata: %v", err)
	}
	defer iter.Close() //nolint:errcheck
	seen := map[string]bool{}
	for iter.Next(ctx) {
		seen[iter.Value().Issue.ID] = true
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("IterDependentsWithMetadata iterate: %v", err)
	}
	if !seen["wc-perm-dep"] || !seen["wc-wisp-dep"] {
		t.Errorf("IterDependentsWithMetadata(wc-target) streamed %v, want both wc-perm-dep and wc-wisp-dep", seen)
	}
}
