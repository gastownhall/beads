package dolt

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func revOf(t *testing.T, ctx context.Context, store *DoltStore, id string) int64 {
	t.Helper()
	iss, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return iss.Revision
}

// TestUpdateIssueIfMatch covers the whole-row CAS happy path, the stale-token
// mismatch (typed conflict, row untouched), the nil-token unconditional path,
// and the guarded delete.
func TestUpdateIssueIfMatch(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	makeCASIssue(t, ctx, store, "cas-u")

	r0 := revOf(t, ctx, store, "cas-u")

	// nil expected == unconditional (last-writer-wins); still bumps revision.
	iss, err := store.UpdateIssueIfMatch(ctx, "cas-u", map[string]interface{}{"title": "a"}, nil, "tester")
	if err != nil {
		t.Fatalf("unconditional update: %v", err)
	}
	if iss.Title != "a" || iss.Revision == r0 {
		t.Fatalf("unconditional update: title=%q revision=%d (r0=%d)", iss.Title, iss.Revision, r0)
	}
	r1 := iss.Revision

	// Correct expected revision succeeds and moves the token.
	iss, err = store.UpdateIssueIfMatch(ctx, "cas-u", map[string]interface{}{"title": "b"}, &r1, "tester")
	if err != nil {
		t.Fatalf("matched update: %v", err)
	}
	if iss.Title != "b" || iss.Revision == r1 {
		t.Fatalf("matched update: title=%q revision=%d (r1=%d)", iss.Title, iss.Revision, r1)
	}
	r2 := iss.Revision

	// Stale expected revision fails with a typed conflict and does NOT mutate.
	stale := r1
	_, err = store.UpdateIssueIfMatch(ctx, "cas-u", map[string]interface{}{"title": "c"}, &stale, "tester")
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("stale update: want ErrPreconditionFailed, got %v", err)
	}
	var pfe *storage.PreconditionFailedError
	if !errors.As(err, &pfe) {
		t.Fatalf("stale update: want *PreconditionFailedError, got %T", err)
	}
	if pfe.ExpectedRevision != r1 || pfe.CurrentRevision != r2 {
		t.Fatalf("conflict revisions: expected=%d current=%d, want expected=%d current=%d",
			pfe.ExpectedRevision, pfe.CurrentRevision, r1, r2)
	}
	if pfe.CurrentIssue == nil || pfe.CurrentIssue.Title != "b" {
		t.Fatalf("conflict CurrentIssue must reflect the winner's state (title=b), got %+v", pfe.CurrentIssue)
	}
	if cur := revOf(t, ctx, store, "cas-u"); cur != r2 {
		t.Fatalf("stale update clobbered the row: revision now %d, want %d", cur, r2)
	}

	// Guarded delete: stale fails (row survives), correct succeeds.
	if err := store.DeleteIssueIfMatch(ctx, "cas-u", &stale); !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("stale delete: want ErrPreconditionFailed, got %v", err)
	}
	if _, err := store.GetIssue(ctx, "cas-u"); err != nil {
		t.Fatalf("issue must survive a failed guarded delete: %v", err)
	}
	if err := store.DeleteIssueIfMatch(ctx, "cas-u", &r2); err != nil {
		t.Fatalf("matched delete: %v", err)
	}
	if _, err := store.GetIssue(ctx, "cas-u"); err == nil {
		t.Fatal("issue should be gone after a matched guarded delete")
	}
}

// TestUpdateIssueIfMatchDisjointColumnRace is the Q1 proof: N concurrent writers
// guard on the SAME revision but write DIFFERENT columns. A naive per-bead counter
// would let Dolt cell-merge them all (each bumps rev base->base+1, cells converge,
// every write lands). The random nonce forces a revision-cell conflict so exactly
// one wins. Verified on issues AND wisps.
func TestUpdateIssueIfMatchDisjointColumnRace(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("issues", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "cas-race-i")
		runDisjointRace(t, ctx, store, "cas-race-i")
	})

	t.Run("wisps", func(t *testing.T) {
		wisp := &types.Issue{
			ID: "cas-race-w", Title: "wisp race", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2, Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
			t.Fatalf("create wisp: %v", err)
		}
		runDisjointRace(t, ctx, store, "cas-race-w")
	})
}

func runDisjointRace(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	base := revOf(t, ctx, store, id)
	fields := []string{"title", "description", "design", "notes", "acceptance_criteria"}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts, others := 0, 0, 0

	for i := range fields {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exp := base // every writer guards on the same observed revision
			_, err := store.UpdateIssueIfMatch(ctx, id,
				map[string]interface{}{fields[i]: "w" + strconv.Itoa(i)}, &exp, "tester")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, storage.ErrPreconditionFailed):
				conflicts++
			default:
				others++
				t.Errorf("racer %d: unexpected non-CAS error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%s: wins = %d, want exactly 1 (split-brain / lost update if >1)", id, wins)
	}
	if conflicts != len(fields)-1 {
		t.Fatalf("%s: conflicts = %d, want %d", id, conflicts, len(fields)-1)
	}
	if others != 0 {
		t.Fatalf("%s: unexpected non-CAS errors = %d", id, others)
	}
	if after := revOf(t, ctx, store, id); after == base {
		t.Fatalf("%s: revision unchanged after a winning CAS", id)
	}
}
