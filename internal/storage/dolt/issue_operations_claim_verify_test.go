package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The public facade reaches the same coordination-critical writes the store's
// own ClaimIssue/UpdateIssue do, so it needs the same verify-after-write
// resolution (bd-zccb9). Failure injection happens at the write-func seam,
// matching claim_verify_test.go.

// TestIssueOperationsClaimVerifyPostcondition pins which facade updates are
// claim-family and what state each one must leave behind.
func TestIssueOperationsClaimVerifyPostcondition(t *testing.T) {
	assignee := func(v string) *string { return &v }
	status := func(v types.Status) *types.Status { return &v }

	cases := []struct {
		name         string
		request      publicops.UpdateRequest
		wantVerified bool
		wantDesc     string
		holds        [][2]string // assignee, status pairs the postcondition accepts
		rejects      [][2]string
	}{
		{
			name:         "bare claim",
			request:      publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true},
			wantVerified: true,
			wantDesc:     `assignee="alice" status="in_progress"`,
			holds:        [][2]string{{"alice", "in_progress"}},
			rejects:      [][2]string{{"", "open"}, {"bob", "in_progress"}, {"alice", "open"}},
		},
		{
			name: "claim with overriding status patch",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusBlocked},
			}},
			wantVerified: true,
			wantDesc:     `assignee="alice" status="blocked"`,
			holds:        [][2]string{{"alice", "blocked"}},
			rejects:      [][2]string{{"alice", "in_progress"}},
		},
		{
			name: "claim with overriding assignee patch",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: "dispatcher"},
			}},
			wantVerified: true,
			wantDesc:     `assignee="dispatcher" status="in_progress"`,
			holds:        [][2]string{{"dispatcher", "in_progress"}},
			rejects:      [][2]string{{"alice", "in_progress"}},
		},
		{
			name: "claim with an ordinary scalar patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "new title"},
			}},
		},
		{
			name: "claim with a labels patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Labels: publicops.LabelPatch{Add: []string{"label"}},
			}},
		},
		{
			name: "claim with a metadata patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"key": json.RawMessage(`"value"`)}},
			}},
		},
		{
			name: "claim with a parent patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				ParentID: publicops.Field[string]{Set: true, Value: "parent"},
			}},
		},
		{
			name: "claim with a persistence patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Claim: true, Patch: publicops.IssuePatch{
				Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModeEphemeral},
			}},
		},
		{
			name: "guarded assignee transfer",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedAssignee: assignee("bob"), Patch: publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: "carol"},
			}},
			wantVerified: true,
			wantDesc:     `assignee="carol"`,
			holds:        [][2]string{{"carol", "open"}, {"carol", "in_progress"}},
			rejects:      [][2]string{{"bob", "open"}},
		},
		{
			// be-vc51/ga-2vy9p2: the CAS authorizes a reclaim under a
			// different spelling of the same identity without rewriting the
			// stored spelling (issueops.ClaimIssueInTx's idempotent-reclaim
			// branch), so the postcondition must recognize the stored
			// spelling as the same actor rather than requiring a byte-exact
			// match against the requested one — and must still reject a
			// genuinely different identity (gastown.dog-3) or a widened
			// cross-axis spelling (gastown--mayor, the rig-qualified slash
			// axis, is not gastown__mayor, the dot axis).
			name:         "bare claim accepts a respelled identity of the same actor (ga-2vy9p2)",
			request:      publicops.UpdateRequest{Actor: "gastown.mayor", IssueID: "bd-1", Claim: true},
			wantVerified: true,
			wantDesc:     `assignee="gastown.mayor" status="in_progress"`,
			holds:        [][2]string{{"gastown.mayor", "in_progress"}, {"gastown__mayor", "in_progress"}},
			rejects:      [][2]string{{"gastown--mayor", "in_progress"}, {"gastown.dog-3", "in_progress"}, {"someone-else", "in_progress"}},
		},
		{
			// Same asymmetry as above, at the guarded-update site
			// (claim_verify.go's guardedUpdatePostcondition): a coordination
			// write that sets assignee to a respelling of an identity must
			// verify against identity equivalence, not byte equality.
			name: "guarded assignee transfer accepts a respelled identity of the written value (ga-2vy9p2)",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedAssignee: assignee("bob"), Patch: publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: "gastown.mayor"},
			}},
			wantVerified: true,
			wantDesc:     `assignee="gastown.mayor"`,
			holds:        [][2]string{{"gastown.mayor", "open"}, {"gastown__mayor", "in_progress"}},
			rejects:      [][2]string{{"gastown--mayor", "open"}, {"bob", "open"}},
		},
		{
			name: "guarded status write",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedStatus: status(types.StatusOpen), Patch: publicops.IssuePatch{
				Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
			}},
			wantVerified: true,
			wantDesc:     `status="in_progress"`,
			holds:        [][2]string{{"", "in_progress"}, {"bob", "in_progress"}},
			rejects:      [][2]string{{"", "open"}},
		},
		{
			name: "guarded status write with a scalar patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedStatus: status(types.StatusOpen), Patch: publicops.IssuePatch{
				Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
				Title:  publicops.Field[string]{Set: true, Value: "new title"},
			}},
		},
		{
			name: "guarded assignee transfer with a labels patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedAssignee: assignee("bob"), Patch: publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: "carol"},
				Labels:   publicops.LabelPatch{Add: []string{"label"}},
			}},
		},
		{
			name: "guarded status write with a metadata patch is not verified",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedStatus: status(types.StatusOpen), Patch: publicops.IssuePatch{
				Status:   publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
				Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"key": json.RawMessage(`"value"`)}},
			}},
		},
		{
			name: "guarded edit of ordinary fields is not claim-family",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", ExpectedStatus: status(types.StatusOpen), Patch: publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "new title"},
			}},
		},
		{
			name: "unguarded ordinary update is not claim-family",
			request: publicops.UpdateRequest{Actor: "alice", IssueID: "bd-1", Patch: publicops.IssuePatch{
				Assignee: publicops.Field[string]{Set: true, Value: "carol"},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			post, verified := updateClaimPostcondition(tc.request)
			if verified != tc.wantVerified {
				t.Fatalf("updateClaimPostcondition verified = %v, want %v", verified, tc.wantVerified)
			}
			if !verified {
				return
			}
			if post.desc != tc.wantDesc {
				t.Errorf("postcondition desc = %q, want %q", post.desc, tc.wantDesc)
			}
			for _, state := range tc.holds {
				if !post.want(state[0], types.Status(state[1])) {
					t.Errorf("postcondition rejected accepted state assignee=%q status=%q", state[0], state[1])
				}
			}
			for _, state := range tc.rejects {
				if post.want(state[0], types.Status(state[1])) {
					t.Errorf("postcondition accepted lost state assignee=%q status=%q", state[0], state[1])
				}
			}
		})
	}
}

