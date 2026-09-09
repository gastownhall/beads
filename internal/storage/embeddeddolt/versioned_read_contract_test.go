package embeddeddolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
)

// errAsOfReadNotImplemented is Phase 3's honest starting point on this leg:
// nothing implements R7.1 as-of reads yet (that is be-x5jqd.5's job), so
// every hook below reports exactly that instead of pretending to resolve a
// selector nothing tracks. Un-skipping these cases with a hook that fails
// loudly for a documented reason is the point of be-x5jqd.2 — none of them
// is meant to pass.
var errAsOfReadNotImplemented = errors.New("R7.1 as-of reads are not implemented yet on this leg (be-x5jqd.5)")

func asOfReadResolveNotImplemented(ctx context.Context, storeID, id string, selector conformance.AsOfSelector) (conformance.AsOfReadResult, error) {
	return conformance.AsOfReadResult{}, errAsOfReadNotImplemented
}

func asOfReadMintAtNotImplemented(ctx context.Context, storeID, id string, acceptedAt time.Time) (conformance.Address, error) {
	return "", errAsOfReadNotImplemented
}

func asOfReadMakeUnanswerableNotImplemented(ctx context.Context, storeID, id string, restriction conformance.Restriction) error {
	return errAsOfReadNotImplemented
}

// TestAsOfReadContract wires this leg into the R7.1 as-of-read contract.
// be-x5jqd.2 wires every hook to an honest "not implemented" stub instead of
// leaving them nil, so each case below now runs for real and fails for that
// documented reason rather than skipping; this file exists so
// TestEveryLegWiresEveryRoleContract counts this leg, and so the cases start
// passing for real the moment be-x5jqd.5 gives this leg a real Resolve.
func TestAsOfReadContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.AsOfReadFixture{
		IssuePrefix:      "asof",
		Resolve:          asOfReadResolveNotImplemented,
		MintAt:           asOfReadMintAtNotImplemented,
		MakeUnanswerable: asOfReadMakeUnanswerableNotImplemented,
	}

	t.Run("SelectorAcceptsEitherAnInstantOrAVersionAddress", func(t *testing.T) {
		conformance.RunAsOfReadSelectorAcceptsEitherAnInstantOrAVersionAddress(t, ctx, fixture)
	})
	t.Run("ReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater", func(t *testing.T) {
		conformance.RunAsOfReadReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater(t, ctx, fixture)
	})
	t.Run("ReportsTheAddressItServed", func(t *testing.T) {
		conformance.RunAsOfReadReportsTheAddressItServed(t, ctx, fixture)
	})
	t.Run("RefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion", func(t *testing.T) {
		conformance.RunAsOfReadRefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion(t, ctx, fixture)
	})
	t.Run("IsCompleteOnlyForTheAnsweringStoresHoldings", func(t *testing.T) {
		conformance.RunAsOfReadIsCompleteOnlyForTheAnsweringStoresHoldings(t, ctx, fixture)
	})
}
