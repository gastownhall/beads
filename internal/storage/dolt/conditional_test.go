package dolt

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func strptr(s string) *string { return &s }

// makeCASIssue creates a fresh open issue for CAS tests and returns its ID.
func makeCASIssue(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:          id,
		Title:       "cas test " + id,
		Description: "",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue %s: %v", id, err)
	}
}

func TestCompareAndSetMetadataKey(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const key = "gc.control_epoch"

	t.Run("claim-once on absent key succeeds, then conflicts", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "cas-claim")
		// First claim-once (expected nil == absent) succeeds.
		got, err := store.CompareAndSetMetadataKey(ctx, "cas-claim", key, nil, "owner-A", "tester")
		if err != nil {
			t.Fatalf("first claim-once: %v", err)
		}
		if v, _ := store.SlotGet(ctx, "cas-claim", key); v != "owner-A" {
			t.Fatalf("after claim, value = %q want owner-A", v)
		}
		if got == nil {
			t.Fatal("expected post-write issue, got nil")
		}
		// Second claim-once on the now-present key must fail (someone holds it).
		_, err = store.CompareAndSetMetadataKey(ctx, "cas-claim", key, nil, "owner-B", "tester")
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("second claim-once: want ErrPreconditionFailed, got %v", err)
		}
		var pfe *storage.PreconditionFailedError
		if !errors.As(err, &pfe) {
			t.Fatalf("want *PreconditionFailedError, got %T", err)
		}
		if pfe.CurrentValue == nil || *pfe.CurrentValue != "owner-A" {
			t.Fatalf("conflict CurrentValue = %v want owner-A", pfe.CurrentValue)
		}
		if pfe.ExpectedValue != nil {
			t.Fatalf("claim-once conflict ExpectedValue should be nil, got %v", pfe.ExpectedValue)
		}
		// The losing write must not have changed the value.
		if v, _ := store.SlotGet(ctx, "cas-claim", key); v != "owner-A" {
			t.Fatalf("value clobbered by losing claim: %q", v)
		}
	})

	t.Run("expected-value match and mismatch", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "cas-expect")
		if _, err := store.CompareAndSetMetadataKey(ctx, "cas-expect", key, nil, "4", "tester"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// Wrong expected -> conflict, value unchanged.
		_, err := store.CompareAndSetMetadataKey(ctx, "cas-expect", key, strptr("99"), "5", "tester")
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("wrong expected: want ErrPreconditionFailed, got %v", err)
		}
		if v, _ := store.SlotGet(ctx, "cas-expect", key); v != "4" {
			t.Fatalf("value changed on failed CAS: %q", v)
		}
		// Correct expected -> success.
		if _, err := store.CompareAndSetMetadataKey(ctx, "cas-expect", key, strptr("4"), "5", "tester"); err != nil {
			t.Fatalf("correct expected: %v", err)
		}
		if v, _ := store.SlotGet(ctx, "cas-expect", key); v != "5" {
			t.Fatalf("value = %q want 5", v)
		}
	})

	t.Run("sibling keys untouched", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "cas-sibling")
		if err := store.SlotSet(ctx, "cas-sibling", "other.key", "keep-me", "tester"); err != nil {
			t.Fatalf("seed sibling: %v", err)
		}
		if _, err := store.CompareAndSetMetadataKey(ctx, "cas-sibling", key, nil, "1", "tester"); err != nil {
			t.Fatalf("cas: %v", err)
		}
		if v, _ := store.SlotGet(ctx, "cas-sibling", "other.key"); v != "keep-me" {
			t.Fatalf("sibling key clobbered: %q", v)
		}
	})

	t.Run("dotted keys reference flat members", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "cas-dotted")
		// A key containing dots must be treated as one flat member, not a nested path.
		if _, err := store.CompareAndSetMetadataKey(ctx, "cas-dotted", "a.b.c", nil, "flat", "tester"); err != nil {
			t.Fatalf("dotted cas: %v", err)
		}
		if v, _ := store.SlotGet(ctx, "cas-dotted", "a.b.c"); v != "flat" {
			t.Fatalf("dotted key value = %q want flat", v)
		}
	})

	t.Run("missing bead is ErrNotFound", func(t *testing.T) {
		_, err := store.CompareAndSetMetadataKey(ctx, "cas-nonexistent", key, nil, "x", "tester")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
}

func TestCompareAndClearMetadataKey(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	const key = "gc.drain.reserved_by"

	t.Run("full reserve/release lifecycle", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "rel-life")
		// reserve
		if _, err := store.CompareAndSetMetadataKey(ctx, "rel-life", key, nil, "me", "tester"); err != nil {
			t.Fatalf("reserve: %v", err)
		}
		// release (holder matches)
		if _, err := store.CompareAndClearMetadataKey(ctx, "rel-life", key, "me", "tester"); err != nil {
			t.Fatalf("release: %v", err)
		}
		if _, err := store.SlotGet(ctx, "rel-life", key); err == nil {
			t.Fatal("key should be absent after release")
		}
		// re-reserve after release (reusable)
		if _, err := store.CompareAndSetMetadataKey(ctx, "rel-life", key, nil, "you", "tester"); err != nil {
			t.Fatalf("re-reserve after release: %v", err)
		}
		if v, _ := store.SlotGet(ctx, "rel-life", key); v != "you" {
			t.Fatalf("after re-reserve value = %q want you", v)
		}
	})

	t.Run("release held by a different value conflicts", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "rel-other")
		if _, err := store.CompareAndSetMetadataKey(ctx, "rel-other", key, nil, "owner", "tester"); err != nil {
			t.Fatalf("reserve: %v", err)
		}
		_, err := store.CompareAndClearMetadataKey(ctx, "rel-other", key, "not-owner", "tester")
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Fatalf("release by wrong holder: want ErrPreconditionFailed, got %v", err)
		}
		if v, _ := store.SlotGet(ctx, "rel-other", key); v != "owner" {
			t.Fatalf("reservation cleared by wrong holder: %q", v)
		}
	})

	t.Run("release of already-absent key is idempotent success", func(t *testing.T) {
		makeCASIssue(t, ctx, store, "rel-absent")
		if _, err := store.CompareAndClearMetadataKey(ctx, "rel-absent", key, "me", "tester"); err != nil {
			t.Fatalf("release of absent key should be idempotent success, got %v", err)
		}
	})
}

// TestCompareAndSetMetadataKeyConcurrent is the load-bearing conformance test:
// N goroutines race to claim-once the same key; exactly one must win and the
// rest must get a typed precondition failure (not a raw serialization error, not
// a double claim).
func TestCompareAndSetMetadataKeyConcurrent(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx := context.Background()
	makeCASIssue(t, ctx, store, "cas-race")

	const n = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts, others := 0, 0, 0
	var winner string

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := "owner-" + string(rune('A'+i))
			_, err := store.CompareAndSetMetadataKey(ctx, "cas-race", "gc.drain.reserved_by", nil, owner, "tester")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				winner = owner
			case errors.Is(err, storage.ErrPreconditionFailed):
				conflicts++
			default:
				others++
				t.Errorf("unexpected error from racer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (split-brain if >1)", wins)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}
	if others != 0 {
		t.Fatalf("unexpected non-CAS errors = %d", others)
	}
	if v, _ := store.SlotGet(ctx, "cas-race", "gc.drain.reserved_by"); v != winner {
		t.Fatalf("final value %q != winner %q", v, winner)
	}
}
