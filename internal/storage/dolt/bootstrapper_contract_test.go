package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestBootstrapperContract runs the Bootstrapper and InitVerifier contract
// against the server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. The identity is GLOBAL to a workspace and
// cannot be namespaced under the fixture prefix, so every case seeds it
// explicitly instead of relying on what the case before it left. setupTestStore
// already marks the PARENT parallel; no subtest here calls t.Parallel, because
// two of them take a log delta.
func TestBootstrapperContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltBootstrapperFixture(t)
	defer cleanup()

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

func newDoltBootstrapperFixture(t *testing.T) (conformance.BootstrapperFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	bootstrapper, err := store.Bootstrapper()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("Bootstrapper(): %v", err)
	}
	verifier, err := store.InitVerifier()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("InitVerifier(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, "boot")
	fixture := conformance.BootstrapperFixture{
		Bootstrapper: bootstrapper,
		InitVerifier: verifier,
		// Past both roles, through the store's own config and metadata seams:
		// the frozen kit can SET a prefix but has no way to unset one, and an
		// unidentified substrate is the state a bootstrap needs on a database
		// setupTestStore already initialized.
		SeedIdentity: func(ctx context.Context, prefix, projectID string) error {
			if err := store.SetConfig(ctx, workapi.ConfigKeyIssuePrefix, prefix); err != nil {
				return err
			}
			return store.SetMetadata(ctx, workapi.MetadataKeyProjectID, projectID)
		},
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
