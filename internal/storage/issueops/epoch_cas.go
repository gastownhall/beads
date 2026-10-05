package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// This file implements R20 epoch-transition enforcement (gastownhall/beads#5898
// revision 9, #6136): a store-wide epoch generation
// counter (store_epoch, migration 0067) plus a durable record of the
// addresses minted under each generation (epoch_minted_addresses, migration
// 0071), used to answer whether a previously-minted address is still served
// by the store's current epoch. It adds no RetentionFixture/R17 resolve,
// remove, hold, force-remove, erase, or mint logic — R20 epoch reasoning is
// evaluated entirely on its own (out of scope for this slice).
//
// ALL THREE LEGS SHARE THIS BODY, the same way CompareAndSetMetadataKeyInTx
// and R16's CompareAndSetVersionInTx do: the two Dolt-backed stores wrap it
// in their own transaction, and the unit-of-work provider reaches it through
// its own leg-specific adapter. Each leg's own contract-test file is the
// translation boundary to and from backend/conformance's
// Address/EpochBumpTrigger/RetentionAnswer vocabulary — this file knows
// nothing about the conformance package.
//
// ADDRESSES ARE NEVER RECOMPUTED FROM store_epoch. Each is a deterministic
// token over (storeID, id, the epoch current at mint time) — see
// epochAddress — persisted once at mint: a later mint of the same id under a
// bumped epoch produces a DIFFERENT address and a new row, and every
// address's row survives to answer StillServes/Resolve after the epoch moves
// on.
//
// SURVIVAL IS RECORDED ON THE ROW, NEVER INFERRED (gastownhall/beads#6664,
// bee-ghosttrack review 5360880888, Major 1). An address is served unless its
// own row says gone_at_epoch, and a row minted in an epoch that has not
// happened yet is not served: the answer is a function of the address's row
// and the current epoch, and nothing reads another row to reach it. Gone is
// terminal. Two writers set gone_at_epoch, both guarded by IS NULL so that
// nothing already lost is touched and nothing is ever cleared:
// LoseVersionInTx, for a Version the store lost, and BumpEpochCarryingInTx,
// for the losing lineage when two live lineages of one id meet at a
// token-scheme change.
//
// A restore or a destructive reinit moves the counter and writes no address
// row (BumpEpochInTx), so every address that was served stays Live at its own
// address with no re-mint. A token-scheme change is the one bump that writes
// address rows (BumpEpochCarryingInTx): it carries every live lineage to the
// new epoch with one carry row whose carried_from names the lineage's ROOT
// address, never the previous carry, so lineages stay flat and any member of
// one reaches its newest address in a single indexed lookup.
//
// THE EPOCH COUNTER IS PER DATABASE, NOT PER STORE (gastownhall/beads#6664,
// bee-ghosttrack review 5360880888, Minor 3). store_epoch has no store_id
// column, and storeID appears only in error text: one bump advances the epoch
// seen through every storeID that shares the database. There is deliberately
// no storeID guard in this body, because the conformance suite uses several
// synthetic storeIDs in one database.
//
// epochAddress'S ENCODING IS LENGTH-PREFIXED, NOT BARE-COLON-JOINED
// (gastownhall/beads#6664, bee-ghosttrack review 5268699223, item B2): a
// colon inside storeID or id can no longer shift the (storeID, id, epoch)
// field boundary onto a different triple the way the old "epch:%s:%s:%d"
// form allowed. MintUnderEpochInTx additionally refuses a storeID or id
// containing ":" outright (validateEpochAddressInputs) so an address stays
// readable by eye without needing to count length prefixes — belt and
// braces, not a correctness dependency of epochAddress itself. Given that,
// upsertEpochMintedAddressInTx's exists branch is a pure no-op: an
// address's row existing now provably means its stored triple already
// matches the incoming one, so there is nothing left to write.

// EpochRestriction is this file's own local answer vocabulary for R20 —
// deliberately not backend/conformance.Restriction (see package doc above).
// It omits GoneRetention/GoneErasure: those are RetentionFixture/R17
// outcomes this slice does not produce.
type EpochRestriction int

