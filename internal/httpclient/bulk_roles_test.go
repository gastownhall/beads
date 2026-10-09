// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/bulk_roles_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The three BULK writes — sweepIssues, deleteIssues, batchCreateIssues — at the
// role boundary, with no server in sight.
//
// What is provable here and nowhere else is the REQUEST: which members reached
// the wire, which refused before the dial, and which never reached it at all.
// The served tier (served_bulk_test.go) answers the other half — what the
// server does with them — and cannot see a dropped member, because a server
// only rejects the parameters it receives.

func bulkSweeper(t *testing.T, w *stubWire) issueops.Sweeper {
	t.Helper()
	sweeper, err := stubStore(t, w).Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	return sweeper
}

func bulkDeleter(t *testing.T, w *stubWire) issueops.Deleter {
	t.Helper()
	deleter, err := stubStore(t, w).Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	return deleter
}

func bulkCreator(t *testing.T, w *stubWire) issueops.BatchCreator {
	t.Helper()
	creator, err := stubStore(t, w).BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	return creator
}

// TestSweepSendsEveryNarrowingMemberExplicitly is the sweep's whole request
// mapping in one assertion, and the two BOOLEANS are why it exists.
//
// protect_referenced DEFAULTS ON over the wire — an unauthenticated surface is
// where a default must be the guarded one — while the role's zero value is off.
// A client that omitted a false one would silently turn `bd prune
// --ignore-references` into a protected sweep and report skips the caller never
// asked for: a NARROWER answer than the request, which is refuse-not-drop's
// failure class in the other direction.
func TestSweepSendsEveryNarrowingMemberExplicitly(t *testing.T) {
	cutoff := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	w := &stubWire{}
	if _, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{
		Actor:             "sweeper",
		Tier:              issueops.SweepDurable,
		ClosedBefore:      &cutoff,
		IDPattern:         "bd-*",
		ProtectReferenced: false,
		DryRun:            true,
	}); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}

	got := w.lastSweep
	if got.Tier != apigen.Durable {
		t.Errorf("tier = %q, want %q", got.Tier, apigen.Durable)
	}
	if got.Actor == nil || *got.Actor != "sweeper" {
		t.Errorf("actor = %v, want \"sweeper\"", got.Actor)
	}
	if got.Pattern == nil || *got.Pattern != "bd-*" {
		t.Errorf("pattern = %v, want \"bd-*\"", got.Pattern)
	}
	if got.ClosedBefore == nil || !got.ClosedBefore.Equal(cutoff) {
		t.Errorf("closed_before = %v, want %v", got.ClosedBefore, cutoff)
	}
	if got.ProtectReferenced == nil {
		t.Fatal("protect_referenced was omitted; the wire defaults it ON, so an omitted false is a protection the caller declined")
	}
	if *got.ProtectReferenced {
		t.Error("protect_referenced = true for a request that asked for false")
	}
	if got.DryRun == nil || !*got.DryRun {
		t.Errorf("dry_run = %v, want true", got.DryRun)
	}
}

// TestTheBulkWritesOmitABlankActorRatherThanSendingOne pins the one member
// whose EMPTY spelling the two sides disagree about.
//
// The role accepts an empty Actor — a deleted row leaves nothing to attribute
// the deletion on — and the server refuses an actor that is empty AFTER
// TRIMMING. So the test is the TRIM, not `!= ""`: a tab-and-space Actor is an
// accepted request against every local backend, and forwarding it would turn
// that request into a 400 on this backend alone.
func TestTheBulkWritesOmitABlankActorRatherThanSendingOne(t *testing.T) {
	for _, actor := range []string{"", " ", "\t \n"} {
		w := &stubWire{}
		if _, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{
			Tier: issueops.SweepEphemeral, Actor: actor,
		}); err != nil {
			t.Fatalf("Sweep(actor=%q): %v", actor, err)
		}
		if w.lastSweep.Actor != nil {
			t.Errorf("sweep actor = %q for a blank Actor %q; the server refuses one that is empty after trimming",
				*w.lastSweep.Actor, actor)
		}

		w = &stubWire{}
		if _, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{
			IDs: []string{"bd-1"}, Force: true, Actor: actor,
		}); err != nil {
			t.Fatalf("Delete(actor=%q): %v", actor, err)
		}
		if w.lastDelete.Actor != nil {
			t.Errorf("delete actor = %q for a blank Actor %q", *w.lastDelete.Actor, actor)
		}
	}

	// A blank Actor on the batch create is a REFUSAL rather than an omission:
	// batchCreateIssues publishes actor as required, and the role's own contract
	// says a create batch must name its creator.
	w := &stubWire{}
	_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
		Actor: "  ", Items: []issueops.BatchCreateItem{{Issue: &issueops.Issue{Title: "t"}}},
	})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("CreateBatch(blank actor) = %v, want ErrValidation", err)
	}
	if len(w.calls) != 0 {
		t.Errorf("a blank-actor batch create dialed %v", w.calls)
	}

	// The pattern rides along: absent matches every bead in the tier, and
	// sending an empty string would be a second spelling of the same request.
	w = &stubWire{}
	if _, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{Tier: issueops.SweepEphemeral}); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
	if w.lastSweep.Pattern != nil {
		t.Errorf("pattern = %q for a request that named none", *w.lastSweep.Pattern)
	}
}

