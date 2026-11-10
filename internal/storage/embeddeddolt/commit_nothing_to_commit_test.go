//go:build cgo

package embeddeddolt_test

import (
	"testing"
)

// TestEmbeddedCommitToleratesNothingToCommit is the GH#3886 regression test:
// `bd bootstrap` always builds an embedded store (no ServerMode) and, on a
// pristine store, calls SetConfig followed by CommitWithConfig with an
// otherwise-clean working set. Before this fix, EmbeddedDoltStore.Commit
// wrapped ANY DOLT_COMMIT error — including Dolt's benign "nothing to
// commit" — as a hard failure, so bootstrap died even though the server
// store (DoltStore) has always tolerated the identical case. Both entry
// points that alias to Commit (Commit itself and CommitWithConfig) must
// succeed here with no working-set changes at all.
func TestEmbeddedCommitToleratesNothingToCommit(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	t.Run("Commit", func(t *testing.T) {
		te := newTestEnv(t, "cnt1")
		// newTestEnv already committed the seed config, so the working set is
		// clean here: this Commit call has nothing new to stage.
		if err := te.store.Commit(ctx, "test: nothing to commit"); err != nil {
			t.Fatalf("Commit on clean working set: %v", err)
		}
	})

	t.Run("CommitWithConfig", func(t *testing.T) {
		te := newTestEnv(t, "cnt2")
		if err := te.store.CommitWithConfig(ctx, "test: nothing to commit"); err != nil {
			t.Fatalf("CommitWithConfig on clean working set: %v", err)
		}
	})
}
