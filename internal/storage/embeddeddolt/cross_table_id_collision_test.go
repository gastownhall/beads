//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateRejectsCrossTableIDCollision is the regression test for GH#4455:
// `bd create` must never put the same ID in both the issues and wisps tables.
// Issues and wisps share one ID space but live in separate tables, so a dual
// presence row makes merge-based lookups (bd ready/search) hard-error for the
// whole store. The fix rejects a create whose ID already exists in the sibling
// table; #4163 only made the lookups tolerant of an already-corrupted store.
func TestCreateRejectsCrossTableIDCollision(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()

	// Direction 1: permanent issue first, then a wisp with the same ID.
	t.Run("issue then wisp", func(t *testing.T) {
		te := newTestEnv(t, "ct")
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "ct-aaa", Title: "perm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create permanent issue: %v", err)
		}
		err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "ct-aaa", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester")
		if err == nil {
			t.Fatal("expected cross-table collision to be rejected, got nil error")
		}
		if !strings.Contains(err.Error(), "already exists in the issues table") {
			t.Fatalf("unexpected error: %v", err)
		}
		// The wisp must not have been written.
		var wispCount int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{"ct-aaa"}, &wispCount)
		if wispCount != 0 {
			t.Fatalf("colliding wisp was written: wisps rows = %d, want 0", wispCount)
		}
	})

	// Direction 2: wisp first, then a permanent issue with the same ID.
	t.Run("wisp then issue", func(t *testing.T) {
		te := newTestEnv(t, "ct")
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "ct-bbb", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("create wisp: %v", err)
		}
		err := te.store.CreateIssue(ctx, &types.Issue{
			ID: "ct-bbb", Title: "perm", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester")
		if err == nil {
			t.Fatal("expected cross-table collision to be rejected, got nil error")
		}
		if !strings.Contains(err.Error(), "already exists in the wisps table") {
			t.Fatalf("unexpected error: %v", err)
		}
		var issueCount int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{"ct-bbb"}, &issueCount)
		if issueCount != 0 {
			t.Fatalf("colliding issue was written: issues rows = %d, want 0", issueCount)
		}
	})

	// Non-regression: re-creating an ID in its own table still upserts.
	t.Run("same table recreate still allowed", func(t *testing.T) {
		te := newTestEnv(t, "ct")
		mk := func(title string) error {
			return te.store.CreateIssue(ctx, &types.Issue{
				ID: "ct-ccc", Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			}, "tester")
		}
		if err := mk("first"); err != nil {
			t.Fatalf("first create: %v", err)
		}
		if err := mk("second"); err != nil {
			t.Fatalf("same-table recreate must still succeed, got: %v", err)
		}
	})
}

// TestPromoteStillWorksWithCollisionGuard locks in the carve-out: promotion
// inserts into issues while the wisp row still exists (PromoteFromEphemeralInTx),
// then deletes the wisp. It calls InsertIssueIfNew directly and bypasses the
// create-path collision guard, so promotion must remain unaffected.
func TestPromoteStillWorksWithCollisionGuard(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	ctx := t.Context()
	te := newTestEnv(t, "ct")

	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "ct-eph", Title: "ephemeral", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "tester"); err != nil {
		t.Fatalf("create ephemeral wisp: %v", err)
	}
	if err := te.store.PromoteFromEphemeral(ctx, "ct-eph", "tester"); err != nil {
		t.Fatalf("promote must succeed despite collision guard: %v", err)
	}

	var issueCount, wispCount int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{"ct-eph"}, &issueCount)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{"ct-eph"}, &wispCount)
	if issueCount != 1 || wispCount != 0 {
		t.Fatalf("after promote: issues=%d wisps=%d, want issues=1 wisps=0", issueCount, wispCount)
	}
}
