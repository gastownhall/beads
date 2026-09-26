package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// TestRetentionContract wires this leg into the R20 retention contract.
// Phase 0 leaves every hook nil in every backend's fixture kit (architecture
// §12), so each case below skips by name; this file exists so
// TestEveryLegWiresEveryRoleContract counts this leg, and so the cases start
// running for real the moment this leg's fixture kit grows a non-nil hook.
func TestRetentionContract(t *testing.T) {
	ctx := context.Background()
	fixture := conformance.RetentionFixture{IssuePrefix: "rete"}

	t.Run("RemovalLeavesTheAddressAbleToAnswer", func(t *testing.T) {
		conformance.RunRemovalLeavesTheAddressAbleToAnswer(t, ctx, fixture)
	})
	t.Run("RemovalReasonIsRetentionErasureOrReorganizationDistinctly", func(t *testing.T) {
		conformance.RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly(t, ctx, fixture)
	})
	t.Run("RemovalReportsTheSurvivingRetainedWindow", func(t *testing.T) {
		conformance.RunRemovalReportsTheSurvivingRetainedWindow(t, ctx, fixture)
	})
	t.Run("RemovalNeverReassignsASurvivingAddress", func(t *testing.T) {
		conformance.RunRemovalNeverReassignsASurvivingAddress(t, ctx, fixture)
	})
	t.Run("GoneIsDistinguishableFromUnknown", func(t *testing.T) {
		conformance.RunGoneIsDistinguishableFromUnknown(t, ctx, fixture)
	})
	t.Run("AnAddressNeverResolvesToADifferentState", func(t *testing.T) {
		conformance.RunAnAddressNeverResolvesToADifferentState(t, ctx, fixture)
	})
	t.Run("ADestructiveOperationEnumeratesAffectedAddressesFirst", func(t *testing.T) {
		conformance.RunADestructiveOperationEnumeratesAffectedAddressesFirst(t, ctx, fixture)
	})
	t.Run("AHoldPreventsRemovalAndReportsInRetainedBounds", func(t *testing.T) {
		conformance.RunAHoldPreventsRemovalAndReportsInRetainedBounds(t, ctx, fixture)
	})
	t.Run("ForcingAHeldRemovalRecordsWhoWhenWhy", func(t *testing.T) {
		conformance.RunForcingAHeldRemovalRecordsWhoWhenWhy(t, ctx, fixture)
	})
	t.Run("EveryRetentionAnswerNamesItsProducingStore", func(t *testing.T) {
		conformance.RunEveryRetentionAnswerNamesItsProducingStore(t, ctx, fixture)
	})
	t.Run("AStoreWithNoLineageKnowledgeAnswersUnknownNotGone", func(t *testing.T) {
		conformance.RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone(t, ctx, fixture)
	})
	t.Run("AStoreThatRemovesStateStillAnswersGoneDurably", func(t *testing.T) {
		conformance.RunAStoreThatRemovesStateStillAnswersGoneDurably(t, ctx, fixture)
	})
	t.Run("ErasureMintsACorrectedVersionRatherThanEditingInPlace", func(t *testing.T) {
		conformance.RunErasureMintsACorrectedVersionRatherThanEditingInPlace(t, ctx, fixture)
	})
}

// TestEpochContract runs the R20 epoch contract (gastownhall/beads#5898
// revision 9, this slice: be-x5jqd.4 / #6136) against the server-backed
// store, which reaches internal/storage/issueops's epoch Tx functions
// through this leg's own retrying write transaction or read transaction
// (DoltStore.BumpEpoch/MintUnderEpoch/CurrentAddressFor and
// DoltStore.CurrentEpoch/StillServes/Resolve) — see
// internal/storage/dolt/epoch_cas.go.
//
// All three legs run that one shared body, so this is not an independent
// vote on the design — it is the check on THIS leg's wrapper and this file's
// own conformance-vocabulary translation.
func TestEpochContract(t *testing.T) {
	fixture, ctx, cleanup := newEpochDoltFixture(t, "epch")
	defer cleanup()

	t.Run("AnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange", func(t *testing.T) {
		conformance.RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange(t, ctx, fixture)
	})
	t.Run("EpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed", func(t *testing.T) {
		conformance.RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed(t, ctx, fixture)
	})
}

