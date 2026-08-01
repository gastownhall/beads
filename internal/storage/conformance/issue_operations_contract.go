package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the behavior contract every implementation of
// publicops.Lifecycle must satisfy, independent of how it reaches storage.
// There are three of them — the direct store, the embedded store, and the
// unit-of-work backend — and the first two share an execution path the third
// does not. Behavior asserted only against one backend has repeatedly drifted
// on the others, so each of these runs against all three from one spec.

// RunIssueOperationsCreateRoutesInfraTypesToWisps pins the facade create
// against the same infra-type routing the stores' own CreateIssue applies: a
// configured infra type is ephemeral and lives in the wisp tables, never in
// issues.
func RunIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	for key, value := range map[string]string{"types.custom": "agent", "types.infra": "agent"} {
		if err := fixture.SetConfig(ctx, key, value); err != nil {
			t.Fatalf("SetConfig(%s): %v", key, err)
		}
	}

	result, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra bead", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent")},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = false, want true for infra type %q", result.Issue.IssueType)
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", result.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", result.Issue.ID, 0)

	// A no-history infra create keeps its no-history retention rather than
	// being upgraded to ephemeral, matching CreateIssue.
	noHistory, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "infra no-history", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent"), NoHistory: true},
	})
	if err != nil {
		t.Fatalf("Create no-history: %v", err)
	}
	if noHistory.Issue.Ephemeral {
		t.Errorf("no-history infra create Ephemeral = true, want false")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", noHistory.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", noHistory.Issue.ID, 0)

	// A non-infra type is unaffected.
	durable, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "durable bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create durable: %v", err)
	}
	if durable.Issue.Ephemeral {
		t.Errorf("durable create Ephemeral = true, want false")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", durable.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", durable.Issue.ID, 0)
}

