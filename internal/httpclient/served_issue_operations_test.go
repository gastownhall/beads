//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_issue_operations_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Lifecycle PERSISTENCE-SEAM contracts, run through client → in-process bd
// serve → reference store.
//
// This is the half of the Lifecycle role that the three accessor-reachable
// contract files next door deliberately do not hold: every case here ends on a
// COLUMN rather than on a returned row — the persisted `is_blocked` projection,
// `metadata IS NULL`, a committed `AS OF 'HEAD'` read, the plane a create
// routed to — and no read on any role hydrates those. The contract expresses
// that as a raw-SQL seam on its fixture, and this leg answers it the way it
// answers every other out-of-band probe: through the REFERENCE store the server
// is serving from, never through the client.
//
// WHAT THAT MAKES THE TIER WORTH HERE. The subject is one HTTP request. The
// client sends it, recomputes nothing, and every postcondition below is read
// out of the database afterwards — so a settlement the server did not make
// inside its own transaction, a plane it routed the wrong way, or a metadata
// document it wrote as SQL NULL shows up as a raw-row fact with no role read in
// between to launder it.
//
// UpdateRaw is bound to the REFERENCE store's own untyped funnel
// (env.reference.UpdateIssue), never to the client: storage.UpdateIssue is on
// the client's unsupported allowlist by design (D8: re-routing a raw front
// door would put a second spelling of the edit beside the role's), so there is
// no client funnel to bind. That is fine for every case actually registered in
// this file, because none of them takes the funnel AS ITS SUBJECT — they use it
// only to SEED a precondition an httpLifecycle.Create or the configured-status
// vocabulary cannot reach directly (an already-deferred row, a custom
// "name:category" status create does not parse). The three cases whose SUBJECT
// is the funnel itself (RunIssueOperationsUpdateRawMetadataTakesTheFunnelsValueShapes
// and its siblings) are never called here — asserting the reference store's
// funnel would be the mirror image of seeding through the client, and just as
// empty — so binding this hook carries no risk of laundering a client
// assertion through the store it happens to share a process with.
//
// ONE ENVIRONMENT PER CASE, which the contract requires rather than prefers:
// RunIssueOperationsCreateRoutesInfraTypesToWisps installs BARE workspace-global
// type vocabulary and leaves it installed, and the child-id cases mint against a
// counter a neighbouring case's seeds would move.

func newServedIssueOperationsFixture(t *testing.T, prefix string) conformance.IssueOperationsStagingFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	return conformance.IssueOperationsStagingFixture{
		IssuePrefix: env.prefix,
		// The only hook bound to the CLIENT. The contract calls this field both
		// the seed route and the subject, and where a case seeds through it the
		// seed is itself a served create — every precondition those cases assert
		// afterwards is read back raw.
		Operations:    lifecycle,
		CreateIssue:   env.createIssue,
		AddDependency: env.addDependency,
		GetReadyWork:  env.reference.GetReadyWork,
		SetConfig:     env.setConfig,
		Commit:        env.commitMessage,
		Exec:          env.execRaw,
		QueryScalar:   env.queryScalar,
		UpdateRaw:     env.reference.UpdateIssue,
	}
}

// ── The create half ─────────────────────────────────────────────────────────

// TestServedIssueOperationsCreateRoutesInfraTypesToWisps is the routing case,
// and the plane it asserts is one the client cannot ask for: createIssue
// publishes `ephemeral`, but this case sends a bare infra TYPE and leaves the
// plane to the workspace's own `types.infra` vocabulary. So what runs here is
// the server reading its configured set and routing on it, with the answer read
// out of both tables.
func TestServedIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, t.Context(), newServedIssueOperationsFixture(t, "hio1"))
}

// TestServedIssueOperationsCreateUnderAParentMintsTheNextChildID is the id
// allocator, which is emphatically the SERVER's here: GetNextChildID is on the
// unsupported allowlist, so the client names no id at all and reads the minted
// one off the answer. The case's last arm — a parent whose differently-cased
// twin already has a child — is the one that would catch a counter keyed on a
// case-folded parent.
func TestServedIssueOperationsCreateUnderAParentMintsTheNextChildID(t *testing.T) {
	conformance.RunIssueOperationsCreateUnderAParentMintsTheNextChildID(t, t.Context(), newServedIssueOperationsFixture(t, "hio2"))
}