// TestIssueOperationsGuardedVerifyDoesNotMaskIndeterminateMixedUpdate proves a
// guarded facade update cannot infer that an ordinary patch landed merely from
// matching preexisting coordination state.
func TestIssueOperationsGuardedVerifyDoesNotMaskIndeterminateMixedUpdate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	s.serverMode = true

	id := claimVerifyTestIssue(t, s)
	operations := &issueOperations{store: s}
	expectedStatus := types.StatusOpen
	indeterminate := fmt.Errorf("write commit result indeterminate: %w", ErrCommitIndeterminate)
	err := operations.verifiedUpdate(ctx, publicops.UpdateRequest{
		Actor:          "alice",
		IssueID:        id,
		ExpectedStatus: &expectedStatus,
		Patch: publicops.IssuePatch{
			Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusOpen},
			Title:  publicops.Field[string]{Set: true, Value: "must not be inferred"},
		},
	}, func(context.Context) error {
		return indeterminate
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("mixed guarded facade update error = %v, want ErrCommitIndeterminate", err)
	}

	issue, err := s.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read issue after indeterminate update: %v", err)
	}
	if issue.Title == "must not be inferred" {
		t.Fatal("ordinary title patch unexpectedly landed")
	}
}

// TestIssueOperationsClaimVerifyDoesNotMaskIndeterminateMixedClaim proves a
// facade claim with an ordinary patch cannot infer that patch landed merely
// from matching preexisting coordination state.
func TestIssueOperationsClaimVerifyDoesNotMaskIndeterminateMixedClaim(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	s.serverMode = true

	id := claimVerifyTestIssue(t, s)
	if err := rawClaim(t, s, id, "alice"); err != nil {
		t.Fatalf("seed matching coordination state: %v", err)
	}
	operations := &issueOperations{store: s}
	indeterminate := fmt.Errorf("write commit result indeterminate: %w", ErrCommitIndeterminate)
	err := operations.verifiedUpdate(ctx, publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: id,
		Claim:   true,
		Patch: publicops.IssuePatch{
			Title: publicops.Field[string]{Set: true, Value: "must not be inferred"},
		},
	}, func(context.Context) error {
		return indeterminate
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("mixed facade claim error = %v, want ErrCommitIndeterminate", err)
	}
}

