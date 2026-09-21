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
// revision 9, this slice: be-x5jqd.4 / #6136): a store-wide epoch generation
// counter (store_epoch, migration 0067) plus a durable record of the
// addresses minted under each generation (epoch_minted_addresses, migration
// 0070), used to answer whether a previously-minted address is still served
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
// epochAddress — persisted once at mint and never rewritten in place: a
// later mint of the same id under a bumped epoch produces a DIFFERENT
// address and a new row, so a superseded address's row survives to answer
// StillServes/Resolve as "no longer served" after the epoch moves on.
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
func ensureStoreEpochRow(ctx context.Context, tx DBTX) (int, error) {
	var epoch int
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO store_epoch (id, epoch) VALUES (1, 1)`); err != nil {
			return 0, fmt.Errorf("initialize store_epoch: %w", err)
		}
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read store_epoch: %w", err)
	}
	return epoch, nil
}

// readStoreEpochInTx reports the current epoch without writing:
// store_epoch's singleton row starting absent (migration 0067) and its
// epoch being 1 are the same state, so a read-only caller can answer from
// that default instead of initializing the row the way ensureStoreEpochRow
// does (item B4 above). CurrentEpochInTx, StillServesInTx and
// ResolveEpochInTx all read this way; only BumpEpochInTx and
// MintUnderEpochInTx, which are about to write regardless, use
// ensureStoreEpochRow.
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
type epochMintedAddress struct {
	storeID     string
	mintedID    string
	mintedEpoch int
}

// readEpochMintedAddressInTx reads address's row, if any. found is false
// (with a zero-value row and nil error) when address was never minted —
// distinguishing "not found" from an actual query error is the caller's
// job, the same way readExpectedRevisionRowInTx's callers do it.
func readEpochMintedAddressInTx(ctx context.Context, tx DBTX, address string) (epochMintedAddress, bool, error) {
	var row epochMintedAddress
	err := tx.QueryRowContext(ctx,
		`SELECT store_id, minted_id, minted_epoch FROM epoch_minted_addresses WHERE address = ?`, address,
	).Scan(&row.storeID, &row.mintedID, &row.mintedEpoch)
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

// MintUnderEpochInTx mints id's address under storeID's CURRENT epoch,
// deterministically (epochAddress): minting the same id again under the
// same epoch reproduces the same address and is an idempotent no-op upsert
// of the same row.
func MintUnderEpochInTx(ctx context.Context, tx DBTX, storeID, id string) (string, error) {
	if err := validateEpochAddressInputs(storeID, id); err != nil {
		return "", fmt.Errorf("epoch CAS: mint %s under epoch for %s: %w", id, storeID, err)
	}
	epoch, err := ensureStoreEpochRow(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("epoch CAS: mint %s under epoch for %s: %w", id, storeID, err)
	}
	address := epochAddress(storeID, id, epoch)
	_, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return "", err
	}
	if err := upsertEpochMintedAddressInTx(ctx, tx, address, storeID, id, epoch, found); err != nil {
		return "", err
	}
	return address, nil
}

// mintedIDHasAddressAtEpochInTx reports whether mintedID's mapping was
// CARRIED FORWARD from priorEpoch through to epoch (storeID's current
// epoch) — R20-n's retained-mapping exception: a version that survives an
// epoch transition keeps its prior-epoch address resolving once
// CurrentAddressForInTx (or a fresh MintUnderEpochInTx) has carried its id
// forward into the current epoch. A bare "does mintedID have ANY row at
// epoch" existence check is not that (gastownhall/beads#6664,
// bee-ghosttrack review 5268699223, item B1): a mapping that lapses at an
// intermediate epoch and is only later reused — an unrelated later mint
// that happens to share the same id — would satisfy it too, wrongly
// retaining an already-superseded address across the gap. Requiring
// epoch == priorEpoch + 1 pins this to the single hop immediately following
// priorEpoch, which an unbroken carry-forward always satisfies (every epoch
// bump that keeps serving mintedID re-mints it at the new current epoch,
// one hop at a time) and a lapsed-then-reused mapping never does. mintedID
// is never recomputed from a bumped store_epoch (see the package doc
// above), so this is a lookup for a second, later row sharing the same id,
// not a derivation.
func mintedIDHasAddressAtEpochInTx(ctx context.Context, tx DBTX, storeID, mintedID string, priorEpoch, epoch int) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM epoch_minted_addresses WHERE store_id = ? AND minted_id = ? AND minted_epoch = ?`,
		storeID, mintedID, epoch,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("epoch CAS: check retained mapping for %s at epoch %d: %w", mintedID, epoch, err)
	}
	return count > 0 && epoch-priorEpoch == 1, nil
}

// StillServesInTx reports whether address is still served under storeID's
// CURRENT epoch: an address minted under an earlier epoch is no longer
// served once the epoch has moved past it, UNLESS its underlying id was
// carried forward into the current epoch by a retained mapping (R20-n),
// even though the row itself is never deleted (ResolveEpochInTx must still
// be able to answer for it). An address from a different store, or one
// never minted, is not served either.
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
	if row.mintedEpoch == epoch {
		return true, nil
	}
	retained, err := mintedIDHasAddressAtEpochInTx(ctx, tx, storeID, row.mintedID, row.mintedEpoch, epoch)
	if err != nil {
		return false, fmt.Errorf("epoch CAS: still serves %s for %s: %w", address, storeID, err)
	}
	return retained, nil
}

// ResolveEpochInTx answers R20's epoch-only restriction for address: Live
// while its minting epoch is still current OR its id was carried forward
// into the current epoch by a retained mapping (R20-n), GoneReorganization
// once the epoch has moved past it with no such mapping (a reorganization,
// not a retention or erasure outcome; RetentionFixture/R17 states are out
// of scope here), and Unknown for an address this store never minted.
// ProducingStore is always storeID: this file has no lineage/replica model
// to attribute a foreign store to (unlike RetentionFixture's cross-store
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
	if row.mintedEpoch == epoch {
		return EpochResolveResult{Restriction: EpochRestrictionLive, ProducingStore: storeID, Epoch: &epoch}, nil
	}
	retained, err := mintedIDHasAddressAtEpochInTx(ctx, tx, storeID, row.mintedID, row.mintedEpoch, epoch)
	if err != nil {
		return EpochResolveResult{}, fmt.Errorf("epoch CAS: resolve %s for %s: %w", address, storeID, err)
	}
	if retained {
		return EpochResolveResult{Restriction: EpochRestrictionLive, ProducingStore: storeID, Epoch: &epoch}, nil
	}
	return EpochResolveResult{Restriction: EpochRestrictionGoneReorganization, ProducingStore: storeID, Epoch: &epoch}, nil
}

// CurrentAddressForInTx re-mints oldAddress's underlying id under storeID's
// CURRENT epoch, giving callers a live address to move to once oldAddress
// stops being served. oldAddress must be one storeID has actually minted.
func CurrentAddressForInTx(ctx context.Context, tx DBTX, storeID, oldAddress string) (string, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, oldAddress)
	if err != nil {
		return "", err
	}
	if !found || row.storeID != storeID {
		return "", fmt.Errorf("epoch CAS: current address for %s: %w: %s", storeID, ErrEpochAddressNotFound, oldAddress)
	}
	return MintUnderEpochInTx(ctx, tx, storeID, row.mintedID)
}
