//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// These tests are the embedded-leg half of gastownhall/beads#6664 Major 2
// (bee-ghosttrack, review 5360880888).
// MintUnderEpoch and CurrentAddressFor staged only epoch_minted_addresses,
// while ensureStoreEpochRow lazily inserts store_epoch's singleton row on
// first use, so that row stayed modified and uncommitted. The embedded leg
// never published inside the transaction as the server leg did; it is not free
// of this omission.

// epochTablesInWorkingSet lists the R20 epoch tables that still appear in
// dolt_status, staged or not: after a write has been published, neither may.
func epochTablesInWorkingSet(status *storage.Status) []string {
	var dirty []string
	for _, entry := range append(slices.Clone(status.Staged), status.Unstaged...) {
		if entry.Table == "store_epoch" || entry.Table == "epoch_minted_addresses" {
			dirty = append(dirty, fmt.Sprintf("%s (%s)", entry.Table, entry.Status))
		}
	}
	return dirty
}

// TestFirstMintLeavesTheEpochTablesCommitted reproduces bee-ghosttrack's probe
// (Status() after the first mint on a fresh store read
// unstaged=[{store_epoch modified}]) on the embedded leg.
func TestFirstMintLeavesTheEpochTablesCommitted(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()

	// A pristine store: store_epoch has no row, so this mint is what seeds it.
	if _, err := te.store.MintUnderEpoch(ctx, "epoch-store", "record-a"); err != nil {
		t.Fatalf("MintUnderEpoch on a fresh store: %v", err)
	}

	status, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after the first mint the working set still holds %v: the mint's Dolt commit must include the singleton row it seeded", dirty)
	}
}

// TestCurrentAddressForCommitsAReseededStoreEpochRow covers the same omission
// on the CurrentAddressFor path, which the first-mint probe cannot reach (the
// mint that minted the old address already seeded the row). A store whose
// singleton row is absent is legitimate state — migration 0067 starts the
// table empty and treats "no row" as epoch 1 — and CurrentAddressFor re-seeds
// it through ensureStoreEpochRow exactly as a first mint does.
func TestCurrentAddressForCommitsAReseededStoreEpochRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()

	address, err := te.store.MintUnderEpoch(ctx, "epoch-store", "record-a")
	if err != nil {
		t.Fatalf("MintUnderEpoch: %v", err)
	}
	// Settle everything so far, then leave the singleton row absent with a
	// clean working set.
	if _, err := te.store.CommitAll(ctx, "test: settle the first mint"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	te.exec(t, ctx, "DELETE FROM store_epoch")
	if _, err := te.store.CommitAll(ctx, "test: absent singleton row"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	if _, err := te.store.CurrentAddressFor(ctx, "epoch-store", address); err != nil {
		t.Fatalf("CurrentAddressFor: %v", err)
	}

	status, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after CurrentAddressFor re-seeded the singleton the working set still holds %v: this path must commit store_epoch too", dirty)
	}
}