func TestIssueOperationsClaimCoordinationPatchRetainsIndeterminate(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	s.serverMode = true

	id := claimVerifyTestIssue(t, s)
	if err := rawClaim(t, s, id, "alice"); err != nil {
		t.Fatalf("seed matching claim state: %v", err)
	}
	operations := &issueOperations{store: s}
	indeterminate := fmt.Errorf("write commit result indeterminate: %w", ErrCommitIndeterminate)
	err := operations.verifiedUpdate(ctx, publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: id,
		Claim:   true,
		Patch: publicops.IssuePatch{
			Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		},
	}, func(context.Context) error {
		return indeterminate
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("coordination claim error = %v, want ErrCommitIndeterminate", err)
	}
}

// TestIssueOperationsClaimVerifyFailsLoudlyOnLostFacadeClaim: a facade claim
// whose transaction reports success without landing must fail loudly, exactly
// like the store's own ClaimIssue. A silent phantom claim through the facade
// costs the same duplicated implementation the incident did.
func TestIssueOperationsClaimVerifyFailsLoudlyOnLostFacadeClaim(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)
	operations := &issueOperations{store: s}

	err := operations.verifiedUpdate(ctx, publicops.UpdateRequest{Actor: "alice", IssueID: id, Claim: true}, func(context.Context) error {
		return nil // lie: report success without writing anything
	})
	if err == nil {
		t.Fatal("expected loud failure for a success-reported-but-lost facade claim, got nil")
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Fatalf("error should say the claim did not land, got: %v", err)
	}
}

// TestIssueOperationsClaimVerifyLeavesOrdinaryUpdatesUnwrapped: an update that
// is not claim-family keeps its exit status. A lost label or notes edit is an
// annoyance, not a phantom claim, and re-reading every update would cost a
// round trip per write.
func TestIssueOperationsClaimVerifyLeavesOrdinaryUpdatesUnwrapped(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)
	operations := &issueOperations{store: s}

	request := publicops.UpdateRequest{Actor: "alice", IssueID: id, Patch: publicops.IssuePatch{
		Title: publicops.Field[string]{Set: true, Value: "retitled"},
	}}
	if err := operations.verifiedUpdate(ctx, request, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("ordinary update must keep its exit status, got: %v", err)
	}
}

// TestIssueOperationsClaimVerifyPassesHealthyFacadeClaim: the verify layer does
// not disturb a claim that actually lands.
func TestIssueOperationsClaimVerifyPassesHealthyFacadeClaim(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	id := claimVerifyTestIssue(t, s)
	operations, err := NewIssueOperations(s)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "alice", IssueID: id, Claim: true})
	if err != nil {
		t.Fatalf("facade claim: %v", err)
	}
	if !result.Changed || result.Issue.Assignee != "alice" || result.Issue.Status != types.StatusInProgress {
		t.Fatalf("facade claim result = %#v", result)
	}
	assignee, status, err := s.readClaimState(ctx, id)
	if err != nil {
		t.Fatalf("read claim state: %v", err)
	}
	if assignee != "alice" || status != types.StatusInProgress {
		t.Fatalf("post-claim state: assignee=%q status=%q", assignee, status)
	}
}