// RunIssueOperationsCreateRejectsMissingDependencyTargets pins the facade
// create against reporting success for an issue whose requested relationships
// were never written. The batch engine tolerates a dangling edge so a partial
// import still lands; a guarded single create must refuse the whole request
// with a typed error naming the target, and leave nothing behind.
func RunIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	seed := &types.Issue{ID: fixture.IssuePrefix + "-skipdep-seed", Title: "seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := fixture.CreateIssue(ctx, seed, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name    string
		id      string
		request publicops.CreateRequest
		target  string
	}{
		{
			name:   "explicit dependency",
			id:     fixture.IssuePrefix + "-skipdep-explicit",
			target: fixture.IssuePrefix + "-skipdep-missing-dep",
			request: publicops.CreateRequest{
				Dependencies: []publicops.CreateDependency{{TargetID: fixture.IssuePrefix + "-skipdep-missing-dep", Type: types.DepBlocks}},
			},
		},
		{
			name:    "parent",
			id:      fixture.IssuePrefix + "-skipdep-parent",
			target:  fixture.IssuePrefix + "-skipdep-missing-parent",
			request: publicops.CreateRequest{ParentID: fixture.IssuePrefix + "-skipdep-missing-parent"},
		},
		{
			name:   "waits-for spawner",
			id:     fixture.IssuePrefix + "-skipdep-waits",
			target: fixture.IssuePrefix + "-skipdep-missing-spawner",
			request: publicops.CreateRequest{
				WaitsFor: &publicops.WaitsFor{SpawnerID: fixture.IssuePrefix + "-skipdep-missing-spawner"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			request.Actor = "writer"
			request.ForceIDPrefix = true
			request.Issue = &types.Issue{ID: tc.id, Title: tc.name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			_, err := fixture.Operations.Create(ctx, request)
			if err == nil {
				t.Fatal("Create returned nil error, want a refusal for the missing dependency target")
			}
			if !errors.Is(err, publicops.ErrNotFound) {
				t.Errorf("Create error = %v, want ErrNotFound", err)
			}
			if !errors.Is(err, publicops.ErrValidation) {
				t.Errorf("Create error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("Create error = %v, want it to name the missing target %q", err, tc.target)
			}
			assertIssueOperationsRowCount(t, ctx, fixture, "issues", tc.id, 0)
			assertIssueOperationsRowCount(t, ctx, fixture, "wisps", tc.id, 0)
		})
	}

	// A create whose targets all exist is unaffected.
	result, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{ID: fixture.IssuePrefix + "-skipdep-ok", Title: "ok", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		Dependencies:  []publicops.CreateDependency{{TargetID: seed.ID, Type: types.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("Create with existing target: %v", err)
	}
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != seed.ID {
		t.Fatalf("Create result dependencies = %#v, want one edge to %s", result.Issue.Dependencies, seed.ID)
	}
}

// RunIssueOperationsUpdateFoldsMetadataIntoOneEvent pins a compound update to a
// single event. A guarded update is one atomic mutation, so its history must
// read as one entry; a metadata patch riding along with field edits must not
// write the row twice and fabricate a second event in the stream every history
// consumer sees.
func RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	issue := &types.Issue{
		ID: fixture.IssuePrefix + "-metadata-event", Title: "metadata event", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"keep":"old"}`),
	}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	events := newIssueOperationsEventCounter(t, ctx, fixture, issue.ID)

	updated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		Metadata: publicops.MetadataPatch{
			Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)},
		},
	}})
	if err != nil {
		t.Fatalf("compound update: %v", err)
	}
	if !updated.Changed || updated.Issue.Status != types.StatusInProgress {
		t.Fatalf("compound update result = %#v", updated)
	}
	assertIssueOperationsMetadata(t, "compound update", updated.Issue.Metadata, `{"added":"value","keep":"old"}`)
	events.assert(t, "compound update", 1, map[types.EventType]int{types.EventStatusChanged: 1, types.EventUpdated: 0})

	// A metadata-only patch still records its own single event.
	metadataOnly, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Unset: []string{"keep"}},
	}})
	if err != nil || !metadataOnly.Changed {
		t.Fatalf("metadata-only update = %#v, %v", metadataOnly, err)
	}
	assertIssueOperationsMetadata(t, "metadata-only update", metadataOnly.Issue.Metadata, `{"added":"value"}`)
	events.assert(t, "metadata-only update", 1, map[types.EventType]int{types.EventUpdated: 1})

	// A metadata patch that changes nothing stays elided.
	noOp, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}},
	}})
	if err != nil || noOp.Changed {
		t.Fatalf("no-op metadata update = %#v, %v", noOp, err)
	}
	events.assert(t, "no-op metadata update", 0, nil)
}

// RunIssueOperationsUpdateClosePolicy pins what a generic status update does
// when it crosses from a non-done status into the done category: it answers to
// the same policy `bd close` does — the open-children refusal and the
// live-direct-blocker refusal — and ForceClosePolicy is how a caller overrides
// them. Until now the contract had no boundary-crossing case at all, and that
// gap is exactly how two earlier attempts at a shared policy check reached a
// backend that could not satisfy them without any test noticing.
func RunIssueOperationsUpdateClosePolicy(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	parentID := fixture.IssuePrefix + "-closepolicy-parent"
	seedClosePolicyIssue(t, ctx, fixture, parentID, publicops.CreateRequest{})
	seedClosePolicyIssue(t, ctx, fixture, fixture.IssuePrefix+"-closepolicy-child", publicops.CreateRequest{ParentID: parentID})

	blockerID := fixture.IssuePrefix + "-closepolicy-blocker"
	blockedID := fixture.IssuePrefix + "-closepolicy-blocked"
	seedClosePolicyIssue(t, ctx, fixture, blockerID, publicops.CreateRequest{})
	seedClosePolicyIssue(t, ctx, fixture, blockedID, publicops.CreateRequest{
		Dependencies: []publicops.CreateDependency{{TargetID: blockerID, Type: types.DepBlocks}},
	})

	// An open child refuses, with the typed error and its count, and writes
	// nothing — not the row, not an event.
	events := newIssueOperationsEventCounter(t, ctx, fixture, parentID)
	var openChildrenErr *publicops.CloseOpenChildrenError
	_, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(parentID, false))
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	if openChildrenErr.OpenChildren != 1 {
		t.Errorf("refusal reported %d open children, want 1", openChildrenErr.OpenChildren)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)
	events.assert(t, "refused crossing", 0, nil)

	// A claim rides the same atomic update. An open-child refusal must leave
	// every part of that compound request inert, including the would-be claim.
	var beforeAssignee string
	var beforeRowVersion, beforeClosedAt string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), CAST(row_lock AS CHAR), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?", []any{parentID}, &beforeAssignee, &beforeRowVersion, &beforeClosedAt); err != nil {
		t.Fatalf("read compound-refusal state for %s: %v", parentID, err)
	}
	claimAndClose := closePolicyStatusRequest(parentID, false)
	claimAndClose.Claim = true
	_, err = fixture.Operations.Update(ctx, claimAndClose)
	openChildrenErr = nil
	if !errors.As(err, &openChildrenErr) {
		t.Fatalf("claiming update %s into done with an open child: err = %v, want CloseOpenChildrenError", parentID, err)
	}
	var afterAssignee string
	var afterRowVersion, afterClosedAt string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), CAST(row_lock AS CHAR), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?", []any{parentID}, &afterAssignee, &afterRowVersion, &afterClosedAt); err != nil {
		t.Fatalf("read compound-refusal state after %s: %v", parentID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)
	if afterAssignee != beforeAssignee {
		t.Errorf("compound refusal assignee = %q, want unchanged %q", afterAssignee, beforeAssignee)
	}
	if afterRowVersion != beforeRowVersion {
		t.Errorf("compound refusal row version = %q, want unchanged %q", afterRowVersion, beforeRowVersion)
	}
	if afterClosedAt != beforeClosedAt {
		t.Errorf("compound refusal closed_at = %v, want unchanged %v", afterClosedAt, beforeClosedAt)
	}
	events.assert(t, "refused claiming crossing", 0, nil)

	// A live direct blocker refuses too.
	_, err = fixture.Operations.Update(ctx, closePolicyStatusRequest(blockedID, false))
	if !errors.Is(err, publicops.ErrCloseBlocked) {
		t.Fatalf("update %s into done with a live blocker: err = %v, want ErrCloseBlocked", blockedID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, blockedID, types.StatusOpen)

	// Force bypasses close policy and nothing else. A stale ExpectedVersion is
	// an orthogonal precondition, checked ahead of the policy and never waived
	// by it — the same ordering a checked close applies.
	stale := int64(-1)
	staleRequest := closePolicyStatusRequest(parentID, true)
	staleRequest.ExpectedVersion = &stale
	if _, err := fixture.Operations.Update(ctx, staleRequest); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("forced crossing with a stale version: err = %v, want ErrVersionMismatch", err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusOpen)

	// ForceClosePolicy bypasses both, and only those.
	for _, id := range []string{parentID, blockedID} {
		forced, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(id, true))
		if err != nil {
			t.Fatalf("forced update %s into done: %v", id, err)
		}
		if !forced.Changed || forced.Issue.Status != types.StatusClosed {
			t.Fatalf("forced update %s into done = %#v, want a committed close", id, forced)
		}
	}

	// A done-to-done restatement is filtered out as a no-op before any policy
	// could observe it, so it needs no force even though the child is still open.
	reclose, err := fixture.Operations.Update(ctx, closePolicyStatusRequest(parentID, false))
	if err != nil {
		t.Fatalf("restate %s as done: %v", parentID, err)
	}
	if reclose.Changed {
		t.Errorf("restating %s as done reported Changed = true, want a no-op", parentID)
	}

	// A status change that does not reach the done category is untouched by any
	// of this, open child or not.
	nonCrossing := closePolicyStatusRequest(parentID, false)
	nonCrossing.Patch.Status.Value = types.StatusInProgress
	if _, err := fixture.Operations.Update(ctx, nonCrossing); err != nil {
		t.Fatalf("non-crossing status update on %s: %v", parentID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, parentID, types.StatusInProgress)
}

// RunIssueOperationsUpdateClosedFieldsMatchClose pins the close-lifecycle
// columns a generic update leaves behind (ga-kjkv1). A status update that
// crosses into closed is a close by another name, so it must land the row a
// close lands: close_reason and closed_by_session written, not inherited from
// whatever the previous close left. It also pins the closed_at coherence guard,
// which is reachable only through the raw column map an external-sync or
// backfill caller uses — the typed patch carries no closed_at.
//
// A shared helper alone does not keep the two funnels honest: both already call
// ManageClosedAt, and the pin auto-clear that sits three lines away from it
// exists in issueops and is absent from domain/db. Only a case that asserts the
// stored row on every backend catches that shape of divergence.
func RunIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	// A generic close on a row a previous close stamped must not inherit that
	// close's reason or session. This is the misattribution ga-kjkv1 fixes:
	// `bd show` renders a stale closed_by_session as "Closed by session".
	recloseID := fixture.IssuePrefix + "-closedfields-reclose"
	seedClosePolicyIssue(t, ctx, fixture, recloseID, publicops.CreateRequest{})
	if _, err := fixture.Operations.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: recloseID, Reason: "first pass", Session: "session-one",
	}); err != nil {
		t.Fatalf("close %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after close", "first pass", "session-one", true)

	// Reopen through the generic funnel, not the reopen verb: this is the path
	// that used to leave close_reason and closed_by_session in place.
	if err := fixture.UpdateRaw(ctx, recloseID, map[string]any{"status": string(types.StatusOpen)}, "writer"); err != nil {
		t.Fatalf("generic reopen of %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after generic reopen", "", "", false)

	if err := fixture.UpdateRaw(ctx, recloseID, map[string]any{"status": string(types.StatusClosed)}, "writer"); err != nil {
		t.Fatalf("generic re-close of %s: %v", recloseID, err)
	}
	assertClosedFields(t, ctx, fixture, recloseID, "after generic re-close", "", "", true)

	// An explicit key still wins over its default, so the CLI's own
	// closed_by_session pass-through keeps working.
	explicitID := fixture.IssuePrefix + "-closedfields-explicit"
	seedClosePolicyIssue(t, ctx, fixture, explicitID, publicops.CreateRequest{})
	if err := fixture.UpdateRaw(ctx, explicitID, map[string]any{
		"status": string(types.StatusClosed), "closed_by_session": "session-two", "close_reason": "handled",
	}, "writer"); err != nil {
		t.Fatalf("generic close of %s with explicit close fields: %v", explicitID, err)
	}
	assertClosedFields(t, ctx, fixture, explicitID, "explicit close fields", "handled", "session-two", true)

	// The close-crossing defaults have to be observable on a row that carries
	// stale attribution at the moment it closes. A freshly created row already
	// has both columns empty, and the re-close above routes through a generic
	// reopen that blanks them first, so neither case can tell a funnel that
	// writes the columns from one that merely inherits them. Seeding the stale
	// values onto an OPEN row does: the columns are allowlisted by name, and
	// with no closed_at in the map the coherence guard has nothing to refuse.
	staleID := fixture.IssuePrefix + "-closedfields-stale"
	seedClosePolicyIssue(t, ctx, fixture, staleID, publicops.CreateRequest{})
	if err := fixture.UpdateRaw(ctx, staleID, map[string]any{
		"close_reason": "stale", "closed_by_session": "stale-sess",
	}, "writer"); err != nil {
		t.Fatalf("seed stale close attribution on open %s: %v", staleID, err)
	}
	assertClosedFields(t, ctx, fixture, staleID, "stale attribution staged while open", "stale", "stale-sess", false)

	if err := fixture.UpdateRaw(ctx, staleID, map[string]any{"status": string(types.StatusClosed)}, "writer"); err != nil {
		t.Fatalf("generic close of %s over stale attribution: %v", staleID, err)
	}
	assertClosedFields(t, ctx, fixture, staleID, "generic close over stale attribution", "", "", true)

	// The coherence guard. Stamping closed_at on a row that stays open is
	// refused by name, typed as a validation error, and writes nothing.
	guardID := fixture.IssuePrefix + "-closedfields-guard"
	seedClosePolicyIssue(t, ctx, fixture, guardID, publicops.CreateRequest{})
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	events := newIssueOperationsEventCounter(t, ctx, fixture, guardID)
	err := fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": stamp}, "writer")
	assertClosedAtRefusal(t, err, "stamping closed_at on an open row", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusOpen)
	assertClosedFields(t, ctx, fixture, guardID, "after refused closed_at stamp", "", "", false)
	events.assert(t, "refused closed_at stamp", 0, nil)

	// So is a stamp riding a status that does not land closed.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{"status": string(types.StatusInProgress), "closed_at": stamp}, "writer")
	assertClosedAtRefusal(t, err, "stamping closed_at on a non-closed transition", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusOpen)

	// Landing status and closed_at together is the coherent write, so it is
	// allowed — an external-sync or backfill caller depends on it.
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusClosed), "closed_at": stamp,
	}, "writer"); err != nil {
		t.Fatalf("landing status and closed_at together on %s: %v", guardID, err)
	}
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "coherent close with explicit closed_at", "", "", true)

	// Restamping closed_at on a row that is already closed is the repair path
	// for rows a pre-invariant close left blank; it stays open.
	repaired := stamp.Add(time.Hour)
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": repaired}, "writer"); err != nil {
		t.Fatalf("repairing closed_at on closed %s: %v", guardID, err)
	}

	// Clearing closed_at while the status stays closed is the other incoherent
	// half, and it is refused too.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{"closed_at": nil}, "writer")
	assertClosedAtRefusal(t, err, "clearing closed_at on a closed row", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "after refused closed_at clear", "", "", true)

	// The same refusal must hold when the explicit closed_at happens to equal
	// the value already stored. That is a no-op by VALUE and an incoherent
	// write by INTENT — the caller is asking to reopen the row and keep its
	// closed_at — so the guard has to see the key before the no-op filter can
	// drop it. Otherwise this write and the identical one carrying a stamp one
	// nanosecond off get opposite answers, and the reopen silently clears the
	// column the caller explicitly asked to keep.
	err = fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusOpen), "closed_at": repaired,
	}, "writer")
	assertClosedAtRefusal(t, err, "reopening while restating the row's own closed_at", guardID)
	assertClosePolicyStatus(t, ctx, fixture, guardID, types.StatusClosed)
	assertClosedFields(t, ctx, fixture, guardID, "after refused no-op-valued closed_at reopen", "", "", true)

	// Clearing it as part of a reopen is coherent, so it is allowed.
	if err := fixture.UpdateRaw(ctx, guardID, map[string]any{
		"status": string(types.StatusOpen), "closed_at": nil,
	}, "writer"); err != nil {
		t.Fatalf("reopening %s with an explicit closed_at clear: %v", guardID, err)
	}
	assertClosedFields(t, ctx, fixture, guardID, "reopen with explicit closed_at clear", "", "", false)
}

// assertClosedAtRefusal checks that a coherence refusal is typed as a
// validation error and names both the column and the issue, so a raw-map caller
// with no override can tell exactly which write was rejected.
func assertClosedAtRefusal(t *testing.T, err error, label, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want a refusal", label)
	}
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("%s: err = %v, want ErrValidation", label, err)
	}
	for _, want := range []string{"closed_at", id} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refusal %q does not mention %q", label, err.Error(), want)
		}
	}
}

// assertClosedFields reads the close-lifecycle columns back from storage. The
// stored empty string and SQL NULL are the same "nothing recorded" state to
// every reader, so both collapse to "" here.
func assertClosedFields(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, wantReason, wantSession string, wantClosedAt bool) {
	t.Helper()
	var reason, session, closedAt string
	if err := fixture.QueryScalar(ctx,
		"SELECT COALESCE(close_reason, ''), COALESCE(closed_by_session, ''), COALESCE(CAST(closed_at AS CHAR), '') FROM issues WHERE id = ?",
		[]any{id}, &reason, &session, &closedAt); err != nil {
		t.Fatalf("read close fields for %s (%s): %v", id, label, err)
	}
	if reason != wantReason {
		t.Errorf("%s %s close_reason = %q, want %q", id, label, reason, wantReason)
	}
	if session != wantSession {
		t.Errorf("%s %s closed_by_session = %q, want %q", id, label, session, wantSession)
	}
	if gotClosedAt := closedAt != ""; gotClosedAt != wantClosedAt {
		t.Errorf("%s %s closed_at = %q, want set = %v", id, label, closedAt, wantClosedAt)
	}
}

// RunIssueOperationsUpdateAssigneeTransferFence pins what an assignee edit does
// when it takes an issue away from a live foreign holder: it is refused with
// ErrAlreadyClaimed, and ForceAssigneeTransfer, an ExpectedAssignee
// compare-and-set, or a configured claim.pools alias are the only ways past it.
// The contract had no assignee-transfer case at all, and that gap is exactly how
// one backend came to permit a transfer the other two refuse.
func RunIssueOperationsUpdateAssigneeTransferFence(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	heldID := fixture.IssuePrefix + "-xferfence-held"
	seedClosePolicyIssue(t, ctx, fixture, heldID, publicops.CreateRequest{})
	claimed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s for holder: %v", heldID, err)
	}
	if !claimed.Changed {
		t.Fatalf("claiming %s reported Changed = false, want a committed claim", heldID)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// The fence itself: an unforced transfer away from the live holder is
	// refused, and the refusal writes nothing — not the row, not an event.
	events := newIssueOperationsEventCounter(t, ctx, fixture, heldID)
	if _, err := fixture.Operations.Update(ctx, assigneeTransferRequest(heldID, "rival", "rival")); !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from its holder: err = %v, want ErrAlreadyClaimed", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")
	events.assert(t, "refused transfer", 0, nil)

	// A stale precondition is orthogonal to the fence and is checked ahead of
	// it, so a request that fails both reports the precondition — the same
	// ordering a forced close-policy crossing gets.
	staleVersion := int64(-1)
	staleVersionRequest := assigneeTransferRequest(heldID, "rival", "rival")
	staleVersionRequest.ExpectedVersion = &staleVersion
	if _, err := fixture.Operations.Update(ctx, staleVersionRequest); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("fenced transfer of %s with a stale version: err = %v, want ErrVersionMismatch", heldID, err)
	}
	staleStatus := types.StatusOpen
	staleStatusRequest := assigneeTransferRequest(heldID, "rival", "rival")
	staleStatusRequest.ExpectedStatus = &staleStatus
	if _, err := fixture.Operations.Update(ctx, staleStatusRequest); !errors.Is(err, publicops.ErrStatusMismatch) {
		t.Fatalf("fenced transfer of %s with a stale status: err = %v, want ErrStatusMismatch", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// Restating the holder's own name is not a transfer, so a third party may
	// do it unforced — and it changes nothing.
	reassert, err := fixture.Operations.Update(ctx, assigneeTransferRequest(heldID, "bystander", "holder"))
	if err != nil {
		t.Fatalf("reassert %s's current assignee: %v", heldID, err)
	}
	if reassert.Changed {
		t.Errorf("reasserting %s's current assignee reported Changed = true, want a no-op", heldID)
	}

	// An ExpectedAssignee compare-and-set naming the holder replaces the fence:
	// the caller proved its view of the claim is current.
	casRequest := assigneeTransferRequest(heldID, "rival", "rival")
	holder := "holder"
	casRequest.ExpectedAssignee = &holder
	cas, err := fixture.Operations.Update(ctx, casRequest)
	if err != nil {
		t.Fatalf("compare-and-set transfer of %s: %v", heldID, err)
	}
	if !cas.Changed || cas.Issue.Assignee != "rival" {
		t.Fatalf("compare-and-set transfer of %s = %#v, want a committed transfer to rival", heldID, cas.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "rival")

	// ForceAssigneeTransfer is the unconditional override.
	forcedRequest := assigneeTransferRequest(heldID, "usurper", "usurper")
	forcedRequest.ForceAssigneeTransfer = true
	forced, err := fixture.Operations.Update(ctx, forcedRequest)
	if err != nil {
		t.Fatalf("forced transfer of %s: %v", heldID, err)
	}
	if !forced.Changed || forced.Issue.Assignee != "usurper" {
		t.Fatalf("forced transfer of %s = %#v, want a committed transfer to usurper", heldID, forced.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "usurper")

	// A holder that is a configured claim.pools alias is a group placeholder,
	// not an owner, so taking work from the pool needs no force.
	if err := fixture.SetConfig(ctx, "claim.pools", "pool-crew"); err != nil {
		t.Fatalf("SetConfig(claim.pools): %v", err)
	}
	pooledID := fixture.IssuePrefix + "-xferfence-pooled"
	seedClosePolicyIssue(t, ctx, fixture, pooledID, publicops.CreateRequest{})
	pooledRequest := assigneeTransferRequest(pooledID, "seed", "pool-crew")
	pooledRequest.Claim = true
	if _, err := fixture.Operations.Update(ctx, pooledRequest); err != nil {
		t.Fatalf("assign %s to the pool: %v", pooledID, err)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "pool-crew")
	taken, err := fixture.Operations.Update(ctx, assigneeTransferRequest(pooledID, "member", "member"))
	if err != nil {
		t.Fatalf("unforced transfer of pooled %s: %v", pooledID, err)
	}
	if !taken.Changed || taken.Issue.Assignee != "member" {
		t.Fatalf("unforced transfer of pooled %s = %#v, want a committed transfer to member", pooledID, taken.Issue)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "member")

	// The alias set is the only carve-out: a real holder is still fenced while
	// pools are configured.
	if _, err := fixture.Operations.Update(ctx, assigneeTransferRequest(pooledID, "rival", "rival")); !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Fatalf("unforced transfer of %s away from a non-pool holder: err = %v, want ErrAlreadyClaimed", pooledID, err)
	}
	assertLiveAssignee(t, ctx, fixture, pooledID, "member")
}

// assigneeTransferRequest builds the bare assignee edit whose fencing this case
// pins: actor asks for the issue to be assigned to newAssignee.
func assigneeTransferRequest(id, actor, newAssignee string) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:   actor,
		IssueID: id,
		Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: newAssignee}},
	}
}

// assertLiveAssignee checks the stored holder of an in-progress issue. Every
// state this case asserts is a live claim — the fence only speaks over one — so
// the expected status is fixed, and reading it back proves an assignee edit
// leaves the claim's status alone.
func assertLiveAssignee(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, wantAssignee string) {
	t.Helper()
	var assignee, status string
	if err := fixture.QueryScalar(ctx, "SELECT assignee, status FROM issues WHERE id = ?", []any{id}, &assignee, &status); err != nil {
		t.Fatalf("read assignee and status for %s: %v", id, err)
	}
	if assignee != wantAssignee {
		t.Errorf("%s assignee = %q, want %q", id, assignee, wantAssignee)
	}
	if types.Status(status) != types.StatusInProgress {
		t.Errorf("%s status = %q, want %q", id, status, types.StatusInProgress)
	}
}

// closePolicyStatusRequest builds the generic status update that crosses into
// the done category — the operation whose policy this case pins.
func closePolicyStatusRequest(id string, force bool) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:            "writer",
		IssueID:          id,
		ForceClosePolicy: force,
		Patch:            publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}},
	}
}

func assertClosePolicyStatus(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, want types.Status) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("read status for %s: %v", id, err)
	}
	if types.Status(got) != want {
		t.Errorf("%s status = %q, want %q", id, got, want)
	}
}

// seedClosePolicyIssue creates one open task at an explicit ID, carrying any
// relationships the close-policy case needs. It goes through Create rather than
// the fixture's raw seed hook so the edges recompute is_blocked exactly as a
// real create would.
func seedClosePolicyIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, request publicops.CreateRequest) {
	t.Helper()
	request.Actor = "seed"
	request.ForceIDPrefix = true
	request.Issue = &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if _, err := fixture.Operations.Create(ctx, request); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func assertIssueOperationsRowCount(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, table, id string, want int) {
	t.Helper()
	var got int
	//nolint:gosec // G201: table is one of the contract's hardcoded table names
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", []any{id}, &got); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if got != want {
		t.Errorf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}

func assertIssueOperationsMetadata(t *testing.T, label string, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s metadata %s: %v", label, got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("%s want metadata %s: %v", label, want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s metadata = %s, want %s", label, got, want)
	}
}

// issueOperationsEventCounter reports how many event rows each operation adds
// for one issue.
type issueOperationsEventCounter struct {
	ctx     context.Context
	fixture IssueOperationsStagingFixture
	id      string
	total   int
	byType  map[types.EventType]int
}

func newIssueOperationsEventCounter(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string) *issueOperationsEventCounter {
	t.Helper()
	counter := &issueOperationsEventCounter{ctx: ctx, fixture: fixture, id: id, byType: map[types.EventType]int{}}
	counter.total = counter.count(t, "")
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		counter.byType[eventType] = counter.count(t, eventType)
	}
	return counter
}

func (c *issueOperationsEventCounter) count(t *testing.T, eventType types.EventType) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM events WHERE issue_id = ?"
	args := []any{c.id}
	if eventType != "" {
		query += " AND event_type = ?"
		args = append(args, string(eventType))
	}
	var got int
	if err := c.fixture.QueryScalar(c.ctx, query, args, &got); err != nil {
		t.Fatalf("count events for %s (%q): %v", c.id, eventType, err)
	}
	return got
}

// assert checks the rows added since the previous assert and re-baselines.
func (c *issueOperationsEventCounter) assert(t *testing.T, label string, wantTotal int, wantByType map[types.EventType]int) {
	t.Helper()
	total := c.count(t, "")
	if got := total - c.total; got != wantTotal {
		t.Errorf("%s wrote %d event rows, want %d", label, got, wantTotal)
	}
	c.total = total
	for eventType, want := range wantByType {
		current := c.count(t, eventType)
		if got := current - c.byType[eventType]; got != want {
			t.Errorf("%s wrote %d %q events, want %d", label, got, eventType, want)
		}
	}
	for _, eventType := range []types.EventType{types.EventUpdated, types.EventStatusChanged} {
		c.byType[eventType] = c.count(t, eventType)
	}
}