// TestSweepRefusesATierTheWireDoesNotPublish is the ONE refusal this role
// decides for itself, and it decides it because the tier is an ENUM the client
// has to map. Everything else the role's contract calls invalid — the
// require-a-filter gate, the glob — is refused by the server's own copy of the
// role, so re-deciding it here would be a second definition of the rule that
// keeps a workspace's history from being erased by an omission.
func TestSweepRefusesATierTheWireDoesNotPublish(t *testing.T) {
	for _, tier := range []issueops.SweepTier{"", "wisps"} {
		w := &stubWire{}
		_, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{Tier: tier, IDPattern: "*"})
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("Sweep(tier=%q) error = %v, want ErrValidation", tier, err)
		}
		if len(w.calls) != 0 {
			t.Errorf("Sweep(tier=%q) dialed %v; a request the role calls invalid must not reach a shared server", tier, w.calls)
		}
	}
}

// TestSweepDoesNotWriteThroughTheCallersCutoff pins the pointer the role
// promises never to write through. A marshaler is a read today; the copy is
// what keeps it a read after the next helper is added.
func TestSweepDoesNotWriteThroughTheCallersCutoff(t *testing.T) {
	cutoff := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	w := &stubWire{}
	if _, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{
		Tier: issueops.SweepDurable, ClosedBefore: &cutoff,
	}); err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
	if w.lastSweep.ClosedBefore == &cutoff {
		t.Error("the request body aliases the caller's cutoff pointer rather than copying it")
	}
}

// TestSweepResultCarriesEveryWireMember is the RESPONSE half of the sweep's
// mapping. SweepResult is deliberately not x-go-type-pinned, so the projection
// is hand-written and a member the server grows would otherwise be dropped in
// silence — a result whose numbers are quietly wrong about what was erased.
func TestSweepResultCarriesEveryWireMember(t *testing.T) {
	remaining := int64(13)
	liveDependent := 14
	w := &stubWire{swept: &apigen.SweepResult{
		DryRun: true, Swept: 3, Dependencies: 4, Labels: 5, Events: 6,
		Skipped: apigen.SweepSkips{
			Pinned: 7, Referenced: 8, NotClosed: 9,
			UnknownClosedAt: 10, ClosedAtOrAfterCutoff: 11, Unreadable: 12,
			LiveDependent: &liveDependent,
		},
		ReferencedIds: &[]string{"bd-1"},
		Remaining:     &remaining,
	}}
	result, err := bulkSweeper(t, w).Sweep(t.Context(), issueops.SweepRequest{Tier: issueops.SweepEphemeral})
	if err != nil {
		t.Fatalf("Sweep(): %v", err)
	}
	assertEveryFieldPopulated(t, "SweepResult", reflect.ValueOf(result))
	assertEveryFieldPopulated(t, "SweepSkips", reflect.ValueOf(result.Skipped))
	if !reflect.DeepEqual(result.ReferencedIDs, []string{"bd-1"}) {
		t.Errorf("ReferencedIDs = %v, want [bd-1]", result.ReferencedIDs)
	}
	if result.Remaining != 13 {
		t.Errorf("Remaining = %d, want 13", result.Remaining)
	}
	if result.Skipped.LiveDependent != 14 {
		t.Errorf("Skipped.LiveDependent = %d, want 14", result.Skipped.LiveDependent)
	}
}

// TestSweepSendsTheS4MembersOnceTheServerAdvertisesThem is the encoding half of
// the three S4 additions: tier "wisps-plane", protect_live_dependents and limit
// each reach the wire once the handshake advertises the matching token,
// mirroring TestCountScopeFieldsEncodeOntoTheQuery's idiom for a request body
// rather than a query.
func TestSweepSendsTheS4MembersOnceTheServerAdvertisesThem(t *testing.T) {
	served := &apigen.ContextResponse{Capabilities: []string{
		wire.CapSweepWispsPlane, wire.CapSweepLiveDependents, wire.CapSweepLimit,
	}}

	t.Run("wisps-plane tier", func(t *testing.T) {
		w := &stubWire{}
		sweeper, err := New(testTarget(t), w, served).Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		if _, err := sweeper.Sweep(t.Context(), issueops.SweepRequest{Tier: issueops.SweepWispsPlane}); err != nil {
			t.Fatalf("Sweep(): %v", err)
		}
		if w.lastSweep.Tier != apigen.WispsPlane {
			t.Errorf("tier = %q, want %q", w.lastSweep.Tier, apigen.WispsPlane)
		}
	})

	t.Run("protect_live_dependents", func(t *testing.T) {
		w := &stubWire{}
		sweeper, err := New(testTarget(t), w, served).Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		if _, err := sweeper.Sweep(t.Context(), issueops.SweepRequest{
			Tier: issueops.SweepWispsPlane, ProtectLiveDependents: true,
		}); err != nil {
			t.Fatalf("Sweep(): %v", err)
		}
		if w.lastSweep.ProtectLiveDependents == nil || !*w.lastSweep.ProtectLiveDependents {
			t.Errorf("protect_live_dependents = %v, want true", w.lastSweep.ProtectLiveDependents)
		}
	})

	t.Run("limit", func(t *testing.T) {
		w := &stubWire{}
		sweeper, err := New(testTarget(t), w, served).Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		if _, err := sweeper.Sweep(t.Context(), issueops.SweepRequest{
			Tier: issueops.SweepDurable, Limit: 5,
		}); err != nil {
			t.Fatalf("Sweep(): %v", err)
		}
		if w.lastSweep.Limit == nil || *w.lastSweep.Limit != 5 {
			t.Errorf("limit = %v, want 5", w.lastSweep.Limit)
		}
	})
}

