//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// These tests are the embedded-leg half of gastownhall/beads#6664 Major 2
// (bee-ghosttrack, review 5360880888).
// MintUnderEpoch and CurrentAddressFor staged only epoch_minted_addresses,
// while ensureStoreEpochRow lazily inserts store_epoch's singleton row on
// first use, so that row stayed modified and uncommitted. The embedded leg
// never published inside the transaction as the server leg did; it is not free
// of this omission.
//
// The tests after those run the rest of the epoch write surface on the real
// engine: the token-scheme-change carry (BumpEpochCarrying), the explicit loss
// (LoseVersion), and CurrentAddressFor, which only reads. They check what the
// statement-level unit tests cannot: set semantics across rows, a bump that
// rolls back, and what the working set holds once each write is published.

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

// TestCurrentAddressForNeverReseedsAnAbsentStoreEpochRow is the successor of the
// test that pinned CurrentAddressFor committing a store_epoch row it re-seeded.
// CurrentAddressFor no longer writes anything (gastownhall/beads#6664,
// bee-ghosttrack review 5360880888, Major 1: a lookup must not mint), so the
// same setup now pins the opposite. A store whose singleton row is absent is
// legitimate state — migration 0067 starts the table empty and treats "no row"
// as epoch 1 — and CurrentAddressFor answers from that default: it returns the
// address it was given, leaves the working set clean, and does not create the
// row.
func TestCurrentAddressForNeverReseedsAnAbsentStoreEpochRow(t *testing.T) {
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

	got, err := te.store.CurrentAddressFor(ctx, "epoch-store", address)
	if err != nil {
		t.Fatalf("CurrentAddressFor: %v", err)
	}
	if got != address {
		t.Errorf("CurrentAddressFor = %s, want the address it was given (%s): nothing carried the Version, so it resolves to itself", got, address)
	}

	var rows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM store_epoch", nil, &rows)
	if rows != 0 {
		t.Errorf("store_epoch holds %d rows after CurrentAddressFor, want 0: a lookup must not re-create the singleton row", rows)
	}

	status, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after CurrentAddressFor the working set holds %v: a lookup changes nothing, so there is nothing to commit", dirty)
	}
}

// TestBumpEpochCarryingLeavesTheEpochTablesCommitted is the publication rule
// for the token-scheme-change bump. It writes store_epoch (the counter) and
// epoch_minted_addresses (the carry rows), so its Dolt commit has to stage
// both: a bump that staged only store_epoch would leave the carry rows
// modified and uncommitted.
func TestBumpEpochCarryingLeavesTheEpochTablesCommitted(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	epochMint(t, ctx, te, store, "record-a")
	newEpoch := epochCarry(t, ctx, te, store)

	// The carry has to have happened for the check below to mean anything.
	if got := epochRowsAt(t, ctx, te, store, newEpoch); got != 1 {
		t.Fatalf("after the carrying bump to epoch %d the store holds %d rows minted at that epoch, want 1 carry row", newEpoch, got)
	}
	status, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after the carrying bump the working set still holds %v: its Dolt commit must include the carry rows as well as the counter", dirty)
	}
}

// TestLoseVersionLeavesTheEpochTablesCommitted is the same rule for an explicit
// loss, which writes epoch_minted_addresses alone: a loss the caller believes
// recorded must not be left modified and uncommitted.
func TestLoseVersionLeavesTheEpochTablesCommitted(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	address := epochMint(t, ctx, te, store, "record-a")
	if err := te.store.LoseVersion(ctx, store, address); err != nil {
		t.Fatalf("LoseVersion: %v", err)
	}

	// The loss has to have been recorded for the check below to mean anything.
	epoch, err := te.store.CurrentEpoch(ctx, store)
	if err != nil {
		t.Fatalf("CurrentEpoch: %v", err)
	}
	epochRequireGone(t, ctx, te, store, address, epoch, "LoseVersion recorded the loss of this address")

	status, err := te.store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if dirty := epochTablesInWorkingSet(status); len(dirty) > 0 {
		t.Errorf("after LoseVersion the working set still holds %v: the loss must be published with the row it changed", dirty)
	}
}

