//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// These tests are this leg's half of the publication-failure rule for the R20
// epoch writers (gastownhall/beads#6664); the server leg's half is
// internal/storage/dolt/epoch_cas_publish_test.go. A write whose SQL commit has
// landed and whose Dolt commit then fails must wrap
// storage.ErrCommitIndeterminate and hand back what it applied, so that nothing
// replays a bump that has already advanced the counter.

// withFailedPublication runs write with every Dolt commit after its SQL commit
// refused: afterSQLCommit marks store read-only in that window, so the rows have
// landed when withMutatingDBConn refuses to stage and commit them.
func withFailedPublication(t *testing.T, store *EmbeddedDoltStore, write func()) {
	t.Helper()
	saved := afterSQLCommit
	afterSQLCommit = func() { store.readOnly = true }
	defer func() {
		afterSQLCommit = saved
		store.readOnly = false
	}()
	write()
}

func TestEpochWritesReportAnUnpublishedCommitAsIndeterminate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "epochpub", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const storeID = "epochpub-store"

	requireUnpublished := func(t *testing.T, name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrReadOnly) {
			t.Fatalf("%s = %v, want the refused Dolt commit (ErrReadOnly)", name, err)
		}
		if !errors.Is(err, storage.ErrCommitIndeterminate) {
			t.Fatalf("%s = %v, want it to wrap storage.ErrCommitIndeterminate: the write has applied, and a caller that takes the error for a write that did not would replay it", name, err)
		}
	}
	currentEpoch := func(t *testing.T) int {
		t.Helper()
		epoch, err := store.CurrentEpoch(ctx, storeID)
		if err != nil {
			t.Fatalf("CurrentEpoch: %v", err)
		}
		return epoch
	}
	stillServes := func(t *testing.T, address string) bool {
		t.Helper()
		serves, err := store.StillServes(ctx, storeID, address)
		if err != nil {
			t.Fatalf("StillServes(%s): %v", address, err)
		}
		return serves
	}

	for _, tc := range []struct {
		name string
		bump func(ctx context.Context, storeID, reason string) (int, error)
	}{
		{name: "BumpEpoch", bump: store.BumpEpoch},
		{name: "BumpEpochCarrying", bump: store.BumpEpochCarrying},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := currentEpoch(t)
			var epoch int
			var err error
			withFailedPublication(t, store, func() {
				epoch, err = tc.bump(ctx, storeID, "publication fails")
			})
			requireUnpublished(t, tc.name, err)
			if epoch != before+1 {
				t.Errorf("%s returned epoch %d with the publication error, want %d: the bump applied, the error only says its Dolt commit did not", tc.name, epoch, before+1)
			}
			if got := currentEpoch(t); got != before+1 {
				t.Errorf("CurrentEpoch after an unpublished %s = %d, want %d", tc.name, got, before+1)
			}
		})
	}

	t.Run("MintUnderEpoch", func(t *testing.T) {
		var address string
		var err error
		withFailedPublication(t, store, func() {
			address, err = store.MintUnderEpoch(ctx, storeID, "record-a")
		})
		requireUnpublished(t, "MintUnderEpoch", err)
		if address == "" {
			t.Fatal("MintUnderEpoch returned no address with the publication error, want the address it minted")
		}
		if !stillServes(t, address) {
			t.Errorf("StillServes(%s) = false after an unpublished mint, want the minted address served", address)
		}
	})

	t.Run("LoseVersion", func(t *testing.T) {
		address, err := store.MintUnderEpoch(ctx, storeID, "record-b")
		if err != nil {
			t.Fatalf("MintUnderEpoch: %v", err)
		}
		withFailedPublication(t, store, func() {
			err = store.LoseVersion(ctx, storeID, address)
		})
		requireUnpublished(t, "LoseVersion", err)
		if stillServes(t, address) {
			t.Errorf("StillServes(%s) = true after an unpublished loss, want the loss applied", address)
		}
	})

	// A write refused before its SQL commit applied nothing, so its error must
	// not claim it may have.
	t.Run("a bump refused before its SQL commit", func(t *testing.T) {
		before := currentEpoch(t)
		store.readOnly = true
		epoch, err := store.BumpEpoch(ctx, storeID, "refused")
		store.readOnly = false
		if !errors.Is(err, ErrReadOnly) || errors.Is(err, storage.ErrCommitIndeterminate) {
			t.Fatalf("BumpEpoch on a read-only store = %v, want ErrReadOnly, not marked storage.ErrCommitIndeterminate", err)
		}
		if epoch != 0 {
			t.Errorf("BumpEpoch on a read-only store returned epoch %d, want 0", epoch)
		}
		if got := currentEpoch(t); got != before {
			t.Errorf("CurrentEpoch after a refused bump = %d, want %d", got, before)
		}
	})
}
