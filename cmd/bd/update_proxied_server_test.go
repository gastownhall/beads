package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The plain proxied update joined the claim on issueops.Lifecycle, so these pin
// the same three things its sibling in update_claim_proxied_test.go pins: the
// request this surface makes, the roles it reaches them through, and the fact
// that it opens no unit of work of its own.

func captureStderrDuring(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	defer func() {
		os.Stderr = old
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	return <-done
}

func serializationFailure() error {
	return &mysql.MySQLError{Number: 1213, Message: "serialization failure, transaction rolled back"}
}

func TestProxiedUpdateRunsOnTheLifecycleContract(t *testing.T) {
	before := &types.Issue{ID: "bd-1", Status: types.StatusOpen, Title: "Original"}
	after := &types.Issue{ID: "bd-1", Status: types.StatusOpen, Title: "Renamed", Labels: []string{"keep"}}
	p := claimRoleFixture(t, before, issueops.UpdateResult{Issue: after, Changed: true}, nil)

	oldActor := actor
	actor = "agent"
	t.Cleanup(func() { actor = oldActor })

	in := &updateInput{fields: map[string]any{"title": "Renamed"}}
	got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-1", in)
	if err != nil || fail != nil {
		t.Fatalf("applyUpdateProxiedOne: err = %v, fail = %+v", err, fail)
	}
	if got == nil || got.Title != "Renamed" {
		t.Fatalf("updated issue = %+v, want the renamed row", got)
	}
	// Only the claim strips labels, to keep its published response shape; a
	// plain update carries them, as the direct route's does.
	if len(got.Labels) != 1 || got.Labels[0] != "keep" {
		t.Errorf("labels = %v, wanted the contract's hydrated labels kept on a plain update", got.Labels)
	}

	req := p.lifecycle.request
	if req.Claim {
		t.Error("a plain update reached the contract as a claim")
	}
	if req.Actor != "agent" || req.IssueID != "bd-1" || !req.Patch.Title.Set || req.Patch.Title.Value != "Renamed" {
		t.Errorf("request = %+v, want a title edit of bd-1 by agent", req)
	}
	// The commit message this path has always written, carried on the request
	// instead of being spelled at the commit site.
	if req.Provenance != "bd: update bd-1" {
		t.Errorf("Provenance = %q, want the message the proxied update has always written", req.Provenance)
	}
	// A CLI update resolves either plane; only the HTTP surface restricts it.
	if req.IssuePlaneOnly {
		t.Error("the CLI update restricted the plane; `bd update` has always resolved a wisp id")
	}
	if p.uows != 0 {
		t.Errorf("the update path opened %d units of work; the contract owns the transaction", p.uows)
	}
}

// TestProxiedUpdatePatchRoutesMergeOpsAsOperations locks in the lost-update fix
// on the proxied-server route: metadata edits and note appends must reach the
// contract as merge OPERATIONS, never as values pre-merged from the pre-read
// row (a read from an earlier transaction — merging there erased keys a
// concurrent writer committed after it).
func TestProxiedUpdatePatchRoutesMergeOpsAsOperations(t *testing.T) {
	current := &types.Issue{
		ID:       "bd-spec-1",
		Status:   types.StatusOpen,
		Notes:    "existing notes",
		Metadata: json.RawMessage(`{"existing":"yes"}`),
	}

	t.Run("append_notes", func(t *testing.T) {
		patch := mustProxiedUpdatePatch(t, &updateInput{
			fields:         map[string]any{},
			hasAppendNotes: true,
			appendNotes:    "appended",
		}, current)
		if !patch.AppendNotes.Set || patch.AppendNotes.Value != "appended" {
			t.Errorf("AppendNotes = %+v, want the raw append text", patch.AppendNotes)
		}
		if patch.Notes.Set {
			t.Errorf("Notes = %+v: notes must NOT be pre-merged from the pre-read row", patch.Notes)
		}
	})

	t.Run("merge_metadata", func(t *testing.T) {
		patch := mustProxiedUpdatePatch(t, &updateInput{
			fields:          map[string]any{},
			mergeMetadataIn: json.RawMessage(`{"new":"key"}`),
		}, current)
		if !patch.Metadata.Merge.Set || string(patch.Metadata.Merge.Value) != `{"new":"key"}` {
			t.Errorf("Metadata.Merge = %+v, want the raw incoming JSON", patch.Metadata.Merge)
		}
		if patch.Metadata.Replace.Set {
			t.Errorf("Metadata.Replace = %+v: metadata must NOT be pre-merged from the pre-read row", patch.Metadata.Replace)
		}
	})

	t.Run("set_and_unset_metadata", func(t *testing.T) {
		patch := mustProxiedUpdatePatch(t, &updateInput{
			fields:        map[string]any{},
			setMetadata:   []string{"tier=gold"},
			unsetMetadata: []string{"existing"},
		}, current)
		if got := string(patch.Metadata.Set["tier"]); got != `"gold"` {
			t.Errorf("Metadata.Set[tier] = %s, want the JSON string value", got)
		}
		if len(patch.Metadata.Unset) != 1 || patch.Metadata.Unset[0] != "existing" {
			t.Errorf("Metadata.Unset = %v, want [existing]", patch.Metadata.Unset)
		}
		if patch.Metadata.Replace.Set {
			t.Errorf("Metadata.Replace = %+v: metadata must NOT be pre-merged from the pre-read row", patch.Metadata.Replace)
		}
	})

	t.Run("clear_defer_status_resolved_from_the_pre_read_row", func(t *testing.T) {
		deferred := &types.Issue{ID: "bd-spec-2", Status: types.StatusDeferred}
		patch := mustProxiedUpdatePatch(t, &updateInput{fields: map[string]any{}, clearDeferStatus: true}, deferred)
		if !patch.Status.Set || patch.Status.Value != types.StatusOpen {
			t.Errorf("Status = %+v, want open (clearDeferStatus on a deferred issue)", patch.Status)
		}
		open := &types.Issue{ID: "bd-spec-3", Status: types.StatusBlocked}
		if patch := mustProxiedUpdatePatch(t, &updateInput{fields: map[string]any{}, clearDeferStatus: true}, open); patch.Status.Set {
			t.Errorf("Status = %+v, want unset: --defer=\"\" must not clobber a non-deferred status", patch.Status)
		}
	})
}

func mustProxiedUpdatePatch(t *testing.T, in *updateInput, before *types.Issue) issueops.IssuePatch {
	t.Helper()
	patch, err := proxiedUpdatePatch(in, before)
	if err != nil {
		t.Fatalf("proxiedUpdatePatch: %v", err)
	}
	return patch
}

// TestProxiedUpdateCarriesForce pins the proxied path's translation of
// `--force`. An earlier attempt was reverted for exactly this missing mapping:
// the proxied caller built a request that never carried the override, so a
// shared policy check refused the close with no way for the user to say
// otherwise. The assignee half only applies to an assignee edit, so
// `--force -s closed` asks for the close-policy half alone.
func TestProxiedUpdateCarriesForce(t *testing.T) {
	closing := map[string]any{"status": string(types.StatusClosed)}

	forced := recordProxiedUpdateRequest(t, &updateInput{fields: closing, force: true})
	if !forced.ForceClosePolicy {
		t.Error("ForceClosePolicy = false with --force")
	}
	if forced.ForceAssigneeTransfer {
		t.Error("ForceAssigneeTransfer = true without an assignee edit to authorize")
	}

	unforced := recordProxiedUpdateRequest(t, &updateInput{fields: closing})
	if unforced.ForceClosePolicy {
		t.Error("ForceClosePolicy = true without --force")
	}

	transfer := recordProxiedUpdateRequest(t, &updateInput{
		fields: map[string]any{"assignee": "thief"}, force: true,
	})
	if !transfer.ForceAssigneeTransfer || !transfer.ForceClosePolicy {
		t.Errorf("request = %+v, want --force to carry both halves on an assignee edit", transfer)
	}
}

// TestProxiedUpdateCarriesConditionalGuards pins the bd-wsqvw guards onto the
// request. The contract runs the compare-and-set inside the mutation
// transaction, the only place it can be atomic with the write it gates.
func TestProxiedUpdateCarriesConditionalGuards(t *testing.T) {
	holder, status := "alice", string(types.StatusInProgress)
	req := recordProxiedUpdateRequest(t, &updateInput{
		fields:     map[string]any{"priority": 0},
		ifAssignee: &holder,
		ifStatus:   &status,
	})
	if req.ExpectedAssignee == nil || *req.ExpectedAssignee != "alice" {
		t.Errorf("ExpectedAssignee = %v, want alice", req.ExpectedAssignee)
	}
	if req.ExpectedStatus == nil || *req.ExpectedStatus != types.StatusInProgress {
		t.Errorf("ExpectedStatus = %v, want in_progress", req.ExpectedStatus)
	}
}

func recordProxiedUpdateRequest(t *testing.T, in *updateInput) issueops.UpdateRequest {
	t.Helper()
	before := &types.Issue{ID: "bd-1", Status: types.StatusOpen}
	p := claimRoleFixture(t, before, issueops.UpdateResult{Issue: before, Changed: true}, nil)
	if _, fail, err := applyUpdateProxiedOne(context.Background(), "bd-1", in); err != nil || fail != nil {
		t.Fatalf("applyUpdateProxiedOne: err = %v, fail = %+v", err, fail)
	}
	return p.lifecycle.request
}

// TestProxiedUpdateExhaustedConflictsFailLoudly proves a write that never lands
// cannot exit as a success: when the contract spends its retry budget losing
// Dolt's commit-time merge, the command reports the failure instead of printing
// "✓ Updated" (a recorded failure suppresses the success line and drives the
// non-zero exit via reportUpdateFailures in runUpdateProxiedServer).
func TestProxiedUpdateExhaustedConflictsFailLoudly(t *testing.T) {
	before := &types.Issue{ID: "bd-retry-2", Status: types.StatusOpen}
	claimRoleFixture(t, before, issueops.UpdateResult{}, serializationFailure())

	var (
		got  *types.Issue
		fail *updateIDFailure
		err  error
	)
	stderr := captureStderrDuring(t, func() {
		in := &updateInput{fields: map[string]any{"title": "never lands"}}
		got, fail, err = applyUpdateProxiedOne(context.Background(), "bd-retry-2", in)
	})
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne returned a hard error: %v", err)
	}
	if fail == nil || got != nil {
		t.Fatalf("fail=%v issue=%v: a write that never landed must be reported as a failure", fail, got)
	}
	if fail.GuardMismatch {
		t.Error("exhausted conflicts must not masquerade as a guard mismatch (that would exit 13 and tell scripts not to retry)")
	}
	if !strings.Contains(stderr, "retries exhausted") {
		t.Errorf("stderr = %q, want a loud retries-exhausted failure", stderr)
	}
}

