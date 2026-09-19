package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the PERSISTENCE-SEAM half of the behavior contract every
// implementation of publicops.Lifecycle must satisfy. There are three of them —
// the direct store, the embedded store, and the unit-of-work backend — and the
// first two share an execution path the third does not. Behavior asserted only
// against one backend has repeatedly drifted on the others, so each of these
// runs against all three from one spec.
//
// EVERY CASE HERE TAKES IssueOperationsStagingFixture, whose QueryScalar and
// UpdateRaw fields are a raw-SQL seam, and whose Operations field is BOTH the
// seed route and the subject. A backend that cannot open a SQL connection, or
// whose composition refuses Create, can run none of them. That used to be the
// whole Lifecycle contract; the accessor-reachable half now lives in
// lifecycle_create_contract.go, lifecycle_update_contract.go and
// lifecycle_close_reopen_contract.go, and what remains here is what genuinely
// needs the seam. Do not add a case to this file that one of those three could
// hold.
//
// WHY EACH SURVIVOR IS STILL HERE, in three honest buckets. Only the first
// names an obstacle that was checked against the destination fixture; a case in
// the third is here because nobody has moved it yet, and saying so is the point.
//
// A SEAM THE ROLE SURFACE DOES NOT PUBLISH:
//
//   - RunIssueOperationsUpdateClosedFieldsMatchClose and
//     RunIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes drive
//     UpdateRaw, and that IS their subject: the closed_at coherence guard and
//     the raw funnel's accepted value shapes are reachable only through the
//     untyped column map an external-sync or backfill caller uses. The typed
//     patch carries no closed_at at all.
//   - RunIssueOperationsUpdateMetadataReplaceClearsAndValidates ends on
//     `SELECT metadata IS NULL`. A hydrated issue reads a NULL column and the
//     empty document back as the same nil bytes (issueops/scan.go), so the
//     clause it pins — metadata is NEVER SQL NULL, which is the predicate a
//     consumer filtering on cleared metadata writes — is only observable as a
//     column probe.
//   - RunIssueOperationsUpdateStatusCrossingSettlesDependers,
//     RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender,
//     RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction and
//     RunIssueOperationsClaimLeavesBlockedStateAlone all assert the persisted
//     `is_blocked` projection through the shared blocked-state probe. No read on
//     any role hydrates that column, so the flag is a raw-row fact by
//     construction.
//   - RunIssueOperationsCreateRoutesInfraTypesToWisps installs BARE
//     workspace-global type vocabulary (types.custom, types.infra) and leaves it
//     installed. That is the hazard RoleContractBundle names as the reason these
//     contracts pay for a fixture PER CASE; the accessor-reachable create
//     contract shares one workspace across its cases, so this belongs where the
//     per-case fixture is.
//
// A PLAUSIBLE OBSTACLE NOBODY HAS CONFIRMED:
//
//   - RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses needs its rows
//     to sit at a status the workspace defines only through config
//     ("ready:active"), which is not a types.Status constant. It reaches that
//     state through the raw funnel because the CREATE path validates against a
//     vocabulary that does not parse the `name:category` spelling. Whether a
//     plain seed hook would write a non-canonical status is a per-backend
//     question nobody has answered, so the move is not claimed to be free.
//
// NO SEAM OBSTACLE — NOT IN THIS SLICE, and each is a candidate for the same
// treatment:
//
//   - RunIssueOperationsUpdateClaimConflictCarriesTheLosingState. Its one
//     raw-funnel step moves a row to `deferred`, which reads like the case
//     above and is not: deferred IS a types.Status constant, and the seed hook
//     the destination fixture carries writes an arbitrary status directly (the
//     close/reopen contract seeds `closed` through exactly that hook). The
//     obstacle is the staging fixture's own seed route, which this contract no
//     longer has to use.
//   - RunIssueOperationsUpdateWritesEveryScalarPatchField,
//     RunIssueOperationsUpdateLabelPatchOrdering,
//     RunIssueOperationsUpdateLabelPatchValueRules,
//     RunIssueOperationsUpdateFoldsMetadataIntoOneEvent,
//     RunIssueOperationsRequestValuesAreNotMutated,
//     RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps,
//     RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary,
//     RunIssueOperationsCreateUnderAParentMintsTheNextChildID,
//     RunIssueOperationsCreateClosedDerivesTheClosedStamp and
//     RunIssueOperationsUpdateStampsStartedAtOnceOnTheFirstInProgress.

// RunIssueOperationsCreateRoutesInfraTypesToWisps pins the facade create
// against the same infra-type routing the stores' own CreateIssue applies: a
// configured infra type is ephemeral and lives in the wisp tables, never in
// issues.
//
// THE LAST ARM IS THE ONE THE FIXTURE USED TO HIDE. Every arm above it
// configures types.infra to "agent", which is ALREADY in the built-in infra set
// (agent/role/message), so a backend that unioned the configured names with the
// defaults, or that ignored the key outright, answers identically to one that
// replaced them. The promise is replacement: a workspace that names its own
// infra types has said which types are ephemeral, and the ones it did not name
// are durable. Getting that backwards versions rows the workspace asked to keep
// out of history, or drops rows it expected versioned — silently, at create.
//
// audit_config_metadata_slots_repomtime.go's testAuditConfiguredInfraTypes pins
// the replacement on the CONFIG READ. This pins it where it is consumed, on all
// three legs, and the unit-of-work provider resolves the set through its own
// config use case.
func RunIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	// "gate" is in the vocabulary from the start but not yet infra: the last
	// arm needs a name that is creatable AND outside the built-in infra set, so
	// that promoting it proves the configured value was read rather than
	// defaulted.
	for key, value := range map[string]string{"types.custom": "agent,gate", "types.infra": "agent"} {
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

	// A configured set REPLACES the built-in one rather than adding to it. The
	// workspace now says gate is its only infra type, so agent — a built-in
	// infra name, and ephemeral in every arm above — has to come back durable.
	if err := fixture.SetConfig(ctx, "types.infra", "gate"); err != nil {
		t.Fatalf("SetConfig(types.infra, gate): %v", err)
	}
	evicted, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "evicted infra type", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("agent")},
	})
	if err != nil {
		t.Fatalf("Create agent once the configured infra set no longer names it: %v", err)
	}
	if evicted.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = true for type agent, want false: a configured types.infra REPLACES the built-in set, it does not extend it")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", evicted.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", evicted.Issue.ID, 0)

	// The control: without it, a backend that had simply stopped routing
	// anything to the wisps plane would pass the arm above.
	promoted, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer",
		Issue: &types.Issue{Title: "promoted infra type", Status: types.StatusOpen, Priority: 2, IssueType: types.IssueType("gate")},
	})
	if err != nil {
		t.Fatalf("Create the newly configured infra type gate: %v", err)
	}
	if !promoted.Issue.Ephemeral {
		t.Errorf("create result Ephemeral = false for the configured infra type gate, want true")
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", promoted.Issue.ID, 1)
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", promoted.Issue.ID, 0)
}

// RunIssueOperationsCreateUnderAParentMintsTheNextChildID pins the ID a create
// with a ParentID and no id of its own comes back with. The minting itself is
// already DRIVEN by RunIssueOperationsCreateInheritsParentLabels — which reads
// the labels that ride along and never looks at the id — so the shape of the id
// is unpinned on every leg today.
//
// TWO GENUINELY DIFFERENT BODIES ANSWER IT. The two stores reach
// issueops.GetNextChildIDTx through the role's create; the unit-of-work
// provider mints in internal/storage/domain/db/child_counter.go, which scans
// and parses the suffix itself against its own parent-table probe. Neither is
// pinned by any contract case, and the id is the one part of a create the
// caller could not have supplied and cannot correct afterwards: it is the
// handle every later command uses.
//
// THE FIXTURE IS THE MIGRATION, in two places.
//
// A SIBLING IS SEEDED OUT OF BAND, at .5, with no .3 or .4 beside it. A counter
// that merely incremented per call would answer .3 here; the promise is that
// the mint SELF-HEALS to one past the highest direct child that exists, which
// is what keeps a restored or hand-edited workspace from minting an id that is
// already taken. A case that only ever creates children in order cannot state
// the difference.
//
// A GRANDCHILD IS SEEDED AT .5.1, which is the trap the scan is written around:
// the ids are matched with a prefix pattern, and a pattern that does not stop
// at the first separator counts .5.1 as a direct child and mints past it.
//
// The last arm carries the collation half (bd-oyvc2.10): two parents whose ids
// differ ONLY IN CASE keep separate counters, because the scan's comparison is
// case-sensitive. Where it is not, one team's beads silently advance another's.
//
// Residue left where it is: GetNextChildID's "advance the counter WITHOUT
// creating an issue" and its wisp-parent routing stay audit-only. Neither is
// expressible here — no role reserves an id, and this fixture has no hook that
// seeds a wisp parent.
func RunIssueOperationsCreateUnderAParentMintsTheNextChildID(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	parent := fixture.IssuePrefix + "-childmint"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, parent)

	assertIssueOperationsMintedChildID(t, ctx, fixture, parent, parent+".1", "the first child of a childless parent")
	assertIssueOperationsMintedChildID(t, ctx, fixture, parent, parent+".2", "the second child")

	// The out-of-band sibling and its own child. Neither is created through the
	// role, so the counter never saw them go in.
	seedIssueOperationsLabeledIssue(t, ctx, fixture, parent+".5")
	seedIssueOperationsLabeledIssue(t, ctx, fixture, parent+".5.1")
	assertIssueOperationsMintedChildID(t, ctx, fixture, parent, parent+".6",
		"the child after a seeded .5 with a grandchild at .5.1")

	lower := fixture.IssuePrefix + "-childcase"
	upper := fixture.IssuePrefix + "-childCASE"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, lower)
	seedIssueOperationsLabeledIssue(t, ctx, fixture, upper)
	seedIssueOperationsLabeledIssue(t, ctx, fixture, upper+".7")
	assertIssueOperationsMintedChildID(t, ctx, fixture, lower, lower+".1",
		"the first child of a parent whose differently-cased twin already has one")
}

