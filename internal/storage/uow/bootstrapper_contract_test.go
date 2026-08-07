package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestBootstrapperContract runs the Bootstrapper and InitVerifier contract
// against the unit-of-work provider — the one implementation that does not run
// internal/storage/issueops.BootstrapInTx, so this is the wiring where a
// genuine body divergence shows up. The two store backends share those bodies
// between them, which makes this the SECOND of two votes rather than the third.
//
// This body reads, refuses and writes inside ONE transaction and every
// assertion reads back through a NEW one, so a write that never committed shows
// up here and nowhere else.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so the config and metadata planes and dolt_log are
// database-global and a parallel subtest would corrupt another subtest's seeded
// identity and history delta.
func TestBootstrapperContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWBootstrapperFixture(t, ctx)

	t.Run("IdentifiesAFreshSubstrate", func(t *testing.T) {
		conformance.RunBootstrapperIdentifiesAFreshSubstrate(t, ctx, fixture)
	})
	t.Run("StoresThePrefixWithoutItsTrailingHyphen", func(t *testing.T) {
		conformance.RunBootstrapperStoresThePrefixWithoutItsTrailingHyphen(t, ctx, fixture)
	})
	t.Run("RefusesAnIdentifiedSubstrate", func(t *testing.T) {
		conformance.RunBootstrapperRefusesAnIdentifiedSubstrate(t, ctx, fixture)
	})
	t.Run("RefusesASubstrateCarryingOnlyAPrefix", func(t *testing.T) {
		conformance.RunBootstrapperRefusesASubstrateCarryingOnlyAPrefix(t, ctx, fixture)
	})
	t.Run("RefusesASubstrateCarryingOnlyAProjectID", func(t *testing.T) {
		conformance.RunBootstrapperRefusesASubstrateCarryingOnlyAProjectID(t, ctx, fixture)
	})
	t.Run("RefusesAnInvalidRequestWithoutWriting", func(t *testing.T) {
		conformance.RunBootstrapperRefusesAnInvalidRequestWithoutWriting(t, ctx, fixture)
	})
	t.Run("LeavesTheSubstrateUntouchedWhenItCannotComplete", func(t *testing.T) {
		conformance.RunBootstrapperLeavesTheSubstrateUntouchedWhenItCannotComplete(t, ctx, fixture)
	})
	t.Run("RecordsAtMostOneHistoryEntry", func(t *testing.T) {
		conformance.RunBootstrapperRecordsAtMostOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("VerifierAnswersEmptyForAnUnidentifiedSubstrate", func(t *testing.T) {
		conformance.RunInitVerifierAnswersEmptyForAnUnidentifiedSubstrate(t, ctx, fixture)
	})
	t.Run("VerifierReportsAPartialIdentityAsItStands", func(t *testing.T) {
		conformance.RunInitVerifierReportsAPartialIdentityAsItStands(t, ctx, fixture)
	})
	t.Run("VerifierReportsAFailedReadAsAnError", func(t *testing.T) {
		conformance.RunInitVerifierReportsAFailedReadAsAnError(t, ctx, fixture)
	})
	t.Run("VerifierWritesNothing", func(t *testing.T) {
		conformance.RunInitVerifierWritesNothing(t, ctx, fixture)
	})
}

func newUOWBootstrapperFixture(t *testing.T, ctx context.Context) conformance.BootstrapperFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, "boot")
	// Through the capability accessors, not NewBootstrapper/NewInitVerifier: a
	// provider that stopped offering either role is the regression, and a
	// constructor call would hide it.
	bootstrapSource, ok := provider.(BootstrapperSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Bootstrapper accessor", provider)
	}
	bootstrapper, err := bootstrapSource.Bootstrapper()
	if err != nil {
		t.Fatalf("Bootstrapper(): %v", err)
	}
	verifySource, ok := provider.(InitVerifierSource)
	if !ok {
		t.Fatalf("provider %T does not offer the InitVerifier accessor", provider)
	}
	verifier, err := verifySource.InitVerifier()
	if err != nil {
		t.Fatalf("InitVerifier(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, "boot")
	return conformance.BootstrapperFixture{
		Bootstrapper: bootstrapper,
		InitVerifier: verifier,
		// Past both roles, in their own committed transaction: the frozen kit
		// can set a prefix but cannot unset one, and an unidentified substrate
		// is the state a bootstrap needs to be reachable at all.
		SeedIdentity: func(ctx context.Context, prefix, projectID string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				cfg := uw.ConfigUseCase()
				if err := cfg.SetConfig(ctx, workapi.ConfigKeyIssuePrefix, prefix); err != nil {
					return "", err
				}
				return "bd: seed workspace identity",
					cfg.SetMetadata(ctx, workapi.MetadataKeyProjectID, projectID)
			})
		},
		CountHistory: kit.CountHistory,
	}
}
