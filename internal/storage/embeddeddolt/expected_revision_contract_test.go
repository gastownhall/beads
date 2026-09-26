//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestExpectedRevisionContract runs the R16/R17 expected-revision contract
// against the embedded-Dolt-backed store, which reaches
// internal/storage/issueops.CompareAndSetVersionInTx through this leg's own
// issue-operation transaction (EmbeddedDoltStore.CompareAndSetVersion) or a
// read-only connection (EmbeddedDoltStore.CurrentVersion) — see
// internal/storage/embeddeddolt/expected_revision_cas.go.
//
// All three legs run that one shared body, so this is not an independent
// vote on the design — it is the check on THIS leg's wrapper and this file's
// own "_attribution"/error-sentinel translation. See the contract file's
// header comment.
// CompareAndSetVersion/CurrentVersion/MutateOutsideExpectedRevision are left
// nil: be-80f4a.1 removes R16's per-record CAS backing (architect-ruled
// design departure from gastownhall/beads#5898), so this leg has nothing to
// wire pending Part B. See FixtureIsAnHonestSkipPendingPartB below.
func TestExpectedRevisionContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	_ = newTestEnv(t, "erev")
	ctx := t.Context()
	fixture := conformance.ExpectedRevisionFixture{
		IssuePrefix: "erev",
	}

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
