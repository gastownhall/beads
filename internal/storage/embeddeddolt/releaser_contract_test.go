//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReleaserContract runs the Releaser contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/storage/issueops.ReleaseIssueInTx) and differs only in how it
// reaches a transaction, in that its version commit is published after that
// transaction rather than inside it, and in carrying no verify-by-re-read —
// the check the server-backed leg has for a degraded server, which an
// in-process engine does not need. That is what this wiring catches; it is not
// an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, every case namespaces its ids, and the history
// deltas need the subtests sequential anyway.
func TestReleaserContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rel")
	ctx := t.Context()
	fixture := newEmbeddedReleaserFixture(t, te, "rel")

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

func newEmbeddedReleaserFixture(t *testing.T, te *testEnv, prefix string) conformance.ReleaserFixture {
	t.Helper()
	releaser, err := te.store.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.ReleaserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Releaser:      releaser,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
		CommitPending: embeddedCommitPending(te),
	}
}
