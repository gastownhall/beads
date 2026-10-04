package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// These are sqlmock tests because each one pins a SHAPE: which statements the
// shared epoch body runs, in what order, and above all which statements it does
// not run. sqlmock rejects any statement without a registered expectation, so a
// test that registers no ExpectExec is a test that forbids every write. What a
// statement does to real rows (set semantics, carry, collision) is checked
// against a real store by the embedded leg's tests.
//
// The survival model these tests pin (gastownhall/beads#6664, bee-ghosttrack
// review 5360880888, Major 1): an address's status is a function of its OWN
// row and the current epoch. It is Live unless gone_at_epoch is set, and a row
// minted in an epoch that has not happened yet is not served. Nothing infers
// status from other rows.
const (
	epochSQLRow        = `SELECT store_id, minted_id, minted_epoch, carried_from, gone_at_epoch FROM epoch_minted_addresses WHERE address = \?`
	epochSQLReadEpoch  = `SELECT epoch FROM store_epoch WHERE id = 1`
	epochSQLNewest     = `SELECT address FROM epoch_minted_addresses WHERE carried_from = \? AND store_id = \? AND gone_at_epoch IS NULL AND minted_epoch <= \? ORDER BY minted_epoch DESC LIMIT 1`
	epochSQLLoseRoot   = `UPDATE epoch_minted_addresses SET gone_at_epoch = \? WHERE address = \? AND store_id = \? AND gone_at_epoch IS NULL`
	epochSQLLoseCarry  = `UPDATE epoch_minted_addresses SET gone_at_epoch = \? WHERE carried_from = \? AND store_id = \? AND gone_at_epoch IS NULL`
	epochSQLInsertRoot = `INSERT INTO epoch_minted_addresses \(address, store_id, minted_id, minted_epoch, minted_at\) VALUES \(\?, \?, \?, \?, \?\)`
)

var epochSQLRowColumns = []string{"store_id", "minted_id", "minted_epoch", "carried_from", "gone_at_epoch"}

// epochTestRow is one epoch_minted_addresses row as the shared body reads it.
type epochTestRow struct {
	storeID     string
	mintedID    string
	mintedEpoch int
	// carriedFrom is nil for a root row, else the lineage root's address.
	carriedFrom any
	// goneAtEpoch is nil while the lineage is served, else the epoch it was
	// lost in.
	goneAtEpoch any
}

// expectEpochRow registers the read of address's row. A nil row is an address
// the store never minted.
func expectEpochRow(mock sqlmock.Sqlmock, address string, row *epochTestRow) {
	rows := sqlmock.NewRows(epochSQLRowColumns)
	if row != nil {
		rows.AddRow(row.storeID, row.mintedID, row.mintedEpoch, row.carriedFrom, row.goneAtEpoch)
	}
	mock.ExpectQuery(epochSQLRow).WithArgs(address).WillReturnRows(rows)
}

// expectEpochRead registers the read of store_epoch's singleton. It takes no
// arguments: the counter is per database, so no storeID ever reaches this SQL.
func expectEpochRead(mock sqlmock.Sqlmock, epoch int) {
	mock.ExpectQuery(epochSQLReadEpoch).
		WithArgs([]driver.Value{}...).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(epoch))
}

func intPtr(v int) *int { return &v }