// newEpochDoltFixture wires this leg's *DoltStore into the R20 epoch
// contract. Like newExpectedRevisionDoltFixture, this fixture needs none of
// roleFixtureKit's CreateIssue/CreateWisp/QueryScalar/CountHistory hooks —
// it has no fields for them — so it does not route through
// newDoltRoleFixtureKit at all; IssuePrefix is set directly, the same value
// the kit would have set verbatim.
func newEpochDoltFixture(t *testing.T, prefix string) (conformance.EpochFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	return conformance.EpochFixture{
		IssuePrefix:       prefix,
		CurrentEpoch:      epochDoltCurrentEpoch(store),
		BumpEpoch:         epochDoltBumpEpoch(store),
		MintUnderEpoch:    epochDoltMintUnderEpoch(store),
		StillServes:       epochDoltStillServes(store),
		Resolve:           epochDoltResolve(store),
		CurrentAddressFor: epochDoltCurrentAddressFor(store),
	}, ctx, stop
}

func epochDoltCurrentEpoch(store *DoltStore) func(ctx context.Context, storeID string) (int, error) {
	return func(ctx context.Context, storeID string) (int, error) {
		return store.CurrentEpoch(ctx, storeID)
	}
}

func epochDoltBumpEpoch(store *DoltStore) func(ctx context.Context, storeID string, trigger conformance.EpochBumpTrigger) (int, error) {
	return func(ctx context.Context, storeID string, trigger conformance.EpochBumpTrigger) (int, error) {
		return store.BumpEpoch(ctx, storeID, trigger.String())
	}
}

func epochDoltMintUnderEpoch(store *DoltStore) func(ctx context.Context, storeID, id string) (conformance.Address, error) {
	return func(ctx context.Context, storeID, id string) (conformance.Address, error) {
		address, err := store.MintUnderEpoch(ctx, storeID, id)
		if err != nil {
			return "", err
		}
		return conformance.Address(address), nil
	}
}

func epochDoltStillServes(store *DoltStore) func(ctx context.Context, storeID string, address conformance.Address) (bool, error) {
	return func(ctx context.Context, storeID string, address conformance.Address) (bool, error) {
		return store.StillServes(ctx, storeID, string(address))
	}
}

func epochDoltResolve(store *DoltStore) func(ctx context.Context, storeID string, address conformance.Address) (conformance.RetentionAnswer, error) {
	return func(ctx context.Context, storeID string, address conformance.Address) (conformance.RetentionAnswer, error) {
		result, err := store.Resolve(ctx, storeID, string(address))
		if err != nil {
			return conformance.RetentionAnswer{}, err
		}
		return epochResolveResultToConformance(result), nil
	}
}

func epochDoltCurrentAddressFor(store *DoltStore) func(ctx context.Context, storeID string, oldAddress conformance.Address) (conformance.Address, error) {
	return func(ctx context.Context, storeID string, oldAddress conformance.Address) (conformance.Address, error) {
		address, err := store.CurrentAddressFor(ctx, storeID, string(oldAddress))
		if err != nil {
			return "", err
		}
		return conformance.Address(address), nil
	}
}

// epochRestrictionToConformance translates this leg's local
// storeops.EpochRestriction into backend/conformance's own Restriction
// vocabulary — the contract-test file is the translation boundary, per
// epoch_cas.go's package doc.
func epochRestrictionToConformance(r storeops.EpochRestriction) conformance.Restriction {
	switch r {
	case storeops.EpochRestrictionLive:
		return conformance.RestrictionLive
	case storeops.EpochRestrictionGoneReorganization:
		return conformance.RestrictionGoneReorganization
	default:
		return conformance.RestrictionUnknown
	}
}

func epochResolveResultToConformance(r storeops.EpochResolveResult) conformance.RetentionAnswer {
	return conformance.RetentionAnswer{
		Restriction:    epochRestrictionToConformance(r.Restriction),
		ProducingStore: r.ProducingStore,
		Epoch:          r.Epoch,
	}
}