const (
	EpochRestrictionLive EpochRestriction = iota
	EpochRestrictionGoneReorganization
	EpochRestrictionUnknown
)

// EpochResolveResult is ResolveEpochInTx's answer. Epoch is non-nil exactly
// when the answer required comparing an epoch (Live and GoneReorganization);
// it is nil for Unknown, which never looks store_epoch up at all.
type EpochResolveResult struct {
	Restriction    EpochRestriction
	ProducingStore string
	Epoch          *int
}

// ErrEpochAddressNotFound means the address named was never minted by
// storeID — distinct from an ordinary "gone" answer, which requires having
// minted the address in the first place.
var ErrEpochAddressNotFound = errors.New("epoch CAS: address not minted by this store")

// ErrEpochAddressNotServed means the address exists in this store but the
// store does not serve it: its lineage was lost, or it was minted in an epoch
// that has not happened yet. It is distinct from ErrEpochAddressNotFound so a
// caller can tell an address the store never minted from one it minted and no
// longer serves.
var ErrEpochAddressNotServed = errors.New("epoch CAS: address exists in this store but is not served")

// ErrEpochAddressSeparator means a storeID or id passed to
// MintUnderEpochInTx contains ":", the character epochAddress's encoding
// uses as a field delimiter (gastownhall/beads#6664, bee-ghosttrack review
// 5268699223, item B2b). epochAddress's length-prefixed encoding does not
// actually depend on this for correctness (see epochAddress's own doc
// comment), but refusing it outright keeps a minted address readable by eye.
var ErrEpochAddressSeparator = errors.New("epoch CAS: storeID or id contains \":\", epochAddress's field separator")

// epochAddress is a minted address: a deterministic token over storeID, id,
// and the epoch current at mint time, so re-minting the same id under the
// same epoch always reproduces the same address. storeID and id are
// length-prefixed, not just colon-joined (gastownhall/beads#6664,
// bee-ghosttrack review 5268699223, item B2a): reading exactly len(storeID)
// bytes for the first field and len(id) bytes for the second makes the
// (storeID, id) boundary unambiguous no matter what characters either one
// contains, so two different triples can never collide on the same address.
// MintUnderEpochInTx also refuses a storeID or id containing ":" outright
// (validateEpochAddressInputs, item B2b) so an address stays readable by
// eye without relying on that.
func epochAddress(storeID, id string, epoch int) string {
	return fmt.Sprintf("epch:%d:%s:%d:%s:%d", len(storeID), storeID, len(id), id, epoch)
}

// validateEpochAddressInputs rejects a storeID or id containing ":" before
// MintUnderEpochInTx mints an address from them (item B2b above).
func validateEpochAddressInputs(storeID, id string) error {
	if strings.Contains(storeID, ":") {
		return fmt.Errorf("%w: storeID %q", ErrEpochAddressSeparator, storeID)
	}
	if strings.Contains(id, ":") {
		return fmt.Errorf("%w: id %q", ErrEpochAddressSeparator, id)
	}
	return nil
}

