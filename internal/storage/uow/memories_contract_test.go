package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestMemoriesContract runs the Memories contract against the unit-of-work
// provider — the one implementation that does not compose the …InTx functions
// in internal/storage/memoryops. The two store backends share those, which
// makes this the SECOND of two votes rather than the third.
//
// It is also the wiring the atomicity upgrade was written for: this route used
// to pre-read in a separate RunTxRead and then open a RunTx to write, so the
// "Remembered" versus "Updated" verb and the value `bd forget` printed
// described a moment that had already passed.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so the config plane and dolt_log are database-global.
// ListOfAnEmptyPlaneAnswersAnEmptyMap runs FIRST because it needs an untouched
// plane.
func TestMemoriesContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWMemoriesFixture(t, ctx, "mem")

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

func newUOWMemoriesFixture(t *testing.T, ctx context.Context, prefix string) conformance.MemoriesFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewMemories: a provider that stopped
	// offering the role is the regression a constructor call would hide.
	source, ok := provider.(MemoriesSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Memories accessor", provider)
	}
	memories, err := source.Memories()
	if err != nil {
		t.Fatalf("Memories(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.MemoriesFixture{
		IssuePrefix:  kit.IssuePrefix,
		Memories:     memories,
		SetConfig:    kit.SetConfig,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
