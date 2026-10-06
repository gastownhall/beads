//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_batch_apply_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The BatchApplier contract, run through client -> in-process bd serve ->
// reference store: the widest role on this surface and the last of the wire
// wave's three, wired by client wave ga-mijra.
//
// WHAT THE COMPOSITION BUYS HERE that the three in-tree legs cannot. Those legs
// run two bodies through different transaction wrappers; this one adds a fourth
// wrapper that is a NETWORK, and the states it can lose are exactly the ones
// this contract is built to separate: the ORDER of a heterogeneous plan, which
// becomes an array on a JSON wire and comes back positional; the row-version
// tokens, which are 64-bit and would corrupt through a float; the absent-versus-
// zero distinction on every guard; a metadata value's source literal; and the
// identity of the item that refused, which on an all-or-nothing operation
// exists nowhere but the problem document's item_* members.
//
// ONE ENVIRONMENT PER CASE, like the other per-role families here: the contract
// namespaces its ids by prefix precisely so it can be run that way, and a
// shared database would turn an id collision between two contracts into a
// debugging session.

// applyParkBead owns this family's one remaining park: L-apply-ref's unparks
// when the wire grows a typed ref-key member on the problem document.
// W-DepAddItem.HasSpawner's park UNPARKED under issues.batchApply.depAddLineage
// (S5) — ApplyDepAddItem now publishes a spawner member — and
// TestServedBatchApplyStampsSpawnerIDOnlyWhenNamed runs unskipped below.
const applyParkBead = "ga-mijra"

func newServedBatchApplyFixture(t *testing.T, prefix string) conformance.BatchApplyFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	applier, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	return conformance.BatchApplyFixture{
		IssuePrefix:          env.prefix,
		BatchApplier:         applier,
		CreateIssue:          env.createIssue,
		CreateWisp:           env.createWisp,
		QueryScalar:          env.queryScalar,
		CountHistory:         env.countHistory,
		CountHistoryMatching: env.countHistoryMatching,
		CommitPending:        env.commitPending,
	}
}

func TestServedBatchApplyAppliesEveryItemInDeclarationOrder(t *testing.T) {
	conformance.RunBatchApplyAppliesEveryItemInDeclarationOrder(t, t.Context(), newServedBatchApplyFixture(t, "hba00"))
}

func TestServedBatchApplyBindsEachNamedKeyToItsMintedID(t *testing.T) {
	conformance.RunBatchApplyBindsEachNamedKeyToItsMintedID(t, t.Context(), newServedBatchApplyFixture(t, "hba01"))
}

func TestServedBatchApplyResolvesABackwardKeyRef(t *testing.T) {
	conformance.RunBatchApplyResolvesABackwardKeyRef(t, t.Context(), newServedBatchApplyFixture(t, "hba02"))
}

func TestServedBatchApplyRefusesAKeyDeclaredLater(t *testing.T) {
	conformance.RunBatchApplyRefusesAKeyDeclaredLater(t, t.Context(), newServedBatchApplyFixture(t, "hba03"))
}

func TestServedBatchApplyRefusesAKeyNoItemDeclares(t *testing.T) {
	conformance.RunBatchApplyRefusesAKeyNoItemDeclares(t, t.Context(), newServedBatchApplyFixture(t, "hba04"))
}

func TestServedBatchApplyRefusesARefNamingNeitherOrBoth(t *testing.T) {
	conformance.RunBatchApplyRefusesARefNamingNeitherOrBoth(t, t.Context(), newServedBatchApplyFixture(t, "hba05"))
}

func TestServedBatchApplyRollsBackEverythingWhenTheLastItemRefuses(t *testing.T) {
	conformance.RunBatchApplyRollsBackEverythingWhenTheLastItemRefuses(t, t.Context(), newServedBatchApplyFixture(t, "hba06"))
}

func TestServedBatchApplyNeverReordersItsItems(t *testing.T) {
	conformance.RunBatchApplyNeverReordersItsItems(t, t.Context(), newServedBatchApplyFixture(t, "hba07"))
}

