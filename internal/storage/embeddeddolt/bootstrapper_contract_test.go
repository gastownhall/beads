//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestBootstrapperContract runs the Bootstrapper and InitVerifier contract
// against the embedded store, which runs the SAME bodies the server-backed
// store does (internal/storage/issueops.BootstrapInTx and VerifyIdentityInTx)
// and differs only in how it reaches a transaction and in the engine
// underneath. That is what this wiring catches; it is not an independent vote
// on the bodies.
//
// One environment for the whole suite, and here that is a correctness
// requirement: the identity is global to a workspace, so the subtests run
// sequentially over one plane, each seeding the state it asserts about.
func TestBootstrapperContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "boot")
	ctx := t.Context()
	fixture := newEmbeddedBootstrapperFixture(t, te)

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
	// ZERO, not one, and not a range. This store reaches its transaction
	// through withConn, which mints no Dolt commit, because `bd init`'s own
	// commit at the front door is what records the identity; the unit-of-work
	// wiring pins ONE because its proxied front door has no commit of its own.
	// The two are a ratified split, so do not "fix" either by matching it to
	// the other — read RunBootstrapperRecordsExactlyOneHistoryEntry first.
	t.Run("RecordsNoHistoryEntryOfItsOwn", func(t *testing.T) {
		conformance.RunBootstrapperRecordsNoHistoryEntryOfItsOwn(t, ctx, fixture)
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

func newEmbeddedBootstrapperFixture(t *testing.T, te *testEnv) conformance.BootstrapperFixture {
	t.Helper()
	bootstrapper, err := te.store.Bootstrapper()
	if err != nil {
		t.Fatalf("Bootstrapper(): %v", err)
	}
	verifier, err := te.store.InitVerifier()
	if err != nil {
		t.Fatalf("InitVerifier(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, "boot")
	return conformance.BootstrapperFixture{
		Bootstrapper: bootstrapper,
		InitVerifier: verifier,
		// Past both roles, through the store's own config and metadata seams:
		// the frozen kit can set a prefix but cannot unset one, and an
		// unidentified substrate is the state a bootstrap needs to be reachable
		// at all on a database newTestEnv already initialized.
		SeedIdentity: func(ctx context.Context, prefix, projectID string) error {
			if err := te.store.SetConfig(ctx, workapi.ConfigKeyIssuePrefix, prefix); err != nil {
				return err
			}
			return te.store.SetMetadata(ctx, workapi.MetadataKeyProjectID, projectID)
		},
		CountHistory: kit.CountHistory,
	}
}