// assertIssueOperationsMintedChildID creates one child under parent with no id
// of its own and holds the minted id to want, then checks the row is really
// there: an id the caller cannot resolve afterwards is not an answer.
func assertIssueOperationsMintedChildID(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, parent, want, label string) {
	t.Helper()
	created, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:    "writer",
		ParentID: parent,
		Issue: &types.Issue{
			Title: label, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		},
	})
	if err != nil {
		t.Fatalf("create %s under %s: %v", label, parent, err)
	}
	if created.Issue == nil {
		t.Fatalf("create %s under %s returned no issue", label, parent)
	}
	if created.Issue.ID != want {
		t.Errorf("%s was minted %q, want %q", label, created.Issue.ID, want)
		return
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "issues", want, 1)
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
	seedIssueOperationsPlainIssue(t, ctx, fixture, recloseID)
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
	seedIssueOperationsPlainIssue(t, ctx, fixture, explicitID)
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
	seedIssueOperationsPlainIssue(t, ctx, fixture, staleID)
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
	seedIssueOperationsPlainIssue(t, ctx, fixture, guardID)
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

	// CLOSE PROVENANCE SURVIVES A PERSISTENCE MOVE, which is the same columns
	// asked a harder question. Everything above keeps a row in the issues
	// plane; a persistence move DELETES the row from one plane and re-inserts it
	// into the other (issueops.MoveIssuePersistenceInTx), so the close columns
	// only survive if the copy carries them and the insert lists them. A move
	// that dropped one would blank attribution nobody asked it to touch, and
	// `bd show` would render a closed issue with no record of who closed it.
	//
	// It is asserted against the plane that now HOLDS the row, not through the
	// result issue: a result hydrated from the pre-move struct reports a session
	// that is no longer in any table.
	moveID := fixture.IssuePrefix + "-closedfields-move"
	seedIssueOperationsPlainIssue(t, ctx, fixture, moveID)
	if _, err := fixture.Operations.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: moveID, Reason: "moved", Session: "move-session",
	}); err != nil {
		t.Fatalf("close %s: %v", moveID, err)
	}
	assertClosedFieldsInTable(t, ctx, fixture, "issues", moveID, "after close", "moved", "move-session", true)

	// Every directed pair of the three modes is covered by walking them in this
	// order: persistent -> ephemeral -> persistent -> no_history -> ephemeral ->
	// no_history -> persistent. The ephemeral and no_history modes share the
	// wisps plane, so the pair between them is a same-plane move and the rest
	// cross.
	for _, move := range []struct {
		mode    publicops.PersistenceMode
		holds   string
		vacates string
	}{
		{publicops.PersistenceModeEphemeral, "wisps", "issues"},
		{publicops.PersistenceModePersistent, "issues", "wisps"},
		{publicops.PersistenceModeNoHistory, "wisps", "issues"},
		{publicops.PersistenceModeEphemeral, "wisps", "issues"},
		{publicops.PersistenceModeNoHistory, "wisps", "issues"},
		{publicops.PersistenceModePersistent, "issues", "wisps"},
	} {
		if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
			Actor: "writer", IssueID: moveID,
			Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: move.mode}},
		}); err != nil {
			t.Fatalf("move %s to %s: %v", moveID, move.mode, err)
		}
		label := "after moving to " + string(move.mode)
		assertIssueOperationsRowCount(t, ctx, fixture, move.holds, moveID, 1)
		assertIssueOperationsRowCount(t, ctx, fixture, move.vacates, moveID, 0)
		assertClosedFieldsInTable(t, ctx, fixture, move.holds, moveID, label, "moved", "move-session", true)
	}
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

// assertClosedFields reads the close-lifecycle columns back from the issues
// plane, where every durable case in this file leaves its row.
func assertClosedFields(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, wantReason, wantSession string, wantClosedAt bool) {
	t.Helper()
	assertClosedFieldsInTable(t, ctx, fixture, "issues", id, label, wantReason, wantSession, wantClosedAt)
}

// assertClosedFieldsInTable reads the close-lifecycle columns back from the
// plane the row currently lives in. The stored empty string and SQL NULL are
// the same "nothing recorded" state to every reader, so both collapse to ""
// here.
func assertClosedFieldsInTable(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, table, id, label, wantReason, wantSession string, wantClosedAt bool) {
	t.Helper()
	var reason, session, closedAt string
	//nolint:gosec // G201: table is one of the contract's hardcoded table names
	if err := fixture.QueryScalar(ctx,
		"SELECT COALESCE(close_reason, ''), COALESCE(closed_by_session, ''), COALESCE(CAST(closed_at AS CHAR), '') FROM "+table+" WHERE id = ?",
		[]any{id}, &reason, &session, &closedAt); err != nil {
		t.Fatalf("read close fields for %s in %s (%s): %v", id, table, label, err)
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

// seedIssueOperationsPlainIssue creates one bare open task at an explicit ID. It
// goes through Create rather than the fixture's raw seed hook so the row starts
// out exactly as a real create leaves one — is_blocked settled, the create event
// recorded — which is the state every case below then mutates.
//
// It takes no request of its own. The graph-shaped seeds that used to travel
// through here moved out with the cases that needed them
// (lifecycle_update_contract.go seeds its own edges through AddDependency), and
// a parameter every caller fills with the zero value is one a reader has to
// check rather than read.
func seedIssueOperationsPlainIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string) {
	t.Helper()
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "seed",
		ForceIDPrefix: true,
		Issue:         &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}); err != nil {
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

// RunIssueOperationsUpdateClaimConflictCarriesTheLosingState pins the payload a
// lost claim comes back with. The leaf promises a *ClaimConflictError "carrying
// the state that beat it" (issueops/issueops.go:399-401) and says which sentinel
// each shape wears: a foreign assignment is ErrAlreadyClaimed, an ineligible
// status is ErrNotClaimable (issueops.go:215-217).
//
// The sentinel alone was already reachable; the TYPED fields were not. A caller
// that reports who won without parsing prose reads them, and the two
// implementations that build this error do so from separate reads — the
// store-backed body re-selects the row after a lost CAS
// (internal/storage/issueops/claim.go:154), the unit-of-work one takes what the
// repository handed back (internal/storage/domain/issue.go:566) — so nothing but
// a case over both spellings keeps the payload honest.
func RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	heldID := fixture.IssuePrefix + "-claimconflict-held"
	seedIssueOperationsPlainIssue(t, ctx, fixture, heldID)
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true}); err != nil {
		t.Fatalf("claim %s for holder: %v", heldID, err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")

	// A foreign live claim: the refusal names the holder and the status that
	// beat the compare-and-set, and writes nothing.
	events := newIssueOperationsEventCounter(t, ctx, fixture, heldID)
	_, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "rival", IssueID: heldID, Claim: true})
	conflict := assertIssueOperationsClaimConflict(t, err, "foreign live claim", heldID)
	if conflict != nil {
		if conflict.Assignee != "holder" {
			t.Errorf("foreign claim conflict Assignee = %q, want %q", conflict.Assignee, "holder")
		}
		if conflict.Status != types.StatusInProgress {
			t.Errorf("foreign claim conflict Status = %q, want %q", conflict.Status, types.StatusInProgress)
		}
	}
	if !errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Errorf("foreign claim error = %v, want ErrAlreadyClaimed", err)
	}
	if errors.Is(err, publicops.ErrNotClaimable) {
		t.Errorf("foreign claim error = %v, want it NOT to match ErrNotClaimable — the leaf gives the two shapes different sentinels", err)
	}
	assertLiveAssignee(t, ctx, fixture, heldID, "holder")
	events.assert(t, "refused foreign claim", 0, nil)

	// An ineligible status: nobody holds the issue, so the refusal carries the
	// status rather than an assignee, and wears the other sentinel.
	deferredID := fixture.IssuePrefix + "-claimconflict-deferred"
	seedIssueOperationsPlainIssue(t, ctx, fixture, deferredID)
	if err := fixture.UpdateRaw(ctx, deferredID, map[string]any{"status": string(types.StatusDeferred)}, "writer"); err != nil {
		t.Fatalf("defer %s: %v", deferredID, err)
	}
	deferredEvents := newIssueOperationsEventCounter(t, ctx, fixture, deferredID)
	_, err = fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: deferredID, Claim: true})
	conflict = assertIssueOperationsClaimConflict(t, err, "ineligible status claim", deferredID)
	if conflict != nil {
		if conflict.Status != types.StatusDeferred {
			t.Errorf("ineligible-status conflict Status = %q, want %q", conflict.Status, types.StatusDeferred)
		}
		if conflict.Assignee != "" {
			t.Errorf("ineligible-status conflict Assignee = %q, want empty — nobody held it", conflict.Assignee)
		}
	}
	if !errors.Is(err, publicops.ErrNotClaimable) {
		t.Errorf("ineligible-status claim error = %v, want ErrNotClaimable", err)
	}
	if errors.Is(err, publicops.ErrAlreadyClaimed) {
		t.Errorf("ineligible-status claim error = %v, want it NOT to match ErrAlreadyClaimed", err)
	}
	assertIssueOperationsAssigneeAndStatus(t, ctx, fixture, deferredID, "", types.StatusDeferred)
	deferredEvents.assert(t, "refused ineligible claim", 0, nil)

	// CLAIM UNDER A STALE ExpectedVersion, which is the ONE legal claim/guard
	// composition and was pinned by nothing.
	//
	// internal/storage/issueops/aggregate.go refuses Claim beside
	// ExpectedAssignee or ExpectedStatus before the unit of work opens, so
	// ExpectedVersion is the only precondition a claim may carry — it is the
	// optimistic fence a caller uses to claim a row it has already read. The
	// positive half (claim with a current version succeeds) is covered
	// elsewhere and passes even when the guard is bypassed entirely, so the
	// refusal is the half that carries the promise.
	fencedID := fixture.IssuePrefix + "-claimfence"
	seedIssueOperationsPlainIssue(t, ctx, fixture, fencedID)
	var currentVersion int64
	if err := fixture.QueryScalar(ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{fencedID}, &currentVersion); err != nil {
		t.Fatalf("read row_lock for %s: %v", fencedID, err)
	}
	staleVersion := currentVersion - 1
	fenceEvents := newIssueOperationsEventCounter(t, ctx, fixture, fencedID)
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "racer", IssueID: fencedID, Claim: true, ExpectedVersion: &staleVersion,
	}); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("claim guarded on a stale version: err = %v, want ErrVersionMismatch", err)
	}
	// The claim must not have landed: a bypassed fence shows up as an assignee,
	// not as an error, so the row is what says whether the guard ran.
	assertIssueOperationsAssigneeAndStatus(t, ctx, fixture, fencedID, "", types.StatusOpen)
	fenceEvents.assert(t, "claim refused by a stale version fence", 0, nil)
}