// TestSweepRefusesTheS4MembersWhenTheServerLacksTheCapability is the skew half:
// each of the three S4 additions refuses BEFORE dialing when the handshake does
// not advertise its token, mirroring
// TestCountScopeRefusesLocallyWhenTheServerLacksTheCapability.
func TestSweepRefusesTheS4MembersWhenTheServerLacksTheCapability(t *testing.T) {
	masked := &apigen.ContextResponse{Capabilities: []string{"issues.sweep"}}

	for _, tc := range []struct {
		name string
		req  issueops.SweepRequest
		cap  string
	}{
		{"wisps-plane tier", issueops.SweepRequest{Tier: issueops.SweepWispsPlane}, wire.CapSweepWispsPlane},
		{"protect_live_dependents", issueops.SweepRequest{Tier: issueops.SweepDurable, ProtectLiveDependents: true}, wire.CapSweepLiveDependents},
		{"limit", issueops.SweepRequest{Tier: issueops.SweepDurable, Limit: 5}, wire.CapSweepLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{}
			sweeper, err := New(testTarget(t), w, masked).Sweeper()
			if err != nil {
				t.Fatalf("Sweeper(): %v", err)
			}
			_, err = sweeper.Sweep(t.Context(), tc.req)
			if err == nil {
				t.Fatal("Sweep returned no error, want a pre-dial capability refusal")
			}
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
			}
			if unsup.Capability != tc.cap {
				t.Errorf("Capability = %q, want %q", unsup.Capability, tc.cap)
			}
			if unsup.Op != "Sweeper.Sweep" {
				t.Errorf("Op = %q, want %q", unsup.Op, "Sweeper.Sweep")
			}
			if len(w.calls) != 0 {
				t.Errorf("dialed %v, want none: a pre-dial refusal must never reach the wire", w.calls)
			}
		})
	}

	// The converse: a plain ephemeral/durable sweep with neither new boolean
	// nor a limit dials normally against the same masked server — the gate
	// guards the three additions, not the operation.
	t.Run("no S4 member set dials normally", func(t *testing.T) {
		w := &stubWire{}
		sweeper, err := New(testTarget(t), w, masked).Sweeper()
		if err != nil {
			t.Fatalf("Sweeper(): %v", err)
		}
		if _, err := sweeper.Sweep(t.Context(), issueops.SweepRequest{Tier: issueops.SweepDurable}); err != nil {
			t.Fatalf("Sweep(): %v", err)
		}
		if len(w.calls) != 1 {
			t.Errorf("dialed %d times, want 1", len(w.calls))
		}
	})
}

// TestDeleteResultCarriesEveryWireMember is the same guard on the delete, whose
// result is likewise unpinned.
func TestDeleteResultCarriesEveryWireMember(t *testing.T) {
	w := &stubWire{deleted: &apigen.DeleteIssuesResult{
		DryRun: true, Deleted: 2, Dependencies: 3, Labels: 4, Events: 5,
		ReferencesUpdated: 6, Orphaned: &[]string{"bd-9"},
	}}
	result, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{IDs: []string{"bd-1"}, Force: true})
	if err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	assertEveryFieldPopulated(t, "DeleteResult", reflect.ValueOf(result))
	if !reflect.DeepEqual(result.Orphaned, []string{"bd-9"}) {
		t.Errorf("Orphaned = %v, want [bd-9]", result.Orphaned)
	}
}

