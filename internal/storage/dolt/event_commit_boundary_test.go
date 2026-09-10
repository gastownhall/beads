package dolt

import (
	"errors"
	"testing"
)

func TestAddCommentSQLCommitResponseLossAccountsCircuitOnce(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	driver := &claimCommitBoundaryDriver{sqlCommitErr: testConnectionLoss}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.AddComment(t.Context(), "comment-boundary", "alice", "hello")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("AddComment() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("AddComment() error = %v, want cause %v", err, testConnectionLoss)
	}

	driver.mu.Lock()
	if driver.eventInserts != 1 || driver.txAttempts != 1 || driver.txCommits != 1 {
		driver.mu.Unlock()
		t.Fatalf("AddComment attempts = events:%d transactions:%d commits:%d, want 1, 1, 1",
			driver.eventInserts, driver.txAttempts, driver.txCommits)
	}
	driver.mu.Unlock()

	state := breaker.readState()
	if state.State != circuitClosed || state.Failures != 1 {
		t.Fatalf("circuit state after one lost response = %+v, want closed with one failure", state)
	}
}

func TestAddCommentRejectsWriteAfterCircuitOpens(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold {
		breaker.RecordFailure()
	}
	driver := &claimCommitBoundaryDriver{}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	err := store.AddComment(t.Context(), "comment-boundary", "alice", "hello")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("AddComment() error = %v, want ErrCircuitOpen", err)
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.txAttempts != 0 || driver.eventInserts != 0 || driver.doltCommits != 0 {
		t.Fatalf("writes after open circuit = transactions:%d events:%d Dolt commits:%d, want all zero",
			driver.txAttempts, driver.eventInserts, driver.doltCommits)
	}
}

func TestAddCommentTerminalSuccessResetsCircuitFailures(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	driver := &claimCommitBoundaryDriver{}
	store := newClaimCommitBoundaryStore(driver)
	store.breaker = breaker
	t.Cleanup(func() { _ = store.db.Close() })

	if err := store.AddComment(t.Context(), "comment-boundary", "alice", "hello"); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	breaker.RecordFailure()
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after successful comment and one failure = %q, want %q", state, circuitClosed)
	}
}