// assertIssueOperationsClaimConflict checks the refusal is the typed conflict
// naming the issue, and hands it back so the caller can assert the payload. It
// reports rather than fatals on the type so one bad shape does not hide the
// other arm's evidence.
func assertIssueOperationsClaimConflict(t *testing.T, err error, label, id string) *publicops.ClaimConflictError {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want a claim conflict", label)
	}
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("%s: err = %v (%T), want *ClaimConflictError", label, err, err)
		return nil
	}
	if conflict.IssueID != id {
		t.Errorf("%s: conflict IssueID = %q, want %q", label, conflict.IssueID, id)
	}
	return conflict
}

// RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses pins the claim
// eligibility rule at the Lifecycle seam: the leaf says an issue is claimable
// from "built-in StatusOpen or a configured active status"
// (issueops/issueops.go:213-217), so a workspace that spells its own
// draft -> ready -> in_progress lifecycle can claim from ready, and a wip
// custom stays fenced.
//
// Both claim bodies resolve the vocabulary through
// issueops.ClaimableSourceStatusesInTx, but they build the SQL predicate around
// it separately (internal/storage/issueops/claim.go:65 vs
// internal/storage/domain/db/issue.go:373), and the only test that covered this
// spoke to one store's ClaimIssue rather than to the guarded verb.
func RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	if err := fixture.SetConfig(ctx, "status.custom", "ready:active,reviewing:wip"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	// The create path validates status against a vocabulary that does not parse
	// the "name:category" spelling, so each row is created open and moved with
	// the raw funnel — the same way a custom-status row comes to exist in a real
	// workspace.
	readyID := fixture.IssuePrefix + "-customclaim-ready"
	reviewingID := fixture.IssuePrefix + "-customclaim-reviewing"
	for _, seed := range []struct {
		id     string
		status types.Status
	}{{readyID, "ready"}, {reviewingID, "reviewing"}} {
		seedIssueOperationsPlainIssue(t, ctx, fixture, seed.id)
		if err := fixture.UpdateRaw(ctx, seed.id, map[string]any{"status": string(seed.status)}, "writer"); err != nil {
			t.Fatalf("move %s to %s: %v", seed.id, seed.status, err)
		}
	}

	claimed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "agent-a", IssueID: readyID, Claim: true})
	if err != nil {
		t.Fatalf("claim %s from a configured active status: %v", readyID, err)
	}
	if !claimed.Changed {
		t.Errorf("claiming %s from a configured active status reported Changed = false, want a committed claim", readyID)
	}
	if claimed.Issue.Assignee != "agent-a" || claimed.Issue.Status != types.StatusInProgress {
		t.Errorf("claim result = assignee %q status %q, want agent-a/in_progress", claimed.Issue.Assignee, claimed.Issue.Status)
	}
	assertLiveAssignee(t, ctx, fixture, readyID, "agent-a")

	// A wip custom is not an active custom, so the anti-steal fence still holds
	// and the row is untouched.
	events := newIssueOperationsEventCounter(t, ctx, fixture, reviewingID)
	_, err = fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "agent-b", IssueID: reviewingID, Claim: true})
	if !errors.Is(err, publicops.ErrNotClaimable) {
		t.Fatalf("claim %s from a configured wip status: err = %v, want ErrNotClaimable", reviewingID, err)
	}
	assertIssueOperationsAssigneeAndStatus(t, ctx, fixture, reviewingID, "", "reviewing")
	events.assert(t, "refused wip-status claim", 0, nil)
}

// RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps pins the plane restriction
// the leaf declares on UpdateRequest.IssuePlaneOnly (issueops/issueops.go:251-260):
// with the flag set, an ID that names a wisp is ErrNotFound rather than an
// ephemeral row to update; with the zero value the same ID resolves and the
// update lands.
//
// It was pinned only against a stubbed unit of work, while the store-backed
// backends implement it in their shared execution body with no test at all.
func RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	wispID := fixture.IssuePrefix + "-planeonly-wisp"
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "seed", ForceIDPrefix: true,
		Issue: &types.Issue{ID: wispID, Title: "seeded title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	}); err != nil {
		t.Fatalf("seed wisp %s: %v", wispID, err)
	}
	assertIssueOperationsRowCount(t, ctx, fixture, "wisps", wispID, 1)

	var beforeRowLock string
	if err := fixture.QueryScalar(ctx, "SELECT CAST(row_lock AS CHAR) FROM wisps WHERE id = ?", []any{wispID}, &beforeRowLock); err != nil {
		t.Fatalf("read wisp row lock for %s: %v", wispID, err)
	}
	restricted := publicops.UpdateRequest{
		Actor: "writer", IssueID: wispID, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "restricted title"}},
	}
	if _, err := fixture.Operations.Update(ctx, restricted); !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("issue-plane-only update of wisp %s: err = %v, want ErrNotFound", wispID, err)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp title after refused plane-only update", "seeded title",
		"SELECT title FROM wisps WHERE id = ?", []any{wispID})
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp row lock after refused plane-only update", beforeRowLock,
		"SELECT CAST(row_lock AS CHAR) FROM wisps WHERE id = ?", []any{wispID})

	// The zero value keeps the both-plane auto-resolve, so the same edit lands.
	unrestricted := restricted
	unrestricted.IssuePlaneOnly = false
	landed, err := fixture.Operations.Update(ctx, unrestricted)
	if err != nil {
		t.Fatalf("both-plane update of wisp %s: %v", wispID, err)
	}
	if !landed.Changed || landed.Issue.Title != "restricted title" {
		t.Fatalf("both-plane update of wisp %s = %#v, want the title edit committed", wispID, landed)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "wisp title after both-plane update", "restricted title",
		"SELECT title FROM wisps WHERE id = ?", []any{wispID})

	// The restriction is about the PLANE, not about the flag: a durable issue
	// updates normally with it set.
	durableID := fixture.IssuePrefix + "-planeonly-durable"
	seedIssueOperationsPlainIssue(t, ctx, fixture, durableID)
	durable, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: durableID, IssuePlaneOnly: true,
		Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "durable title"}},
	})
	if err != nil {
		t.Fatalf("issue-plane-only update of durable %s: %v", durableID, err)
	}
	if !durable.Changed || durable.Issue.Title != "durable title" {
		t.Fatalf("issue-plane-only update of durable %s = %#v, want the title edit committed", durableID, durable)
	}
}

// RunIssueOperationsUpdateLabelPatchOrdering pins the order LabelPatch applies
// its three edits in: Replace, then Add, then Remove, "so removal wins when the
// same label appears in more than one edit" (issueops/issueops.go:56-58). A
// label named in every edit therefore ends up absent, and a patch that restates
// the current set is a no-op.
//
// The store-backed backends resolve the whole patch to a target set before
// touching the label tables (internal/storage/issueops/aggregate.go:276); the
// unit-of-work one replays the three edits as three separate use-case calls
// (internal/storage/domain/issue.go:648-680) and had no LabelPatch coverage of
// any kind.
func RunIssueOperationsUpdateLabelPatchOrdering(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-labelpatch"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, id, "old", "shared")
	assertIssueOperationsLabels(t, ctx, fixture, id, "seeded", "old", "shared")

	patched, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{
			Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "shared"}},
			Add:     []string{"add", "shared"},
			Remove:  []string{"old", "shared"},
		},
	}})
	if err != nil {
		t.Fatalf("ordered label patch on %s: %v", id, err)
	}
	if !patched.Changed {
		t.Errorf("ordered label patch on %s reported Changed = false, want a committed edit", id)
	}
	// "shared" was replaced in, added again, and removed: removal wins. "old"
	// survived neither the replacement nor the removal.
	assertIssueOperationsStringSet(t, "ordered label patch result labels", patched.Issue.Labels, "add", "replace")
	assertIssueOperationsLabels(t, ctx, fixture, id, "after ordered label patch", "add", "replace")

	// A patch that restates the current set changes nothing.
	restated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "add"}}},
	}})
	if err != nil {
		t.Fatalf("restated label patch on %s: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating %s's label set reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after restated label patch", "add", "replace")
}

// RunIssueOperationsUpdateLabelPatchValueRules pins what LabelPatch now says
// about the VALUES its edits carry, which it said nothing about before: the
// create-side field rules apply, so an overlong label is ErrFieldTooLong and
// the whole update writes nothing; repetition is free in both directions.
//
// The overlong leg is the one with teeth. The label tables are VARCHAR(255),
// so a backend that let the value through would SILENTLY TRUNCATE it and the
// caller would find a label it never asked for — which is why the case asserts
// the refusal AND that no row with that prefix landed, rather than only the
// error.
//
// The empty-string leg was the last thing in this file to be adjudicated
// (bd-yby99.29): the store bodies wrote a labels row keyed on "" and the
// unit-of-work one dropped the entry. Dropping won, so the assertion is a
// NO-OP rather than a partial write — an Add carrying only "" must leave the
// label set alone AND report Changed false, which is what tells a dropped
// entry apart from one that was written and then swept.
func RunIssueOperationsUpdateLabelPatchValueRules(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-labelvalues"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, id, "kept")

	overlong := strings.Repeat("x", types.MaxFieldLen+1)
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{overlong}},
	}}); !errors.Is(err, publicops.ErrFieldTooLong) {
		t.Fatalf("adding a %d-character label: err = %v, want ErrFieldTooLong", len(overlong), err)
	}
	var truncated int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label LIKE 'xxx%'", []any{id}, &truncated); err != nil {
		t.Fatalf("look for a truncated label on %s: %v", id, err)
	}
	if truncated != 0 {
		t.Errorf("%s carries %d label rows from the refused overlong add, want none: the column would truncate it silently", id, truncated)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the refused overlong add", "kept")

	// The same value twice in one edit is applied once.
	duplicated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{"twice", "twice"}},
	}})
	if err != nil {
		t.Fatalf("adding one label twice in one edit on %s: %v", id, err)
	}
	if !duplicated.Changed {
		t.Errorf("adding a new label twice on %s reported Changed = false, want a committed edit", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the duplicated add", "kept", "twice")

	// Removing a label the issue does not carry is a no-op.
	absent, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Remove: []string{"never-applied"}},
	}})
	if err != nil {
		t.Fatalf("removing an absent label from %s: %v", id, err)
	}
	if absent.Changed {
		t.Errorf("removing a label %s does not carry reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after removing an absent label", "kept", "twice")

	// An empty-string entry is dropped, and dropping it is a NO-OP: an Add
	// carrying only "" must not move Changed, because a backend that wrote the
	// row and swept it later would also leave the label set correct here.
	emptyOnly, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Add: []string{""}},
	}})
	if err != nil {
		t.Fatalf("adding an empty-string label to %s: %v", id, err)
	}
	if emptyOnly.Changed {
		t.Errorf("adding only an empty-string label to %s reported Changed = true, want a no-op", id)
	}
	var emptyRows int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ''", []any{id}, &emptyRows); err != nil {
		t.Fatalf("look for an empty label row on %s: %v", id, err)
	}
	if emptyRows != 0 {
		t.Errorf("%s carries %d label rows keyed on the empty string, want none", id, emptyRows)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after adding an empty-string label", "kept", "twice")

	// The same entry alongside a real one drops only itself, which is the
	// reason dropping beat refusing: one stray value does not fail the edit.
	mixed, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"kept", ""}}},
	}})
	if err != nil {
		t.Fatalf("replacing labels on %s with a real value and an empty one: %v", id, err)
	}
	if !mixed.Changed {
		t.Errorf("replacing %s's labels down to one value reported Changed = false, want a committed edit", id)
	}
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ''", []any{id}, &emptyRows); err != nil {
		t.Fatalf("look for an empty label row on %s after the mixed replace: %v", id, err)
	}
	if emptyRows != 0 {
		t.Errorf("%s carries %d label rows keyed on the empty string after a mixed replace, want none", id, emptyRows)
	}
	assertIssueOperationsLabels(t, ctx, fixture, id, "after the mixed replace", "kept")
}