// TestStillServesAndResolveAnswerFromTheAddressRowAndTheEpochAlone pins the
// whole resolver. Each case registers exactly the statements the answer may
// use, the address's own row and then the epoch, so a third statement (for
// instance a look at the id's other rows, which is what the earlier chain
// check did) is an unexpected call the mock rejects. An address nobody minted,
// or another store's, is answered from the row alone: the epoch is not read.
func TestStillServesAndResolveAnswerFromTheAddressRowAndTheEpochAlone(t *testing.T) {
	t.Parallel()

	const storeID = "status-store"
	const currentEpoch = 5
	root := epochAddress(storeID, "record-a", 1)

	cases := []struct {
		name            string
		row             *epochTestRow
		wantServes      bool
		wantRestriction EpochRestriction
		readsEpoch      bool
	}{
		{
			name:            "a row minted in an earlier epoch and never lost is served",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1},
			wantServes:      true,
			wantRestriction: EpochRestrictionLive,
			readsEpoch:      true,
		},
		{
			name:            "a row minted in the current epoch is served",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch},
			wantServes:      true,
			wantRestriction: EpochRestrictionLive,
			readsEpoch:      true,
		},
		{
			name:            "a carry row is served on its own row, with no look at the root",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 3, carriedFrom: root},
			wantServes:      true,
			wantRestriction: EpochRestrictionLive,
			readsEpoch:      true,
		},
		{
			name:            "a lost row is not served",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1, goneAtEpoch: 3},
			wantServes:      false,
			wantRestriction: EpochRestrictionGoneReorganization,
			readsEpoch:      true,
		},
		{
			name:            "a row lost in the epoch it was minted in is not served",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch, goneAtEpoch: currentEpoch},
			wantServes:      false,
			wantRestriction: EpochRestrictionGoneReorganization,
			readsEpoch:      true,
		},
		{
			name:            "a row minted in an epoch that has not happened yet fails closed",
			row:             &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch + 2},
			wantServes:      false,
			wantRestriction: EpochRestrictionGoneReorganization,
			readsEpoch:      true,
		},
		{
			name:            "an address the store never minted is unknown",
			row:             nil,
			wantServes:      false,
			wantRestriction: EpochRestrictionUnknown,
		},
		{
			name:            "an address another store minted is unknown",
			row:             &epochTestRow{storeID: "other-store", mintedID: "record-a", mintedEpoch: 1},
			wantServes:      false,
			wantRestriction: EpochRestrictionUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" (StillServes)", func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, root, tc.row)
			if tc.readsEpoch {
				expectEpochRead(mock, currentEpoch)
			}

			got, err := StillServesInTx(context.Background(), tx, storeID, root)
			if err != nil {
				t.Fatalf("StillServesInTx: %v", err)
			}
			if got != tc.wantServes {
				t.Errorf("StillServesInTx = %v, want %v", got, tc.wantServes)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("StillServesInTx ran a different set of statements than the row read and the epoch read: %v", err)
			}
		})
		t.Run(tc.name+" (Resolve)", func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, root, tc.row)
			if tc.readsEpoch {
				expectEpochRead(mock, currentEpoch)
			}

			got, err := ResolveEpochInTx(context.Background(), tx, storeID, root)
			if err != nil {
				t.Fatalf("ResolveEpochInTx: %v", err)
			}
			if got.Restriction != tc.wantRestriction {
				t.Errorf("ResolveEpochInTx.Restriction = %v, want %v", got.Restriction, tc.wantRestriction)
			}
			if got.ProducingStore != storeID {
				t.Errorf("ResolveEpochInTx.ProducingStore = %q, want %q", got.ProducingStore, storeID)
			}
			var wantEpoch *int
			if tc.readsEpoch {
				wantEpoch = intPtr(currentEpoch)
			}
			switch {
			case wantEpoch == nil && got.Epoch != nil:
				t.Errorf("ResolveEpochInTx.Epoch = %d, want nil: an unknown address never looks the epoch up", *got.Epoch)
			case wantEpoch != nil && got.Epoch == nil:
				t.Errorf("ResolveEpochInTx.Epoch = nil, want the current epoch %d", *wantEpoch)
			case wantEpoch != nil && *got.Epoch != *wantEpoch:
				t.Errorf("ResolveEpochInTx.Epoch = %d, want the current epoch %d", *got.Epoch, *wantEpoch)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("ResolveEpochInTx ran a different set of statements than the row read and the epoch read: %v", err)
			}
		})
	}
}