// ensureStoreEpochRow lazily initializes store_epoch's singleton row
// (migration 0067; the table starts empty) to epoch 1 on first use, and
// reports the current epoch either way. Only BumpEpochInTx and
// MintUnderEpochInTx call this: both are about to write regardless, so
// creating the row here is not a surprising extra side effect. Every
// read-only path calls readStoreEpochInTx instead (gastownhall/beads#6664,
// bee-ghosttrack review 5268699223, item B4): a write from what callers
// reasonably expect to be a read is surprising at best, and a hard failure
// against a genuinely read-only connection at worst.
//
// The seed follows version_history.go's seed of the same row (gastownhall/
// beads#6664, bee-ghosttrack review 5360880888): read first, so the INSERT
// happens once per store rather than once per mint; INSERT IGNORE, so a row
// that turns up between the read and the write is not an error; and read the
// row back, so the epoch reported is what the row holds even when the seed was
// ignored, not the 1 it would have written.
func ensureStoreEpochRow(ctx context.Context, tx DBTX) (int, error) {
	var epoch int
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO store_epoch (id, epoch) VALUES (1, 1)`); err != nil {
			return 0, fmt.Errorf("initialize store_epoch: %w", err)
		}
		err = tx.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&epoch)
	}
	if err != nil {
		return 0, fmt.Errorf("read store_epoch: %w", err)
	}
	return epoch, nil
}

// EpochMintDirtyTables names the tables a mint (MintUnderEpochInTx) can leave
// modified, for the leg that publishes them to Dolt history. A token-scheme
// bump (BumpEpochCarryingInTx) leaves the same two tables modified. store_epoch is in the set although a mint
// never updates it: ensureStoreEpochRow INSERTs the singleton row on first use,
// so a mint that published only epoch_minted_addresses would leave that row
// modified and uncommitted, and the Dolt commit would hold minted rows without
// the epoch row they depend on (gastownhall/beads#6664 Major 2, bee-ghosttrack
// review 5360880888). Staging a table that turns out to be clean is free.
func EpochMintDirtyTables() ChangedTables {
	return ChangedTables{"epoch_minted_addresses": true, "store_epoch": true}
}

// readStoreEpochInTx reports the current epoch without writing:
// store_epoch's singleton row starting absent (migration 0067) and its
// epoch being 1 are the same state, so a read-only caller can answer from
// that default instead of initializing the row the way ensureStoreEpochRow
// does (item B4 above). CurrentEpochInTx, StillServesInTx, ResolveEpochInTx,
// CurrentAddressForInTx and LoseVersionInTx all read this way; only
// BumpEpochInTx and MintUnderEpochInTx, which are about to write regardless,
// use ensureStoreEpochRow.
func readStoreEpochInTx(ctx context.Context, tx DBTX) (int, error) {
	var epoch int
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read store_epoch: %w", err)
	}
	return epoch, nil
}

// epochMintedAddress is one row as read from epoch_minted_addresses.
// carriedFrom is NULL for a root row (a fresh mint) and otherwise the address of
// the lineage's root; goneAtEpoch is NULL while the store serves the row and
// otherwise the epoch it was lost in.
type epochMintedAddress struct {
	storeID     string
	mintedID    string
	mintedEpoch int
	carriedFrom sql.NullString
	goneAtEpoch sql.NullInt64
}

// served reports whether the store serves the row at epoch. A row is served
// unless it was lost, and a row minted in an epoch that has not happened yet
// fails closed rather than being trusted.
func (r epochMintedAddress) served(epoch int) bool {
	return !r.goneAtEpoch.Valid && r.mintedEpoch <= epoch
}

// lineageRoot is the address every member of the row's lineage names: the
// row's carried_from when it is a carry row, and its own address when it is a
// root.
func (r epochMintedAddress) lineageRoot(address string) string {
	if r.carriedFrom.Valid {
		return r.carriedFrom.String
	}
	return address
}

// readEpochMintedAddressInTx reads address's row, if any. found is false
// (with a zero-value row and nil error) when address was never minted —
// distinguishing "not found" from an actual query error is the caller's
// job, the same way readExpectedRevisionRowInTx's callers do it.
func readEpochMintedAddressInTx(ctx context.Context, tx DBTX, address string) (epochMintedAddress, bool, error) {
	var row epochMintedAddress
	err := tx.QueryRowContext(ctx,
		`SELECT store_id, minted_id, minted_epoch, carried_from, gone_at_epoch FROM epoch_minted_addresses WHERE address = ?`, address,
	).Scan(&row.storeID, &row.mintedID, &row.mintedEpoch, &row.carriedFrom, &row.goneAtEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return epochMintedAddress{}, false, nil
	}
	if err != nil {
		return epochMintedAddress{}, false, fmt.Errorf("epoch CAS: read minted address %s: %w", address, err)
	}
	return row, true, nil
}

// upsertEpochMintedAddressInTx writes address's row. exists (from the
// caller's own prior read) says whether one already does: if so, this is a
// pure no-op (gastownhall/beads#6664, bee-ghosttrack review 5268699223,
// item B2c) — epochAddress is now injective (item B2a), so an existing row
// at this exact address provably already carries this exact (storeID,
// mintedID, mintedEpoch) triple, and the old exists-branch UPDATE (which
// had no store_id/minted_id guard) could only ever have mattered by
// rewriting a DIFFERENT triple's row out from under it on an address
// collision the new encoding no longer allows. The not-exists branch treats
// a duplicate-key error from the INSERT as a benign race rather than a hard
// failure (item B3): two concurrent minters computing the same address can
// both attempt the same insert, and since it would insert the identical row
// the loser would otherwise have upserted anyway, treating its
// duplicate-key error as success is correct, not merely convenient.
func upsertEpochMintedAddressInTx(ctx context.Context, tx DBTX, address, storeID, mintedID string, mintedEpoch int, exists bool) error {
	if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at) VALUES (?, ?, ?, ?, ?)`,
		address, storeID, mintedID, mintedEpoch, time.Now().UTC(),
	); err != nil {
		if dberrors.IsDuplicateKey(err) {
			return nil
		}
		return fmt.Errorf("epoch CAS: insert minted address %s: %w", address, err)
	}
	return nil
}