// TestServedIssueOperationsCreateClosedDerivesTheClosedStamp is PARKED, and
// it is the one park in this file whose refusal is not a gap but the point.
//
// The derivation it pins reads the CREATION STAMP the caller supplied — that is
// what "one second past the later of created_at and updated_at" is a function of
// — and created_at is exactly the member createIssue withholds, because a
// caller-supplied creation time makes the row disagree with the journal entry
// that records it. So the case's subject is the import path, and importing is
// the act this surface says belongs to `bd import` rather than to a create.
// The park will outlive every other one here.
func TestServedIssueOperationsCreateClosedDerivesTheClosedStamp(t *testing.T) {
	skipKnownDivergence(t, "W-CreateRequest.Issue", createParkBead,
		"every arm supplies Issue.CreatedAt and Issue.UpdatedAt, which are the two stamps the derivation is a "+
			"function of and the two createIssue deliberately does not publish; the client refuses them per "+
			"member (asserted by TestCreateRefusesEveryMemberTheWireExcludes) rather than deriving a closed_at "+
			"from stamps the server chose")
	conformance.RunIssueOperationsCreateClosedDerivesTheClosedStamp(t, t.Context(), newServedIssueOperationsFixture(t, "hio3"))
}

func TestServedIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t *testing.T) {
	conformance.RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction(t, t.Context(), newServedIssueOperationsFixture(t, "hio4"))
}

// The two STAGING cases, which are the only ones in this package that read
// `AS OF 'HEAD'`. They dirty an unrelated durable row in the working set, then
// create, then ask what the create's own commit swept in — and over this wire
// the commit is entirely the server's, made in a process that never saw the
// dirty row. The unit-of-work leg waives both because it has no caller-held
// working set; this leg has one, because the reference store the server serves
// is a real embedded engine and the fixture holds it.

func TestServedIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t *testing.T) {
	conformance.RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t, t.Context(), newServedIssueOperationsFixture(t, "hio5"))
}

func TestServedIssueOperationsCreateParentChildRecomputesWaitsForClosure(t *testing.T) {
	conformance.RunIssueOperationsCreateParentChildRecomputesWaitsForClosure(t, t.Context(), newServedIssueOperationsFixture(t, "hio6"))
}

// ── The update half ─────────────────────────────────────────────────────────

func TestServedIssueOperationsUpdateLabelPatchOrdering(t *testing.T) {
	conformance.RunIssueOperationsUpdateLabelPatchOrdering(t, t.Context(), newServedIssueOperationsFixture(t, "hio7"))
}

// TestServedIssueOperationsUpdateLabelPatchValueRules is PARKED on
// L-update-fieldlen, which this wave FOUND: its first arm asserts
// ErrFieldTooLong for a 256-character label, and the operation refuses that at
// the edge with the same `invalid_argument`/`invalid_value` pair every other
// bad value on the member earns.
//
// Everything else the case is about — the duplicate add applied once, the
// absent remove that is a no-op, the empty string dropped rather than stored —
// is unaffected, and the incremental algebra beside them is exercised over this
// wire by TestServedUpdateAppliesTheOrderedLabelEditServerSide.
func TestServedIssueOperationsUpdateLabelPatchValueRules(t *testing.T) {
	skipKnownDivergence(t, "L-update-fieldlen", fieldLenParkBead,
		"the case's first arm binds types.ErrFieldTooLong for an over-long label, and the operation applies "+
			"that bound at the edge with the invalid_value reason a malformed member also earns (asserted, "+
			"ceiling and both legs included, by TestServedUpdateFieldLengthBoundsAreTheServersAndNotThisClients)")
	conformance.RunIssueOperationsUpdateLabelPatchValueRules(t, t.Context(), newServedIssueOperationsFixture(t, "hio8"))
}

// TestServedIssueOperationsUpdateMetadataReplaceClearsAndValidates ends on
// `SELECT metadata IS NULL`, which is the whole reason it lives on this fixture:
// a hydrated issue reads a NULL column and the empty document back as the same
// nil bytes, so the clause — metadata is NEVER SQL NULL — is invisible to every
// read this surface publishes.
func TestServedIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T) {
	conformance.RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t, t.Context(), newServedIssueOperationsFixture(t, "hio9"))
}

func TestServedIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, t.Context(), newServedIssueOperationsFixture(t, "hioa"))
}

func TestServedIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	conformance.RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary(t, t.Context(), newServedIssueOperationsFixture(t, "hiob"))
}

// The two STATUS-CROSSING settlement cases. They are the update's half of what
// the close cases assert next door, and they are the pair the blocked-state kit
// calls two genuine votes: issueops update.go and domain/db issue.go each decide
// independently when a status move counts as a crossing.

func TestServedIssueOperationsUpdateStatusCrossingSettlesDependers(t *testing.T) {
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesDependers(t, t.Context(), newServedIssueOperationsFixture(t, "hioc"))
}

func TestServedIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t *testing.T) {
	conformance.RunIssueOperationsUpdateStatusCrossingSettlesAConditionalBlocksDepender(t, t.Context(), newServedIssueOperationsFixture(t, "hiod"))
}

