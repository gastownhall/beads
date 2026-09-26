package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestExpectedRevisionContract runs the R16/R17 expected-revision contract
// against the unit-of-work provider, which reaches the same
// internal/storage/issueops.CompareAndSetVersionInTx the two store backends
// wrap — through the domain issue repository (IssueSQLRepository.
// CompareAndSetVersion / CurrentVersion, internal/storage/domain/db/issue.go)
// rather than through a store accessor. UNLIKE MetadataCAS, this role has no
// public issueops interface for the provider to advertise through a Source
// accessor — the conformance contract's own CompareAndSetVersion hook is a
// bare function type (conformance.PerRecordCASWrite) — so the hooks below
// call RunTxResult/RunTxRead directly instead of going through a
// provider.ExpectedRevisionCAS()-style constructor.
//
// So this is the third wrapper over ONE body, not a third vote. What it can
// still catch is this leg's own wrapper: a request field dropped between this
// file and the use case, a commit message composed for a write that was
// refused, a refusal that stops matching errors.Is on the way back up.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so expected_revision_records is database-global and a
// parallel subtest would corrupt another subtest's row.
func TestExpectedRevisionContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWExpectedRevisionFixture(t, ctx, "erev")

	t.Run("AcceptsAWriteNamingTheCurrentVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion(t, ctx, fixture)
	})
	t.Run("AcceptsAWriteNamingNoVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingNoVersion(t, ctx, fixture)
	})
	t.Run("CoversFieldsOutsideAnyWatchedSubset", func(t *testing.T) {
		conformance.RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionAddress", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionAddress(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionsChangeAttribution", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionsChangeAttribution(t, ctx, fixture)
	})
	t.Run("RefusalIsATypedOutcomeNotAGenericError", func(t *testing.T) {
		conformance.RunRefusalIsATypedOutcomeNotAGenericError(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromAnAcceptedWrite", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromAnAcceptedWrite(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromNotFound", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromNotFound(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromValidationFailure", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromValidationFailure(t, ctx, fixture)
	})
	t.Run("RefusalNeverSilentlyPicksAWinner", func(t *testing.T) {
		conformance.RunRefusalNeverSilentlyPicksAWinner(t, ctx, fixture)
	})
	t.Run("FixtureIsAnHonestSkipPendingPartB", func(t *testing.T) {
		if fixture.CompareAndSetVersion != nil {
			t.Error("CompareAndSetVersion is wired, want nil: be-80f4a.1 removes R16's per-record CAS backing (architect-ruled design departure from gastownhall/beads#5898) and this leg's fixture must honestly skip pending Part B, not fake green")
		}
		if fixture.CurrentVersion != nil {
			t.Error("CurrentVersion is wired, want nil: see CompareAndSetVersion above")
		}
		if fixture.MutateOutsideExpectedRevision != nil {
			t.Error("MutateOutsideExpectedRevision is wired, want nil: see CompareAndSetVersion above")
		}
	})
}

// newUOWExpectedRevisionFixture wires this leg's UnitOfWorkProvider into the
// R16/R17 contract. Unlike newUOWMetadataCASFixture/newUOWCommenterFixture,
// this fixture needs none of roleFixtureKit's CreateIssue/CreateWisp/
// QueryScalar/CountHistory hooks — it has no fields for them — so it does not
// route through newUOWRoleFixtureKit at all, the same reasoning
// newExpectedRevisionDoltFixture gives on the server-backed leg.
// CompareAndSetVersion/CurrentVersion/MutateOutsideExpectedRevision are left
// nil: be-80f4a.1 removes R16's per-record CAS backing (architect-ruled
// design departure from gastownhall/beads#5898), so this leg has nothing to
// wire pending Part B. See FixtureIsAnHonestSkipPendingPartB above.
func newUOWExpectedRevisionFixture(t *testing.T, ctx context.Context, prefix string) conformance.ExpectedRevisionFixture {
	t.Helper()
	_ = newUOWRoleFixtureProvider(t, ctx, prefix)
	return conformance.ExpectedRevisionFixture{
		IssuePrefix: prefix,
	}
}
