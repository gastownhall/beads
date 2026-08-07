package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMemoriesContract runs the Memories contract against the server-backed
// store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. Config keys are GLOBAL to a workspace, so the
// cases namespace their probe keys under the fixture prefix — except the two
// that cannot be, issue_prefix and the memory that shadows it, which are the
// point of the trap class.
//
// ORDER MATTERS HERE, in two places: ListOfAnEmptyPlaneAnswersAnEmptyMap has to
// see an untouched plane and so runs FIRST, and the refused-write case takes a
// history delta. Both are only meaningful while the subtests run sequentially.
// setupTestStore already marks the PARENT parallel; no subtest here calls
// t.Parallel.
func TestMemoriesContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltMemoriesFixture(t, "mem")
	defer cleanup()

	t.Run("ListOfAnEmptyPlaneAnswersAnEmptyMap", func(t *testing.T) {
		conformance.RunMemoriesListOfAnEmptyPlaneAnswersAnEmptyMap(t, ctx, fixture)
	})
	t.Run("RememberStoresContentVerbatim", func(t *testing.T) {
		conformance.RunMemoriesRememberStoresContentVerbatim(t, ctx, fixture)
	})
	t.Run("RememberDerivesTheKeyWhenAbsent", func(t *testing.T) {
		conformance.RunMemoriesRememberDerivesTheKeyWhenAbsent(t, ctx, fixture)
	})
	t.Run("RememberWithExplicitKeyStoresVerbatim", func(t *testing.T) {
		conformance.RunMemoriesRememberWithExplicitKeyStoresVerbatim(t, ctx, fixture)
	})
	t.Run("RememberReplacesAndReportsIt", func(t *testing.T) {
		conformance.RunMemoriesRememberReplacesAndReportsIt(t, ctx, fixture)
	})
	t.Run("RememberRefusesEmptyContent", func(t *testing.T) {
		conformance.RunMemoriesRememberRefusesEmptyContent(t, ctx, fixture)
	})
	t.Run("RememberRefusesAWhitespaceOnlyKey", func(t *testing.T) {
		conformance.RunMemoriesRememberRefusesAWhitespaceOnlyKey(t, ctx, fixture)
	})
	t.Run("RememberRefusesAnUnderivableKey", func(t *testing.T) {
		conformance.RunMemoriesRememberRefusesAnUnderivableKey(t, ctx, fixture)
	})
	t.Run("RecallAnswersTheStoredValue", func(t *testing.T) {
		conformance.RunMemoriesRecallAnswersTheStoredValue(t, ctx, fixture)
	})
	t.Run("RecallReportsAMissAsNotFoundNotAnError", func(t *testing.T) {
		conformance.RunMemoriesRecallReportsAMissAsNotFoundNotAnError(t, ctx, fixture)
	})
	t.Run("RecallConflatesStoredEmptyWithAbsent", func(t *testing.T) {
		conformance.RunMemoriesRecallConflatesStoredEmptyWithAbsent(t, ctx, fixture)
	})
	t.Run("ForgetRemovesExactlyTheNamedRow", func(t *testing.T) {
		conformance.RunMemoriesForgetRemovesExactlyTheNamedRow(t, ctx, fixture)
	})
	t.Run("ForgetNeverTouchesTheSettingsPlane", func(t *testing.T) {
		conformance.RunMemoriesForgetNeverTouchesTheSettingsPlane(t, ctx, fixture)
	})
	t.Run("ForgetReportsTheForgottenValue", func(t *testing.T) {
		conformance.RunMemoriesForgetReportsTheForgottenValue(t, ctx, fixture)
	})
	t.Run("ForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing", func(t *testing.T) {
		conformance.RunMemoriesForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing(t, ctx, fixture)
	})
	t.Run("ListReturnsOnlyTheMemoryPlane", func(t *testing.T) {
		conformance.RunMemoriesListReturnsOnlyTheMemoryPlane(t, ctx, fixture)
	})
	t.Run("ListSearchMatchesTheUserKeyNotTheStorageKey", func(t *testing.T) {
		conformance.RunMemoriesListSearchMatchesTheUserKeyNotTheStorageKey(t, ctx, fixture)
	})
	t.Run("ListSearchMatchesKeyOrValueCaseInsensitively", func(t *testing.T) {
		conformance.RunMemoriesListSearchMatchesKeyOrValueCaseInsensitively(t, ctx, fixture)
	})
	t.Run("ARefusedWriteRecordsNoHistory", func(t *testing.T) {
		conformance.RunMemoriesARefusedWriteRecordsNoHistory(t, ctx, fixture)
	})
}

func newDoltMemoriesFixture(t *testing.T, prefix string) (conformance.MemoriesFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the accessor, never newMemories: the accessor is where each
	// storage decorator adds its layer.
	memories, err := store.Memories()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("Memories(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.MemoriesFixture{
		IssuePrefix:  kit.IssuePrefix,
		Memories:     memories,
		SetConfig:    kit.SetConfig,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
