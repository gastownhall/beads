package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestExpectedRevisionContract wires this leg into the R16/R17
// expected-revision contract. Every hook is nil (architecture §12's Phase 0
// default), so each case below skips by name; this file exists so
// TestEveryLegWiresEveryRoleContract counts this leg, and so the cases
// start running for real the moment this leg grows a real
// CompareAndSetVersion (be-80f4a.2 / gastownhall/beads#6358).
//
// This leg previously wired every hook to an honest "not implemented" stub
// (be-x5jqd.1) instead of leaving them nil, so each case ran for real and
// failed for that documented reason rather than skipping. That premise —
// that be-x5jqd.3 would give this leg its CompareAndSetVersion — went
// stale once review moved the real implementation to be-80f4a.2 /
// gastownhall/beads#6358 instead; a stub whose failure message blames a
// bead that will never land it is worse than an honest skip, so this file
// reverts to nil hooks (gastownhall/beads#6664, bee-ghosttrack review
// 5268699223).
func TestExpectedRevisionContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.ExpectedRevisionFixture{IssuePrefix: "erev"}

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
