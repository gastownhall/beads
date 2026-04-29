//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// seedTwinIDIssue creates a minimal open issue with the given ID for routing
// resolver tests.
func seedTwinIDIssue(t *testing.T, ctx context.Context, st interface {
	CreateIssue(context.Context, *types.Issue, string) error
}, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "seed " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := st.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("seed CreateIssue %s: %v", id, err)
	}
}

// TestResolveTwinID covers the in-process branches of resolveTwinID. The
// command-level integration test in dep_routing_deadlock_test.go exercises
// the same helper end-to-end via the bd binary, but is gated by the
// 'integration' build tag and thus does not contribute to the patch
// coverage reported by codecov on PRs (see GH#3587).
func TestResolveTwinID(t *testing.T) {
	tmpDir := t.TempDir()
	localStore := newTestStoreIsolatedDB(t, filepath.Join(tmpDir, "local", ".beads", "beads.db"), "src")
	fromStore := newTestStoreIsolatedDB(t, filepath.Join(tmpDir, "from", ".beads", "beads.db"), "tgt")
	ctx := context.Background()

	seedTwinIDIssue(t, ctx, localStore, "src-aaa")
	seedTwinIDIssue(t, ctx, fromStore, "tgt-bbb")

	t.Run("FromStoreHit_AvoidsReopeningSameDB", func(t *testing.T) {
		// The deadlock-avoidance path: fromStore != localStore and the ID
		// lives in fromStore. Must resolve via fromStore without re-opening
		// any database.
		got, cleanup, err := resolveTwinID(ctx, localStore, fromStore, "tgt-bbb")
		if err != nil {
			t.Fatalf("resolveTwinID() error = %v", err)
		}
		defer cleanup()
		if got != "tgt-bbb" {
			t.Errorf("resolveTwinID() = %q, want %q", got, "tgt-bbb")
		}
	})

	t.Run("FallsThroughToLocalStore_WhenNotInFromStore", func(t *testing.T) {
		// fromStore differs from localStore but the ID is only in localStore.
		// The first lookup should fail silently and the second should hit.
		got, cleanup, err := resolveTwinID(ctx, localStore, fromStore, "src-aaa")
		if err != nil {
			t.Fatalf("resolveTwinID() error = %v", err)
		}
		defer cleanup()
		if got != "src-aaa" {
			t.Errorf("resolveTwinID() = %q, want %q", got, "src-aaa")
		}
	})

	t.Run("FromStoreEqualsLocalStore_SkipsFirstLookup", func(t *testing.T) {
		// When both arguments point at the same store, the fromStore != localStore
		// guard skips the first branch and resolution falls straight through to
		// localStore.
		got, cleanup, err := resolveTwinID(ctx, localStore, localStore, "src-aaa")
		if err != nil {
			t.Fatalf("resolveTwinID() error = %v", err)
		}
		defer cleanup()
		if got != "src-aaa" {
			t.Errorf("resolveTwinID() = %q, want %q", got, "src-aaa")
		}
	})

	t.Run("NilFromStore_ResolvesViaLocalStore", func(t *testing.T) {
		// Defensive path: callers may pass a nil fromStore. The nil guard on
		// branch 1 should skip to localStore.
		got, cleanup, err := resolveTwinID(ctx, localStore, nil, "src-aaa")
		if err != nil {
			t.Fatalf("resolveTwinID() error = %v", err)
		}
		defer cleanup()
		if got != "src-aaa" {
			t.Errorf("resolveTwinID() = %q, want %q", got, "src-aaa")
		}
	})

	t.Run("NotFound_ReturnsError", func(t *testing.T) {
		// ID exists nowhere. nil fromStore so the fromStore branch is skipped
		// up front and resolution traverses localStore → prefix routing →
		// auto-routing → final error. Without routes.jsonl or contributor
		// mode wired up, the two fallbacks return their own errors and we
		// reach the not-found return statement.
		_, cleanup, err := resolveTwinID(ctx, localStore, nil, "src-zzz999")
		defer cleanup()
		if err == nil {
			t.Fatal("resolveTwinID() expected error for missing ID, got nil")
		}
		if !strings.Contains(err.Error(), "no issue found") {
			t.Errorf("resolveTwinID() error = %v, want it to mention 'no issue found'", err)
		}
	})
}