func TestServedBatchApplyEndGateRefusesAHierarchyTheRequestBuilt(t *testing.T) {
	conformance.RunBatchApplyEndGateRefusesAHierarchyTheRequestBuilt(t, t.Context(), newServedBatchApplyFixture(t, "hba08"))
}

func TestServedBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck(t *testing.T) {
	conformance.RunBatchApplyEndGateCycleSurvivesSkipPerEdgeCycleCheck(t, t.Context(), newServedBatchApplyFixture(t, "hba09"))
}

func TestServedBatchApplyExpectedVersionThatMatchesLetsTheItemThrough(t *testing.T) {
	conformance.RunBatchApplyExpectedVersionThatMatchesLetsTheItemThrough(t, t.Context(), newServedBatchApplyFixture(t, "hba0a"))
}

func TestServedBatchApplyStaleExpectedVersionRefusesTheWholeRequest(t *testing.T) {
	conformance.RunBatchApplyStaleExpectedVersionRefusesTheWholeRequest(t, t.Context(), newServedBatchApplyFixture(t, "hba0b"))
}

func TestServedBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched(t *testing.T) {
	conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemTouched(t, t.Context(), newServedBatchApplyFixture(t, "hba0c"))
}

func TestServedBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated(t *testing.T) {
	conformance.RunBatchApplyRefusesExpectedVersionOnARowAnEarlierItemCreated(t, t.Context(), newServedBatchApplyFixture(t, "hba0d"))
}

func TestServedBatchApplyEvaluatesExpectedStatusAsModified(t *testing.T) {
	conformance.RunBatchApplyEvaluatesExpectedStatusAsModified(t, t.Context(), newServedBatchApplyFixture(t, "hba0e"))
}

func TestServedBatchApplyEvaluatesExpectedAssigneeAsModified(t *testing.T) {
	conformance.RunBatchApplyEvaluatesExpectedAssigneeAsModified(t, t.Context(), newServedBatchApplyFixture(t, "hba0f"))
}

func TestServedBatchApplyClosePolicyEvaluatesAtTheCloseItem(t *testing.T) {
	conformance.RunBatchApplyClosePolicyEvaluatesAtTheCloseItem(t, t.Context(), newServedBatchApplyFixture(t, "hba10"))
}

func TestServedBatchApplyAllowsAClosedParentToGainAnOpenChild(t *testing.T) {
	conformance.RunBatchApplyAllowsAClosedParentToGainAnOpenChild(t, t.Context(), newServedBatchApplyFixture(t, "hba11"))
}

func TestServedBatchApplyUpdateAfterCloseInOneRequest(t *testing.T) {
	conformance.RunBatchApplyUpdateAfterCloseInOneRequest(t, t.Context(), newServedBatchApplyFixture(t, "hba12"))
}

func TestServedBatchApplyReportsChangedPerItem(t *testing.T) {
	conformance.RunBatchApplyReportsChangedPerItem(t, t.Context(), newServedBatchApplyFixture(t, "hba13"))
}

func TestServedBatchApplyANoOpBatchRecordsNoHistory(t *testing.T) {
	conformance.RunBatchApplyANoOpBatchRecordsNoHistory(t, t.Context(), newServedBatchApplyFixture(t, "hba14"))
}

func TestServedBatchApplyRecordsOneEntryForAWriteThatLandedNothing(t *testing.T) {
	conformance.RunBatchApplyRecordsOneEntryForAWriteThatLandedNothing(t, t.Context(), newServedBatchApplyFixture(t, "hba15"))
}

func TestServedBatchApplyRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunBatchApplyRecordsExactlyOneHistoryEntry(t, t.Context(), newServedBatchApplyFixture(t, "hba16"))
}

func TestServedBatchApplyHistoryNamesTheActorAndReadsTheProvenance(t *testing.T) {
	conformance.RunBatchApplyHistoryNamesTheActorAndReadsTheProvenance(t, t.Context(), newServedBatchApplyFixture(t, "hba17"))
}

func TestServedBatchApplyARefusedRequestRecordsNoHistory(t *testing.T) {
	conformance.RunBatchApplyARefusedRequestRecordsNoHistory(t, t.Context(), newServedBatchApplyFixture(t, "hba18"))
}

func TestServedBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory(t *testing.T) {
	conformance.RunBatchApplyAnEphemeralBatchKeepsItsWispsAndRecordsNoDurableHistory(t, t.Context(), newServedBatchApplyFixture(t, "hba19"))
}

func TestServedBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated(t *testing.T) {
	conformance.RunBatchApplyRefusesACrossPlaneEdgeBetweenRowsItCreated(t, t.Context(), newServedBatchApplyFixture(t, "hba1a"))
}

func TestServedBatchApplyAcceptsAnExternalEdgeTarget(t *testing.T) {
	conformance.RunBatchApplyAcceptsAnExternalEdgeTarget(t, t.Context(), newServedBatchApplyFixture(t, "hba1b"))
}

func TestServedBatchApplyNormalizesTheWaitsForGate(t *testing.T) {
	conformance.RunBatchApplyNormalizesTheWaitsForGate(t, t.Context(), newServedBatchApplyFixture(t, "hba1c"))
}

func TestServedBatchApplySplicesAForwardMetadataRef(t *testing.T) {
	conformance.RunBatchApplySplicesAForwardMetadataRef(t, t.Context(), newServedBatchApplyFixture(t, "hba1d"))
}

func TestServedBatchApplySplicesASelfMetadataRef(t *testing.T) {
	conformance.RunBatchApplySplicesASelfMetadataRef(t, t.Context(), newServedBatchApplyFixture(t, "hba1e"))
}

// TestServedBatchApplyRefusesAMetadataRefNoItemDeclares is the family's
// response-shape park, a divergence of shape rather than of behavior: the
// refusal happens, nothing is written, and the diagnosis a caller ACTS on —
// DeclaredLater — arrives whole. What the wire cannot carry is WHICH entry of
// the refs map failed.
func TestServedBatchApplyRefusesAMetadataRefNoItemDeclares(t *testing.T) {
	skipKnownDivergence(t, "L-apply-ref", applyParkBead,
		"the case binds RefError.Member to `metadata_ref <key>`, and the wire names the MEMBER that held the bad ref "+
			"(`items[0].create.metadata_refs`) without saying which entry of it failed — RefError.Member is diagnostic "+
			"prose on both sides rather than a vocabulary, so the server maps it onto the document's own member names. "+
			"The refusal itself, the untouched tables and the false DeclaredLater are asserted by "+
			"TestServedBatchApplyRefusesAMetadataRefWithoutNamingTheKey.")
	// The call stays BELOW the skip and compiled, which is this package's park
	// convention and is load-bearing rather than decorative: a parked case that
	// only named its contract in prose is a dangling reference nothing
	// type-checks, so a renamed entrypoint or a changed fixture would orphan the
	// park silently. Here it breaks the build.
	conformance.RunBatchApplyRefusesAMetadataRefNoItemDeclares(t, t.Context(), newServedBatchApplyFixture(t, "hba1f"))
}

func TestServedBatchApplyTheSpliceRecordsAnUpdateEvent(t *testing.T) {
	conformance.RunBatchApplyTheSpliceRecordsAnUpdateEvent(t, t.Context(), newServedBatchApplyFixture(t, "hba20"))
}

func TestServedBatchApplyKeepsAStoredNullApartFromAnEmptyString(t *testing.T) {
	conformance.RunBatchApplyKeepsAStoredNullApartFromAnEmptyString(t, t.Context(), newServedBatchApplyFixture(t, "hba21"))
}

func TestServedBatchApplyLandsAnIdempotencyRecordWithItsWork(t *testing.T) {
	conformance.RunBatchApplyLandsAnIdempotencyRecordWithItsWork(t, t.Context(), newServedBatchApplyFixture(t, "hba22"))
}