// CurrentEpochInTx reports storeID's current epoch generation. store_epoch
// has no store_id column: migration 0067 established it as one physical
// counter per database, matching production reality (one logical store per
// database already) — storeID is accepted here only to satisfy
// EpochFixture's hook signature.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
func CurrentEpochInTx(ctx context.Context, tx DBTX, storeID string) (int, error) {
	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("epoch CAS: current epoch for %s: %w", storeID, err)
	}
	return epoch, nil
}

// BumpEpochInTx advances storeID's epoch generation by one and records why
// (R20-a: a bump is triggered only by restore, destructive-reinit, or
// token-scheme-change — the leg adapter converts the fixture's
// conformance.EpochBumpTrigger to this plain string via trigger.String(),
// keeping that vocabulary out of this file per the package doc above).
//
// It writes store_epoch and nothing else: a restore or a destructive reinit
// leaves every address row alone, so every address that was served stays Live
// at its own address. A token-scheme change, which does write address rows,
// calls BumpEpochCarryingInTx instead.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
func BumpEpochInTx(ctx context.Context, tx DBTX, storeID, reason string) (int, error) {
	if _, err := ensureStoreEpochRow(ctx, tx); err != nil {
		return 0, fmt.Errorf("epoch CAS: bump epoch for %s: %w", storeID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE store_epoch SET epoch = epoch + 1, bumped_at = ?, bumped_reason = ? WHERE id = 1`,
		time.Now().UTC(), reason,
	); err != nil {
		return 0, fmt.Errorf("epoch CAS: bump epoch for %s: %w", storeID, err)
	}
	var newEpoch int
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&newEpoch); err != nil {
		return 0, fmt.Errorf("epoch CAS: read epoch after bump for %s: %w", storeID, err)
	}
	return newEpoch, nil
}

// BumpEpochCarryingInTx is BumpEpochInTx for a token-scheme change, the one
// trigger that changes how addresses are encoded. It advances the epoch exactly
// as BumpEpochInTx does and also records the carry: every lineage the store
// still serves gets one new row at the new epoch, whose address is the
// deterministic token for (store, id, new epoch) and whose carried_from names
// the lineage's ROOT address. The old addresses keep resolving, and
// CurrentAddressForInTx reports the new one from any member of the lineage.
//
// The carry is DATABASE-WIDE on purpose. The counter is per database, so this
// bump moves the epoch every storeID sees, and carrying only storeID's rows
// would strand every other store's addresses at the scheme boundary. storeID is
// used for error text only, exactly as in BumpEpochInTx. It costs one read of
// every live row and one insert per live id, in one transaction: acceptable
// because a scheme change is a once-per-change migration, not a runtime path.
//
// Two live lineages of one id cannot both receive an address, because an
// address is a function of (store, id, epoch) and so is one per id per epoch.
// The lineage holding the newest row is carried, and every other lineage of
// that id is marked gone at the new epoch (the safe direction: over-void, never
// revive). Lineages already lost are not read, so they are never carried, and a
// row minted in an epoch that has not happened yet is not read either: it stays
// not served. A row already sitting at a carry's address means the counter
// rewound, so that is a failure of the bump, not the benign race a mint
// tolerates.
//
// CALLERS MUST HOLD THE WORKSPACE GATE (internal/workspacegate) EXCLUSIVELY FOR
// THE BUMP, as a restore does. This is the one epoch operation that needs
// mints excluded by contract: the store cannot make a mint conflict with a bump
// (a mint has no cell of its own to change), so a mint that commits after this
// bump read the live rows would leave a live row that was never carried.
func BumpEpochCarryingInTx(ctx context.Context, tx DBTX, storeID, reason string) (int, error) {
	newEpoch, err := BumpEpochInTx(ctx, tx, storeID, reason)
	if err != nil {
		return 0, err
	}
	candidates, err := readEpochCarryCandidatesInTx(ctx, tx, newEpoch)
	if err != nil {
		return 0, fmt.Errorf("epoch CAS: carry addresses to epoch %d for %s: %w", newEpoch, storeID, err)
	}

	// The candidates are ordered newest first within each (store, id), so the
	// first row of a group holds the lineage that wins it.
	type lineageKey struct{ storeID, mintedID string }
	var groups []lineageKey
	winningRoot := map[lineageKey]string{}
	type losingLineage struct{ storeID, root string }
	var losers []losingLineage
	seenLoser := map[losingLineage]bool{}
	for _, c := range candidates {
		key := lineageKey{c.storeID, c.mintedID}
		winner, seen := winningRoot[key]
		if !seen {
			winningRoot[key] = c.root()
			groups = append(groups, key)
			continue
		}
		if loser := (losingLineage{c.storeID, c.root()}); c.root() != winner && !seenLoser[loser] {
			seenLoser[loser] = true
			losers = append(losers, loser)
		}
	}

	for _, loser := range losers {
		if err := loseLineageInTx(ctx, tx, loser.storeID, loser.root, newEpoch); err != nil {
			return 0, fmt.Errorf("epoch CAS: carry addresses to epoch %d for %s: %w", newEpoch, storeID, err)
		}
	}
	for _, key := range groups {
		address := epochAddress(key.storeID, key.mintedID, newEpoch)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at, carried_from) VALUES (?, ?, ?, ?, ?, ?)`,
			address, key.storeID, key.mintedID, newEpoch, time.Now().UTC(), winningRoot[key],
		); err != nil {
			return 0, fmt.Errorf("epoch CAS: carry %s to epoch %d for %s: %w", key.mintedID, newEpoch, storeID, err)
		}
	}
	return newEpoch, nil
}