// TestDeleteSendsTheIDsVerbatim pins that the client normalizes NOTHING.
//
// The role collapses duplicates and trims, and so does the server's copy of it.
// Doing it again here would be a second implementation of "which ids were
// named" on the one operation where a disagreement deletes the wrong rows — and
// normalizing in place would hand the caller back a shorter, reordered version
// of the list a CLI then echoes.
func TestDeleteSendsTheIDsVerbatim(t *testing.T) {
	ids := []string{"  bd-1  ", "bd-1", "bd-2"}
	snapshot := append([]string(nil), ids...)

	w := &stubWire{}
	if _, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{
		IDs: ids, Actor: "deleter", Cascade: true, Force: true, DryRun: true,
	}); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if !reflect.DeepEqual(w.lastDelete.Ids, snapshot) {
		t.Errorf("ids = %v, want the caller's list verbatim %v", w.lastDelete.Ids, snapshot)
	}
	if !reflect.DeepEqual(ids, snapshot) {
		t.Errorf("the caller's slice changed across the call: %v", ids)
	}
	for name, got := range map[string]*bool{
		"cascade": w.lastDelete.Cascade, "force": w.lastDelete.Force, "dry_run": w.lastDelete.DryRun,
	} {
		if got == nil || !*got {
			t.Errorf("%s = %v, want true", name, got)
		}
	}
	if w.lastDelete.Actor == nil || *w.lastDelete.Actor != "deleter" {
		t.Errorf("actor = %v, want \"deleter\"", w.lastDelete.Actor)
	}
	// The body must not alias the caller's slice: a marshaler reads it, but a
	// shared backing array is one helper away from a write.
	if len(w.lastDelete.Ids) > 0 && &w.lastDelete.Ids[0] == &ids[0] {
		t.Error("the request body aliases the caller's id slice rather than copying it")
	}
}

// TestDeleteRefusesAnUnusableIDList pins the refusals the role's own contract
// states, all of them decidable from the request alone. The empty case is the
// dangerous one: answering it as a cheerful "deleted 0" is how a caller whose
// id list came out empty concludes the workspace was already clean.
func TestDeleteRefusesAnUnusableIDList(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"   "}, {"bd-1", ""}} {
		w := &stubWire{}
		_, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{IDs: ids, Force: true})
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("Delete(%v) error = %v, want ErrValidation", ids, err)
		}
		if len(w.calls) != 0 {
			t.Errorf("Delete(%v) dialed %v; a request the role calls invalid must not reach a shared server", ids, w.calls)
		}
	}
}

// TestDeleteRefusesMoreIDsThanTheWireCarries is ledger row L-delete-bound's
// pin, and it asserts the boundary from BOTH sides: exactly the bound serves,
// one more refuses, and the refusal never dials.
//
// "Never dials" is the assertion that matters. What this row forbids is
// SPLITTING: two requests are two transactions and two history entries where
// the contract promises one, and the dependents guard — which can only see the
// ids in front of it — would refuse a pair the caller deliberately listed
// together.
func TestDeleteRefusesMoreIDsThanTheWireCarries(t *testing.T) {
	ids := make([]string, maxDeleteIDs)
	for i := range ids {
		ids[i] = "bd-" + strings.Repeat("0", 4) + string(rune('a'+i%26))
	}

	w := &stubWire{}
	if _, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{IDs: ids, Force: true}); err != nil {
		t.Fatalf("Delete(%d ids) at the bound: %v", maxDeleteIDs, err)
	}
	if len(w.calls) != 1 {
		t.Errorf("a delete at the bound dialed %v, want one request", w.calls)
	}

	w = &stubWire{}
	_, err := bulkDeleter(t, w).Delete(t.Context(), issueops.DeleteRequest{IDs: append(ids, "bd-over"), Force: true})
	assertRefusedBy(t, err, "L-delete-bound")
	if len(w.calls) != 0 {
		t.Errorf("a delete over the bound dialed %v; it must refuse rather than split", w.calls)
	}
}

// TestDeleteSendsTheRowVersionPrecondition is the compare-and-delete guard's
// round trip, and it is the operation where getting the encoding wrong is worst:
// the close it mirrors leaves a row behind to compare afterwards and this leaves
// nothing at all.
//
// The absent leg is the one that matters. `expected_version` 0 is a legal token
// — the migration-0054 backfill left rows holding it — so a client that encoded
// "no guard" as 0 would arm a guard on every unguarded delete, and the requests
// it then refused would be exactly the ones aimed at never-written rows.
//
// The MULTI-ID refusal is deliberately NOT anticipated here. The wire refuses a
// guard beside more than one DISTINCT id, and distinctness is measured after
// trimming and collapsing duplicates — the same normalization this role sends
// its ids verbatim to avoid re-implementing, on the operation where a
// disagreement about which ids were named deletes the wrong rows. So that
// refusal is the server's, and the served tier asserts it end to end.
func TestDeleteSendsTheRowVersionPrecondition(t *testing.T) {
	version := int64(9)
	for _, tc := range []struct {
		name string
		want *int64
		req  issueops.DeleteRequest
	}{
		{name: "guarded", want: &version, req: issueops.DeleteRequest{ExpectedVersion: &version}},
		{name: "zero is a real token", want: new(int64), req: issueops.DeleteRequest{ExpectedVersion: new(int64)}},
		{name: "unguarded", want: nil, req: issueops.DeleteRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{}
			req := tc.req
			req.IDs = []string{"bd-1"}
			if _, err := bulkDeleter(t, w).Delete(t.Context(), req); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			assertExpectedVersion(t, "deleteIssues", w.lastDelete.ExpectedVersion, tc.want)
		})
	}
}

