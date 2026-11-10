package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestPublicDirectWritesRejectOpenCircuitBeforeTransaction(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*DoltStore) error
	}{
		{name: "update explicit-id wisp", run: func(store *DoltStore) error {
			return store.UpdateIssue(t.Context(), "explicit-wisp", map[string]interface{}{"title": "changed"}, "alice")
		}},
		{name: "checked update explicit-id wisp", run: func(store *DoltStore) error {
			return store.UpdateIssueChecked(t.Context(), "explicit-wisp", map[string]interface{}{"title": "changed"}, "alice", storage.UpdateIssueOptions{})
		}},
		{name: "claim explicit-id wisp", run: func(store *DoltStore) error {
			return store.ClaimIssue(t.Context(), "explicit-wisp", "alice")
		}},
		{name: "close explicit-id wisp", run: func(store *DoltStore) error {
			return store.CloseIssue(t.Context(), "explicit-wisp", "done", "alice", "session")
		}},
		{name: "checked close explicit-id wisp", run: func(store *DoltStore) error {
			_, err := store.CloseIssueChecked(t.Context(), "explicit-wisp", "alice", storage.CloseIssueOptions{Reason: "done"})
			return err
		}},
		{name: "delete explicit-id wisp", run: func(store *DoltStore) error {
			return store.DeleteIssue(t.Context(), "explicit-wisp")
		}},
		{name: "delete issues", run: func(store *DoltStore) error {
			_, err := store.DeleteIssues(t.Context(), []string{"explicit-wisp"}, false, true, false)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "")
			breaker := newTestCircuitBreaker(t)
			for range circuitFailureThreshold {
				breaker.RecordFailure()
			}
			driver := &claimCommitBoundaryDriver{activeWisp: true}
			store := newClaimCommitBoundaryStore(driver)
			store.breaker = breaker
			t.Cleanup(func() { _ = store.db.Close() })

			err := tc.run(store)
			if !errors.Is(err, ErrCircuitOpen) {
				t.Fatalf("operation error = %v, want ErrCircuitOpen", err)
			}
			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.txAttempts != 0 {
				t.Fatalf("transactions started after circuit opened = %d, want 0", driver.txAttempts)
			}
		})
	}
}

func TestExplicitIDWispUpdateTerminalSuccessResetsCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &claimCommitBoundaryDriver{activeWisp: true}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	if err := store.UpdateIssue(t.Context(), "explicit-wisp", map[string]interface{}{}, "alice"); err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	breaker.RecordFailure()
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after successful wisp update and one failure = %q, want %q", state, circuitClosed)
	}
}

func TestExplicitIDWispUpdateIndeterminateCommitTripsCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &claimCommitBoundaryDriver{activeWisp: true, sqlCommitErr: testConnectionLoss}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.UpdateIssue(t.Context(), "explicit-wisp", map[string]interface{}{}, "alice")
	if !errors.Is(err, ErrCommitIndeterminate) || !errors.Is(err, testConnectionLoss) {
		t.Fatalf("UpdateIssue() error = %v, want indeterminate connection loss", err)
	}
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after indeterminate wisp commit = %q, want %q", state, circuitOpen)
	}

	driver.mu.Lock()
	before := driver.txAttempts
	driver.mu.Unlock()
	err = store.UpdateIssue(t.Context(), "explicit-wisp", map[string]interface{}{}, "alice")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("next UpdateIssue() error = %v, want ErrCircuitOpen", err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.txAttempts != before {
		t.Fatalf("transactions after circuit trip = %d, want unchanged %d", driver.txAttempts, before)
	}
}

func TestDeleteIssueIndeterminateCommitTripsCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &claimCommitBoundaryDriver{sqlCommitErr: testConnectionLoss}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.DeleteIssue(t.Context(), "delete-boundary")
	if !errors.Is(err, ErrCommitIndeterminate) || !errors.Is(err, testConnectionLoss) {
		t.Fatalf("DeleteIssue() error = %v, want indeterminate connection loss", err)
	}
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after indeterminate delete commit = %q, want %q", state, circuitOpen)
	}

	driver.mu.Lock()
	before := driver.txAttempts
	driver.mu.Unlock()
	err = store.DeleteIssue(t.Context(), "delete-boundary")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("next DeleteIssue() error = %v, want ErrCircuitOpen", err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.txAttempts != before {
		t.Fatalf("transactions after circuit trip = %d, want unchanged %d", driver.txAttempts, before)
	}
}

func TestPublicPullsRejectOpenCircuitBeforeRouting(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*DoltStore) error
	}{
		{name: "default remote", run: func(store *DoltStore) error { return store.Pull(t.Context()) }},
		{name: "named remote", run: func(store *DoltStore) error { return store.PullRemote(t.Context(), "origin") }},
		{name: "peer remote", run: func(store *DoltStore) error {
			_, err := store.PullFrom(t.Context(), "peer")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "")
			breaker := newTestCircuitBreaker(t)
			for range circuitFailureThreshold {
				breaker.RecordFailure()
			}
			driver := &claimCommitBoundaryDriver{}
			store := newClaimCommitBoundaryStore(driver)
			store.breaker = breaker
			store.readOnly = true
			t.Cleanup(func() { _ = store.db.Close() })

			err := tc.run(store)
			if !errors.Is(err, ErrCircuitOpen) {
				t.Fatalf("pull error = %v, want ErrCircuitOpen", err)
			}
			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.txAttempts != 0 {
				t.Fatalf("transactions started after circuit opened = %d, want 0", driver.txAttempts)
			}
		})
	}
}
