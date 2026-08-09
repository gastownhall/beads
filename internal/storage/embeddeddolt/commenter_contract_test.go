//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCommenterContract runs the Commenter contract against the embedded
// store, which shares its validate/execute body with the server-backed store
// and differs in the transaction wrapper and the engine underneath. That is
// what this wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced, and the history
// deltas need the subtests sequential anyway.
func TestCommenterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cmt")
	ctx := t.Context()
	fixture := newEmbeddedCommenterFixture(t, te, "cmt")

	t.Run("StoresTextVerbatim", func(t *testing.T) {
		conformance.RunCommenterStoresTextVerbatim(t, ctx, fixture)
	})
	t.Run("ResultMirrorsTheStoredRow", func(t *testing.T) {
		conformance.RunCommenterResultMirrorsTheStoredRow(t, ctx, fixture)
	})
	t.Run("CommentOnAWispLandsOnTheWispThread", func(t *testing.T) {
		conformance.RunCommenterCommentOnAWispLandsOnTheWispThread(t, ctx, fixture)
	})
	t.Run("RefusesAnIDOnNeitherPlane", func(t *testing.T) {
		conformance.RunCommenterRefusesAnIDOnNeitherPlane(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyIssueID", func(t *testing.T) {
		conformance.RunCommenterRefusesAnEmptyIssueID(t, ctx, fixture)
	})
	t.Run("DoesNotResolvePrefixes", func(t *testing.T) {
		conformance.RunCommenterDoesNotResolvePrefixes(t, ctx, fixture)
	})
	t.Run("RecordsExactlyOneHistoryEntry", func(t *testing.T) {
		conformance.RunCommenterRecordsExactlyOneHistoryEntry(t, ctx, fixture)
	})
	t.Run("LeavesTheAnchorIssueUntouched", func(t *testing.T) {
		conformance.RunCommenterLeavesTheAnchorIssueUntouched(t, ctx, fixture)
	})
	t.Run("RefusesBlankText", func(t *testing.T) {
		conformance.RunCommenterRefusesBlankText(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyAuthor", func(t *testing.T) {
		conformance.RunCommenterRefusesAnEmptyAuthor(t, ctx, fixture)
	})
	t.Run("LeavesTheCallersRequestAlone", func(t *testing.T) {
		conformance.RunCommenterLeavesTheCallersRequestAlone(t, ctx, fixture)
	})
}

func newEmbeddedCommenterFixture(t *testing.T, te *testEnv, prefix string) conformance.CommenterFixture {
	t.Helper()
	commenter, err := te.store.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.CommenterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Commenter:    commenter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
