//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMemoriesContract runs the Memories contract against the embedded store,
// which hands back the SAME body the server-backed store does (the …InTx
// functions in internal/storage/memoryops), differing only in the engine
// underneath. It is not an independent vote on the body.
//
// One environment for the whole suite, and here that is a CORRECTNESS
// requirement: config keys are global to a workspace, the trap class writes
// issue_prefix and its shadow by name, ListOfAnEmptyPlaneAnswersAnEmptyMap has
// to see an untouched plane and so runs FIRST, and the refused-write case takes
// a history delta — all of which need the subtests sequential over one plane.
func TestMemoriesContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mem")
	ctx := t.Context()
	fixture := newEmbeddedMemoriesFixture(t, te, "mem")

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

func newEmbeddedMemoriesFixture(t *testing.T, te *testEnv, prefix string) conformance.MemoriesFixture {
	t.Helper()
	// Through the accessor, never newMemories: the accessor is where each
	// storage decorator adds its layer.
	memories, err := te.store.Memories()
	if err != nil {
		t.Fatalf("Memories(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.MemoriesFixture{
		IssuePrefix:  kit.IssuePrefix,
		Memories:     memories,
		SetConfig:    kit.SetConfig,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