// TestBatchCreateRefusesMoreItemsThanTheWireCarries is L-batchcreate-bound's
// pin, on the same argument: the request IS the transaction.
func TestBatchCreateRefusesMoreItemsThanTheWireCarries(t *testing.T) {
	items := make([]issueops.BatchCreateItem, maxBatchCreateItems)
	for i := range items {
		items[i] = issueops.BatchCreateItem{Issue: &issueops.Issue{Title: "t"}}
	}

	w := &stubWire{}
	if _, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{Actor: "w", Items: items}); err != nil {
		t.Fatalf("CreateBatch(%d items) at the bound: %v", maxBatchCreateItems, err)
	}
	if len(w.calls) != 1 {
		t.Errorf("a batch at the bound dialed %v, want one request", w.calls)
	}

	w = &stubWire{}
	over := append(items, issueops.BatchCreateItem{Issue: &issueops.Issue{Title: "over"}})
	_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{Actor: "w", Items: over})
	assertRefusedBy(t, err, "L-batchcreate-bound")
	if len(w.calls) != 0 {
		t.Errorf("a batch over the bound dialed %v; it must refuse rather than split", w.calls)
	}
}

// TestBatchCreateSendsTheItemMembersTheWireCarries is the item mapping, member
// by member. PRIORITY is the one to read twice: 0 is P0/critical and a real
// request, so it is sent always — an absent member is the workspace default,
// which would silently reprioritize every critical issue in a plan.
func TestBatchCreateSendsTheItemMembersTheWireCarries(t *testing.T) {
	w := &stubWire{}
	if _, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{{
			Issue: &issueops.Issue{
				Title: "the title", Description: "d", Design: "des", AcceptanceCriteria: "ac",
				Priority: 0, IssueType: types.TypeBug, Assignee: "a", Labels: []string{"alpha"},
			},
			Dependencies: []issueops.CreateDependency{{TargetID: "bd-9", Type: types.DepBlocks}},
		}},
	}); err != nil {
		t.Fatalf("CreateBatch(): %v", err)
	}

	body := w.lastBatchCreate
	if body.Actor != "planner" {
		t.Errorf("actor = %q, want \"planner\"", body.Actor)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Items))
	}
	item := body.Items[0]
	if item.Title != "the title" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Priority == nil || *item.Priority != 0 {
		t.Errorf("priority = %v, want an explicit 0: absent means the workspace default", item.Priority)
	}
	for name, got := range map[string]*string{
		"description": item.Description, "design": item.Design,
		"acceptance_criteria": item.AcceptanceCriteria, "assignee": item.Assignee,
		"issue_type": item.IssueType,
	} {
		if got == nil {
			t.Errorf("%s was omitted for a populated field", name)
		}
	}
	if item.Labels == nil || !reflect.DeepEqual(*item.Labels, []string{"alpha"}) {
		t.Errorf("labels = %v, want [alpha]", item.Labels)
	}
	if item.Dependencies == nil || len(*item.Dependencies) != 1 {
		t.Fatalf("dependencies = %v, want one edge", item.Dependencies)
	}
	if edge := (*item.Dependencies)[0]; edge.TargetId != "bd-9" || edge.Type != string(types.DepBlocks) {
		t.Errorf("edge = %+v, want {bd-9 blocks}", edge)
	}
}

// TestBatchCreateReadsTheGeneratedIDsBackOffTheResponse pins the one fact the
// request cannot carry and every front door needs.
func TestBatchCreateReadsTheGeneratedIDsBackOffTheResponse(t *testing.T) {
	w := &stubWire{created: &apigen.BatchCreateResponse{Items: []apigen.Issue{
		{ID: "bd-11", Title: "one", Labels: []string{"alpha"}},
		{ID: "bd-12", Title: "two"},
	}}}
	result, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{Title: "one"}},
			{Issue: &issueops.Issue{Title: "two"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateBatch(): %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("Issues = %d, want 2", len(result.Issues))
	}
	for i, want := range []string{"bd-11", "bd-12"} {
		if result.Issues[i] == nil || result.Issues[i].ID != want {
			t.Errorf("Issues[%d] = %v, want id %q in request order", i, result.Issues[i], want)
		}
	}
	if labels := result.Issues[0].Labels; !reflect.DeepEqual(labels, []string{"alpha"}) {
		t.Errorf("Issues[0].Labels = %v, want [alpha]: the snapshot is promised hydrated", labels)
	}
	if result.Issues[0] == result.Issues[1] {
		t.Error("the result entries alias one issue")
	}
}

