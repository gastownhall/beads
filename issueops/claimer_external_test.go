package issueops_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestClaimerIsItsOwnRoleWithOneMethod pins the shape claim was given rather
// than the one it could have had: a role beside Lifecycle and Reader, reached
// by its own accessor, instead of a fifth verb on Lifecycle. The method count
// is part of that — the NEXT capability must not be appended here either.
func TestClaimerIsItsOwnRoleWithOneMethod(t *testing.T) {
	var _ issueops.Claimer = claimerProbe{}

	role := reflect.TypeFor[issueops.Claimer]()
	if got := role.NumMethod(); got != 1 {
		t.Fatalf("Claimer declares %d methods, want exactly one; a new capability gets a new role", got)
	}
	if got := role.Method(0).Name; got != "Claim" {
		t.Errorf("Claimer's method = %q, want Claim", got)
	}
	if _, found := reflect.TypeFor[issueops.Lifecycle]().MethodByName("Claim"); found {
		t.Error("Lifecycle grew a Claim method; claim is its own role")
	}
}

// TestClaimRequestAndResultHaveUsefulZeroValues: a zero request names nobody
// and a zero result reports no mutation, so a caller that forgets a field gets
// a refusal rather than a claim by the empty actor.
func TestClaimRequestAndResultHaveUsefulZeroValues(t *testing.T) {
	var request issueops.ClaimRequest
	if request.Actor != "" || request.IssueID != "" {
		t.Errorf("zero ClaimRequest carries a value: %#v", request)
	}
	var result issueops.ClaimResult
	if result.Issue != nil || result.Changed {
		t.Errorf("zero ClaimResult reports a mutation: %#v", result)
	}
}

// TestClaimConflictErrorWrapsTheRefusalItReports is the whole design of the
// typed conflict: it ADDS the losing state without replacing anything. The
// sentinel still matches, and the message is byte-for-byte the refusal's — the
// carefully-worded copy that deliberately names no release command, and the
// fragments beads.ParseClaimConflict still parses.
func TestClaimConflictErrorWrapsTheRefusalItReports(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
		status   issueops.Status
	}{
		{"already claimed", issueops.ErrAlreadyClaimed, issueops.StatusInProgress},
		{"not claimable", issueops.ErrNotClaimable, issueops.StatusClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := fmt.Errorf("claim bd-1: %w: already assigned to %q", tc.sentinel, "bob")
			var err error = &issueops.ClaimConflictError{
				IssueID:  "bd-1",
				Assignee: "bob",
				Status:   tc.status,
				Err:      refusal,
			}

			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("ClaimConflictError does not match its sentinel: %v", err)
			}
			if got := err.Error(); got != refusal.Error() {
				t.Errorf("Error() = %q, want the wrapped refusal byte-for-byte (%q)", got, refusal.Error())
			}

			var conflict *issueops.ClaimConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("errors.As did not recover the conflict from %v", err)
			}
			if conflict.IssueID != "bd-1" || conflict.Assignee != "bob" || conflict.Status != tc.status {
				t.Errorf("conflict = %#v, want the state read inside the losing transaction", conflict)
			}
		})
	}
}

type claimerProbe struct{}

func (claimerProbe) Claim(context.Context, issueops.ClaimRequest) (issueops.ClaimResult, error) {
	return issueops.ClaimResult{}, nil
}
