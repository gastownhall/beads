package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCommenterContract runs the Commenter contract against the server-backed
// store.
//
// The cases are subtests of one parent so the whole role suite shares one
// store and one copy-on-write branch: each Run namespaces its ids under the
// fixture prefix, and the history cases take before/after deltas, which is
// only meaningful while the subtests run sequentially. setupTestStore already
// marks the PARENT parallel; no subtest here calls t.Parallel.
func TestCommenterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltCommenterFixture(t, "cmt")
	defer cleanup()

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
	t.Run("RecordsAtMostOneHistoryEntry", func(t *testing.T) {
		conformance.RunCommenterRecordsAtMostOneHistoryEntry(t, ctx, fixture)
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

func newDoltCommenterFixture(t *testing.T, prefix string) (conformance.CommenterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	commenter, err := store.Commenter()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("Commenter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.CommenterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Commenter:    commenter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
