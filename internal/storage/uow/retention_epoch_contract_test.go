package uow

import (
	"context"
	"fmt"
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
// revision 9, this slice: be-x5jqd.4 / #6136) against the unit-of-work
// provider, which reaches the same internal/storage/issueops's epoch Tx
// functions the two store backends wrap — directly against the unit of
// work's own runner (the *baseUOW/base.tx.Runner() pattern
// expected_revision_contract_test.go's expectedRevisionUOWMutateOutside and
// commenter_contract_test.go's SeedCommentAt both use), NOT through the
// domain issue repository: EpochFixture's six hooks are store-singleton/
// administrative operations with no natural correspondence to
// IssueUseCase's per-issue CRUD/notification semantics, so unlike
// CompareAndSetVersion there is no domain/use-case method to route through
// in the first place — every hook below needs the escape hatch, not just
// one.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so store_epoch/epoch_minted_addresses are
// database-global and a parallel subtest would corrupt another subtest's
// row — same reasoning as TestExpectedRevisionContract's.
func TestEpochContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWEpochFixture(t, ctx, "epch")

	t.Run("AnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange", func(t *testing.T) {
		conformance.RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange(t, ctx, fixture)
	})
	t.Run("EpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed", func(t *testing.T) {
		conformance.RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed(t, ctx, fixture)
	})
}

// newUOWEpochFixture wires this leg's UnitOfWorkProvider into the R20 epoch
// contract. Like newUOWExpectedRevisionFixture, this fixture needs none of
// roleFixtureKit's CreateIssue/CreateWisp/QueryScalar/CountHistory hooks —
// it has no fields for them — so it does not route through
// newUOWRoleFixtureKit at all.
func newUOWEpochFixture(t *testing.T, ctx context.Context, prefix string) conformance.EpochFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	return conformance.EpochFixture{
		IssuePrefix:       prefix,
		CurrentEpoch:      epochUOWCurrentEpoch(provider),
		BumpEpoch:         epochUOWBumpEpoch(provider),
		MintUnderEpoch:    epochUOWMintUnderEpoch(provider),
		StillServes:       epochUOWStillServes(provider),
		Resolve:           epochUOWResolve(provider),
		CurrentAddressFor: epochUOWCurrentAddressFor(provider),
	}
}

// epochUOWRunner recovers the *baseUOW a given attempt's UnitOfWork actually
// is, the same type assertion expectedRevisionUOWMutateOutside makes: Tx.
// Runner() is unexported outside this package and none of the six epoch
// operations has a domain/use-case method to reach it through instead.
func epochUOWRunner(uw UnitOfWork) (storeops.DBTX, error) {
	base, ok := uw.(*baseUOW)
	if !ok {
		return nil, fmt.Errorf("epoch CAS: unit of work %T does not expose the runner the epoch Tx functions need", uw)
	}
	return base.tx.Runner(), nil
}

func epochUOWCurrentEpoch(provider UnitOfWorkProvider) func(ctx context.Context, storeID string) (int, error) {
	return func(ctx context.Context, storeID string) (int, error) {
		return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (int, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return 0, err
			}
			return storeops.CurrentEpochInTx(ctx, runner, storeID)
		})
	}
}

func epochUOWBumpEpoch(provider UnitOfWorkProvider) func(ctx context.Context, storeID string, trigger conformance.EpochBumpTrigger) (int, error) {
	return func(ctx context.Context, storeID string, trigger conformance.EpochBumpTrigger) (int, error) {
		return RunTxResult(ctx, provider, func(ctx context.Context, uw UnitOfWork) (int, string, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return 0, "", err
			}
			epoch, err := storeops.BumpEpochInTx(ctx, runner, storeID, trigger.String())
			if err != nil {
				return 0, "", err
			}
			return epoch, fmt.Sprintf("bd: bump epoch for %s (%s)", storeID, trigger.String()), nil
		})
	}
}

func epochUOWMintUnderEpoch(provider UnitOfWorkProvider) func(ctx context.Context, storeID, id string) (conformance.Address, error) {
	return func(ctx context.Context, storeID, id string) (conformance.Address, error) {
		address, err := RunTxResult(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, string, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return "", "", err
			}
			address, err := storeops.MintUnderEpochInTx(ctx, runner, storeID, id)
			if err != nil {
				return "", "", err
			}
			return address, fmt.Sprintf("bd: mint %s under epoch for %s", id, storeID), nil
		})
		if err != nil {
			return "", err
		}
		return conformance.Address(address), nil
	}
}

func epochUOWStillServes(provider UnitOfWorkProvider) func(ctx context.Context, storeID string, address conformance.Address) (bool, error) {
	return func(ctx context.Context, storeID string, address conformance.Address) (bool, error) {
		return RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (bool, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return false, err
			}
			return storeops.StillServesInTx(ctx, runner, storeID, string(address))
		})
	}
}

func epochUOWResolve(provider UnitOfWorkProvider) func(ctx context.Context, storeID string, address conformance.Address) (conformance.RetentionAnswer, error) {
	return func(ctx context.Context, storeID string, address conformance.Address) (conformance.RetentionAnswer, error) {
		result, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (storeops.EpochResolveResult, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return storeops.EpochResolveResult{}, err
			}
			return storeops.ResolveEpochInTx(ctx, runner, storeID, string(address))
		})
		if err != nil {
			return conformance.RetentionAnswer{}, err
		}
		return epochResolveResultToConformance(result), nil
	}
}

func epochUOWCurrentAddressFor(provider UnitOfWorkProvider) func(ctx context.Context, storeID string, oldAddress conformance.Address) (conformance.Address, error) {
	return func(ctx context.Context, storeID string, oldAddress conformance.Address) (conformance.Address, error) {
		address, err := RunTxResult(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, string, error) {
			runner, err := epochUOWRunner(uw)
			if err != nil {
				return "", "", err
			}
			address, err := storeops.CurrentAddressForInTx(ctx, runner, storeID, string(oldAddress))
			if err != nil {
				return "", "", err
			}
			return address, fmt.Sprintf("bd: current address for %s (%s)", oldAddress, storeID), nil
		})
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