// TestServedIssueOperationsRequestValuesAreNotMutated is the aliasing tripwire,
// and the one case here whose answer this leg gets for free in a way worth
// saying out loud: the request is SERIALIZED on the way out and the result is
// DESERIALIZED on the way back, so there is no pointer for the role to write
// through even if it wanted to. It runs anyway, because that argument is about
// today's transport and the promise is the role's.
func TestServedIssueOperationsRequestValuesAreNotMutated(t *testing.T) {
	conformance.RunIssueOperationsRequestValuesAreNotMutated(t, t.Context(), newServedIssueOperationsFixture(t, "hioe"))
}

// ── The parks ───────────────────────────────────────────────────────────────
//
// Two, and both are a member updateIssue publishes nothing for. The refusals
// themselves are asserted by TestUpdateRefusesEveryMemberTheWireExcludes,
// which RUNS.
//
// The THREE CLAIM cases below are MOSTLY not parks any more: a claim-only
// UpdateRequest — an actor and an id, nothing else — is claimIssue's own
// request shape, and httpLifecycle.claimOnlyUpdate dials the Claimer role for
// it instead of refusing. What remains permanently refused
// (W-UpdateRequest.Claim) is a claim COMBINED with a patch, a guard or a force
// override — PATCH claim, which no operation on this wire publishes — and two
// of these three cases drive no such combination, so they run for real against
// a served claimIssue and an in-process bd serve over embedded Dolt.
//
// The conflict case parks ITS OWN LAST SECTION ONLY: claimIssue's wire request
// is the actor alone (apigen.ClaimRequest carries no expected_version), so the
// one legal claim/guard composition the local role allows — Claim with a
// stale ExpectedVersion — IS a combination by this wire's own accounting and
// refuses on W-UpdateRequest.Claim exactly as any other claim+guard request
// does. The case's first two sections (foreign live claim, ineligible status)
// are claim-only and are not affected; only the whole function parks, because
// the contract does not offer a way to run part of a case.

func TestServedIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T) {
	skipKnownDivergence(t, "W-UpdateRequest.Claim", parkBead,
		"the case's last section drives Claim with a stale ExpectedVersion — the one claim/guard composition the local role "+
			"allows — and claimIssue's wire request is the actor alone (no expected_version member), so this client refuses "+
			"the combination rather than dropping the guard and answering an unguarded claim; the case's first two sections "+
			"(foreign live claim, ineligible status) are claim-only and are themselves proven servable by the next two cases")
	conformance.RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t, t.Context(), newServedIssueOperationsFixture(t, "hiof"))
}

func TestServedIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T) {
	conformance.RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t, t.Context(), newServedIssueOperationsFixture(t, "hiog"))
}

func TestServedIssueOperationsClaimLeavesBlockedStateAlone(t *testing.T) {
	conformance.RunIssueOperationsClaimLeavesBlockedStateAlone(t, t.Context(), newServedIssueOperationsFixture(t, "hioh"))
}

// fieldLenParkBead is the bead the length-bound park cites, and it is separate
// from parkBead because it retires on a different event: the write-side parks
// wait on the wire growing a MEMBER, and this one waits on it growing a REASON.
// The behaviour is already served; only its classification is coarse.
const fieldLenParkBead = "ga-2ltro.19"

// TestServedIssueOperationsUpdateIssuePlaneOnlyRefusesWisps is the staging
// twin of the accessor-reachable plane case next door, and it parks for the
// same reason: dropping the restriction would EDIT the wisp the caller asked to
// be told did not exist.
func TestServedIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T) {
	skipKnownDivergence(t, "W-UpdateRequest.IssuePlaneOnly", parkBead,
		"updateIssue publishes no plane restriction and the server's role auto-resolves both planes, so the "+
			"client refuses the flag rather than silently editing the wisp the case asked it to refuse")
	conformance.RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t, t.Context(), newServedIssueOperationsFixture(t, "hioi"))
}

// TestServedIssueOperationsUpdateWritesEveryScalarPatchField parks on FOUR
// members at once — spec_id, await_id, owner and closed_by_session — and the
// citation names spec_id because it is the first the encoder reaches. `owner` is
// the odd one: ApplyPatchBody publishes it and IssuePatchBody does not, which
// the document itself calls an accident of order rather than a decision, so this
// park is the one here most likely to narrow rather than retire.
func TestServedIssueOperationsUpdateWritesEveryScalarPatchField(t *testing.T) {
	skipKnownDivergence(t, "W-IssuePatch.SpecID", parkBead,
		"the case writes every scalar the patch carries, and four of them — spec_id, await_id, owner and "+
			"closed_by_session — are members IssuePatchBody does not publish; the client refuses each by name "+
			"rather than reporting a landed edit that dropped them")
	conformance.RunIssueOperationsUpdateWritesEveryScalarPatchField(t, t.Context(), newServedIssueOperationsFixture(t, "hioj"))
}