// TestBatchCreateDoesNotWriteThroughTheCallersItems pins the no-mutation
// promise, and the ID is the field that matters: the role assigns one and it
// must land on the RESULT, never on the caller's issue — an implementation that
// wrote it back would leave the caller's next create refusing its own struct as
// an occupied id.
//
// The whole request is compared, not just the id, because CreateBatchRequest
// travels by value: Items and the issues it points at are what a body could
// otherwise write through, and a label slice handed straight to a marshaler is
// one helper away from being written through too.
func TestBatchCreateDoesNotWriteThroughTheCallersItems(t *testing.T) {
	issue := &issueops.Issue{
		Title: "caller owned", Priority: 2, IssueType: types.TypeTask, Labels: []string{"kept"},
	}
	request := issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{{
			Issue:        issue,
			Dependencies: []issueops.CreateDependency{{TargetID: "bd-9", Type: types.DepBlocks}},
		}},
	}
	snapshot := issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{{
			Issue: &issueops.Issue{
				Title: "caller owned", Priority: 2, IssueType: types.TypeTask, Labels: []string{"kept"},
			},
			Dependencies: []issueops.CreateDependency{{TargetID: "bd-9", Type: types.DepBlocks}},
		}},
	}

	w := &stubWire{created: &apigen.BatchCreateResponse{Items: []apigen.Issue{
		{ID: "bd-assigned", Title: "caller owned", Labels: []string{"kept", "server-added"}},
	}}}
	result, err := bulkCreator(t, w).CreateBatch(t.Context(), request)
	if err != nil {
		t.Fatalf("CreateBatch(): %v", err)
	}
	if result.Issues[0].ID != "bd-assigned" {
		t.Errorf("the assigned id is %q on the result, want bd-assigned", result.Issues[0].ID)
	}
	if issue.ID != "" {
		t.Errorf("the caller's issue was stamped with id %q; the assigned id belongs to the result", issue.ID)
	}
	if !reflect.DeepEqual(request, snapshot) {
		t.Errorf("CreateBatch mutated the caller's request:\n got %+v\nwant %+v", *request.Items[0].Issue, *snapshot.Items[0].Issue)
	}
	// The labels on the wire are a COPY: the request body outlives this call
	// inside a marshaler, and a shared backing array would let it grow through
	// the caller's slice.
	if body := w.lastBatchCreate.Items[0].Labels; body == nil || &(*body)[0] == &issue.Labels[0] {
		t.Error("the request body aliases the caller's label slice rather than copying it")
	}
}

// TestBatchCreateRefusesAShortAnswer pins the all-or-nothing read-back. A
// server answering a different length has no partial outcome to describe, so
// the client reports it rather than indexing into it.
func TestBatchCreateRefusesAShortAnswer(t *testing.T) {
	w := &stubWire{created: &apigen.BatchCreateResponse{Items: []apigen.Issue{{ID: "bd-11"}}}}
	_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{Title: "one"}},
			{Issue: &issueops.Issue{Title: "two"}},
		},
	})
	if err == nil {
		t.Fatal("a two-item batch answered with one issue was accepted")
	}
}

// batchCreateRoleRefusedIssueMembers are the two members the ROLE refuses in
// its own right, before the wire is consulted: "Issue.Comments and
// Issue.Dependencies must be empty because edges are supplied through the
// item's own Dependencies". They earn ErrValidation rather than a ledger row,
// because a local backend refuses them too — this is not a divergence.
var batchCreateRoleRefusedIssueMembers = map[string]string{
	"Comments":     "a create batch has no way to supply comments at all",
	"Dependencies": "edges are supplied through the item's own Dependencies",
}