// RunIssueOperationsUpdateMetadataReplaceClearsAndValidates pins
// MetadataPatch.Replace itself: it "replaces the complete metadata document",
// "a nil or empty Value clears metadata", and "a nonempty Value must be valid
// JSON". The exclusivity rule beside it is already pinned; the three clauses
// about the value are not — the clear was asserted only against a private
// unit-of-work helper, and neither arm was pinned behaviorally on any backend.
//
// It also pins the REPRESENTATION the clause now states: metadata is never SQL
// NULL, and an issue created with no metadata holds the same empty document a
// clear leaves behind. The create-side leg is what makes that one fact rather
// than two — without it, "cleared reads as {}" would still leave a caller
// unable to write one filter that matches both ways of having no metadata.
func RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	bare := fixture.IssuePrefix + "-metadata-bare"
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: bare, Title: "no metadata at all", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", bare, err)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, bare, "created with no metadata", `{}`)
	assertIssueOperationsMetadataIsNotNull(t, ctx, fixture, bare, "created with no metadata")

	id := fixture.IssuePrefix + "-metadata-replace"
	issue := &types.Issue{
		ID: id, Title: "metadata replace", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Metadata: json.RawMessage(`{"keep":"old","drop":"old"}`),
	}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	// Invalid JSON is refused, and the stored document survives untouched.
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)
	_, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"broken":`)}},
	}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("metadata replacement with invalid JSON: err = %v, want ErrValidation", err)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after refused replacement", `{"keep":"old","drop":"old"}`)
	events.assert(t, "refused metadata replacement", 0, nil)

	// A nonempty document replaces the whole value rather than merging into it.
	replaced, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"fresh":"new"}`)}},
	}})
	if err != nil {
		t.Fatalf("metadata replacement on %s: %v", id, err)
	}
	if !replaced.Changed {
		t.Errorf("metadata replacement on %s reported Changed = false, want a committed edit", id)
	}
	assertIssueOperationsMetadata(t, "metadata replacement", replaced.Issue.Metadata, `{"fresh":"new"}`)
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after replacement", `{"fresh":"new"}`)

	// A nil Value clears the document.
	cleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: nil}},
	}})
	if err != nil {
		t.Fatalf("metadata clear on %s: %v", id, err)
	}
	if !cleared.Changed {
		t.Errorf("metadata clear on %s reported Changed = false, want a committed edit", id)
	}
	// MetadataPatch.Replace now states the representation: clearing stores the
	// empty JSON document and the column is never NULL. Both halves are pinned,
	// because a backend that stored NULL would satisfy a JSON comparison of the
	// scanned value (the helper reads a NULL back as "null") and only the IS
	// NULL probe tells them apart — which is exactly the predicate a consumer
	// filtering on cleared metadata writes.
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after clear", `{}`)
	assertIssueOperationsMetadataIsNotNull(t, ctx, fixture, id, "after clear")

	// An empty Value clears an already-clear document, which is a no-op.
	recleared, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage{}}},
	}})
	if err != nil {
		t.Fatalf("metadata re-clear on %s: %v", id, err)
	}
	if recleared.Changed {
		t.Errorf("re-clearing %s's metadata reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after re-clear", `{}`)
}

// RunIssueOperationsRequestValuesAreNotMutated pins the leaf's promise that
// "implementations never mutate caller-owned request values"
// (issueops/issueops.go:377-384) and that results are detached snapshots
// (issueops.go:329-346). Everything a request carries by reference is at
// risk — the labels slice, the metadata bytes, the external-reference pointer,
// the issue struct itself — and the create body has a documented reason to want
// to write back into it: infra-type routing sets Ephemeral and ID minting fills
// in an ID, both on the attempt clone rather than on what the caller passed.
//
// The non-mutation half was pinned only on the unit-of-work backend and the
// detachment half only in one store's wisp test, so neither was stated once and
// answered by all three.
func RunIssueOperationsRequestValuesAreNotMutated(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	targetID := fixture.IssuePrefix + "-detach-target"
	seedIssueOperationsPlainIssue(t, ctx, fixture, targetID)

	externalRef := "caller-ref"
	callerLabels := []string{"caller-label"}
	callerMetadata := json.RawMessage(`{"caller":"owned"}`)
	callerIssue := &types.Issue{
		Title: "caller title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Labels: callerLabels, Metadata: callerMetadata, ExternalRef: &externalRef,
	}
	callerDependencies := []publicops.CreateDependency{{TargetID: targetID, Type: types.DepBlocks}}
	created, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor: "writer", Issue: callerIssue, Dependencies: callerDependencies,
	})
	if err != nil {
		t.Fatalf("create from a caller-owned request: %v", err)
	}

	// Nothing the caller handed over came back changed — including the ID field
	// the create filled in on its own copy and the Dependencies field it built
	// there.
	if callerIssue.ID != "" {
		t.Errorf("create wrote the minted ID %q back into the caller's issue", callerIssue.ID)
	}
	if callerIssue.Ephemeral {
		t.Error("create wrote its routing decision back into the caller's issue")
	}
	if len(callerIssue.Dependencies) != 0 {
		t.Errorf("create wrote %d dependency records back into the caller's issue", len(callerIssue.Dependencies))
	}
	if !reflect.DeepEqual(callerLabels, []string{"caller-label"}) {
		t.Errorf("create mutated the caller's labels slice: %v", callerLabels)
	}
	if string(callerMetadata) != `{"caller":"owned"}` {
		t.Errorf("create mutated the caller's metadata bytes: %s", callerMetadata)
	}
	if externalRef != "caller-ref" {
		t.Errorf("create mutated the caller's external reference: %q", externalRef)
	}
	if !reflect.DeepEqual(callerDependencies, []publicops.CreateDependency{{TargetID: targetID, Type: types.DepBlocks}}) {
		t.Errorf("create mutated the caller's dependency slice: %#v", callerDependencies)
	}

	// The result is a detached snapshot: corrupting it reaches neither the
	// caller's own values nor the stored row.
	createdID := created.Issue.ID
	if len(created.Issue.Labels) != 1 {
		t.Fatalf("create result labels = %v, want exactly the one requested label", created.Issue.Labels)
	}
	created.Issue.Labels[0] = "corrupted-label"
	if callerLabels[0] != "caller-label" {
		t.Errorf("the create result's labels alias the caller's slice: %v", callerLabels)
	}
	assertIssueOperationsLabels(t, ctx, fixture, createdID, "after corrupting the create result", "caller-label")
	assertIssueOperationsStoredMetadata(t, ctx, fixture, createdID, "after corrupting the create result", `{"caller":"owned"}`)

	// The same for update: its patch carries caller-owned collections too.
	patchAdd := []string{"added-label"}
	patchRemove := []string{"caller-label"}
	patchSet := map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}
	patchRef := "patched-ref"
	updated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: createdID,
		Patch: publicops.IssuePatch{
			ExternalRef: publicops.Field[*string]{Set: true, Value: &patchRef},
			Labels:      publicops.LabelPatch{Add: patchAdd, Remove: patchRemove},
			Metadata:    publicops.MetadataPatch{Set: patchSet},
		},
	})
	if err != nil {
		t.Fatalf("update from a caller-owned request: %v", err)
	}
	if !reflect.DeepEqual(patchAdd, []string{"added-label"}) || !reflect.DeepEqual(patchRemove, []string{"caller-label"}) {
		t.Errorf("update mutated the caller's label slices: add %v remove %v", patchAdd, patchRemove)
	}
	if !reflect.DeepEqual(patchSet, map[string]json.RawMessage{"added": json.RawMessage(`"value"`)}) {
		t.Errorf("update mutated the caller's metadata map: %v", patchSet)
	}
	if patchRef != "patched-ref" {
		t.Errorf("update mutated the caller's external reference: %q", patchRef)
	}

	assertIssueOperationsStringSet(t, "update result labels", updated.Issue.Labels, "added-label")
	updated.Issue.Labels[0] = "corrupted-label"
	if patchAdd[0] != "added-label" {
		t.Errorf("the update result's labels alias the caller's slice: %v", patchAdd)
	}
	assertIssueOperationsLabels(t, ctx, fixture, createdID, "after corrupting the update result", "added-label")
	assertIssueOperationsStoredMetadata(t, ctx, fixture, createdID, "after corrupting the update result", `{"caller":"owned","added":"value"}`)
}

