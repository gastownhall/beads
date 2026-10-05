package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestExpectedRevisionContract wires the http leg into the R16/R17
// expected-revision contract, the same way dolt, embeddeddolt and uow do
// (see internal/storage/dolt/expected_revision_contract_test.go). Phase 0
// leaves every hook nil in every backend's fixture kit (architecture §12 of
// be-hs42e.1), so this client — which implements no whole-of-state
// expectedRevision CAS wire operation, and has none to implement until that
// phase ships one — is in exactly the same all-nil state every other leg
// already is. Said honestly: these ten entrypoints are COUNTED here, the
// hook is UNBUILT ON EVERY LEG (not just this one), and every case below
// runs ZERO assertions as a result — this file exists so
// TestEveryLegWiresEveryRoleContract counts the http leg for these ten
// entrypoints, and so the cases start running for real the moment a
// CompareAndSetVersion wire operation exists for this client to bind here.
func TestExpectedRevisionContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.ExpectedRevisionFixture{IssuePrefix: "herev"}

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
}