func TestServedBatchApplyBoundsTheItemCount(t *testing.T) {
	fixture := newServedBatchApplyFixture(t, "hba23")
	if raceEnabled {
		// Under -race the full 1000-item apply outlasts the client's 60s
		// per-request timeout (wire.DefaultTimeout) on the embedded lane's
		// executors, so only the "at bound" half's applied count drops, to
		// 150 (the whole case then took 26s there), as the embedded tier's
		// own TestBatchApplyContract does; see
		// RunBatchApplyBoundsTheItemCountAtScale for what that leaves pinned.
		// The refusing half still sends MaxApplyBatchItems+1 over the wire,
		// and scripts/conformance.sh runs the full size without -race.
		conformance.RunBatchApplyBoundsTheItemCountAtScale(t, t.Context(), fixture, 150)
		return
	}
	conformance.RunBatchApplyBoundsTheItemCount(t, t.Context(), fixture)
}

func TestServedBatchApplyReplayMintsANewSetOfRows(t *testing.T) {
	conformance.RunBatchApplyReplayMintsANewSetOfRows(t, t.Context(), newServedBatchApplyFixture(t, "hba24"))
}

func TestServedBatchApplyDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunBatchApplyDoesNotMutateTheCallerRequest(t, t.Context(), newServedBatchApplyFixture(t, "hba25"))
}

func TestServedBatchApplyRefusesAnUnusableRequest(t *testing.T) {
	conformance.RunBatchApplyRefusesAnUnusableRequest(t, t.Context(), newServedBatchApplyFixture(t, "hba26"))
}

// TestServedBatchApplyStampsSpawnerIDOnlyWhenNamed wires the dep_add
// HasSpawner/ThreadID lineage conformance case onto the http leg (S5): a
// served default Config always advertises CapBatchApplyDepAddLineage (it is
// in httpapi's unconditional behaviorCapabilities), so this exercises the
// member on the WIRE, round-tripping through refuseUnservedDepAddLineage's
// pre-dial check (which never fires here, since the capability is present)
// and applyDepAddItemBody's encoding, server-side batch_apply.go decode, and
// the role's own StampWaitsForSpawnerID.
func TestServedBatchApplyStampsSpawnerIDOnlyWhenNamed(t *testing.T) {
	conformance.RunBatchApplyStampsSpawnerIDOnlyWhenNamed(t, t.Context(), newServedBatchApplyFixture(t, "hba27"))
}

// TestServedBatchApplyCarriesThreadIDOntoTheStoredEdge wires
// DepAddItem.ThreadID's round trip onto the http leg (S5, 2026-10 Opus-review
// HIGH-2): this is the ONE case that proves the field survives BOTH sides of
// the wire at once — the client's applyDepAddItemBody encode and the server's
// applyDepAddItemMembers decode — since either one dropping thread_id leaves
// the raw dependencies row's thread_id column blank and fails the assertion.
func TestServedBatchApplyCarriesThreadIDOntoTheStoredEdge(t *testing.T) {
	conformance.RunBatchApplyCarriesThreadIDOntoTheStoredEdge(t, t.Context(), newServedBatchApplyFixture(t, "hba28"))
}

// TestServedBatchApplyCarriesThreadIDOntoAnExistingEdge wires the re-add arm of
// the same round trip: a thread named on a dep_add whose edge already exists
// must cross the wire and reach the stored row, not stop at the server's
// change-free check.
func TestServedBatchApplyCarriesThreadIDOntoAnExistingEdge(t *testing.T) {
	conformance.RunBatchApplyCarriesThreadIDOntoAnExistingEdge(t, t.Context(), newServedBatchApplyFixture(t, "hba29"))
}

// TestServedBatchApplyRefusesAThreadIDLongerThanItsColumn wires the thread_id
// bound onto the http leg, where the server's decode refuses it before the
// role's plan would; both have to arrive as the same ErrValidation.
func TestServedBatchApplyRefusesAThreadIDLongerThanItsColumn(t *testing.T) {
	conformance.RunBatchApplyRefusesAThreadIDLongerThanItsColumn(t, t.Context(), newServedBatchApplyFixture(t, "hba2a"))
}

