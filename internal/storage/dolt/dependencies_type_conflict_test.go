package dolt

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestAddDependencyTypeConflictReturnsTypedError proves the embedded
// issueops/DoltStore write stack returns the SAME typed conflict as the
// domain/db (proxied-server) stack when an edge of a different type already
// exists between a pair: a *domain.DependencyTypeConflictError, errors.As-able
// with the existing/requested types readable off it, whose message is still
// byte-identical to the historical fmt.Errorf string so callers that string-match
// the old wording keep working.
//
// It asserts against domain.DependencyTypeConflictError rather than the public
// beads.DependencyTypeConflictError alias only because package dolt cannot import
// the root beads package (root beads imports dolt); the two are the same type via
// the alias, and the public-alias errors.As is locked separately in the root
// errors_test.
//
// Both DoltStore.AddDependency write seams funnel through the single typed-conflict
// return at issueops/dependencies.go, but wrap it differently, so both are covered:
//   - issues source: commits via withRetryTx (backoff-permanent, no retry loop).
//   - wisp source: addWispDependency (dolt/wisps.go) uses its own BeginTx/Commit,
//     returning the conflict before it reaches Commit — a distinct wrapping point.
//
// The external/cross-prefix route shares the same issueops/dependencies.go return
// and is not separately exercised here.
func TestAddDependencyTypeConflictReturnsTypedError(t *testing.T) {
	t.Run("issues_source", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createPerm(t, ctx, store, "tc-a")
		createPerm(t, ctx, store, "tc-b")
		assertTypeConflict(t, ctx, store, "tc-a", "tc-b")
	})

	t.Run("wisp_source", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		createWisp(t, ctx, store, "tc-w")
		createPerm(t, ctx, store, "tc-wt")
		assertTypeConflict(t, ctx, store, "tc-w", "tc-wt")
	})
}

// assertTypeConflict adds a blocks edge source -> target, then adds a related
// edge over the same pair and asserts the second add returns the typed conflict
// with correct fields and a byte-identical message.
func assertTypeConflict(t *testing.T, ctx context.Context, store *DoltStore, source, target string) {
	t.Helper()

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: source, DependsOnID: target, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency blocks %s -> %s: %v", source, target, err)
	}

	err := store.AddDependency(ctx, &types.Dependency{
		IssueID: source, DependsOnID: target, Type: types.DepRelated,
	}, "tester")
	if err == nil {
		t.Fatalf("AddDependency(related) over existing blocks edge %s -> %s = nil, want type-conflict error", source, target)
	}

	var conflict *domain.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("errors.As(err, *domain.DependencyTypeConflictError) = false; err = %v", err)
	}
	if conflict.IssueID != source || conflict.DependsOnID != target ||
		conflict.ExistingType != "blocks" || conflict.RequestedType != "related" {
		t.Errorf("conflict fields = %+v, want {IssueID:%s DependsOnID:%s ExistingType:blocks RequestedType:related}", conflict, source, target)
	}

	// Byte-identical to the pre-taxonomy fmt.Errorf message.
	want := fmt.Sprintf(`dependency %s -> %s already exists with type "blocks" (requested "related"); remove it first with 'bd dep remove' then re-add`, source, target)
	if err.Error() != want {
		t.Errorf("message = %q, want byte-identical %q", err.Error(), want)
	}
}