// TestBatchCreateRefusesEveryMemberTheWireExcludes is the refuse-not-drop gate
// for this operation, and the pin behind every one of its ledger rows.
//
// It runs three ways, and each catches a different staleness:
//
//	carried -> wire     the eight members the client claims to carry are
//	                    exactly the ones apigen.BatchCreateItem publishes for
//	                    the issue itself.
//	source -> partition every populatable field of types.Issue is carried,
//	                    ignored by the role, refused by the role, or refused by
//	                    the wire. There is no fifth arm, so a member added
//	                    upstream cannot arrive unclassified.
//	behavior            every wire-refused member really fails, citing its
//	                    ledger row, WITHOUT dialing.
func TestBatchCreateRefusesEveryMemberTheWireExcludes(t *testing.T) {
	t.Run("carried members are the wire's own", func(t *testing.T) {
		published := bodyMembers(t, reflect.TypeOf(apigen.BatchCreateItem{}))
		// `dependencies` is driven by the ITEM's own field, not by the issue.
		delete(published, "dependencies")

		var claimed []string
		for field, wireMember := range batchCreateCarriedIssueMembers {
			if _, ok := reflect.TypeOf(issueops.Issue{}).FieldByName(field); !ok {
				t.Errorf("the carried table names Issue.%s, which does not exist", field)
			}
			if !published[wireMember] {
				t.Errorf("Issue.%s claims wire member %q, which BatchCreateItem does not publish", field, wireMember)
			}
			claimed = append(claimed, wireMember)
		}
		sort.Strings(claimed)
		if want := sortedKeys(published); !reflect.DeepEqual(claimed, want) {
			t.Errorf("the client carries\n  %v\nthe wire's item publishes\n  %v", claimed, want)
		}
	})

	t.Run("the item container has exactly two halves", func(t *testing.T) {
		// The CONTAINER above types.Issue, which no other gate can see.
		//
		// issueops.BatchCreateItem is not a wire body — its Issue half FLATTENS
		// onto eight members of apigen.BatchCreateItem while its Dependencies
		// half is one — so it cannot be a writeShape: a shape row claims one
		// member per field, and the wire -> source direction would read the
		// eight as undriven. The two halves are therefore classified by
		// different mechanisms, and this is what says there are only two.
		//
		// It is not hypothetical. BatchCreateItem is "the per-item HALF of
		// CreateRequest", and CreateRequest carries ParentID,
		// InheritLabelsFromParent and WaitsFor; the day one of those is pulled
		// down onto the item, every gate below would stay green while the client
		// dropped it.
		got := populatableFields(reflect.TypeOf(issueops.BatchCreateItem{}))
		sort.Strings(got)
		want := []string{"Dependencies", "Issue"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("BatchCreateItem carries %v, want %v.\n"+
				"A new member reaches no wire member and no refusal: carry it in batchCreateItem, or refuse it with a ledger row.", got, want)
		}
	})

	refused := wireRefusedIssueMembers(t)

	t.Run("the partition covers every member", func(t *testing.T) {
		if len(refused) == 0 {
			t.Fatal("no member is wire-refused; the partition tables have swallowed the population this gate exists for")
		}
		for _, name := range []string{"ID", "Status", "Ephemeral", "Metadata", "Pinned"} {
			if !slices.Contains(refused, name) {
				t.Errorf("Issue.%s is not in the refused population; a create that dropped it is data loss", name)
			}
		}
		for field := range roleIgnoredCreateIssueMembers {
			if _, ok := reflect.TypeOf(issueops.Issue{}).FieldByName(field); !ok {
				t.Errorf("the ignored table names Issue.%s, which does not exist", field)
			}
			if _, dup := batchCreateCarriedIssueMembers[field]; dup {
				t.Errorf("Issue.%s is both carried and ignored", field)
			}
		}
	})

	t.Run("every refused member fails without dialing", func(t *testing.T) {
		for _, name := range refused {
			t.Run(name, func(t *testing.T) {
				issue := &issueops.Issue{Title: "t"}
				setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

				w := &stubWire{}
				_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
					Actor: "planner",
					Items: []issueops.BatchCreateItem{{Issue: issue}},
				})
				assertRefusedBy(t, err, "W-BatchCreateItem.Issue")
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the refusal does not name the member: %v", err)
				}
				if len(w.calls) != 0 {
					t.Errorf("the refused member reached the wire: %v", w.calls)
				}
			})
		}
	})

	t.Run("a CreatedBy naming the actor rides the server's stamp", func(t *testing.T) {
		// The server stamps every item's created_by from the actor, so that one
		// value is carried by the stamp; the sweep above refuses any other.
		w := &stubWire{}
		if _, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
			Actor: "planner",
			Items: []issueops.BatchCreateItem{{Issue: &issueops.Issue{Title: "t", CreatedBy: "planner"}}},
		}); err != nil {
			t.Fatalf("CreateBatch with CreatedBy == Actor = %v, want it carried by the server's stamp", err)
		}
		if w.lastBatchCreate.Actor != "planner" {
			t.Errorf("sent actor = %q, want the creator the stamp will write", w.lastBatchCreate.Actor)
		}
	})

	t.Run("the role's own two are ErrValidation", func(t *testing.T) {
		for name := range batchCreateRoleRefusedIssueMembers {
			issue := &issueops.Issue{Title: "t"}
			setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

			w := &stubWire{}
			_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
				Actor: "planner",
				Items: []issueops.BatchCreateItem{{Issue: issue}},
			})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("Issue.%s error = %v, want ErrValidation — a local backend refuses it too", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("Issue.%s reached the wire: %v", name, w.calls)
			}
		}
	})

	t.Run("the request's own two", func(t *testing.T) {
		for _, tc := range []struct {
			row     string
			request issueops.CreateBatchRequest
		}{
			{"W-CreateBatchRequest.Provenance", issueops.CreateBatchRequest{Provenance: "bd: create from plan.md"}},
			{"W-CreateBatchRequest.ForceIDPrefix", issueops.CreateBatchRequest{ForceIDPrefix: true}},
		} {
			req := tc.request
			req.Actor = "planner"
			req.Items = []issueops.BatchCreateItem{{Issue: &issueops.Issue{Title: "t"}}}

			w := &stubWire{}
			_, err := bulkCreator(t, w).CreateBatch(t.Context(), req)
			assertRefusedBy(t, err, tc.row)
			if len(w.calls) != 0 {
				t.Errorf("%s reached the wire: %v", tc.row, w.calls)
			}
		}
	})

	t.Run("the edge's three", func(t *testing.T) {
		for _, tc := range []struct {
			row  string
			edge issueops.CreateDependency
		}{
			{"W-CreateDependency.Reverse", issueops.CreateDependency{TargetID: "bd-9", Type: types.DepBlocks, Reverse: true}},
			{"W-CreateDependency.Metadata", issueops.CreateDependency{TargetID: "bd-9", Type: types.DepBlocks, Metadata: `{"gate":"x"}`}},
			{"W-CreateDependency.ThreadID", issueops.CreateDependency{TargetID: "bd-9", Type: types.DepBlocks, ThreadID: "th-1"}},
		} {
			w := &stubWire{}
			_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
				Actor: "planner",
				Items: []issueops.BatchCreateItem{{
					Issue:        &issueops.Issue{Title: "t"},
					Dependencies: []issueops.CreateDependency{tc.edge},
				}},
			})
			assertRefusedBy(t, err, tc.row)
			if len(w.calls) != 0 {
				t.Errorf("%s reached the wire: %v", tc.row, w.calls)
			}
		}
	})
}