// TestServedBatchApplyRefusesAMetadataRefWithoutNamingTheKey is the running pin
// beside the park above: the same request, driven end to end, asserting
// everything the contract asserts EXCEPT the key inside the member — the
// refusal is a *RefError matching ErrValidation, DeclaredLater is false, the
// item is named, and nothing was written.
//
// It is written so it CAN fail: the positive half re-runs the identical plan
// with the ref pointed at a key the request does declare, and the row lands
// with the resolved id spliced over the metadata key. A client that refused
// every metadata ref would pass the refusal half alone.
func TestServedBatchApplyRefusesAMetadataRefWithoutNamingTheKey(t *testing.T) {
	ctx := t.Context()
	fixture := newServedBatchApplyFixture(t, "hbaref")

	id := fixture.IssuePrefix + "-ghostmeta-row"
	item := issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
		Key:          "only",
		Issue:        &issueops.Issue{ID: id, Title: "names a key nothing declares", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		MetadataRefs: map[string]issueops.Ref{"gc.retry_of": {Key: "never-declared"}},
	}}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor: "apply-writer", ForceIDPrefix: true, Items: []issueops.ApplyItem{item},
	})
	var refErr *issueops.RefError
	if !errors.As(err, &refErr) {
		t.Fatalf("a metadata_ref naming an undeclared key: error = %v, want *RefError", err)
	}
	if refErr.DeclaredLater {
		t.Errorf("RefError = %#v, want DeclaredLater false: no item declares that key at all", refErr)
	}
	if refErr.Index != 0 {
		t.Errorf("RefError.Index = %d, want 0", refErr.Index)
	}
	if refErr.Key != "never-declared" {
		t.Errorf("RefError.Key = %q, want the unresolvable key", refErr.Key)
	}
	// The degraded half, asserted rather than left implicit: the member is
	// named without the key inside it (L-apply-ref). Asserting it here is what
	// makes the ledger row falsifiable — the day the wire carries the key, this
	// fails and the park retires with it.
	if refErr.Member != "metadata_refs" {
		t.Errorf("RefError.Member = %q, want the member alone; L-apply-ref records that the key is not on the wire", refErr.Member)
	}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation through RefError.Unwrap", err)
	}
	var rows int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{id}, &rows); err != nil {
		t.Fatalf("counting the refused row: %v", err)
	}
	if rows != 0 {
		t.Errorf("the refused plan wrote %s anyway", id)
	}

	// The positive half. The identical plan with the ref pointed at a key the
	// request DOES declare lands, and the splice writes the resolved id.
	peer := fixture.IssuePrefix + "-ghostmeta-peer"
	item.Create.MetadataRefs = map[string]issueops.Ref{"gc.retry_of": {Key: "peer"}}
	if _, err := fixture.BatchApplier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor: "apply-writer", ForceIDPrefix: true,
		Items: []issueops.ApplyItem{item, {Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
			Key:   "peer",
			Issue: &issueops.Issue{ID: peer, Title: "the key it names", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}}},
	}); err != nil {
		t.Fatalf("the same plan with a declared key refused: %v", err)
	}
	var metadata string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(metadata, '') FROM issues WHERE id = ?", []any{id}, &metadata); err != nil {
		t.Fatalf("reading the spliced metadata: %v", err)
	}
	if !strings.Contains(metadata, peer) {
		t.Errorf("metadata = %q, want the resolved id %s spliced over gc.retry_of", metadata, peer)
	}
}

// TestServedBatchApplyStampsEachCreateItemsCreatedByFromTheActor is the served
// half of created_by on the plan: ApplyCreateItem publishes no created_by, so
// the stored creator exists only because the server stamps it from the actor
// (internal/httpapi's batch_apply.go). A create item whose CreatedBy names that
// actor — the shape the graph apply sends — is carried by the stamp rather than
// refused.
func TestServedBatchApplyStampsEachCreateItemsCreatedByFromTheActor(t *testing.T) {
	ctx := t.Context()
	env := newServedEnv(t, "hbacby")
	applier, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}

	res, err := applier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor: "plan-writer",
		Items: []issueops.ApplyItem{{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
			Key:   "planned",
			Issue: &issueops.Issue{Title: "planned", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedBy: "plan-writer"},
		}}},
	})
	if err != nil {
		t.Fatalf("ApplyBatch in the graph apply's shape = %v, want it served", err)
	}
	id := res.Keys["planned"]
	if id == "" {
		t.Fatalf("Keys = %v, want the minted id bound to %q", res.Keys, "planned")
	}
	stored, err := env.getIssue(ctx, id)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	if stored.CreatedBy != "plan-writer" {
		t.Errorf("stored created_by = %q, want the actor %q", stored.CreatedBy, "plan-writer")
	}
}

