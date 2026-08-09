package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

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

func TestVerifiedClaimWriteDoesNotReplayDefiniteMySQLError(t *testing.T) {
	cause := &mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"}
	driver := &failureDriver{commitErrs: []error{cause}}
	store := newFailureStore(driver)
	store.serverMode = true
	defer func() { _ = store.db.Close() }()

	calls := 0
	err := store.verifiedClaimWrite(context.Background(), "claim-target", claimedBy("alice"), func() error {
		calls++
		return store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil })
	})
	if !errors.Is(err, cause) {
		t.Fatalf("verifiedClaimWrite() error = %v, want %v", err, cause)
	}
	if calls != 1 {
		t.Fatalf("definite MySQL response entered claim replay: calls = %d, want 1", calls)
	}
	if got := driver.prepares.Load(); got != 1 {
		t.Fatalf("definite MySQL response entered claim verification: prepares = %d, want only the initial wisp lookup", got)
	}
}

func TestVerifiedReadyClaimDoesNotReplayDefiniteMySQLError(t *testing.T) {
	cause := &mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"}
	driver := &failureDriver{commitErrs: []error{cause}}
	store := newFailureStore(driver)
	store.serverMode = true
	defer func() { _ = store.db.Close() }()

	calls := 0
	claimed, err := store.verifiedReadyClaim(context.Background(), "alice", func() (*types.Issue, error) {
		calls++
		err := store.withRetryTx(context.Background(), func(*sql.Tx) error { return nil })
		return &types.Issue{ID: "ready-target"}, err
	})
	if !errors.Is(err, cause) {
		t.Fatalf("verifiedReadyClaim() error = %v, want %v", err, cause)
	}
	if claimed != nil {
		t.Fatalf("verifiedReadyClaim() issue = %+v, want nil on definite error", claimed)
	}
	if calls != 1 {
		t.Fatalf("definite MySQL response entered ready-claim replay: calls = %d, want 1", calls)
	}
	if got := driver.prepares.Load(); got != 0 {
		t.Fatalf("definite MySQL response entered ready-claim verification: prepares = %d, want 0", got)
	}
}

// TestVerifiedClaimWriteRetainsAppliedIndeterminate proves matching issue
// fields alone do not prove the lease and actor-attributed event also landed.
func TestVerifiedClaimWriteRetainsAppliedIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	s.serverMode = true

	id := claimVerifyTestIssue(t, s)
	if err := rawClaim(t, s, id, "alice"); err != nil {
		t.Fatalf("raw claim: %v", err)
	}

	calls := 0
	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		calls++
		return fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", ErrCommitIndeterminate)
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("applied-indeterminate error = %v, want ErrCommitIndeterminate", err)
	}
	if calls != 1 {
		t.Fatalf("write must not be replayed when the re-read shows it applied; ran %d times", calls)
	}
}

// TestVerifiedClaimWriteDoesNotReplayIndeterminate pins the public no-replay
// contract when the verified postcondition does not match.
func TestVerifiedClaimWriteDoesNotReplayIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	calls := 0
	indeterminate := fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", ErrCommitIndeterminate)
	err := s.verifiedClaimWrite(ctx, id, claimedBy("alice"), func() error {
		calls++
		return indeterminate
	})
	if err != indeterminate {
		t.Fatalf("error = %v, want original indeterminate error %v", err, indeterminate)
	}
	if calls != 1 {
		t.Fatalf("write runs = %d, want exactly one after indeterminate commit", calls)
	}

	assignee, status, err := s.readClaimState(ctx, id)
	if err != nil {
		t.Fatalf("read claim state: %v", err)
	}
	if assignee != "" || status != types.StatusOpen {
		t.Fatalf("claim was replayed: assignee=%q status=%q", assignee, status)
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

// TestVerifiedReadyClaimDoesNotReplayIndeterminate proves ready-claim does not
// re-scan the ready front and possibly claim a different issue after an
// indeterminate commit response.
func TestVerifiedReadyClaimDoesNotReplayIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)

	calls := 0
	indeterminate := fmt.Errorf("write commit result indeterminate after connection loss: i/o timeout (%w)", ErrCommitIndeterminate)
	got, err := s.verifiedReadyClaim(ctx, "alice", func() (*types.Issue, error) {
		calls++
		return &types.Issue{ID: id}, indeterminate
	})
	if err != indeterminate {
		t.Fatalf("error = %v, want original indeterminate error %v", err, indeterminate)
	}
	if got != nil {
		t.Fatalf("claimed issue = %+v, want nil", got)
	}
	if calls != 1 {
		t.Fatalf("write runs = %d, want exactly one after indeterminate commit", calls)
	}

	assignee, status, verr := s.readClaimState(ctx, id)
	if verr != nil {
		t.Fatalf("read claim state: %v", verr)
	}
	if assignee != "" || status != types.StatusOpen {
		t.Fatalf("ready claim was replayed: assignee=%q status=%q", assignee, status)
	}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
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
