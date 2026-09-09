package uow

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// errExpectedRevisionCASNotImplemented is Phase 3's honest starting point on
// this leg: nothing implements expectedRevision CAS yet (that is
// be-x5jqd.3's job), so every hook below reports exactly that instead of
// pretending to enforce a precondition nothing checks. Un-skipping these
// cases with a hook that fails loudly for a documented reason is the point
// of be-x5jqd.1 — none of them is meant to pass.
var errExpectedRevisionCASNotImplemented = errors.New("expectedRevision CAS is not implemented yet on this leg (be-x5jqd.3)")

func expectedRevisionCASNotImplemented(ctx context.Context, id string, expected *conformance.Address, patch map[string]any) (conformance.ExpectedRevisionResult, error) {
	return conformance.ExpectedRevisionResult{}, errExpectedRevisionCASNotImplemented
}

func expectedRevisionCurrentVersionNotImplemented(ctx context.Context, id string) (conformance.Address, error) {
	return "", errExpectedRevisionCASNotImplemented
}

func expectedRevisionMutateOutsideNotImplemented(ctx context.Context, id string, field, value string) error {
	return errExpectedRevisionCASNotImplemented
}

// TestExpectedRevisionContract wires this leg into the R16/R17
// expected-revision contract. Phase 3 (be-x5jqd.1) wires every hook to an
// honest "not implemented" stub instead of leaving them nil, so each case
// below now runs for real and fails for that documented reason rather than
// skipping; this file exists so TestEveryLegWiresEveryRoleContract counts
// this leg, and so the cases start passing for real the moment be-x5jqd.3
// gives this leg a real CompareAndSetVersion.
func TestExpectedRevisionContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.ExpectedRevisionFixture{
		IssuePrefix:                   "erev",
		CompareAndSetVersion:          expectedRevisionCASNotImplemented,
		CurrentVersion:                expectedRevisionCurrentVersionNotImplemented,
		MutateOutsideExpectedRevision: expectedRevisionMutateOutsideNotImplemented,
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
}