// TestCurrentAddressForInTxReadsAndNeverWrites pins that CurrentAddressFor is
// a pure read (bee-ghosttrack, review 5360880888, Major 1). The earlier form
// minted a fresh address under every read, which made a lost address look
// served again. Both Dolt legs open a read-write transaction for their read
// path and always roll it back, so a stray write would vanish with no error:
// the transaction type cannot enforce this. These cases register no ExpectExec,
// so any INSERT, UPDATE or DELETE is an unexpected call.
func TestCurrentAddressForInTxReadsAndNeverWrites(t *testing.T) {
	t.Parallel()

	const storeID = "current-address-store"
	const currentEpoch = 5
	root := epochAddress(storeID, "record-a", 1)
	firstCarry := epochAddress(storeID, "record-a", 3)
	newestCarry := epochAddress(storeID, "record-a", 5)

	cases := []struct {
		name string
		// asked is the address the caller holds; row is its row.
		asked string
		row   *epochTestRow
		// newest is the lineage's newest carry row, "" when it has none.
		newest string
		want   string
	}{
		{
			name:  "a survivor nothing carried returns its own address",
			asked: root,
			row:   &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1},
			want:  root,
		},
		{
			name:   "the root of a carried lineage returns the newest carry",
			asked:  root,
			row:    &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1},
			newest: newestCarry,
			want:   newestCarry,
		},
		{
			name:   "an older carry resolves through the lineage root, one lookup from any member",
			asked:  firstCarry,
			row:    &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 3, carriedFrom: root},
			newest: newestCarry,
			want:   newestCarry,
		},
		{
			name:   "the newest carry returns itself",
			asked:  newestCarry,
			row:    &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 5, carriedFrom: root},
			newest: newestCarry,
			want:   newestCarry,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, tc.asked, tc.row)
			expectEpochRead(mock, currentEpoch)
			lookup := mock.ExpectQuery(epochSQLNewest).WithArgs(root, storeID, int64(currentEpoch))
			if tc.newest == "" {
				lookup.WillReturnRows(sqlmock.NewRows([]string{"address"}))
			} else {
				lookup.WillReturnRows(sqlmock.NewRows([]string{"address"}).AddRow(tc.newest))
			}

			got, err := CurrentAddressForInTx(context.Background(), tx, storeID, tc.asked)
			if err != nil {
				t.Fatalf("CurrentAddressForInTx: %v", err)
			}
			if got != tc.want {
				t.Errorf("CurrentAddressForInTx = %q, want %q", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("CurrentAddressForInTx ran a different set of statements than reads: %v", err)
			}
		})
	}
}

