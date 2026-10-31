package dolt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// verifyProjectIdentity used to conflate "the database has no _project_id"
// (a legitimate old-style database — still skips) with "the metadata read
// failed" (a failure to read, which silently granted the connection). These
// tests pin the split: a read error that survives bounded retry refuses the
// open with a named error, an empty _project_id keeps skipping (covered
// end-to-end by TestCrossProject_IdentityCheck_SoftSkip_EmptyDBProjectID),
// and the bounded retry itself really retries before anything surfaces.

func TestWithRetryTransientReadErrorRetriesThenSucceeds(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	store := &DoltStore{}

	calls := 0
	err := store.withRetry(context.Background(), func() error {
		calls++
		if calls == 1 {
			// The warming-window class: a server restart refuses
			// connections until it comes back (isRetryableError).
			return errors.New("dial tcp: connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRetry() error = %v, want nil (transient error must be retried, not surfaced)", err)
	}
	if calls != 2 {
		t.Fatalf("withRetry() calls = %d, want 2 (the retry must actually retry)", calls)
	}
}

func TestWithRetryPersistentReadErrorRefusesAfterBoundedRetry(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	store := &DoltStore{}

	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	err := store.withRetry(ctx, func() error {
		calls++
		return errors.New("dial tcp: connection refused")
	})
	if err == nil {
		t.Fatal("withRetry() error = nil, want refusal after the retry window")
	}
	if calls < 2 {
		t.Fatalf("withRetry() calls = %d, want >= 2 (the refusal must come only AFTER the bounded retry ran)", calls)
	}
}

// TestCrossProject_IdentityCheck_ReadErrorRefuses covers the end-to-end
// refusal: a database whose metadata table is unreadable (here: dropped,
// simulating a foreign/unfinished database and the read failures of a dolt
// restart window) must be refused at open with a named error, not silently
// adopted the way the pre-split soft-skip adopted it.
func TestCrossProject_IdentityCheck_ReadErrorRefuses(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := testContext(t)
	defer cancel()

	f := newIdentityTestFixture(t, "readerr")

	ownerStore, err := openStoreWithOwnBudget(t, f.config())
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	if err := ownerStore.SetMetadata(ctx, "_project_id", "77777777-7777-7777-7777-777777777777"); err != nil {
		ownerStore.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	// Make the database's identity UNREADABLE rather than empty: the read
	// itself now fails (Error 1146, non-retryable), which is what survives
	// to the verifier after bounded retry.
	if _, err := ownerStore.db.ExecContext(ctx, "DROP TABLE metadata"); err != nil {
		ownerStore.Close()
		t.Fatalf("drop metadata table: %v", err)
	}
	ownerStore.Close()

	f.saveLocalProjectID(t, "77777777-7777-7777-7777-777777777777")
	reopenStore, err := openStoreWithOwnBudget(t, f.config())
	if err == nil {
		reopenStore.Close()
		t.Fatal("expected open to refuse when the project identity cannot be read, got nil error")
	}
	if !strings.Contains(err.Error(), "cannot verify project identity") {
		t.Fatalf("expected a 'cannot verify project identity' refusal, got: %v", err)
	}
}