// TestServedBatchApplyAnswersEveryItemWithoutASnapshot is the running pin on
// ledger row L-apply-snapshot, and it is a DIFFERENTIAL rather than a golden:
// the same plan is applied through the client and through the reference
// store's own role, and the two results are compared member for member EXCEPT
// the snapshot. What the row admits — that Issue is nil on this leg — is
// asserted beside a local run where it is not, so the divergence is measured
// rather than declared.
//
// The positive half is the whole comparison: a client that answered an empty
// result, or dropped a revision, or lost the edge's DependsOnID, would satisfy
// "no snapshot" and fail here.
func TestServedBatchApplyAnswersEveryItemWithoutASnapshot(t *testing.T) {
	ctx := t.Context()
	env := newServedEnv(t, "hbasnap")
	remote, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	local, err := env.reference.BatchApplier()
	if err != nil {
		t.Fatalf("reference BatchApplier(): %v", err)
	}

	plan := func(tag string) issueops.ApplyBatchRequest {
		root := env.prefix + "-snap-" + tag + "-root"
		leaf := env.prefix + "-snap-" + tag + "-leaf"
		issue := func(id string) *issueops.Issue {
			return &issueops.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		}
		return issueops.ApplyBatchRequest{
			Actor: "apply-writer", ForceIDPrefix: true,
			Items: []issueops.ApplyItem{
				{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "root", Issue: issue(root)}},
				{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "leaf", Issue: issue(leaf)}},
				{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
					Source: issueops.Ref{Key: "leaf"}, Target: issueops.Ref{Key: "root"}, Type: types.DepParentChild,
				}},
				{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{Key: "leaf"}, Reason: "done"}},
			},
		}
	}

	through, err := remote.ApplyBatch(ctx, plan("wire"))
	if err != nil {
		t.Fatalf("ApplyBatch over the wire: %v", err)
	}
	beside, err := local.ApplyBatch(ctx, plan("local"))
	if err != nil {
		t.Fatalf("ApplyBatch through the reference store: %v", err)
	}

	if len(through.Items) != len(beside.Items) {
		t.Fatalf("the wire answered %d items and the reference %d", len(through.Items), len(beside.Items))
	}
	if len(through.Keys) != len(beside.Keys) {
		t.Errorf("the wire bound %d keys and the reference %d", len(through.Keys), len(beside.Keys))
	}
	for key, id := range through.Keys {
		if id == "" {
			t.Errorf("Keys[%q] is empty; the key binding is the one fact the request cannot carry", key)
		}
	}
	for i := range through.Items {
		remoteItem, localItem := through.Items[i], beside.Items[i]
		if remoteItem.Kind != localItem.Kind || remoteItem.Changed != localItem.Changed {
			t.Errorf("items[%d]: wire %s/changed=%v, reference %s/changed=%v",
				i, remoteItem.Kind, remoteItem.Changed, localItem.Kind, localItem.Changed)
		}
		if (remoteItem.DependsOnID == "") != (localItem.DependsOnID == "") {
			t.Errorf("items[%d]: wire DependsOnID %q, reference %q — the edge target is set for dep_add and absent elsewhere",
				i, remoteItem.DependsOnID, localItem.DependsOnID)
		}
		if (remoteItem.RowVersion == 0) != (localItem.RowVersion == 0) {
			t.Errorf("items[%d]: wire revision %d, reference %d — a dep_add is 0 on both and a row item is not on either",
				i, remoteItem.RowVersion, localItem.RowVersion)
		}
		// The divergence the row admits, measured in both directions at once.
		if remoteItem.Issue != nil {
			t.Errorf("items[%d] came back with a snapshot; the wire result is lean (L-apply-snapshot)", i)
		}
		if localItem.Kind != issueops.ItemDepAdd && localItem.Issue == nil {
			t.Errorf("items[%d] carries no snapshot on the REFERENCE leg either; this case would then be asserting "+
				"nothing about the divergence L-apply-snapshot records", i)
		}
	}
}