// TestBumpEpochCarryingWritesOneCarryRowPerLiveIDNamingTheLineageRoot pins the
// write surface of the token-scheme-change bump on the real engine
// (gastownhall/beads#6664, bee-ghosttrack review 5360880888, Major 1): the bump
// records the carry itself, one row per live id at the new epoch, instead of
// leaving it to be inferred from a later mint. Each carry row names the lineage
// ROOT in carried_from, so a second carry still names the first root, lineages
// stay flat, and every address of a lineage is one lookup from the newest.
func TestBumpEpochCarryingWritesOneCarryRowPerLiveIDNamingTheLineageRoot(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	rootA := epochMint(t, ctx, te, store, "record-a")
	rootB := epochMint(t, ctx, te, store, "record-b")

	firstEpoch := epochCarry(t, ctx, te, store)
	if got := epochRowsAt(t, ctx, te, store, firstEpoch); got != 2 {
		t.Fatalf("after the carrying bump to epoch %d the store holds %d rows minted at that epoch, want 2: one carry row per live id", firstEpoch, got)
	}
	carryA := epochRowAddress(t, ctx, te, store, "record-a", firstEpoch)
	carryB := epochRowAddress(t, ctx, te, store, "record-b", firstEpoch)
	for _, c := range []struct{ id, carry, root string }{
		{"record-a", carryA, rootA},
		{"record-b", carryB, rootB},
	} {
		if carriedFrom := epochCarriedFrom(t, ctx, te, c.carry); !carriedFrom.Valid || carriedFrom.String != c.root {
			t.Errorf("the carry row of %s names carried_from %v, want its root %s", c.id, carriedFrom, c.root)
		}
		if goneAt := epochGoneAt(t, ctx, te, c.carry); goneAt.Valid {
			t.Errorf("the carry row of %s is already marked gone at epoch %d, want it live", c.id, goneAt.Int64)
		}
	}
	for _, c := range []struct{ old, carry string }{{rootA, carryA}, {rootB, carryB}} {
		if got, err := te.store.CurrentAddressFor(ctx, store, c.old); err != nil || got != c.carry {
			t.Errorf("CurrentAddressFor(%s) = %q, %v, want the carry address %s", c.old, got, err, c.carry)
		}
		epochRequireServed(t, ctx, te, store, c.old, "the old address keeps resolving through the carry")
		epochRequireServed(t, ctx, te, store, c.carry, "the carry address is served under the new epoch")
	}

	// A second carry still names the first ROOT, not the previous carry row.
	secondEpoch := epochCarry(t, ctx, te, store)
	secondCarryA := epochRowAddress(t, ctx, te, store, "record-a", secondEpoch)
	if carriedFrom := epochCarriedFrom(t, ctx, te, secondCarryA); !carriedFrom.Valid || carriedFrom.String != rootA {
		t.Errorf("the second carry row of record-a names carried_from %v, want the first root %s: carried_from names the root, never the previous carry", carriedFrom, rootA)
	}
	for _, member := range []string{rootA, carryA} {
		if got, err := te.store.CurrentAddressFor(ctx, store, member); err != nil || got != secondCarryA {
			t.Errorf("CurrentAddressFor(%s) = %q, %v, want %s: every address of the lineage points straight at the newest one", member, got, err, secondCarryA)
		}
	}
}