// TestBatchCreateCannotNameAnItemOfItsOwnBatch is ledger row
// L-batchcreate-inbatch's pin.
//
// The role's headline capability — an edge onto an item created EARLIER in the
// same request — needs that earlier item to have named an id for itself, and
// the wire's item publishes no id member. The gap is in the SCHEMA rather than
// the handler: `target_id`'s own description names this case, so it is worth
// asserting structurally as well as behaviorally.
func TestBatchCreateCannotNameAnItemOfItsOwnBatch(t *testing.T) {
	published := bodyMembers(t, reflect.TypeOf(apigen.BatchCreateItem{}))
	if published["id"] {
		t.Fatal("BatchCreateItem publishes an id member; an in-batch edge is expressible and L-batchcreate-inbatch retires")
	}

	w := &stubWire{}
	_, err := bulkCreator(t, w).CreateBatch(t.Context(), issueops.CreateBatchRequest{
		Actor: "planner",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{ID: "bd-first", Title: "the blocker"}},
			{
				Issue:        &issueops.Issue{Title: "the blocked"},
				Dependencies: []issueops.CreateDependency{{TargetID: "bd-first", Type: types.DepBlocks}},
			},
		},
	})
	assertRefusedBy(t, err, "W-BatchCreateItem.Issue")
	if len(w.calls) != 0 {
		t.Errorf("the explicit id reached the wire: %v", w.calls)
	}
}

// wireRefusedIssueMembers is the complement the gate above drives: every
// populatable field of types.Issue that is neither carried by the wire, nor
// ignored by the role, nor refused by the role in its own right.
func wireRefusedIssueMembers(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range populatableFields(reflect.TypeOf(issueops.Issue{})) {
		if _, ok := batchCreateCarriedIssueMembers[name]; ok {
			continue
		}
		if _, ok := roleIgnoredCreateIssueMembers[name]; ok {
			continue
		}
		if _, ok := batchCreateRoleRefusedIssueMembers[name]; ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

// assertRefusedBy checks a refusal is the TYPED one and cites the row it
// should. A refusal that merely matched ErrRefused would pass while citing the
// wrong divergence, which is how a ledger row stops describing anything.
func assertRefusedBy(t *testing.T, err error, row string) {
	t.Helper()
	var refusal *encode.RefusedError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want *encode.RefusedError citing %s", err, row)
	}
	if refusal.Row.ID != row {
		t.Errorf("refusal cites ledger row %s, want %s", refusal.Row.ID, row)
	}
}

// assertEveryFieldPopulated fails on any zero-valued field of a projected
// result, which is how a dropped member of an unpinned wire body shows up.
//
// skip names fields with no wire-side counterpart AT ALL — not a member this
// projection dropped, but one the wire body never published in the first
// place, so a fully populated wire body can never make the field non-zero no
// matter what the projection does. Each one must be backed by its own
// divergence-ledger row on the REQUEST side (the member the server would need
// to be ASKED to produce this answer), or this exemption is just a quieter way
// to drop it.
func assertEveryFieldPopulated(t *testing.T, name string, value reflect.Value, skip ...string) {
	t.Helper()
	shape := value.Type()
	for i := range shape.NumField() {
		field := shape.Field(i)
		if !field.IsExported() {
			continue
		}
		if slices.Contains(skip, field.Name) {
			continue
		}
		if value.Field(i).IsZero() {
			t.Errorf("%s.%s is zero after projecting a fully populated wire body; the member was dropped",
				name, field.Name)
		}
	}
}

// setNonZero populates one field with a value distinguishable from its zero, so
// the refusal sweep can ask "was this member honored?" of any shape the struct
// grows. It fails rather than skipping on a kind it does not know: a silently
// unset field would make its subtest assert nothing.
func setNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	if !v.CanSet() {
		t.Fatalf("cannot set a %s", v.Type())
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("sentinel")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			v.Set(reflect.ValueOf(json.RawMessage(`{"k":"v"}`)))
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)))
			return
		}
		t.Fatalf("no non-zero value for struct %s", v.Type())
	default:
		t.Fatalf("no non-zero value for kind %s (%s)", v.Kind(), v.Type())
	}
}