// TestServedBatchApplyGcCloseWithMetadataIfMatch pins the first of DESIGN
// §3.8's two gc mappings (2026-10 Opus-review HIGH-3, no new bd surface): gc's
// RAW-transaction CloseWithMetadataIfMatch maps onto BatchApplier[update{Patch
// Metadata, ExpectedVersion}, close{}] in ONE request — see the BatchApplier
// package doc.
//
// It is atomic because the whole request is: the matching-version half lands
// both the metadata write and the close together, and the stale-version half
// below refuses the update, which refuses the request, so the close beside it
// never runs and NEITHER change lands.
func TestServedBatchApplyGcCloseWithMetadataIfMatch(t *testing.T) {
	ctx := t.Context()
	fixture := newServedBatchApplyFixture(t, "hbagc0")

	id := fixture.IssuePrefix + "-close-with-metadata"
	if err := fixture.CreateIssue(ctx, &types.Issue{ID: id, Title: "closed with a metadata guard", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "apply-seed"); err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
	var version int64
	if err := fixture.QueryScalar(ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{id}, &version); err != nil {
		t.Fatalf("reading row_lock for %s: %v", id, err)
	}

	_, err := fixture.BatchApplier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemUpdate, Update: &issueops.UpdateItem{
				Target: issueops.Ref{ID: id},
				Patch: issueops.IssuePatch{Metadata: issueops.MetadataPatch{
					Merge: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"closed_via":"gc"}`)},
				}},
				ExpectedVersion: &version,
			}},
			{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{ID: id}}},
		},
	})
	if err != nil {
		t.Fatalf("CloseWithMetadataIfMatch with the current version: %v", err)
	}
	if got := batchApplyColumnDirect(t, ctx, &fixture, "status", id); got != string(types.StatusClosed) {
		t.Errorf("status = %q, want %q", got, types.StatusClosed)
	}
	if got := batchApplyColumnDirect(t, ctx, &fixture, "metadata", id); !strings.Contains(got, "closed_via") {
		t.Errorf("metadata = %q, want it to carry the merged key", got)
	}

	// The stale half: the same shape, a version that is no longer current. The
	// WHOLE request refuses and ZERO rows change — neither the metadata merge
	// nor the close.
	id2 := fixture.IssuePrefix + "-close-with-metadata-stale"
	if err := fixture.CreateIssue(ctx, &types.Issue{ID: id2, Title: "a stale guard leaves zero changes", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "apply-seed"); err != nil {
		t.Fatalf("seeding %s: %v", id2, err)
	}
	var version2 int64
	if err := fixture.QueryScalar(ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{id2}, &version2); err != nil {
		t.Fatalf("reading row_lock for %s: %v", id2, err)
	}
	stale := version2 - 1
	_, err = fixture.BatchApplier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemUpdate, Update: &issueops.UpdateItem{
				Target: issueops.Ref{ID: id2},
				Patch: issueops.IssuePatch{Metadata: issueops.MetadataPatch{
					Merge: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"closed_via":"gc"}`)},
				}},
				ExpectedVersion: &stale,
			}},
			{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{ID: id2}}},
		},
	})
	if !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Fatalf("a stale ExpectedVersion: error = %v, want ErrVersionMismatch", err)
	}
	if got := batchApplyColumnDirect(t, ctx, &fixture, "status", id2); got == string(types.StatusClosed) {
		t.Error("the close landed despite the whole request refusing on the stale guard")
	}
	if got := batchApplyColumnDirect(t, ctx, &fixture, "metadata", id2); strings.Contains(got, "closed_via") {
		t.Error("the metadata merge landed despite the whole request refusing on the stale guard")
	}
}