// TestBumpEpochCarryingCarriesOnlyTheLineageHoldingTheNewestRow pins the
// collision rule. A mint never retires an address, so one id can have two live
// lineages: here a second root minted after a restore. Both cannot receive an
// address, because an address is a function of the store, the id and the epoch
// and so is one per id per epoch. The lineage holding the newest row is
// carried, and the other is marked gone at the bump's epoch: over-voiding is
// the safe direction, reviving is not.
func TestBumpEpochCarryingCarriesOnlyTheLineageHoldingTheNewestRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	first := epochMint(t, ctx, te, store, "record-x")
	epochBump(t, ctx, te, store)
	second := epochMint(t, ctx, te, store, "record-x")
	if first == second {
		t.Fatalf("minting record-x again after a bump returned %s, want a second root for the new epoch", first)
	}

	newEpoch := epochCarry(t, ctx, te, store)

	carry := epochRowAddress(t, ctx, te, store, "record-x", newEpoch)
	if carriedFrom := epochCarriedFrom(t, ctx, te, carry); !carriedFrom.Valid || carriedFrom.String != second {
		t.Errorf("the carry row names carried_from %v, want %s: the lineage holding the newest row is the one carried", carriedFrom, second)
	}
	if goneAt := epochGoneAt(t, ctx, te, first); !goneAt.Valid || goneAt.Int64 != int64(newEpoch) {
		t.Errorf("the losing lineage's root has gone_at_epoch %v, want %d: it is marked gone at the epoch of the bump", goneAt, newEpoch)
	}
	if rows := epochCarryRowsOf(t, ctx, te, first); rows != 0 {
		t.Errorf("%d carry rows name the losing lineage's root, want 0: a lineage that lost the collision is not carried", rows)
	}

	epochRequireGone(t, ctx, te, store, first, newEpoch, "the lineage that lost the collision is no longer served")
	epochRequireServed(t, ctx, te, store, second, "the lineage holding the newest row is still served")
	if got, err := te.store.CurrentAddressFor(ctx, store, second); err != nil || got != carry {
		t.Errorf("CurrentAddressFor(%s) = %q, %v, want the carry address %s", second, got, err, carry)
	}
	if got, err := te.store.CurrentAddressFor(ctx, store, first); err == nil {
		t.Errorf("CurrentAddressFor(the losing lineage's root) = %s with no error, want an error: a lost lineage has no current address", got)
	}
}

// TestBumpEpochCarryingNeitherCarriesNorServesARowFromAFutureEpoch pins how the
// carry treats an anomaly a rewound counter can leave behind: a row minted in
// an epoch that has not happened yet. It is not carried, and it is not served
// either: a status read fails closed rather than trusting it. The carried live
// row beside it is what makes the check mean anything.
func TestBumpEpochCarryingNeitherCarriesNorServesARowFromAFutureEpoch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	live := epochMint(t, ctx, te, store, "record-a")
	future := epochMint(t, ctx, te, store, "record-future")
	te.exec(t, ctx, "UPDATE epoch_minted_addresses SET minted_epoch = minted_epoch + 5 WHERE address = ?", future)

	newEpoch := epochCarry(t, ctx, te, store)

	if got := epochRowsAt(t, ctx, te, store, newEpoch); got != 1 {
		t.Fatalf("after the carrying bump to epoch %d the store holds %d rows minted at that epoch, want 1: the live row is carried and the row from a future epoch is not", newEpoch, got)
	}
	carry := epochRowAddress(t, ctx, te, store, "record-a", newEpoch)
	if carriedFrom := epochCarriedFrom(t, ctx, te, carry); !carriedFrom.Valid || carriedFrom.String != live {
		t.Errorf("the carry row names carried_from %v, want %s", carriedFrom, live)
	}
	if rows := epochCarryRowsOf(t, ctx, te, future); rows != 0 {
		t.Errorf("%d carry rows name the future-epoch row, want 0: a row from an epoch that has not happened is not carried", rows)
	}
	epochRequireServed(t, ctx, te, store, live, "the live row is served")
	epochRequireGone(t, ctx, te, store, future, newEpoch, "a row minted in an epoch that has not happened yet fails closed")
}

