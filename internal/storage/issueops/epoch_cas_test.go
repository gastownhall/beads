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