// RunIssueOperationsCreateClosedDerivesTheClosedStamp pins what a create that
// arrives already closed does about the column it did not fill: a closed issue
// with no closed_at gets one derived from the timestamps it DID carry, one
// second past the later of created_at and updated_at
// (internal/storage/issueops/create.go PrepareIssueForInsert).
//
// The path is `bd import` and every restore and tracker-sync: rows arrive
// closed, from a system that recorded when they were made and last touched but
// not when they were finished. A backend that left the column NULL produces a
// closed backlog with no completion dates — every cycle-time and throughput
// report over the imported range silently drops those rows — and a backend that
// stamped "now" dates the whole import to the day it ran.
//
// THE AUDIT CASE'S FIXTURE CANNOT SEE WHICH TIMESTAMP THE BODY READ.
// audit_issue-lifecycle.go's testAuditCreateClosedDerivesClosedAt seeds
// created_at and updated_at to the SAME instant, so max(created, updated) and
// created and updated are all the same number, and a body that read any one of
// them passes. The second arm below moves them apart, which is the whole of
// what "max" means.
//
// The third arm is the guard on the other side: the derivation is a default,
// not a policy. A caller that supplied its own completion time keeps it, and a
// body that derived unconditionally would overwrite the one fact the import
// actually knew.
//
// HOW MANY VOTES: one. All three legs reach PrepareIssueForInsert through
// issueops.PreparePublicCreateRequest before any backend-specific create body
// runs — the unit-of-work provider included (internal/storage/uow calls it in
// Create before handing the issue to its own domain use case). The case is
// worth having as a stated promise, but it is one body read three times, not a
// second opinion.
func RunIssueOperationsCreateClosedDerivesTheClosedStamp(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	base := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	supplied := time.Date(2019, 6, 7, 8, 9, 10, 0, time.UTC)
	for index, test := range []struct {
		name         string
		createdAt    time.Time
		updatedAt    time.Time
		closedAt     *time.Time
		wantClosedAt time.Time
	}{
		{
			name: "from a row whose stamps agree", createdAt: base, updatedAt: base,
			wantClosedAt: base.Add(time.Second),
		},
		{
			// updated_at is LATER, so a body reading created_at alone lands a
			// second past the wrong one.
			name: "from the later of the two stamps", createdAt: base, updatedAt: base.Add(72 * time.Hour),
			wantClosedAt: base.Add(72*time.Hour + time.Second),
		},
		{
			name: "never over a stamp the caller supplied", createdAt: base, updatedAt: base,
			closedAt: &supplied, wantClosedAt: supplied,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := fixture.IssuePrefix + "-createclosed-" + strconv.Itoa(index)
			if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
				Actor:         "writer",
				ForceIDPrefix: true,
				Issue: &types.Issue{
					ID: id, Title: id, Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask,
					CreatedAt: test.createdAt, UpdatedAt: test.updatedAt, ClosedAt: test.closedAt,
				},
			}); err != nil {
				t.Fatalf("create closed %s: %v", id, err)
			}
			// The two source columns are read back first: a create that
			// normalized them to "now" would leave the expectation below
			// describing a row that does not exist, and the case would fail
			// somewhere unhelpful instead of saying so.
			assertIssueOperationsStoredColumns(t, ctx, fixture, id, "the stamps the derivation reads", []issueOperationsColumnValue{
				{"created_at", test.createdAt.Format(issueOperationsStoredTimeLayout)},
				{"updated_at", test.updatedAt.Format(issueOperationsStoredTimeLayout)},
			})
			assertIssueOperationsStoredColumns(t, ctx, fixture, id, "the derived close stamp", []issueOperationsColumnValue{
				{"closed_at", test.wantClosedAt.Format(issueOperationsStoredTimeLayout)},
			})
		})
	}
}

// RunIssueOperationsUpdateWritesEveryScalarPatchField pins the whole scalar and
// pointer surface of issueops.IssuePatch — seventeen fields — against the
// columns each one maps to, and then pins the other half of that mapping:
// restating what is already stored is a no-op that leaves the row alone.
//
// Each backend builds the map itself. The two stores go through
// issueops.UpdateFields; the unit-of-work backend builds its own spec in
// internal/storage/uow updateSpec. The contract named only a handful of the
// fields, so one dropped from either map was invisible here.
// issueops.IssuePatch.Owner was the live example: the string "Owner" appeared
// nowhere in this file, and the field's only pin anywhere was a single
// unit-of-work test.
//
// NEITHER HALF STANDS ALONE, which is why they are one case.
//
// A restatement-only case passes against a body that DROPS a field: the stored
// value never moves, so Changed comes out false for the wrong reason. That is
// what the write pass rules out, and it reads the RAW ROW to do it — a result
// issue hydrated from the patch rather than from storage answers with the value
// the caller just handed in, so it cannot say whether anything reached the
// column.
//
// A write-only case passes against a body that treats a field as always
// changed, rewriting the row on every idempotent update and advancing row_lock
// under a caller's compare-and-set. That is what the restatement pass rules
// out, and row_lock is the mark with teeth there: updated_at is a second-
// granularity column with ON UPDATE CURRENT_TIMESTAMP, so two writes inside one
// second leave it identical whether or not the row moved.
func RunIssueOperationsUpdateWritesEveryScalarPatchField(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-scalarsurface"
	seededMinutes := 11
	seededRef := "seeded-ref"
	seededDue := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	seededDefer := time.Date(2031, 4, 5, 6, 7, 8, 0, time.UTC)
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: "seeded title", Description: "seeded description", Design: "seeded design",
		AcceptanceCriteria: "seeded acceptance", Notes: "seeded notes",
		SpecID: "seeded-spec", AwaitID: "seeded-await",
		Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask,
		Assignee: "seeded-assignee", Owner: "seeded-owner", ClosedBySession: "seeded-session",
		EstimatedMinutes: &seededMinutes, ExternalRef: &seededRef,
		DueAt: &seededDue, DeferUntil: &seededDefer,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	patchedMinutes := 22
	patchedRef := "patched-ref"
	patchedDue := time.Date(2032, 5, 6, 7, 8, 9, 0, time.UTC)
	patchedDefer := time.Date(2032, 4, 5, 6, 7, 8, 0, time.UTC)
	// EVERY value below differs from the seeded one. A field whose seeded and
	// patched values agreed could not witness its own mapping: the column would
	// read correctly whether the update carried the field or dropped it.
	//
	// The status move is open -> in_progress, which stays out of the done
	// category so close policy never sees it, and the assignee edit rides a row
	// that is not yet in progress, so the transfer fence stands down. Both are
	// pinned by their own cases; this one is about the mapping.
	patch := publicops.IssuePatch{
		Title:              publicops.Field[string]{Set: true, Value: "patched title"},
		Description:        publicops.Field[string]{Set: true, Value: "patched description"},
		Design:             publicops.Field[string]{Set: true, Value: "patched design"},
		AcceptanceCriteria: publicops.Field[string]{Set: true, Value: "patched acceptance"},
		Notes:              publicops.Field[string]{Set: true, Value: "patched notes"},
		SpecID:             publicops.Field[string]{Set: true, Value: "patched-spec"},
		AwaitID:            publicops.Field[string]{Set: true, Value: "patched-await"},
		Status:             publicops.Field[publicops.Status]{Set: true, Value: types.StatusInProgress},
		Priority:           publicops.Field[int]{Set: true, Value: 1},
		IssueType:          publicops.Field[publicops.IssueType]{Set: true, Value: types.TypeBug},
		Assignee:           publicops.Field[string]{Set: true, Value: "patched-assignee"},
		Owner:              publicops.Field[string]{Set: true, Value: "patched-owner"},
		ClosedBySession:    publicops.Field[string]{Set: true, Value: "patched-session"},
		EstimatedMinutes:   publicops.Field[*int]{Set: true, Value: &patchedMinutes},
		ExternalRef:        publicops.Field[*string]{Set: true, Value: &patchedRef},
		DueAt:              publicops.Field[*time.Time]{Set: true, Value: &patchedDue},
		DeferUntil:         publicops.Field[*time.Time]{Set: true, Value: &patchedDefer},
	}
	stored := []issueOperationsColumnValue{
		{"title", "patched title"},
		{"description", "patched description"},
		{"design", "patched design"},
		{"acceptance_criteria", "patched acceptance"},
		{"notes", "patched notes"},
		{"spec_id", "patched-spec"},
		{"await_id", "patched-await"},
		{"status", string(types.StatusInProgress)},
		{"priority", "1"},
		{"issue_type", string(types.TypeBug)},
		{"assignee", "patched-assignee"},
		{"owner", "patched-owner"},
		{"closed_by_session", "patched-session"},
		{"estimated_minutes", "22"},
		{"external_ref", "patched-ref"},
		{"due_at", patchedDue.Format(issueOperationsStoredTimeLayout)},
		{"defer_until", patchedDefer.Format(issueOperationsStoredTimeLayout)},
	}

	written, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: patch})
	if err != nil {
		t.Fatalf("full scalar patch on %s: %v", id, err)
	}
	if !written.Changed {
		t.Errorf("full scalar patch on %s reported Changed = false, want a committed edit", id)
	}
	// The one field with no other assertion anywhere in this file, read off the
	// result as well as the row: a caller renders what the result carries.
	if written.Issue.Owner != "patched-owner" {
		t.Errorf("full scalar patch result Owner = %q, want %q", written.Issue.Owner, "patched-owner")
	}
	assertIssueOperationsStoredColumns(t, ctx, fixture, id, "after the full scalar patch", stored)

	// The restatement. Same patch, same values, and now they are what the row
	// already holds.
	before := readIssueOperationsRowMarks(t, ctx, fixture, id)
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)
	restated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: patch})
	if err != nil {
		t.Fatalf("restated scalar patch on %s: %v", id, err)
	}
	if restated.Changed {
		t.Errorf("restating every scalar field of %s reported Changed = true, want a no-op", id)
	}
	assertIssueOperationsStoredColumns(t, ctx, fixture, id, "after the restated scalar patch", stored)
	if after := readIssueOperationsRowMarks(t, ctx, fixture, id); after != before {
		t.Errorf("restating every scalar field of %s rewrote the row: %+v, want it unchanged at %+v", id, after, before)
	}
	events.assert(t, "restated scalar patch", 0, nil)
}