// TestBumpEpochCarryingFailsAndRollsBackWhenARowAlreadyExistsAtTheNewEpoch pins
// the one place a duplicate key is not benign. A mint tolerates losing a race
// to an identical insert, but a row already sitting at the carry's address means
// the epoch counter rewound. The bump must then fail and roll back whole,
// including its increment of the counter, rather than report a carry it did not
// make.
func TestBumpEpochCarryingFailsAndRollsBackWhenARowAlreadyExistsAtTheNewEpoch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	epochMint(t, ctx, te, store, "record-a")
	startEpoch, err := te.store.CurrentEpoch(ctx, store)
	if err != nil {
		t.Fatalf("CurrentEpoch: %v", err)
	}
	nextEpoch := epochBump(t, ctx, te, store)
	epochMint(t, ctx, te, store, "record-a") // a row now exists at nextEpoch
	te.exec(t, ctx, "UPDATE store_epoch SET epoch = ? WHERE id = 1", startEpoch)

	if got, err := te.store.BumpEpochCarrying(ctx, store, "token-scheme-change"); err == nil {
		t.Errorf("BumpEpochCarrying = epoch %d with no error although a row already exists at epoch %d: the counter rewound, so the carry must fail the bump", got, nextEpoch)
	}
	epoch, err := te.store.CurrentEpoch(ctx, store)
	if err != nil {
		t.Fatalf("CurrentEpoch: %v", err)
	}
	if epoch != startEpoch {
		t.Errorf("CurrentEpoch = %d after the failed bump, want %d: a failed carry rolls the counter's increment back with it", epoch, startEpoch)
	}
}

// TestBumpEpochCarryingCarriesTheLiveRowsOfEveryStoreInTheDatabase pins why the
// carry is database-wide. The epoch counter is per database, so one bump moves
// the epoch seen through every storeID; carrying only the rows of the storeID
// that asked would strand every other store's addresses at the scheme boundary.
func TestBumpEpochCarryingCarriesTheLiveRowsOfEveryStoreInTheDatabase(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const first, second = "epoch-store-one", "epoch-store-two"

	oldFirst := epochMint(t, ctx, te, first, "record-a")
	oldSecond := epochMint(t, ctx, te, second, "record-b")

	newEpoch := epochCarry(t, ctx, te, first) // made under the first storeID only

	for _, c := range []struct{ store, id, old string }{
		{first, "record-a", oldFirst},
		{second, "record-b", oldSecond},
	} {
		carry := epochRowAddress(t, ctx, te, c.store, c.id, newEpoch)
		if carriedFrom := epochCarriedFrom(t, ctx, te, carry); !carriedFrom.Valid || carriedFrom.String != c.old {
			t.Errorf("the carry row of %s in %s names carried_from %v, want its root %s", c.id, c.store, carriedFrom, c.old)
		}
		if got, err := te.store.CurrentAddressFor(ctx, c.store, c.old); err != nil || got != carry {
			t.Errorf("CurrentAddressFor(%s) in %s = %q, %v, want the carry address %s", c.old, c.store, got, err, carry)
		}
		epochRequireServed(t, ctx, te, c.store, c.old, "the old address keeps resolving through the carry")
		epochRequireServed(t, ctx, te, c.store, carry, "the carry address is served under the new epoch")
	}
}

// TestBumpEpochCarryingDoesNotCarryALostLineage pins that loss is terminal for
// the carry: a lineage the store lost before the bump gets no carry row and
// stays Gone, while the live lineage beside it is carried.
func TestBumpEpochCarryingDoesNotCarryALostLineage(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const store = "epoch-store"

	kept := epochMint(t, ctx, te, store, "record-kept")
	lost := epochMint(t, ctx, te, store, "record-lost")
	if err := te.store.LoseVersion(ctx, store, lost); err != nil {
		t.Fatalf("LoseVersion(%s): %v", lost, err)
	}

	newEpoch := epochCarry(t, ctx, te, store)

	if got := epochRowsAt(t, ctx, te, store, newEpoch); got != 1 {
		t.Fatalf("after the carrying bump to epoch %d the store holds %d rows minted at that epoch, want 1: only the live lineage is carried", newEpoch, got)
	}
	epochRowAddress(t, ctx, te, store, "record-kept", newEpoch)
	if rows := epochCarryRowsOf(t, ctx, te, lost); rows != 0 {
		t.Errorf("%d carry rows name the lost lineage's root, want 0: a lost lineage is not carried", rows)
	}
	epochRequireGone(t, ctx, te, store, lost, newEpoch, "the carry must not bring a lost lineage back")
	if got, err := te.store.CurrentAddressFor(ctx, store, lost); err == nil {
		t.Errorf("CurrentAddressFor(a lost address) = %s with no error, want an error", got)
	}
	epochRequireServed(t, ctx, te, store, kept, "the live lineage is served")
}

