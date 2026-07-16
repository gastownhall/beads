package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloseIssueIfMatch covers the guarded-close CAS: a stale token leaves the
// issue OPEN and returns a typed conflict; the matching token closes it and moves
// the revision; a stale guard on an already-closed issue still fails; and a
// matching guard on an already-closed issue is an idempotent success.
func TestCloseIssueIfMatch(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	statusOf := func(id string) types.Status {
		t.Helper()
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return iss.Status
	}

	makeCASIssue(t, ctx, store, "cas-c")
	r0 := revOf(t, ctx, store, "cas-c")

	// Stale token: the close is refused and the issue stays open.
	stale := r0 - 1
	err := store.CloseIssueIfMatch(ctx, "cas-c", "done", "tester", "", &stale)
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("stale close: want ErrPreconditionFailed, got %v", err)
	}
	var pfe *storage.PreconditionFailedError
	if !errors.As(err, &pfe) || pfe.ExpectedRevision != stale || pfe.CurrentRevision != r0 {
		t.Fatalf("stale close conflict: got %+v (want expected=%d current=%d)", pfe, stale, r0)
	}
	if s := statusOf("cas-c"); s != types.StatusOpen {
		t.Fatalf("issue must stay open after a refused guarded close, got %q", s)
	}

	// Matching token: closes and moves the revision.
	if err := store.CloseIssueIfMatch(ctx, "cas-c", "done", "tester", "", &r0); err != nil {
		t.Fatalf("matched close: %v", err)
	}
	if s := statusOf("cas-c"); s != types.StatusClosed {
		t.Fatalf("status after matched close = %q, want closed", s)
	}
	r1 := revOf(t, ctx, store, "cas-c")
	if r1 == r0 {
		t.Fatalf("close did not move the revision (still %d)", r0)
	}

	// A stale guard on the now-closed issue is still a precondition failure — the
	// row changed under the caller.
	if err := store.CloseIssueIfMatch(ctx, "cas-c", "again", "tester", "", &r0); !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("stale close on closed issue: want ErrPreconditionFailed, got %v", err)
	}

	// A matching guard on the already-closed issue is an idempotent success.
	if err := store.CloseIssueIfMatch(ctx, "cas-c", "again", "tester", "", &r1); err != nil {
		t.Fatalf("idempotent close at current revision must succeed, got %v", err)
	}

	// nil token == unconditional close (matches CloseIssue).
	makeCASIssue(t, ctx, store, "cas-c2")
	if err := store.CloseIssueIfMatch(ctx, "cas-c2", "done", "tester", "", nil); err != nil {
		t.Fatalf("unconditional close: %v", err)
	}
	if s := statusOf("cas-c2"); s != types.StatusClosed {
		t.Fatalf("unconditional close status = %q, want closed", s)
	}
}
