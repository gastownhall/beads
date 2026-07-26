package dolt

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Tests for claim-family verify-after-write (bd-zccb9, wyvern incident
// wy-ejph3): under a degraded server the write's exit status is not truth in
// either direction, so claim outcomes are resolved against the database by
// re-read. Failure injection happens at the write-func seam — the write funcs
// here lie in exactly the ways the incident documented.

func claimVerifyTestIssue(t *testing.T, s *DoltStore) string {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	issue := &types.Issue{
		Title:     "claim verify target",
		IssueType: types.TypeTask,
		Priority:  2,
		Status:    types.StatusOpen,
	}
	if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return issue.ID
}

// rawClaim performs the real claim write without the verify wrapper — the
// inner body of ClaimIssue — so tests can control exactly which attempt lands.
func rawClaim(t *testing.T, s *DoltStore, id, actor string) error {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		_, err := issueops.ClaimIssueInTx(ctx, tx, id, actor)
		return err
	})
}

// TestVerifiedClaimWriteConvertsAppliedIndeterminate: the wy-x543k direction —
// the write actually landed but the connection died during commit and bd
// printed an error. Verify-by-re-read must convert it into an accurate success.
func TestVerifiedClaimWriteConvertsAppliedIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)
	if err := rawClaim(t, s, id, "alice"); err != nil {
		t.Fatalf("raw claim: %v", err)
	}

	calls := 0
	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		calls++
		return fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", errCommitPhase)
	})
	if err != nil {
		t.Fatalf("expected applied-indeterminate to resolve to success, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("write must not be replayed when the re-read shows it applied; ran %d times", calls)
	}
}

// TestVerifiedClaimWriteReplaysVerifiedRollback: the wy-ejph3 direction — the
// connection died during commit AND the transaction rolled back. The re-read
// proves nothing landed, so one replay is safe and must proceed.
func TestVerifiedClaimWriteReplaysVerifiedRollback(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	calls := 0
	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		calls++
		if calls == 1 {
			// First attempt: commit-phase connection loss, nothing landed.
			return fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", errCommitPhase)
		}
		return rawClaim(t, s, id, "alice")
	})
	if err != nil {
		t.Fatalf("expected verified-rollback replay to succeed, got: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly one replay (2 write runs), got %d", calls)
	}

	assignee, status, err := s.readClaimState(ctx, id)
	if err != nil {
		t.Fatalf("read claim state: %v", err)
	}
	if assignee != "alice" || status != types.StatusInProgress {
		t.Fatalf("replayed claim not in effect: assignee=%q status=%q", assignee, status)
	}
}

// TestVerifiedClaimWriteFailsLoudlyOnLostWrite: the false-success direction —
// the write reported success but the claim is not in the database. The caller
// must get a loud non-nil error, never a silent phantom claim.
func TestVerifiedClaimWriteFailsLoudlyOnLostWrite(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		return nil // lie: report success without writing anything
	})
	if err == nil {
		t.Fatal("expected loud failure for a success-reported-but-lost claim, got nil")
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Fatalf("error should say the claim did not land, got: %v", err)
	}
}

// TestVerifiedClaimWriteIndeterminateStaysIndeterminateTwice: a replay that
// itself dies at commit phase with nothing landed must not loop forever — the
// one-replay budget exhausts and the caller gets the verified-rollback error.
func TestVerifiedClaimWriteIndeterminateStaysIndeterminateTwice(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	calls := 0
	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		calls++
		return fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", errCommitPhase)
	})
	if err == nil {
		t.Fatal("expected failure after replay budget exhausted, got nil")
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 write runs (original + one replay), got %d", calls)
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Fatalf("error should report the verified rollback, got: %v", err)
	}
}

// TestVerifiedReadyClaimReplayConvertsAppliedIndeterminate: the replay leg of
// the bespoke ready-claim path must keep the wrapper's verify semantics (lion
// review on PR #5006). First attempt: commit-phase loss, verified rolled back
// (nothing landed) -> replay. The replay actually lands the claim but ALSO
// reports commit-phase loss (the wy-x543k direction, now on attempt two). The
// verify pass must convert that into an accurate success instead of returning
// the raw indeterminate error — which would leave an applied claim orphaned
// on an issue the caller was told it failed to claim.
func TestVerifiedReadyClaimReplayConvertsAppliedIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	calls := 0
	got, err := s.verifiedReadyClaim(ctx, "alice", func() (*types.Issue, error) {
		calls++
		if calls == 1 {
			// Commit-phase loss, transaction rolled back: nothing landed.
			return &types.Issue{ID: id}, fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", errCommitPhase)
		}
		// Replay: the claim lands, but the connection dies during commit again.
		if err := rawClaim(t, s, id, "alice"); err != nil {
			return nil, err
		}
		return &types.Issue{ID: id}, fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", errCommitPhase)
	})
	if err != nil {
		t.Fatalf("expected applied-indeterminate replay to resolve to success, got: %v", err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("recovered success must return the claimed issue %s, got %+v", id, got)
	}
	if calls != 2 {
		t.Fatalf("expected exactly one replay (2 write runs), got %d", calls)
	}

	assignee, status, verr := s.readClaimState(ctx, id)
	if verr != nil {
		t.Fatalf("read claim state: %v", verr)
	}
	if assignee != "alice" || status != types.StatusInProgress {
		t.Fatalf("claim not in effect after recovered replay: assignee=%q status=%q", assignee, status)
	}
}

// TestClaimUnclaimVerifiedEndToEnd: the public paths still work with the
// verify layer in place — a healthy claim and unclaim pass their
// postconditions and leave the expected states.
func TestClaimUnclaimVerifiedEndToEnd(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	if err := s.ClaimIssue(ctx, id, "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	assignee, status, err := s.readClaimState(ctx, id)
	if err != nil {
		t.Fatalf("read claim state: %v", err)
	}
	if assignee != "alice" || status != types.StatusInProgress {
		t.Fatalf("post-claim state: assignee=%q status=%q", assignee, status)
	}

	// Idempotent re-claim by the same actor still succeeds through verify.
	if err := s.ClaimIssue(ctx, id, "alice"); err != nil {
		t.Fatalf("idempotent re-claim: %v", err)
	}

	if err := s.UnclaimIssue(ctx, id, "alice", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	assignee, status, err = s.readClaimState(ctx, id)
	if err != nil {
		t.Fatalf("read claim state: %v", err)
	}
	if assignee != "" || status != types.StatusOpen {
		t.Fatalf("post-unclaim state: assignee=%q status=%q", assignee, status)
	}
}