// TestProxiedUpdateGuardMismatchExitsThirteen keeps the one failure class that
// gets its own exit code: a stale --if-assignee/--if-status wrote nothing and
// retrying is pointless, so the batch must exit 13 rather than 1.
func TestProxiedUpdateGuardMismatchExitsThirteen(t *testing.T) {
	before := &types.Issue{ID: "bd-guard-1", Status: types.StatusOpen}
	claimRoleFixture(t, before, issueops.UpdateResult{},
		fmt.Errorf("%w: bd-guard-1 has status %q, expected %q", storage.ErrStatusMismatch, "open", "in_progress"))

	stale := string(types.StatusInProgress)
	var fail *updateIDFailure
	stderr := captureStderrDuring(t, func() {
		_, fail, _ = applyUpdateProxiedOne(context.Background(), "bd-guard-1", &updateInput{
			fields: map[string]any{"priority": 0}, ifStatus: &stale,
		})
	})
	if fail == nil || !fail.GuardMismatch {
		t.Fatalf("fail = %+v, want a guard-mismatch verdict", fail)
	}
	if !strings.Contains(fail.Error, "precondition failed") {
		t.Errorf("fail.Error = %q, want it named as a precondition failure", fail.Error)
	}
	if !strings.Contains(stderr, "status mismatch") {
		t.Errorf("stderr = %q, want the machine-greppable sentinel text", stderr)
	}
}

// TestProxiedUpdateAbortsTheBatchOnCancellation is the plain-update half of the
// claim's own cancellation rule: SIGINT cancels bd's root context, every
// remaining id would fail the same way, so the loop aborts instead of recording
// one "context canceled" failure per id.
func TestProxiedUpdateAbortsTheBatchOnCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cancellation.Error(), func(t *testing.T) {
			before := &types.Issue{ID: "bd-1", Status: types.StatusOpen}
			claimRoleFixture(t, before, issueops.UpdateResult{}, fmt.Errorf("update bd-1: %w", cancellation))

			got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-1",
				&updateInput{fields: map[string]any{"title": "interrupted"}})
			if !errors.Is(err, cancellation) {
				t.Fatalf("err = %v, want %v returned so the batch aborts", err, cancellation)
			}
			if got != nil || fail != nil {
				t.Errorf("issue = %+v, fail = %+v: cancellation is not a per-id verdict", got, fail)
			}
		})
	}
}