// RunIssueOperationsUpdateStampsStartedAtOnceOnTheFirstInProgress pins the
// started_at lifecycle a plain status update carries with it
// (internal/storage/issueops/update.go ManageStartedAt: "auto-sets started_at
// when transitioning to in_progress. If the issue already has a started_at, it
// is preserved"). It drives the UNTYPED FUNNEL, because IssuePatch has no
// started_at member and the funnel is what every `bd update -s` and every
// external-sync caller reaches.
//
// STAMPING AND PRESERVING FAIL IN OPPOSITE DIRECTIONS, so both are here. A body
// that never stamps leaves an in_progress row that has never started, and
// nothing downstream can say how long the work has been running — the lease
// reclaim reads exactly this column. A body that RE-stamps resets that clock
// every time an agent bounces a bead through open and back, which is the shape
// a retry loop produces, and the row then looks freshly started forever.
//
// The closest existing case is
// RunIssueOperationsUpdateClaimIsAMutationWhenThePatchRestoresTheRow, which
// pins preservation of a seeded started_at under CLAIM. Neither the stamp on an
// empty column nor preservation across a plain status patch is pinned anywhere.
//
// THE PRESERVING ROW'S STAMP IS SEEDED YEARS IN THE PAST, and that is the
// fixture doing the work rather than the assertion. started_at is DATETIME(0):
// a row stamped by the first transition and re-stamped by the second, both
// inside one second, holds the same bytes either way, so a case that stamped
// its own precondition could not tell preservation from a rewrite. The stamping
// row therefore proves only that a stamp lands, in a measured window, and the
// preserving row — seeded, never stamped by this case — carries the whole
// preservation claim across a full open/in_progress/open/in_progress cycle.
func RunIssueOperationsUpdateStampsStartedAtOnceOnTheFirstInProgress(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	if fixture.UpdateRaw == nil {
		t.Skip("fixture has no UpdateRaw: this backend's untyped update funnel is unreachable, so ManageStartedAt is UNPINNED here")
	}

	stamping := fixture.IssuePrefix + "-startstamp"
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: stamping, Title: stamping, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", stamping, err)
	}
	assertIssueOperationsStartedAt(t, ctx, fixture, stamping, "before any transition", "")

	lower := time.Now().UTC().Add(-issueOperationsClockSlack).Format(issueOperationsStoredTimeLayout)
	issueOperationsUpdateStatus(t, ctx, fixture, stamping, types.StatusInProgress)
	upper := time.Now().UTC().Add(issueOperationsClockSlack).Format(issueOperationsStoredTimeLayout)
	stamped := issueOperationsStartedAt(t, ctx, fixture, stamping, "after the first in_progress")
	// The stored layout sorts lexicographically, so string bounds are time
	// bounds. A bare "not empty" check would accept the zero time, which is
	// what a body writing an unset *time.Time lands.
	if stamped < lower || stamped > upper {
		t.Errorf("%s started_at = %q after its first in_progress, want a stamp between %q and %q", stamping, stamped, lower, upper)
	}

	preserving := fixture.IssuePrefix + "-startkeep"
	seededStart := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: preserving, Title: preserving, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		StartedAt: &seededStart,
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", preserving, err)
	}
	want := seededStart.Format(issueOperationsStoredTimeLayout)
	// The precondition, not an assumption: a seed hook that dropped the preset
	// stamp would leave every check below comparing a rewrite with a rewrite.
	assertIssueOperationsStartedAt(t, ctx, fixture, preserving, "as seeded", want)

	for _, step := range []types.Status{types.StatusInProgress, types.StatusOpen, types.StatusInProgress} {
		issueOperationsUpdateStatus(t, ctx, fixture, preserving, step)
		assertIssueOperationsStartedAt(t, ctx, fixture, preserving, "after a status update to "+string(step), want)
	}
}

// issueOperationsUpdateStatus drives one status change through the untyped
// funnel, which is the only route to it that carries no patch of its own.
func issueOperationsUpdateStatus(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, status types.Status) {
	t.Helper()
	if err := fixture.UpdateRaw(ctx, id, map[string]any{"status": string(status)}, "writer"); err != nil {
		t.Fatalf("raw status update of %s to %q: %v", id, status, err)
	}
}

func issueOperationsStartedAt(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string) string {
	t.Helper()
	return readIssueOperationsStoredColumns(t, ctx, fixture, id, label, []string{"started_at"})[0].value
}

func assertIssueOperationsStartedAt(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, want string) {
	t.Helper()
	if got := issueOperationsStartedAt(t, ctx, fixture, id, label); got != want {
		t.Errorf("%s started_at %s = %q, want %q", id, label, got, want)
	}
}

// RunIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes pins what the
// UNTYPED update funnel accepts in its metadata slot. The typed
// IssuePatch.Metadata surface is an ordered merge/set/unset document with its
// own owning cases; this is the OTHER entry, the one every `bd update
// --metadata` and every backfill script reaches, where the value arrives as
// whatever the caller's JSON decoder produced.
//
// The shapes are the contract. The two stores funnel through
// storage.NormalizeMetadataValue, which names string, []byte and
// json.RawMessage; the unit-of-work backend funnels through its own
// normalizeUpdateValue in internal/storage/domain/db. Two maps, and nothing
// held them to the same accepted set — a backend that took only one of the
// three would refuse a caller the others serve, or worse, store the Go
// rendering of a []byte as the document.
//
// audit_issue-lifecycle.go's testAuditMetadataJSONRoundTrip drives []byte alone
// and only at the storage seam. The document is compared parsed rather than
// byte-for-byte because a JSON column may reformat and reorder, and the NOT
// NULL probe is the half a value comparison cannot make: a NULL column reads
// back as the literal "null" and compares equal to an empty document.
func RunIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()
	if fixture.UpdateRaw == nil {
		t.Skip("fixture has no UpdateRaw: this backend's untyped update funnel is unreachable, so its metadata slot is UNPINNED here")
	}

	id := fixture.IssuePrefix + "-rawmeta"
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Metadata: json.RawMessage(`{"team":"seeded"}`),
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "as seeded", `{"team":"seeded"}`)

	for _, shape := range []struct {
		name  string
		value any
		want  string
	}{
		{"a []byte", []byte(`{"team":"ops"}`), `{"team":"ops"}`},
		{"a string", `{"team":"sre"}`, `{"team":"sre"}`},
		{"a json.RawMessage", json.RawMessage(`{"team":"platform"}`), `{"team":"platform"}`},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if err := fixture.UpdateRaw(ctx, id, map[string]any{"metadata": shape.value}, "writer"); err != nil {
				t.Fatalf("raw metadata update of %s with %s: %v", id, shape.name, err)
			}
			assertIssueOperationsStoredMetadata(t, ctx, fixture, id, "after a raw update with "+shape.name, shape.want)
			assertIssueOperationsMetadataIsNotNull(t, ctx, fixture, id, "after a raw update with "+shape.name)
		})
	}
}

// RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary pins the
// WRITE side of the issue-type vocabulary. The read side is pinned by
// RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary; on the write side each
// backend has its own guard — issueops.ValidateScalarUpdates for the two stores
// and validateIssueTypeUpdate in internal/storage/domain for the unit-of-work
// one — and neither had a test outside one ad-hoc unit-of-work case.
//
// The refusal is typed (issueops.ErrValidation) and leaves the row alone, under
// Lifecycle.Update's standing promise that a refusal "leaves persistent state
// unchanged". A stored issue_type nothing in the workspace defines is worse
// than a rejected update: `bd list --type` stops matching the row and the
// renderers have no rule for it.
//
// THE SECOND HALF IS WHAT GIVES THE FIRST ONE TEETH. A body that refused every
// type outside the built-in set — dropping the configured-types read entirely —
// would satisfy the refusal above. Configuring the type and landing the same
// update is the only thing that tells a guard reading the workspace vocabulary
// from one hardcoding the built-ins.
func RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-typevocab"
	seedIssueOperationsPlainIssue(t, ctx, fixture, id)

	before := readIssueOperationsRowMarks(t, ctx, fixture, id)
	events := newIssueOperationsEventCounter(t, ctx, fixture, id)
	if _, err := fixture.Operations.Update(ctx, issueOperationsTypeRequest(id, "not-configured")); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("update %s to an issue type the workspace does not define: err = %v, want ErrValidation", id, err)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "issue type after the refused update", string(types.TypeTask),
		"SELECT issue_type FROM issues WHERE id = ?", []any{id})
	if after := readIssueOperationsRowMarks(t, ctx, fixture, id); after != before {
		t.Errorf("the refused issue-type update rewrote %s: %+v, want it unchanged at %+v", id, after, before)
	}
	events.assert(t, "refused issue-type update", 0, nil)

	if err := fixture.SetConfig(ctx, "types.custom", "research"); err != nil {
		t.Fatalf("SetConfig(types.custom): %v", err)
	}
	accepted, err := fixture.Operations.Update(ctx, issueOperationsTypeRequest(id, "research"))
	if err != nil {
		t.Fatalf("update %s to a configured custom issue type: %v", id, err)
	}
	if !accepted.Changed || accepted.Issue.IssueType != types.IssueType("research") {
		t.Fatalf("update of %s to a configured custom type = %#v, want a committed edit to research", id, accepted.Issue)
	}
	assertIssueOperationsScalarValue(t, ctx, fixture, "issue type after the configured update", "research",
		"SELECT issue_type FROM issues WHERE id = ?", []any{id})
}

// issueOperationsTypeRequest builds the bare issue-type edit whose vocabulary
// check the case above pins.
func issueOperationsTypeRequest(id string, issueType publicops.IssueType) publicops.UpdateRequest {
	return publicops.UpdateRequest{
		Actor:   "writer",
		IssueID: id,
		Patch:   publicops.IssuePatch{IssueType: publicops.Field[publicops.IssueType]{Set: true, Value: issueType}},
	}
}

// issueOperationsStoredTimeLayout is how Dolt renders a DATETIME cast to CHAR.
// The columns carry no fractional seconds, so this round-trips exactly.
const issueOperationsStoredTimeLayout = "2006-01-02 15:04:05"

// issueOperationsClockSlack widens the window a case allows around a stamp the
// implementation wrote from its own clock. Every backend here stamps in Go, in
// this process, so the two clocks are the same one; the slack covers the
// truncation to whole seconds and a slow call, not a clock skew.
const issueOperationsClockSlack = 5 * time.Second

// issueOperationsColumnValue names one stored column and the value it holds, so
// a field-surface assertion reports WHICH column disagreed.
type issueOperationsColumnValue struct {
	column string
	value  string
}