// epochCarryCandidate is one live row below the new epoch, as
// BumpEpochCarryingInTx reads it.
type epochCarryCandidate struct {
	address     string
	storeID     string
	mintedID    string
	carriedFrom sql.NullString
}

// root is the address of the candidate's lineage root.
func (c epochCarryCandidate) root() string {
	if c.carriedFrom.Valid {
		return c.carriedFrom.String
	}
	return c.address
}

// readEpochCarryCandidatesInTx reads every row the store still serves below
// newEpoch, newest first within each (store, id). Rows already lost are not
// read, and neither are rows minted at or above newEpoch: those are anomalies
// and stay not served. The result is read to the end before any other statement
// runs on the transaction.
func readEpochCarryCandidatesInTx(ctx context.Context, tx DBTX, newEpoch int) ([]epochCarryCandidate, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT address, store_id, minted_id, carried_from FROM epoch_minted_addresses WHERE gone_at_epoch IS NULL AND minted_epoch < ? ORDER BY store_id, minted_id, minted_epoch DESC`,
		newEpoch,
	)
	if err != nil {
		return nil, fmt.Errorf("read live addresses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var candidates []epochCarryCandidate
	for rows.Next() {
		var c epochCarryCandidate
		if err := rows.Scan(&c.address, &c.storeID, &c.mintedID, &c.carriedFrom); err != nil {
			return nil, fmt.Errorf("scan live address: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read live addresses: %w", err)
	}
	return candidates, nil
}

// loseLineageInTx marks the lineage rooted at root gone at epoch: the root's own
// row, then every carry row that names it. Each statement is guarded by
// gone_at_epoch IS NULL, so a row already lost is never touched and nothing is
// ever cleared. It is the only place gone_at_epoch is written, shared by
// LoseVersionInTx and BumpEpochCarryingInTx.
func loseLineageInTx(ctx context.Context, tx DBTX, storeID, root string, epoch int) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE epoch_minted_addresses SET gone_at_epoch = ? WHERE address = ? AND store_id = ? AND gone_at_epoch IS NULL`,
		epoch, root, storeID,
	); err != nil {
		return fmt.Errorf("mark %s lost at epoch %d: %w", root, epoch, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE epoch_minted_addresses SET gone_at_epoch = ? WHERE carried_from = ? AND store_id = ? AND gone_at_epoch IS NULL`,
		epoch, root, storeID,
	); err != nil {
		return fmt.Errorf("mark the carries of %s lost at epoch %d: %w", root, epoch, err)
	}
	return nil
}

// MintUnderEpochInTx mints id's address under storeID's CURRENT epoch,
// deterministically (epochAddress): minting the same id again under the
// same epoch reproduces the same address and is an idempotent no-op upsert
// of the same row.
//
// A mint only ever INSERTS its own root row. It never updates another row, so
// no mint can retire or revive an address, and it never hands back an address
// the store does not serve: a row already sitting at the computed address is
// only possible when the lineage was minted in this epoch and lost in this same
// epoch, and the mint then returns ErrEpochAddressNotServed instead of an
// address that reads Gone.
func MintUnderEpochInTx(ctx context.Context, tx DBTX, storeID, id string) (string, error) {
	if err := validateEpochAddressInputs(storeID, id); err != nil {
		return "", fmt.Errorf("epoch CAS: mint %s under epoch for %s: %w", id, storeID, err)
	}
	epoch, err := ensureStoreEpochRow(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("epoch CAS: mint %s under epoch for %s: %w", id, storeID, err)
	}
	address := epochAddress(storeID, id, epoch)
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return "", err
	}
	if found && !row.served(epoch) {
		return "", fmt.Errorf("epoch CAS: mint %s under epoch for %s: %w: %s", id, storeID, ErrEpochAddressNotServed, address)
	}
	if err := upsertEpochMintedAddressInTx(ctx, tx, address, storeID, id, epoch, found); err != nil {
		return "", err
	}
	return address, nil
}

// StillServesInTx reports whether address is still served under storeID's
// CURRENT epoch. The answer is read from the address's own row and the epoch and
// from nothing else: the row's gone_at_epoch says whether the store lost it, and
// a row minted in an epoch that has not happened yet is not served. An address
// that survived a restore or a destructive reinit therefore stays served with no
// upkeep, and an address from a different store, or one never minted, is not
// served either.
func StillServesInTx(ctx context.Context, tx DBTX, storeID, address string) (bool, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return false, err
	}
	if !found || row.storeID != storeID {
		return false, nil
	}
	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("epoch CAS: still serves %s for %s: %w", address, storeID, err)
	}
	return row.served(epoch), nil
}

// ResolveEpochInTx answers R20's epoch-only restriction for address: Live while
// the store serves it, GoneReorganization once the store lost it (a
// reorganization, not a retention or erasure outcome; RetentionFixture/R17
// states are out of scope here), and Unknown for an address this store never
// minted. Like StillServesInTx it reads only the address's own row and the
// epoch. ProducingStore is always storeID: this file has no lineage/replica
// model to attribute a foreign store to (unlike RetentionFixture's cross-store
// answers).
func ResolveEpochInTx(ctx context.Context, tx DBTX, storeID, address string) (EpochResolveResult, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return EpochResolveResult{}, err
	}
	if !found || row.storeID != storeID {
		return EpochResolveResult{Restriction: EpochRestrictionUnknown, ProducingStore: storeID}, nil
	}
	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return EpochResolveResult{}, fmt.Errorf("epoch CAS: resolve %s for %s: %w", address, storeID, err)
	}
	if row.served(epoch) {
		return EpochResolveResult{Restriction: EpochRestrictionLive, ProducingStore: storeID, Epoch: &epoch}, nil
	}
	return EpochResolveResult{Restriction: EpochRestrictionGoneReorganization, ProducingStore: storeID, Epoch: &epoch}, nil
}

// CurrentAddressForInTx reports the address oldAddress now resolves to: the
// newest address of its lineage, or oldAddress itself when nothing carried the
// Version to a new address. It only reads. It executes no INSERT, UPDATE or
// DELETE, because a lookup that minted an address could make a lost address
// look served again (gastownhall/beads#6664, bee-ghosttrack review 5360880888,
// Major 1), and both Dolt legs run it on a read-write transaction that is
// always rolled back, so a stray write would vanish silently instead of
// failing. oldAddress must be one storeID minted (ErrEpochAddressNotFound
// otherwise) and still serves (ErrEpochAddressNotServed otherwise).
func CurrentAddressForInTx(ctx context.Context, tx DBTX, storeID, oldAddress string) (string, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, oldAddress)
	if err != nil {
		return "", err
	}
	if !found || row.storeID != storeID {
		return "", fmt.Errorf("epoch CAS: current address for %s: %w: %s", storeID, ErrEpochAddressNotFound, oldAddress)
	}
	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("epoch CAS: current address for %s: %w", storeID, err)
	}
	if !row.served(epoch) {
		return "", fmt.Errorf("epoch CAS: current address for %s: %w: %s", storeID, ErrEpochAddressNotServed, oldAddress)
	}
	var newest string
	err = tx.QueryRowContext(ctx,
		`SELECT address FROM epoch_minted_addresses WHERE carried_from = ? AND store_id = ? AND gone_at_epoch IS NULL AND minted_epoch <= ? ORDER BY minted_epoch DESC LIMIT 1`,
		row.lineageRoot(oldAddress), storeID, epoch,
	).Scan(&newest)
	if errors.Is(err, sql.ErrNoRows) {
		return oldAddress, nil
	}
	if err != nil {
		return "", fmt.Errorf("epoch CAS: current address for %s: read the newest carry of %s: %w", storeID, oldAddress, err)
	}
	return newest, nil
}

// LoseVersionInTx records that storeID no longer serves the Version named by
// address: it marks the lineage ROOT's own row and every carry row of that
// root gone at the current epoch, so every address of the Version resolves
// GoneReorganization from then on. Each statement is guarded by
// gone_at_epoch IS NULL, so nothing already lost is touched and nothing is ever
// cleared (Gone is terminal). Losing a lineage that is already lost matches no
// rows and returns nil, so a retried transaction may replay it. address must be
// one storeID minted, or ErrEpochAddressNotFound is returned and nothing is
// written. A lineage member minted in an epoch that has not happened yet
// violates the table's gone-not-before-mint check, and the resulting error is
// returned rather than papered over.
func LoseVersionInTx(ctx context.Context, tx DBTX, storeID, address string) error {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return err
	}
	if !found || row.storeID != storeID {
		return fmt.Errorf("epoch CAS: lose version for %s: %w: %s", storeID, ErrEpochAddressNotFound, address)
	}
	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("epoch CAS: lose version %s for %s: %w", address, storeID, err)
	}
	if err := loseLineageInTx(ctx, tx, storeID, row.lineageRoot(address), epoch); err != nil {
		return fmt.Errorf("epoch CAS: lose version %s for %s: %w", address, storeID, err)
	}
	return nil
}
