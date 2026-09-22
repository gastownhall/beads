package issueops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestStillServesInTxHonorsARetainedMapping pins R20-n's retained-mapping
// exception (gastownhall/beads#5898 revision 9): "a version that survives
// the transition keeps its address resolving ... under a change of token
// scheme, through a retained mapping from the prior-epoch address, which
// also reports the current one — so an epoch voids what the store lost,
// never what it still serves." Before this fix, StillServesInTx only ever
// checked whether the literal queried address's own row matched the
// current epoch, so a prior-epoch address whose id had already been
// carried forward into the current epoch (via CurrentAddressForInTx or a
// fresh MintUnderEpochInTx) still wrongly reported false.
func TestStillServesInTxHonorsARetainedMapping(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const storeID = "still-serves-store"
	const oldAddress = "epch:still-serves-store:record-a:1"

	mock.ExpectQuery(`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = \?`).
		WithArgs(oldAddress).
		WillReturnRows(sqlmock.NewRows([]string{"store_id", "minted_id", "minted_epoch"}).AddRow(storeID, "record-a", 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(2))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM epoch_minted_addresses WHERE store_id = \? AND minted_id = \? AND minted_epoch = \?`).
		WithArgs(storeID, "record-a", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := StillServesInTx(context.Background(), tx, storeID, oldAddress)
	if err != nil {
		t.Fatalf("StillServesInTx: %v", err)
	}
	if !got {
		t.Fatal("StillServesInTx(a prior-epoch address whose id was carried forward) = false, want true (R20-n's retained-mapping exception)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet retained-mapping SQL expectations: %v", err)
	}
}

// TestStillServesInTxReportsFalseWhenIDWasNotCarriedForward is the
// regression guard for the retained-mapping fix above: a prior-epoch
// address whose id was never re-minted at the current epoch stays not
// served, per R20-n's default (an epoch bump voids what the store lost).
func TestStillServesInTxReportsFalseWhenIDWasNotCarriedForward(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const storeID = "still-serves-store"
	const oldAddress = "epch:still-serves-store:record-b:1"

	mock.ExpectQuery(`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = \?`).
		WithArgs(oldAddress).
		WillReturnRows(sqlmock.NewRows([]string{"store_id", "minted_id", "minted_epoch"}).AddRow(storeID, "record-b", 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(2))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM epoch_minted_addresses WHERE store_id = \? AND minted_id = \? AND minted_epoch = \?`).
		WithArgs(storeID, "record-b", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	got, err := StillServesInTx(context.Background(), tx, storeID, oldAddress)
	if err != nil {
		t.Fatalf("StillServesInTx: %v", err)
	}
	if got {
		t.Fatal("StillServesInTx(a prior-epoch address whose id was never carried forward) = true, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet not-carried-forward SQL expectations: %v", err)
	}
}

// TestResolveEpochInTxHonorsARetainedMapping mirrors the StillServesInTx
// case above for ResolveEpochInTx: a retained mapping resolves Live under
// the CURRENT epoch, not GoneReorganization, and Epoch names the current
// epoch (R20-n).
func TestResolveEpochInTxHonorsARetainedMapping(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const storeID = "resolve-store"
	const oldAddress = "epch:resolve-store:record-a:1"

	mock.ExpectQuery(`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = \?`).
		WithArgs(oldAddress).
		WillReturnRows(sqlmock.NewRows([]string{"store_id", "minted_id", "minted_epoch"}).AddRow(storeID, "record-a", 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(2))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM epoch_minted_addresses WHERE store_id = \? AND minted_id = \? AND minted_epoch = \?`).
		WithArgs(storeID, "record-a", int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := ResolveEpochInTx(context.Background(), tx, storeID, oldAddress)
	if err != nil {
		t.Fatalf("ResolveEpochInTx: %v", err)
	}
	if got.Restriction != EpochRestrictionLive {
		t.Errorf("ResolveEpochInTx(a prior-epoch address whose id was carried forward).Restriction = %v, want EpochRestrictionLive (R20-n's retained-mapping exception)", got.Restriction)
	}
	if got.Epoch == nil || *got.Epoch != 2 {
		t.Errorf("ResolveEpochInTx(a prior-epoch address whose id was carried forward).Epoch = %v, want the current epoch (2)", got.Epoch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet retained-mapping SQL expectations: %v", err)
	}
}

// TestStillServesInTxReportsFalseForALapsedMappingEvenAfterALaterRemint is
// the regression test for gastownhall/beads#6664 (bee-ghosttrack, maintainer
// review 5268699223, item B1): mintedIDHasAddressAtEpochInTx answers R20-n's
// retained-mapping question from the bare existence of ANY row for mintedID
// at the current epoch, not from an unbroken chain of rows through every
// intervening epoch. A mapping that LAPSES at an intermediate epoch (the id
// is not re-minted there) and is only later reused — a later, unrelated
// mint that happens to share the same id — must not retroactively retain
// the ORIGINAL, already-superseded address: R20-n is for a mapping CARRIED
// FORWARD, not merely reused after a gap.
//
// TestStillServesInTxHonorsARetainedMapping above only ever bumps the epoch
// once, so it cannot structurally distinguish a correct chain check from
// this bare-existence bug — both answer the same way on a single hop. This
// case's address was minted at epoch 1; the store is now at epoch 3, and
// record-a was re-minted at epoch 3 but NOT at the epoch 2 in between.
func TestStillServesInTxReportsFalseForALapsedMappingEvenAfterALaterRemint(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const storeID = "gap-store"
	const oldAddress = "epch:gap-store:record-a:1"

	mock.ExpectQuery(`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = \?`).
		WithArgs(oldAddress).
		WillReturnRows(sqlmock.NewRows([]string{"store_id", "minted_id", "minted_epoch"}).AddRow(storeID, "record-a", 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(3))
	// record-a DOES have a row at the current epoch (3): it was re-minted
	// there, after lapsing at epoch 2. A bare-existence check sees this row
	// and wrongly concludes the epoch-1 mapping was retained.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM epoch_minted_addresses WHERE store_id = \? AND minted_id = \? AND minted_epoch = \?`).
		WithArgs(storeID, "record-a", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := StillServesInTx(context.Background(), tx, storeID, oldAddress)
	if err != nil {
		t.Fatalf("StillServesInTx: %v", err)
	}
	if got {
		t.Fatal("StillServesInTx(the epoch-1 address, after record-a's mapping lapsed at epoch 2 and was only re-minted later at epoch 3) = true, want false: a later, non-contiguous re-mint must not retroactively retain a superseded address (B1, gastownhall/beads#6664 review 5268699223)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet lapsed-mapping SQL expectations: %v", err)
	}
}

// TestStillServesInTxHonorsAContinuousMultiHopRetainedMapping is the
// positive counterpart to
// TestStillServesInTxReportsFalseForALapsedMappingEvenAfterALaterRemint: a
// mapping carried forward through EVERY intervening epoch — not just one
// hop — must still report Live. TestStillServesInTxHonorsARetainedMapping
// above only ever bumps once, so it cannot structurally distinguish a
// correct unbroken-chain check from a single-hop-only arithmetic guard
// (today's bug: `epoch-priorEpoch == 1`); both answer the same way after
// one hop. This case bumps three epochs past the minting epoch, with
// record-a re-minted at every intervening epoch and no gap anywhere.
//
// The mock below targets TODAY's query shape (a single COUNT(*) at the
// current epoch, gated by a Go-side epoch-priorEpoch==1 check), not the
// range query the fix introduces: mintedIDHasAddressAtEpochInTx never
// queries the intervening epochs at all today, so a row existing at the
// current epoch is the only fact today's code has — this is the minimal
// reproduction of the bug against the CURRENT implementation.
func TestStillServesInTxHonorsAContinuousMultiHopRetainedMapping(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const storeID = "multihop-store"
	const oldAddress = "epch:multihop-store:record-a:1"

	mock.ExpectQuery(`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = \?`).
		WithArgs(oldAddress).
		WillReturnRows(sqlmock.NewRows([]string{"store_id", "minted_id", "minted_epoch"}).AddRow(storeID, "record-a", 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(4))
	// record-a DOES have a row at the current epoch (4): it was re-minted
	// there, having also been re-minted at every intervening epoch (2, 3)
	// with no gap — a genuinely continuous chain, unlike the lapsed-then-
	// reused B1 case above. Today's code cannot see the difference: it
	// never queries epochs 2/3, so this row's mere existence at epoch 4 is
	// the only fact it has; what SHOULD make this retained (the unbroken
	// chain) instead gets rejected by the arithmetic guard below.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM epoch_minted_addresses WHERE store_id = \? AND minted_id = \? AND minted_epoch = \?`).
		WithArgs(storeID, "record-a", int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := StillServesInTx(context.Background(), tx, storeID, oldAddress)
	if err != nil {
		t.Fatalf("StillServesInTx: %v", err)
	}
	if !got {
		t.Fatal("StillServesInTx(a prior-epoch address whose id was carried forward through 3 intervening epochs with no gap) = false, want true: R20-n's retained-mapping exception is not limited to a single hop (today's epoch-priorEpoch==1 guard wrongly rejects any chain longer than one hop, gastownhall/beads#6664 FINDING 1)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet multi-hop retained-mapping SQL expectations: %v", err)
	}
}

// TestEpochAddressNeverCollidesAcrossDifferentStoreIDBoundaries is the
// regression test for gastownhall/beads#6664 (bee-ghosttrack, maintainer
// review 5268699223, item B2, first part): epochAddress's ":"-joined
// encoding is ambiguous — a colon inside storeID or id lets two
// semantically different (storeID, id, epoch) triples collide on the
// identical address string. This directly contradicts the package doc's
// "persisted once at mint and never rewritten in place" promise: a
// colliding second mint would silently steal the first triple's row
// (upsertEpochMintedAddressInTx's exists-branch UPDATE has no store_id/
// minted_id guard).
func TestEpochAddressNeverCollidesAcrossDifferentStoreIDBoundaries(t *testing.T) {
	t.Parallel()

	// Under the old "epch:%s:%s:%d" encoding, these two distinct triples
	// both produce "epch:store-a:b:c:1": the colon inside the first
	// triple's id ("b:c") lands exactly on the second triple's
	// storeID/id boundary ("store-a:b" / "c").
	const storeA, idA = "store-a", "b:c"
	const storeB, idB = "store-a:b", "c"
	a := epochAddress(storeA, idA, 1)
	b := epochAddress(storeB, idB, 1)
	if a == b {
		t.Fatalf("epochAddress(%q, %q, 1) and epochAddress(%q, %q, 1) both produced %q; a colon inside storeID or id must not let two different triples collide on the same address (B2, gastownhall/beads#6664 review 5268699223)", storeA, idA, storeB, idB, a)
	}
}

// TestUpsertEpochMintedAddressInTxIsANoOpWhenTheAddressAlreadyExists is the
// regression test for gastownhall/beads#6664 (bee-ghosttrack, maintainer
// review 5268699223, item B2, third part): once epochAddress is injective
// (TestEpochAddressNeverCollidesAcrossDifferentStoreIDBoundaries above), a
// row's address already existing provably means its stored (store_id,
// minted_id, minted_epoch) triple already matches the incoming one exactly
// — so the exists branch must perform no write at all, rather than the
// UPDATE it runs today, which (before epochAddress is injective) can steal
// another store's row through a colliding address.
func TestUpsertEpochMintedAddressInTxIsANoOpWhenTheAddressAlreadyExists(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const address = "epch:8:no-op-store:9:record-a:1"

	// Deliberately no ExpectExec/ExpectQuery at all: any write this branch
	// attempts is an unexpected call sqlmock rejects.
	err := upsertEpochMintedAddressInTx(context.Background(), tx, address, "no-op-store", "record-a", 1, true)
	if err != nil {
		t.Fatalf("upsertEpochMintedAddressInTx(exists=true) = %v, want nil: an already-existing address must be a no-op, not attempt a write this test set up no expectation for (B2, gastownhall/beads#6664 review 5268699223)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet no-op-on-exists SQL expectations: %v", err)
	}
}