// readIssueOperationsStoredColumns reads a set of columns back in one query as
// text. Everything is COALESCEd and cast, because the three fixtures scan into
// different destination sets and only *string is common to all of them.
//
// The result is the same pair list an assertion takes, so a case can snapshot
// the columns an operation must NOT touch and hand the snapshot straight back
// as the expectation afterwards.
func readIssueOperationsStoredColumns(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string, columns []string) []issueOperationsColumnValue {
	t.Helper()
	selected := make([]string, len(columns))
	dest := make([]any, len(columns))
	got := make([]issueOperationsColumnValue, len(columns))
	for i, column := range columns {
		selected[i] = "COALESCE(CAST(" + column + " AS CHAR), '')"
		got[i].column = column
		dest[i] = &got[i].value
	}
	//nolint:gosec // G201: the column names are this file's own literals
	query := "SELECT " + strings.Join(selected, ", ") + " FROM issues WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, dest...); err != nil {
		t.Fatalf("read stored columns for %s (%s): %v", id, label, err)
	}
	return got
}

// assertIssueOperationsStoredColumns reads the named columns back and compares
// each as text.
func assertIssueOperationsStoredColumns(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string, want []issueOperationsColumnValue) {
	t.Helper()
	columns := make([]string, len(want))
	for i, field := range want {
		columns[i] = field.column
	}
	assertIssueOperationsColumnValues(t, id, label, readIssueOperationsStoredColumns(t, ctx, fixture, id, label, columns), want)
}

// assertIssueOperationsColumnValues compares two column-value lists position by
// position, for the reads that do not come from a query.
func assertIssueOperationsColumnValues(t *testing.T, id, label string, got, want []issueOperationsColumnValue) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s %s reported %d columns, want %d", id, label, len(got), len(want))
	}
	for i, field := range want {
		if got[i].value != field.value {
			t.Errorf("%s %s %s = %q, want %q", id, label, field.column, got[i].value, field.value)
		}
	}
}

// issueOperationsRowMarks are the two columns that record THAT a row was
// written, whatever the values came out as. row_lock is the one with teeth:
// updated_at has second granularity, so a rewrite inside the same second leaves
// it alone.
type issueOperationsRowMarks struct {
	RowLock   string
	UpdatedAt string
}

func readIssueOperationsRowMarks(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string) issueOperationsRowMarks {
	t.Helper()
	var marks issueOperationsRowMarks
	if err := fixture.QueryScalar(ctx,
		"SELECT CAST(row_lock AS CHAR), CAST(updated_at AS CHAR) FROM issues WHERE id = ?",
		[]any{id}, &marks.RowLock, &marks.UpdatedAt); err != nil {
		t.Fatalf("read row marks for %s: %v", id, err)
	}
	return marks
}

// RunIssueOperationsUpdateStatusCrossingSettlesDependers pins the local-write
// clause of issueops.BlockedStateInvariant on Update, and it is the case with
// the most to say about per-backend WIRING in this file: the decision that a
// status move counts as a crossing is written TWICE, in
// internal/storage/issueops/update.go and in internal/storage/domain/db's
// issue Update, so the three legs are two genuine votes here.
//
// The crossing is open -> pinned and back, deliberately not open -> closed:
// pinned is a status Lifecycle.Close cannot produce, so this branch is
// reachable only through Update. Both directions run, because the mark and the
// unmark are separate SQL and a case that watches one exercises one.
//
// The depender is CREATED with its edge rather than seeded with one, which is
// why the fixture needs no dependency hook: the edge and the flag both come
// from role verbs, and no fixture in this package can write the flag at all.
func RunIssueOperationsUpdateStatusCrossingSettlesDependers(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bsupd-blocker"
	depender := fixture.IssuePrefix + "-bsupd-depender"
	controlBlocker := fixture.IssuePrefix + "-bsupd-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bsupd-ctldepender"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, blocker)
	seedIssueOperationsLabeledIssue(t, ctx, fixture, controlBlocker)
	createIssueOperationsBlockedIssue(t, ctx, fixture, depender, blocker)
	createIssueOperationsBlockedIssue(t, ctx, fixture, controlDepender, controlBlocker)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the create carried the edge and earned the flag")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker), "the control's blocker never moves")

	out := probe.watchFlip(t, []blockedStateRow{blockedIssue(depender)}, []blockedStateRow{blockedIssue(controlDepender)})
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: blocker,
		Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusPinned}},
	}); err != nil {
		t.Fatalf("update %s to pinned: %v", blocker, err)
	}
	out.requireFlippedTo(t, 0, "pinning a blocker takes it out of the active set, so its depender is no longer blocked")

	back := probe.watchFlip(t, []blockedStateRow{blockedIssue(depender)}, []blockedStateRow{blockedIssue(controlDepender)})
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: blocker,
		Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusOpen}},
	}); err != nil {
		t.Fatalf("update %s back to open: %v", blocker, err)
	}
	back.requireFlippedTo(t, 1, "unpinning the blocker returns it to the active set and re-blocks its depender")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the postcondition is the flag AND the live blocker behind it")
}

// RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender pins
// the HALF OF THE PREDICATE'S FIRST CLAUSE THAT NAMES A SECOND EDGE TYPE.
// issueops.BlockedStateInvariant says a row is blocked when "it has a blocks or
// conditional-blocks edge onto a target that is itself neither closed nor
// pinned"; every other is_blocked case in this package seeds the first type
// only, so the second was a word in the doc with no case behind it. The type
// list is spelled out five times in
// internal/storage/issueops/blocked_state.go — the two mark templates, the two
// unmark templates, and the depender load that builds the affected set — and
// dropping `conditional-blocks` from any of them left every case green.
//
// IT IS ON UPDATE, and on the same crossing the sibling case above uses, for
// the reason that case gives: the decision that a status move counts as a
// crossing is written TWICE (internal/storage/issueops/update.go and
// internal/storage/domain/db's issue Update), so this is TWO GENUINE VOTES
// rather than one body seen three times. The DependencyEditor's add path could
// not host it — the direct-source mark there runs a type-agnostic UPDATE of its
// own (markDirectBlockingDependencySourceInTx and its domain/db mirror) once
// the caller's type has already passed the wiring's gate, so on that role the
// mark template's copy of the type list is never the thing that decides.
//
// BOTH DIRECTIONS RUN. The mark and the unmark are separate SQL carrying
// separate copies of the type list, and the unpinning half is the one that
// reaches the MARK template: it must re-block a row whose only cause is a
// conditional-blocks edge.
//
// WHAT THE FIXTURE MAKES OBSERVABLE. The depender carries a conditional-blocks
// edge AND NOTHING ELSE, asserted by counting its `blocks` edges at zero and
// its `conditional-blocks` edges at one. A depender holding both types would be
// blocked either way and this case would pass against a predicate that had
// never heard of the second one — which is the shape of the retired
// fixture-defect case, a subject whose named term was not the term producing
// its value.
//
// The control is blocked through a PLAIN blocks edge. It is the same shape at
// the same moment differing in the one fact under test, so a mutation that
// drops the conditional type reddens the subject and leaves the control exactly
// where it was, and the failure names the term.
func RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bscond-blocker"
	depender := fixture.IssuePrefix + "-bscond-depender"
	controlBlocker := fixture.IssuePrefix + "-bscond-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bscond-ctldepender"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, blocker)
	seedIssueOperationsLabeledIssue(t, ctx, fixture, controlBlocker)
	createIssueOperationsTypedBlockedIssue(t, ctx, fixture, depender, blocker, types.DepConditionalBlocks)
	createIssueOperationsBlockedIssue(t, ctx, fixture, controlDepender, controlBlocker)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	// The conditional term is only observable while it is the ONLY term.
	assertIssueOperationsEdgeTypeCount(t, ctx, fixture, depender, string(types.DepBlocks), 0)
	assertIssueOperationsEdgeTypeCount(t, ctx, fixture, depender, string(types.DepConditionalBlocks), 1)
	assertIssueOperationsEdgeTypeCount(t, ctx, fixture, controlDepender, string(types.DepBlocks), 1)
	assertIssueOperationsEdgeTypeCount(t, ctx, fixture, controlDepender, string(types.DepConditionalBlocks), 0)
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker),
		"a conditional-blocks edge onto a live target blocks its source exactly as a blocks edge does")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker),
		"the control is blocked through the edge type this suite already covered")

	out := probe.watchFlip(t, []blockedStateRow{blockedIssue(depender)}, []blockedStateRow{blockedIssue(controlDepender)})
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: blocker,
		Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusPinned}},
	}); err != nil {
		t.Fatalf("update %s to pinned: %v", blocker, err)
	}
	out.requireFlippedTo(t, 0,
		"pinning the target of a conditional-blocks edge takes it out of the active set, and the unmark template counts that type")

	back := probe.watchFlip(t, []blockedStateRow{blockedIssue(depender)}, []blockedStateRow{blockedIssue(controlDepender)})
	if _, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{
		Actor: "writer", IssueID: blocker,
		Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusOpen}},
	}); err != nil {
		t.Fatalf("update %s back to open: %v", blocker, err)
	}
	back.requireFlippedTo(t, 1,
		"unpinning re-blocks a row whose only cause is a conditional-blocks edge — the MARK template's own copy of the type list")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker),
		"the postcondition is the flag AND the live conditional blocker behind it")
	assertIssueOperationsEdgeTypeCount(t, ctx, fixture, depender, string(types.DepBlocks), 0)
}

// RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction pins
// the create half, and with it the one structural asymmetry between the two
// bodies: the store-backed create runs ONE terminal recompute over the union of
// the created ids and every edge's affected set, while the unit-of-work create
// maintains blocked state per edge as its dependency repository writes them.
// The two are convergent by argument — adds are monotonic, and the one
// non-monotonic add recomputes on both sides — but an argument is not a pinned
// fact, and this is where a divergence would show.
//
// THE EXISTING ROW IS THE FALSIFIABLE TERM. A created row has no pre-value, so
// a case that only read the new row's flag could not tell "the create marked
// it" from "the column defaults that way". The reverse edge points an existing,
// already-read row at the new one, so the create must flip a row it did not
// create.
//
// The child is the transitive half, asserted with zero direct blocker edges of
// its own: creating a child of a blocked parent must leave it blocked in the
// creating transaction, not at the next recompute.
func RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bscreate-blocker"
	waiting := fixture.IssuePrefix + "-bscreate-waiting"
	free := fixture.IssuePrefix + "-bscreate-free"
	created := fixture.IssuePrefix + "-bscreate-new"
	for _, id := range []string{blocker, waiting, free} {
		seedIssueOperationsLabeledIssue(t, ctx, fixture, id)
	}

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requireUnblocked(t, blockedIssue(waiting), "the existing row is clean until the create points it at the new one")

	flip := probe.watchFlip(t, []blockedStateRow{blockedIssue(waiting)}, []blockedStateRow{blockedIssue(free)})
	result, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: created, Title: created, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		},
		Dependencies: []publicops.CreateDependency{
			// The new issue is blocked by an existing one...
			{TargetID: blocker, Type: types.DepBlocks},
			// ...and an existing one is blocked by the new issue.
			{TargetID: waiting, Type: types.DepBlocks, Reverse: true},
		},
	})
	if err != nil {
		t.Fatalf("create %s with edges in both directions: %v", created, err)
	}
	if result.Issue == nil || result.Issue.ID != created {
		t.Fatalf("create result = %#v, want the issue at %s", result.Issue, created)
	}

	flip.requireFlippedTo(t, 1, "a reverse edge created with an issue blocks the row it points at, inside the creating transaction")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(created), blockedIssue(blocker),
		"the created row settled against its own forward edge in the same transaction")

	// The transitive half: a child created under the blocked new row inherits
	// the block with no blocker of its own.
	child, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{Title: "child of a blocked parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		ParentID:      created,
	})
	if err != nil {
		t.Fatalf("create a child under the blocked %s: %v", created, err)
	}
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child.Issue.ID),
		"a child created under a blocked parent inherits the block in its own creating transaction")
	probe.requireUnblocked(t, blockedIssue(free), "the control never entered any affected set")
}

// RunIssueOperationsClaimLeavesBlockedStateAlone pins the last clause of
// issueops.BlockedStateInvariant: settling never reaches outside the affected
// set, so an unrelated blocked row is still blocked — for the same reason —
// after a neighboring claim.
//
// It is the only case in this family with no flag of its own to flip, so its
// falsifiable term is the STATUS the claim really moved: if the claim did not
// land, the case fails before it asserts anything about blocked state. The
// claimed row carries a `related` edge onto an open issue, which is the shape a
// predicate that counted every edge type would mark — so this case is red
// against that mutation and no other case is.
//
// It extends internal/storage/domain/db's ClaimDoesNotChangeIsBlocked, which
// runs on the unit-of-work leg alone, to all three, and adds the updated_at
// half.
func RunIssueOperationsClaimLeavesBlockedStateAlone(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bsclaim-blocker"
	blocked := fixture.IssuePrefix + "-bsclaim-blocked"
	neighbor := fixture.IssuePrefix + "-bsclaim-neighbor"
	claimed := fixture.IssuePrefix + "-bsclaim-claimed"
	seedIssueOperationsLabeledIssue(t, ctx, fixture, blocker)
	seedIssueOperationsLabeledIssue(t, ctx, fixture, neighbor)
	createIssueOperationsBlockedIssue(t, ctx, fixture, blocked, blocker)
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: claimed, Title: claimed, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		},
		Dependencies: []publicops.CreateDependency{{TargetID: neighbor, Type: types.DepRelated}},
	}); err != nil {
		t.Fatalf("create the claim target %s with a non-blocking edge: %v", claimed, err)
	}

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requireBlockedByOpenBlocker(t, blockedIssue(blocked), blockedIssue(blocker), "the bystander is blocked for a reason the claim does not touch")
	probe.requireUnblocked(t, blockedIssue(claimed), "a related edge onto an open issue is not a block")

	// claimed is a FLAG control, not an updated_at control: the claim writes that
	// row on purpose.
	unmoved := probe.watchControls(t, blockedIssue(blocked), blockedIssue(claimed), blockedIssue(neighbor)).
		alsoWrites(blockedIssue(claimed))
	updated, err := fixture.Operations.Update(ctx, publicops.UpdateRequest{Actor: "claimer", IssueID: claimed, Claim: true})
	if err != nil {
		t.Fatalf("claim %s: %v", claimed, err)
	}
	// The must-flip term: without a landed claim this case asserts nothing.
	if !updated.Changed || updated.Issue.Status != types.StatusInProgress {
		t.Fatalf("claim of %s = (Changed %v, status %q), want a committed move to %q",
			claimed, updated.Changed, updated.Issue.Status, types.StatusInProgress)
	}
	if got := probe.rawStatus(t, blockedIssue(claimed)); got != string(types.StatusInProgress) {
		t.Fatalf("stored status for %s = %q, want %q: the claim has to have landed for the rest of this case to mean anything",
			claimed, got, types.StatusInProgress)
	}

	unmoved.requireControlsUnmoved(t, "a claim is not a blocked-state event and reaches nothing outside its own row")
}

// createIssueOperationsBlockedIssue creates one open task blocked by an
// existing issue, through the role's own create-with-edges. The flag it ends up
// carrying is EARNED by that verb; nothing in these fixtures can write it.
func createIssueOperationsBlockedIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, blockerID string) {
	t.Helper()
	createIssueOperationsTypedBlockedIssue(t, ctx, fixture, id, blockerID, types.DepBlocks)
}

// createIssueOperationsTypedBlockedIssue is the same create with the edge type
// named, for the case whose subject is the OTHER type the blocking predicate
// accepts. The type travels on the request rather than on a seeding hook so the
// edge and the flag both come from role verbs.
func createIssueOperationsTypedBlockedIssue(
	t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, blockerID string, depType types.DependencyType,
) {
	t.Helper()
	if _, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		},
		Dependencies: []publicops.CreateDependency{{TargetID: blockerID, Type: depType}},
	}); err != nil {
		t.Fatalf("create %s blocked by %s through a %s edge: %v", id, blockerID, depType, err)
	}
}

// assertIssueOperationsEdgeTypeCount counts one row's outgoing edges OF ONE
// TYPE. The blocked-state cases that name an edge type need it: a subject whose
// flag is attributed to a conditional-blocks edge proves nothing unless the row
// is known to carry no plain blocks edge as well.
func assertIssueOperationsEdgeTypeCount(
	t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, depType string, want int,
) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = ?", []any{id, depType}, &got); err != nil {
		t.Fatalf("count %s edges out of %s: %v", depType, id, err)
	}
	if got != want {
		t.Fatalf("%s carries %d %s edges, want %d: the case attributes its blocked state to a NAMED edge type", id, got, depType, want)
	}
}

// seedIssueOperationsLabeledIssue creates one open task at an explicit ID
// carrying labels, through the store seed hook rather than the guarded create,
// so the labels are already durable state when the case under test runs.
func seedIssueOperationsLabeledIssue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id string, labels ...string) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: labels}
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed labeled %s: %v", id, err)
	}
}

// assertIssueOperationsLabels reads the stored label set back one membership
// query at a time. GROUP_CONCAT ordering is not portable across the three
// fixtures' SQL engines, and the set is what the contract is about.
func assertIssueOperationsLabels(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string, want ...string) {
	t.Helper()
	var total int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{id}, &total); err != nil {
		t.Fatalf("count labels for %s (%s): %v", id, label, err)
	}
	if total != len(want) {
		t.Errorf("%s %s stored label count = %d, want %d (%v)", id, label, total, len(want), want)
	}
	for _, value := range want {
		var present int
		if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{id, value}, &present); err != nil {
			t.Fatalf("look up label %q on %s (%s): %v", value, id, label, err)
		}
		if present != 1 {
			t.Errorf("%s %s stored label %q count = %d, want 1", id, label, value, present)
		}
	}
}

// assertIssueOperationsStringSet compares a result slice as a set, because no
// leaf clause promises an order for labels.
func assertIssueOperationsStringSet(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(gotSorted, wantSorted) && (len(gotSorted) != 0 || len(wantSorted) != 0) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// assertIssueOperationsStoredMetadata reads the metadata column back and
// compares it as a document, since the three fixtures do not agree on
// whitespace or key order in the stored JSON.
func assertIssueOperationsStoredMetadata(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label, want string) {
	t.Helper()
	var stored string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(CAST(metadata AS CHAR), '') FROM issues WHERE id = ?", []any{id}, &stored); err != nil {
		t.Fatalf("read metadata for %s (%s): %v", id, label, err)
	}
	if stored == "" {
		stored = "null"
	}
	assertIssueOperationsMetadata(t, id+" "+label, json.RawMessage(stored), want)
}

// assertIssueOperationsMetadataIsNotNull is the half assertIssueOperations-
// StoredMetadata cannot make: it reads a NULL column back as the JSON literal
// "null" and compares values, so a backend that stored NULL where the leaf
// promises an empty document would need this probe to be caught. It is the
// predicate a consumer filtering on cleared metadata actually writes.
func assertIssueOperationsMetadataIsNotNull(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, label string) {
	t.Helper()
	var isNull bool
	if err := fixture.QueryScalar(ctx, "SELECT metadata IS NULL FROM issues WHERE id = ?", []any{id}, &isNull); err != nil {
		t.Fatalf("probe metadata nullability for %s (%s): %v", id, label, err)
	}
	if isNull {
		t.Errorf("%s metadata (%s) is SQL NULL, want the empty JSON document: metadata is never NULL", id, label)
	}
}

// assertIssueOperationsScalarValue reads one scalar and compares it, reporting
// rather than fatalling so a case keeps collecting evidence.
func assertIssueOperationsScalarValue(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, label, want, query string, args []any) {
	t.Helper()
	var got string
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if got != want {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// assertIssueOperationsAssigneeAndStatus reads back the two columns a claim
// writes, for the refusals that must leave both alone.
func assertIssueOperationsAssigneeAndStatus(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, id, wantAssignee string, wantStatus types.Status) {
	t.Helper()
	var assignee, status string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(assignee, ''), status FROM issues WHERE id = ?", []any{id}, &assignee, &status); err != nil {
		t.Fatalf("read assignee and status for %s: %v", id, err)
	}
	if assignee != wantAssignee {
		t.Errorf("%s assignee = %q, want %q", id, assignee, wantAssignee)
	}
	if types.Status(status) != wantStatus {
		t.Errorf("%s status = %q, want %q", id, status, wantStatus)
	}
}
