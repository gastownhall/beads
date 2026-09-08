package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReleaserContract runs the Releaser contract against the server-backed
// store, which wraps internal/storage/issueops.ReleaseIssueInTx in its own
// write transaction. It is the ONE leg that verifies the release by re-reading
// it afterwards and the one whose version-control entry is recorded INSIDE the
// releasing transaction; the other two publish theirs after it.
//
// It is not an independent vote on the body — all three legs run that same
// function — so what this wiring catches is this store's wrapper: a lost
// transaction, a staged set composed wrong, a refusal that stops matching
// errors.Is on the way back up.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch, which is why every case namespaces its seeds
// under fixture.IssuePrefix plus its own tag. setupTestStore already marks the
// PARENT parallel; no subtest here calls t.Parallel, because the history cases
// take a before/after delta.
func TestReleaserContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltReleaserFixture(t, "rel")
	defer cleanup()

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

// newDoltReleaserFixture composes the frozen role kit with this backend's
// accessor.
func newDoltReleaserFixture(t *testing.T, prefix string) (conformance.ReleaserFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	releaser, err := store.Releaser()
	if err != nil {
		stop()
		t.Fatalf("Releaser(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.ReleaserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Releaser:      releaser,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: doltCommitPending(store),
	}, ctx, stop
}
