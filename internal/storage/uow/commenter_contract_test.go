package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestCommenterContract runs the Commenter contract against the unit-of-work
// provider — the one Commenter implementation that does not share the
// validate/execute body the two stores share, so this is the wiring where a
// genuine body divergence shows up.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the comment tables are database-global
// and a parallel subtest would corrupt another subtest's count deltas.
func TestCommenterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCommenterFixture(t, ctx, "cmt")

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

func newUOWCommenterFixture(t *testing.T, ctx context.Context, prefix string) conformance.CommenterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewCommenter: a provider that
	// stopped offering the role is the regression, and a constructor call
	// would hide it.
	source, ok := provider.(CommenterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Commenter accessor", provider)
	}
	commenter, err := source.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.CommenterFixture{
		IssuePrefix:  kit.IssuePrefix,
		Commenter:    commenter,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
