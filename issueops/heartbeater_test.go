package issueops_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestErrLeaseNotHeldStillMatchesErrNotClaimable pins the compatibility
// promise ErrLeaseNotHeld's doc makes: a caller written against the raw
// heartbeat's ErrNotClaimable keeps classifying the role's refusal.
func TestErrLeaseNotHeldStillMatchesErrNotClaimable(t *testing.T) {
	err := &issueops.LeaseConflictError{IssueID: "x", Err: fmt.Errorf("%w: x status closed", issueops.ErrLeaseNotHeld)}
	if !errors.Is(err, issueops.ErrLeaseNotHeld) || !errors.Is(err, issueops.ErrNotClaimable) {
		t.Fatalf("LeaseConflictError(ErrLeaseNotHeld) = %v: want it to match ErrLeaseNotHeld and ErrNotClaimable", err)
	}
	if errors.Is(err, issueops.ErrAlreadyClaimed) {
		t.Fatal("ErrLeaseNotHeld matches ErrAlreadyClaimed; the two refusals must stay distinguishable")
	}
}

// TestHeartbeatFieldErrorMatchesErrValidation pins the field error's sentinel.
func TestHeartbeatFieldErrorMatchesErrValidation(t *testing.T) {
	var err error = &issueops.HeartbeatFieldError{Field: issueops.HeartbeatFieldActor, Detail: "validation failed: heartbeat actor is required"}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("HeartbeatFieldError = %v: want it to match ErrValidation", err)
	}
}