// TestABumpUnderOneStoreIDMovesTheEpochSeenThroughEveryStoreID pins the fact the
// bump documentation states: the epoch counter is per database, not per store,
// and storeID appears only in error text. It deliberately pins no guard in the
// shared body, because the conformance suite uses several synthetic storeIDs in
// one database.
func TestABumpUnderOneStoreIDMovesTheEpochSeenThroughEveryStoreID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "epch")
	ctx := t.Context()
	const one, two = "epoch-store-one", "epoch-store-two"

	for _, bump := range []struct {
		name string
		run  func() (int, error)
	}{
		{"BumpEpoch", func() (int, error) { return te.store.BumpEpoch(ctx, one, "restore") }},
		{"BumpEpochCarrying", func() (int, error) { return te.store.BumpEpochCarrying(ctx, one, "token-scheme-change") }},
	} {
		before, err := te.store.CurrentEpoch(ctx, two)
		if err != nil {
			t.Fatalf("CurrentEpoch(%s): %v", two, err)
		}
		after, err := bump.run()
		if err != nil {
			t.Fatalf("%s(%s): %v", bump.name, one, err)
		}
		seen, err := te.store.CurrentEpoch(ctx, two)
		if err != nil {
			t.Fatalf("CurrentEpoch(%s): %v", two, err)
		}
		if after != before+1 || seen != after {
			t.Errorf("%s under %s moved the epoch from %d to %d, and CurrentEpoch under %s reads %d: the counter is per database, so one bump advances the epoch every storeID sees", bump.name, one, before, after, two, seen)
		}
	}
}

// epochMint mints id under store, failing the test if the mint fails.
func epochMint(t *testing.T, ctx context.Context, te *testEnv, store, id string) string {
	t.Helper()
	address, err := te.store.MintUnderEpoch(ctx, store, id)
	if err != nil {
		t.Fatalf("MintUnderEpoch(%s, %s): %v", store, id, err)
	}
	return address
}

// epochBump moves the epoch with the counter-only bump (a restore, which
// writes no address rows) and returns the new epoch.
func epochBump(t *testing.T, ctx context.Context, te *testEnv, store string) int {
	t.Helper()
	epoch, err := te.store.BumpEpoch(ctx, store, "restore")
	if err != nil {
		t.Fatalf("BumpEpoch(%s): %v", store, err)
	}
	return epoch
}

// epochCarry runs the token-scheme-change bump, which also carries every live
// address to the new epoch, and returns the new epoch.
func epochCarry(t *testing.T, ctx context.Context, te *testEnv, store string) int {
	t.Helper()
	epoch, err := te.store.BumpEpochCarrying(ctx, store, "token-scheme-change")
	if err != nil {
		t.Fatalf("BumpEpochCarrying(%s): %v", store, err)
	}
	return epoch
}

// epochRowsAt counts the rows of store that were minted at epoch. After a
// carrying bump to epoch that is the number of carry rows it wrote, plus any
// row the test minted there itself.
func epochRowsAt(t *testing.T, ctx context.Context, te *testEnv, store string, epoch int) int {
	t.Helper()
	var count int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM epoch_minted_addresses WHERE store_id = ? AND minted_epoch = ?",
		[]any{store, epoch}, &count)
	return count
}