// batchApplyColumnDirect reads one issues column directly, for the two gc-
// mapping tests above: they are plain package tests rather than
// conformance.Run* cases (the composition is http-leg-only, DESIGN §3.8), so
// they cannot reach the conformance package's unexported batchApplyColumn.
func batchApplyColumnDirect(t *testing.T, ctx context.Context, fixture *conformance.BatchApplyFixture, column, id string) string {
	t.Helper()
	var value string
	//nolint:gosec // G201: column is one of this file's own hardcoded names.
	query := "SELECT COALESCE(" + column + ", '') FROM issues WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, &value); err != nil {
		t.Fatalf("reading issues.%s for %s: %v", column, id, err)
	}
	return value
}

// TestServedBatchApplyGcApplyGraphPlan pins the second of DESIGN §3.8's two gc
// mappings (2026-10 Opus-review HIGH-3): gc's RAW RunInTransaction graph-build
// maps onto one BatchApplier request carrying every item kind the plan needs —
// a keyed create, a MetadataRefs splice, a dep_add edge, a parent-child
// dep_add edge, and a trailing update for AssignAfterCreate — landing
// together as the one plan a caller composed.
func TestServedBatchApplyGcApplyGraphPlan(t *testing.T) {
	ctx := t.Context()
	fixture := newServedBatchApplyFixture(t, "hbagc1")

	epic := fixture.IssuePrefix + "-plan-epic"
	task := fixture.IssuePrefix + "-plan-task"
	blocker := fixture.IssuePrefix + "-plan-blocker"

	result, err := fixture.BatchApplier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []issueops.ApplyItem{
			// A keyed create: the plan's root node.
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Key:   "epic",
				Issue: &issueops.Issue{ID: epic, Title: "the plan's root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			}},
			// A second node whose metadata reaches BACKWARD to the epic's minted
			// id via MetadataRefs — the splice CreateItem.MetadataRefs documents.
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Key:          "task",
				Issue:        &issueops.Issue{ID: task, Title: "spliced to its epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				MetadataRefs: map[string]issueops.Ref{"epic_id": {Key: "epic"}},
			}},
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Key:   "blocker",
				Issue: &issueops.Issue{ID: blocker, Title: "blocks the task", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			}},
			// A dep_add edge, plain.
			{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
				Source: issueops.Ref{Key: "task"}, Target: issueops.Ref{Key: "blocker"}, Type: issueops.DepBlocks,
			}},
			// A dep_add edge of type parent-child — "parent-child included", same
			// item kind as the one above, a different Type.
			{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
				Source: issueops.Ref{Key: "task"}, Target: issueops.Ref{Key: "epic"}, Type: issueops.DepParentChild,
			}},
			// A trailing update for AssignAfterCreate, resolving the key BACKWARD
			// to the row the first items in this same request just minted.
			{Kind: issueops.ItemUpdate, Update: &issueops.UpdateItem{
				Target: issueops.Ref{Key: "task"},
				Patch:  issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true, Value: "alice"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyGraphPlan: %v", err)
	}
	if len(result.Items) != 6 {
		t.Fatalf("ApplyBatch(6 items) returned %d item results", len(result.Items))
	}

	var metadata string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(metadata, '') FROM issues WHERE id = ?", []any{task}, &metadata); err != nil {
		t.Fatalf("reading the spliced metadata: %v", err)
	}
	if !strings.Contains(metadata, result.Keys["epic"]) {
		t.Errorf("task metadata = %q, want the epic's minted id %s spliced over epic_id", metadata, result.Keys["epic"])
	}

	var edges int
	const edgeQuery = `SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = ?
		AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`
	if err := fixture.QueryScalar(ctx, edgeQuery, []any{task, "blocks", blocker}, &edges); err != nil {
		t.Fatalf("counting the blocks edge: %v", err)
	}
	if edges != 1 {
		t.Errorf("blocks edges task->blocker = %d, want 1", edges)
	}
	if err := fixture.QueryScalar(ctx, edgeQuery, []any{task, "parent-child", epic}, &edges); err != nil {
		t.Fatalf("counting the parent-child edge: %v", err)
	}
	if edges != 1 {
		t.Errorf("parent-child edges task->epic = %d, want 1", edges)
	}

	if got := batchApplyColumnDirect(t, ctx, &fixture, "assignee", task); got != "alice" {
		t.Errorf("task assignee = %q, want %q (AssignAfterCreate)", got, "alice")
	}
}
