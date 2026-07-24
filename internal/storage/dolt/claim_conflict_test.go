package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// recoverTail mirrors beads.ParseClaimConflict's extraction: it reconstructs the
// marker from the storage sentinel + fragment (the single source of truth) and
// returns the trailing token. Kept local because internal/storage/dolt cannot
// import the root beads package (import cycle); the root package's own
// claim_roundtrip_test.go exercises the real ParseClaimConflict end to end.
func recoverTail(msg string, sentinel error, fragment string) string {
	marker := sentinel.Error() + fragment
	if i := strings.LastIndex(msg, marker); i >= 0 {
		return msg[i+len(marker):]
	}
	return ""
}

// TestClaimConflictFormatRoundTrip is the producer-tied tripwire for the claim
// string coupling: it drives the REAL claim path (issueops.ClaimIssueInTx via
// DoltStore.ClaimIssue) into both conflict branches and proves the emitted error
// (a) satisfies errors.Is against the storage sentinel and (b) still carries the
// assignee/status recoverable via the exported storage fragments that
// beads.ParseClaimConflict keys on. If the producer stops wrapping with the
// fragment, this test goes red instead of ParseClaimConflict silently returning
// an empty field to a caller.
func TestClaimConflictFormatRoundTrip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Already-claimed branch: alice holds it, bob's claim conflicts.
	mk("cc-held")
	if err := store.ClaimIssue(ctx, "cc-held", "alice"); err != nil {
		t.Fatalf("claim as alice: %v", err)
	}
	err := store.ClaimIssue(ctx, "cc-held", "bob")
	if err == nil {
		t.Fatalf("bob's claim of alice's issue unexpectedly succeeded")
	}
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("claim conflict does not wrap ErrAlreadyClaimed: %v", err)
	}
	if got := recoverTail(err.Error(), storage.ErrAlreadyClaimed, storage.ClaimedByFragment); got != "alice" {
		t.Fatalf("recovered assignee = %q, want %q (from %q)", got, "alice", err.Error())
	}

	// Not-claimable branch: a closed issue is unclaimable, status embedded.
	mk("cc-closed")
	if err := store.CloseIssue(ctx, "cc-closed", "done", "tester", ""); err != nil {
		t.Fatalf("close cc-closed: %v", err)
	}
	err = store.ClaimIssue(ctx, "cc-closed", "carol")
	if err == nil {
		t.Fatalf("claim of a closed issue unexpectedly succeeded")
	}
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("closed-claim error does not wrap ErrNotClaimable: %v", err)
	}
	if got := recoverTail(err.Error(), storage.ErrNotClaimable, storage.NotClaimableStatusFragment); got != string(types.StatusClosed) {
		t.Fatalf("recovered status = %q, want %q (from %q)", got, types.StatusClosed, err.Error())
	}
}
