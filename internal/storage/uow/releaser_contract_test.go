package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReleaserContract runs the Releaser contract against the unit-of-work
// provider, which reaches the same internal/storage/issueops.ReleaseIssueInTx
// the two store backends wrap — through the domain issue repository rather than
// through a store accessor.
//
// So this is the third wrapper over ONE body, not a third vote. What it can
// still catch is this leg's own wrapper, and one case is written for the shape
// that has already bitten it: an EPHEMERAL release writes a row and versions
// nothing, and this leg's commit message is what commits the SQL transaction as
// well as what versions it, so a wrapper reading "no durable tables" as
// "nothing happened" rolls the write back and the wisp comes out still claimed.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global and
// the history deltas are only meaningful while the subtests run sequentially.
func TestReleaserContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWReleaserFixture(t, ctx, "rel")

	t.Run("ReleasesItsOwnClaim", func(t *testing.T) {
		conformance.RunReleaserReleasesItsOwnClaim(t, ctx, fixture)
	})
	t.Run("RefusesAForeignClaimUntilForced", func(t *testing.T) {
		conformance.RunReleaserRefusesAForeignClaimUntilForced(t, ctx, fixture)
	})
	t.Run("ReleasesOnlyTheExpectedHolder", func(t *testing.T) {
		conformance.RunReleaserReleasesOnlyTheExpectedHolder(t, ctx, fixture)
	})
	t.Run("RefusesAnUnheldIssue", func(t *testing.T) {
		conformance.RunReleaserRefusesAnUnheldIssue(t, ctx, fixture)
	})
	t.Run("RefusesAStatusThatCannotBeReleased", func(t *testing.T) {
		conformance.RunReleaserRefusesAStatusThatCannotBeReleased(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunReleaserRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("RefusesAnAbsentID", func(t *testing.T) {
		conformance.RunReleaserRefusesAnAbsentID(t, ctx, fixture)
	})
	t.Run("AttributesTheReleaseToTheActor", func(t *testing.T) {
		conformance.RunReleaserAttributesTheReleaseToTheActor(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunReleaserRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("ReleasesAWispClaimWithoutVersioning", func(t *testing.T) {
		conformance.RunReleaserReleasesAWispClaimWithoutVersioning(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunReleaserDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

func newUOWReleaserFixture(t *testing.T, ctx context.Context, prefix string) conformance.ReleaserFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewReleaser: a provider that stopped
	// offering the role is the regression a constructor call would hide.
	source, ok := provider.(ReleaserSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Releaser accessor", provider)
	}
	releaser, err := source.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ReleaserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Releaser:      releaser,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: uowCommitPending(provider),
	}
}