// TestCurrentAddressForInTxReturnsTypedErrorsForAddressesItCannotAnswer pins the
// two errors a caller can tell apart: an address the store never minted, and
// one it minted but no longer serves (lost, or from an epoch that has not
// happened). Neither runs the lineage lookup, and neither writes.
func TestCurrentAddressForInTxReturnsTypedErrorsForAddressesItCannotAnswer(t *testing.T) {
	t.Parallel()

	const storeID = "typed-error-store"
	const currentEpoch = 5
	address := epochAddress(storeID, "record-a", 1)

	cases := []struct {
		name       string
		row        *epochTestRow
		readsEpoch bool
		want       error
		notWant    error
	}{
		{
			name:       "a lost address is not served",
			row:        &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1, goneAtEpoch: 3},
			readsEpoch: true,
			want:       ErrEpochAddressNotServed,
			notWant:    ErrEpochAddressNotFound,
		},
		{
			name:       "an address from an epoch that has not happened is not served",
			row:        &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch + 1},
			readsEpoch: true,
			want:       ErrEpochAddressNotServed,
			notWant:    ErrEpochAddressNotFound,
		},
		{
			name:    "an address the store never minted is not found",
			row:     nil,
			want:    ErrEpochAddressNotFound,
			notWant: ErrEpochAddressNotServed,
		},
		{
			name:    "an address another store minted is not found",
			row:     &epochTestRow{storeID: "other-store", mintedID: "record-a", mintedEpoch: 1},
			want:    ErrEpochAddressNotFound,
			notWant: ErrEpochAddressNotServed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, address, tc.row)
			if tc.readsEpoch {
				expectEpochRead(mock, currentEpoch)
			}

			got, err := CurrentAddressForInTx(context.Background(), tx, storeID, address)
			if err == nil {
				t.Fatalf("CurrentAddressForInTx = %q with no error, want %v", got, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("CurrentAddressForInTx error = %v, want it to wrap %v", err, tc.want)
			}
			if errors.Is(err, tc.notWant) {
				t.Errorf("CurrentAddressForInTx error = %v, must not wrap %v: callers tell the two apart", err, tc.notWant)
			}
			if got != "" {
				t.Errorf("CurrentAddressForInTx returned %q alongside an error, want an empty address", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// TestLoseVersionInTxMarksTheWholeLineageOnceAndOnlyWhereStillLive pins loss:
// it reads the address's row and the epoch (never creating the singleton), then
// marks the lineage ROOT's own row and every carry row of that root, each
// statement guarded by gone_at_epoch IS NULL so that nothing already lost is
// touched and nothing is ever cleared. Losing a lineage that is already lost
// matches no rows and is not an error, so a retried transaction may replay it.
func TestLoseVersionInTxMarksTheWholeLineageOnceAndOnlyWhereStillLive(t *testing.T) {
	t.Parallel()

	const storeID = "lose-store"
	const currentEpoch = 4
	root := epochAddress(storeID, "record-a", 1)
	carry := epochAddress(storeID, "record-a", 3)

	cases := []struct {
		name         string
		asked        string
		row          *epochTestRow
		rootAffected int64
		carryRows    int64
	}{
		{
			name:         "losing the root marks the root row and its carry rows",
			asked:        root,
			row:          &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1},
			rootAffected: 1,
			carryRows:    1,
		},
		{
			name:         "losing a carry address marks the same lineage through its root",
			asked:        carry,
			row:          &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 3, carriedFrom: root},
			rootAffected: 1,
			carryRows:    1,
		},
		{
			name:         "a lineage that was never carried still runs both guarded statements",
			asked:        root,
			row:          &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1},
			rootAffected: 1,
			carryRows:    0,
		},
		{
			name:         "a lineage that is already lost matches no rows and is not an error",
			asked:        root,
			row:          &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1, goneAtEpoch: 3},
			rootAffected: 0,
			carryRows:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, tc.asked, tc.row)
			expectEpochRead(mock, currentEpoch)
			mock.ExpectExec(epochSQLLoseRoot).
				WithArgs(int64(currentEpoch), root, storeID).
				WillReturnResult(sqlmock.NewResult(0, tc.rootAffected))
			mock.ExpectExec(epochSQLLoseCarry).
				WithArgs(int64(currentEpoch), root, storeID).
				WillReturnResult(sqlmock.NewResult(0, tc.carryRows))

			if err := LoseVersionInTx(context.Background(), tx, storeID, tc.asked); err != nil {
				t.Fatalf("LoseVersionInTx: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("LoseVersionInTx did not run the root update and then the carry update, each guarded: %v", err)
			}
		})
	}
}