// epochRowAddress returns the address of the one row of id in store minted at
// epoch, failing the test unless there is exactly one.
func epochRowAddress(t *testing.T, ctx context.Context, te *testEnv, store, id string, epoch int) string {
	t.Helper()
	var count int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM epoch_minted_addresses WHERE store_id = ? AND minted_id = ? AND minted_epoch = ?",
		[]any{store, id, epoch}, &count)
	if count != 1 {
		t.Fatalf("store %s holds %d rows for %s minted at epoch %d, want exactly 1", store, count, id, epoch)
	}
	var address string
	te.queryScalar(t, ctx, "SELECT address FROM epoch_minted_addresses WHERE store_id = ? AND minted_id = ? AND minted_epoch = ?",
		[]any{store, id, epoch}, &address)
	return address
}

// epochCarriedFrom reads address's carried_from: not Valid for a row that was
// minted fresh.
func epochCarriedFrom(t *testing.T, ctx context.Context, te *testEnv, address string) sql.NullString {
	t.Helper()
	var carriedFrom sql.NullString
	te.queryScalar(t, ctx, "SELECT carried_from FROM epoch_minted_addresses WHERE address = ?",
		[]any{address}, &carriedFrom)
	return carriedFrom
}

// epochGoneAt reads address's gone_at_epoch: not Valid while the address is
// live.
func epochGoneAt(t *testing.T, ctx context.Context, te *testEnv, address string) sql.NullInt64 {
	t.Helper()
	var goneAt sql.NullInt64
	te.queryScalar(t, ctx, "SELECT gone_at_epoch FROM epoch_minted_addresses WHERE address = ?",
		[]any{address}, &goneAt)
	return goneAt
}

// epochCarryRowsOf counts the carry rows that name root as their lineage root.
func epochCarryRowsOf(t *testing.T, ctx context.Context, te *testEnv, root string) int {
	t.Helper()
	var count int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM epoch_minted_addresses WHERE carried_from = ?",
		[]any{root}, &count)
	return count
}

// epochRequireServed fails unless the store serves address: StillServes is true
// and Resolve answers Live. why says what the assertion checks.
func epochRequireServed(t *testing.T, ctx context.Context, te *testEnv, store, address, why string) {
	t.Helper()
	serves, err := te.store.StillServes(ctx, store, address)
	if err != nil {
		t.Fatalf("StillServes(%s): %v", address, err)
	}
	if !serves {
		t.Errorf("StillServes(%s) = false, want true: %s", address, why)
	}
	result, err := te.store.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", address, err)
	}
	if result.Restriction != storeops.EpochRestrictionLive {
		t.Errorf("Resolve(%s).Restriction = %d, want Live (%d): %s", address, result.Restriction, storeops.EpochRestrictionLive, why)
	}
}

// epochRequireGone fails unless address is no longer served at epoch:
// StillServes is false and Resolve answers GoneReorganization naming epoch.
func epochRequireGone(t *testing.T, ctx context.Context, te *testEnv, store, address string, epoch int, why string) {
	t.Helper()
	serves, err := te.store.StillServes(ctx, store, address)
	if err != nil {
		t.Fatalf("StillServes(%s): %v", address, err)
	}
	if serves {
		t.Errorf("StillServes(%s) = true, want false: %s", address, why)
	}
	result, err := te.store.Resolve(ctx, store, address)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", address, err)
	}
	if result.Restriction != storeops.EpochRestrictionGoneReorganization {
		t.Errorf("Resolve(%s).Restriction = %d, want GoneReorganization (%d): %s", address, result.Restriction, storeops.EpochRestrictionGoneReorganization, why)
	}
	if result.Epoch == nil {
		t.Errorf("Resolve(%s).Epoch = nil, want the current epoch %d: %s", address, epoch, why)
	} else if *result.Epoch != epoch {
		t.Errorf("Resolve(%s).Epoch = %d, want the current epoch %d: %s", address, *result.Epoch, epoch, why)
	}
}