// TestLoseVersionInTxReportsAnAddressTheStoreNeverMinted pins that a loss
// naming an address this store never minted is an error and writes nothing.
func TestLoseVersionInTxReportsAnAddressTheStoreNeverMinted(t *testing.T) {
	t.Parallel()

	const storeID = "lose-absent-store"
	address := epochAddress(storeID, "record-a", 1)

	cases := []struct {
		name string
		row  *epochTestRow
	}{
		{name: "an address nobody minted", row: nil},
		{name: "an address another store minted", row: &epochTestRow{storeID: "other-store", mintedID: "record-a", mintedEpoch: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			expectEpochRow(mock, address, tc.row)

			err := LoseVersionInTx(context.Background(), tx, storeID, address)
			if !errors.Is(err, ErrEpochAddressNotFound) {
				t.Fatalf("LoseVersionInTx = %v, want it to wrap ErrEpochAddressNotFound", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("a loss that cannot be recorded must run the row read and nothing else: %v", err)
			}
		})
	}
}

// TestLoseVersionInTxSurfacesAStatementFailure pins that a failed update is
// returned, not swallowed. A lineage member minted in an epoch that has not
// happened yet violates the table's gone-not-before-mint check, and the loss
// must report that anomaly rather than paper over it.
func TestLoseVersionInTxSurfacesAStatementFailure(t *testing.T) {
	t.Parallel()

	const storeID = "lose-failure-store"
	address := epochAddress(storeID, "record-a", 1)
	checkViolation := errors.New("check constraint violated")

	_, mock, tx := beginMockTx(t)
	expectEpochRow(mock, address, &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: 1})
	expectEpochRead(mock, 2)
	mock.ExpectExec(epochSQLLoseRoot).
		WithArgs(int64(2), address, storeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(epochSQLLoseCarry).
		WithArgs(int64(2), address, storeID).
		WillReturnError(checkViolation)

	err := LoseVersionInTx(context.Background(), tx, storeID, address)
	if !errors.Is(err, checkViolation) {
		t.Fatalf("LoseVersionInTx = %v, want it to wrap the failed statement's error", err)
	}
}

// TestBumpEpochInTxWritesOnlyStoreEpoch pins that a plain bump (restore and
// destructive reinit) touches the counter and nothing else. Restore replaces
// the database and reinit keeps what survives, so neither may write an address
// row: an address row a bump wrote would be an address that was not minted.
// Only BumpEpochCarryingInTx, for a token-scheme change, writes address rows.
func TestBumpEpochInTxWritesOnlyStoreEpoch(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	expectEpochRead(mock, 3)
	mock.ExpectExec(`UPDATE store_epoch SET epoch = epoch \+ 1, bumped_at = \?, bumped_reason = \? WHERE id = 1`).
		WithArgs(sqlmock.AnyArg(), "restore").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectEpochRead(mock, 4)

	got, err := BumpEpochInTx(context.Background(), tx, "bump-store", "restore")
	if err != nil {
		t.Fatalf("BumpEpochInTx: %v", err)
	}
	if got != 4 {
		t.Errorf("BumpEpochInTx = %d, want 4", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("BumpEpochInTx ran statements beyond the store_epoch read, update and read-back: %v", err)
	}
}

// TestABumpUnderOneStoreIDMovesTheEpochSeenUnderEveryOther pins the sentence
// the epoch functions' docs now carry (bee-ghosttrack, review 5360880888,
// Minor 3): the counter is per database, not per store, and storeID appears
// only in error text. No SQL here takes the storeID as an argument, so a bump
// made under one storeID is seen under every storeID that shares the database.
// There is deliberately no storeID guard: the conformance suite uses several
// synthetic storeIDs in one database.
func TestABumpUnderOneStoreIDMovesTheEpochSeenUnderEveryOther(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	expectEpochRead(mock, 2)
	mock.ExpectExec(`UPDATE store_epoch SET epoch = epoch \+ 1, bumped_at = \?, bumped_reason = \? WHERE id = 1`).
		WithArgs(sqlmock.AnyArg(), "restore").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectEpochRead(mock, 3)
	expectEpochRead(mock, 3)

	bumped, err := BumpEpochInTx(context.Background(), tx, "store-one", "restore")
	if err != nil {
		t.Fatalf("BumpEpochInTx under store-one: %v", err)
	}
	seen, err := CurrentEpochInTx(context.Background(), tx, "store-two")
	if err != nil {
		t.Fatalf("CurrentEpochInTx under store-two: %v", err)
	}
	if seen != bumped {
		t.Errorf("CurrentEpochInTx(store-two) = %d after a bump under store-one reported %d, want the same epoch: the counter is per database", seen, bumped)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMintUnderEpochInTxNeverHandsBackAnAddressTheStoreDoesNotServe pins the
// mint's refusal. A mint is deterministic in (storeID, id, epoch), so it can
// find a row already sitting at the address it computes; that row is only
// possible when the lineage was minted in this epoch and lost in this same
// epoch. Handing that address back would give the caller an address that
// reads Gone, so the mint returns ErrEpochAddressNotServed instead, and it
// does not touch the row.
func TestMintUnderEpochInTxNeverHandsBackAnAddressTheStoreDoesNotServe(t *testing.T) {
	t.Parallel()

	const storeID = "mint-store"
	const currentEpoch = 5
	address := epochAddress(storeID, "record-a", currentEpoch)

	_, mock, tx := beginMockTx(t)
	expectEpochRead(mock, currentEpoch)
	expectEpochRow(mock, address, &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch, goneAtEpoch: currentEpoch})

	got, err := MintUnderEpochInTx(context.Background(), tx, storeID, "record-a")
	if !errors.Is(err, ErrEpochAddressNotServed) {
		t.Fatalf("MintUnderEpochInTx = (%q, %v), want it to wrap ErrEpochAddressNotServed", got, err)
	}
	if got != "" {
		t.Errorf("MintUnderEpochInTx returned %q alongside the refusal, want an empty address", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a refused mint must read and write nothing else: %v", err)
	}
}

// TestMintUnderEpochInTxInsertsOnlyAFreshRootRow pins that the only write a
// mint ever makes is the INSERT of its own root row. A mint that finds its
// address already served returns it with no write at all, so it can never
// modify, retire or revive another row.
func TestMintUnderEpochInTxInsertsOnlyAFreshRootRow(t *testing.T) {
	t.Parallel()

	const storeID = "mint-store"
	const currentEpoch = 5
	address := epochAddress(storeID, "record-a", currentEpoch)

	t.Run("an absent row is inserted as a root row", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		expectEpochRead(mock, currentEpoch)
		expectEpochRow(mock, address, nil)
		mock.ExpectExec(epochSQLInsertRoot).
			WithArgs(address, storeID, "record-a", int64(currentEpoch), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))

		got, err := MintUnderEpochInTx(context.Background(), tx, storeID, "record-a")
		if err != nil {
			t.Fatalf("MintUnderEpochInTx: %v", err)
		}
		if got != address {
			t.Errorf("MintUnderEpochInTx = %q, want %q", got, address)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("the mint's only write must be the root INSERT: %v", err)
		}
	})

	t.Run("a served row at the address is returned with no write", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		expectEpochRead(mock, currentEpoch)
		expectEpochRow(mock, address, &epochTestRow{storeID: storeID, mintedID: "record-a", mintedEpoch: currentEpoch})

		got, err := MintUnderEpochInTx(context.Background(), tx, storeID, "record-a")
		if err != nil {
			t.Fatalf("MintUnderEpochInTx: %v", err)
		}
		if got != address {
			t.Errorf("MintUnderEpochInTx = %q, want %q", got, address)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("minting an address that is already served must write nothing: %v", err)
		}
	})
}

// TestMintUnderEpochInTxTreatsADuplicateKeyAsABenignRace keeps the mint's
// existing tolerance: two concurrent minters computing the same address can
// both attempt the same INSERT, and the one that loses the race has still
// minted the address.
func TestMintUnderEpochInTxTreatsADuplicateKeyAsABenignRace(t *testing.T) {
	t.Parallel()

	const storeID = "mint-race-store"
	const currentEpoch = 5
	address := epochAddress(storeID, "record-a", currentEpoch)

	_, mock, tx := beginMockTx(t)
	expectEpochRead(mock, currentEpoch)
	expectEpochRow(mock, address, nil)
	mock.ExpectExec(epochSQLInsertRoot).
		WithArgs(address, storeID, "record-a", int64(currentEpoch), sqlmock.AnyArg()).
		WillReturnError(errors.New("Error 1062: Duplicate entry for key 'PRIMARY'"))

	got, err := MintUnderEpochInTx(context.Background(), tx, storeID, "record-a")
	if err != nil {
		t.Fatalf("MintUnderEpochInTx on a lost insert race: %v", err)
	}
	if got != address {
		t.Errorf("MintUnderEpochInTx = %q, want %q", got, address)
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
	address := epochAddress("no-op-store", "record-a", 1)

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

// TestEnsureStoreEpochRowSeedsTheSingletonWithInsertIgnore is the regression
// test for the "Unverified" note of gastownhall/beads#6664 (bee-ghosttrack,
// review 5360880888): ensureStoreEpochRow seeded
// store_epoch's singleton with a plain INSERT where version_history.go seeds
// the very same row with INSERT IGNORE. It follows that seed's shape: read
// first, seed only when the row is absent (so the seed happens once per store,
// not once per mint), then read the row back so the epoch it reports is what
// the row actually holds.
func TestEnsureStoreEpochRowSeedsTheSingletonWithInsertIgnore(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	// store_epoch starts empty (migration 0067), so the first read finds no row.
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}))
	mock.ExpectExec(`INSERT IGNORE INTO store_epoch \(id, epoch\) VALUES \(1, 1\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(1))

	got, err := ensureStoreEpochRow(context.Background(), tx)
	if err != nil {
		t.Fatalf("ensureStoreEpochRow on an empty store_epoch: %v", err)
	}
	if got != 1 {
		t.Fatalf("ensureStoreEpochRow on an empty store_epoch = epoch %d, want 1 (a freshly seeded singleton starts at epoch 1)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet store_epoch seed SQL expectations: %v", err)
	}
}

// TestEnsureStoreEpochRowReportsTheEpochOfARowItsSeedDidNotWrite pins why the
// seed reads back instead of returning the literal 1 it would have written:
// INSERT IGNORE writes nothing when the singleton already exists, and the row
// it leaves behind may hold a later epoch than the one this call tried to seed.
func TestEnsureStoreEpochRowReportsTheEpochOfARowItsSeedDidNotWrite(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}))
	// The seed is ignored: another writer's row is already there.
	mock.ExpectExec(`INSERT IGNORE INTO store_epoch \(id, epoch\) VALUES \(1, 1\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT epoch FROM store_epoch WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(3))

	got, err := ensureStoreEpochRow(context.Background(), tx)
	if err != nil {
		t.Fatalf("ensureStoreEpochRow when the seed is ignored: %v", err)
	}
	if got != 3 {
		t.Fatalf("ensureStoreEpochRow when the seed is ignored = epoch %d, want 3 (the epoch the surviving row holds, not the 1 the ignored seed would have written)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet ignored-seed SQL expectations: %v", err)
	}
}

// handWrittenEpochAddresses returns the position of every string literal in src
// that writes an address by hand, meaning one that starts with the address
// encoding's prefix. It reads string literals only, so a comment that merely
// describes an address, or a call to epochAddress, is never reported.
func handWrittenEpochAddresses(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	// The prefix is assembled here so this guard's own source holds no
	// literal that starts with it.
	addressPrefix := "epch" + ":"
	var hits []string
	ast.Inspect(parsed, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.HasPrefix(value, addressPrefix) {
			hits = append(hits, fset.Position(lit.Pos()).String()+": "+lit.Value)
		}
		return true
	})
	return hits, nil
}

// TestEpochCASTestsBuildEveryAddressWithEpochAddress pins the rule that this
// file never writes an address by hand (bee-ghosttrack, review 5360880888,
// Minor 4). The review found colon-form literals of the encoding that
// epochAddress retired, and one length-prefixed literal whose lengths were
// wrong. A hand-written address goes stale the moment the encoding moves,
// while a test that builds it with epochAddress cannot disagree with the code
// under test.
func TestEpochCASTestsBuildEveryAddressWithEpochAddress(t *testing.T) {
	t.Parallel()

	hits, err := handWrittenEpochAddresses("epoch_cas_test.go", nil)
	if err != nil {
		t.Fatalf("parse epoch_cas_test.go: %v", err)
	}
	for _, hit := range hits {
		t.Errorf("%s writes an address by hand; build it with epochAddress(storeID, id, epoch)", hit)
	}
}

// TestHandWrittenAddressGuardHasTeeth proves the guard above can fail: it must
// report a colon-form literal and a length-prefixed literal, and must stay
// quiet for a call to epochAddress and for a comment describing an address.
func TestHandWrittenAddressGuardHasTeeth(t *testing.T) {
	t.Parallel()

	prefix := "epch" + ":"
	cases := []struct {
		name string
		src  string
		want int
	}{
		{name: "a colon-form literal", src: "package p\nvar a = \"" + prefix + "store:id:1\"\n", want: 1},
		{name: "a length-prefixed literal", src: "package p\nvar a = \"" + prefix + "5:store:2:id:1\"\n", want: 1},
		{name: "two literals", src: "package p\nvar a, b = \"" + prefix + "x\", \"" + prefix + "y\"\n", want: 2},
		{name: "a call to epochAddress", src: "package p\nvar a = epochAddress(\"store\", \"id\", 1)\n", want: 0},
		{name: "a comment describing an address", src: "package p\n// the old form was " + prefix + "store:id:1\nvar a = 1\n", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hits, err := handWrittenEpochAddresses("sample.go", tc.src)
			if err != nil {
				t.Fatalf("parse sample: %v", err)
			}
			if len(hits) != tc.want {
				t.Errorf("the guard reported %d hand-written addresses (%v), want %d", len(hits), hits, tc.want)
			}
		})
	}
}
