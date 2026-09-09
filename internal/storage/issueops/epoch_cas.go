package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file implements R20 epoch-transition enforcement (gastownhall/beads#5898
// revision 9, this slice: be-x5jqd.4 / #6136): a store-wide epoch generation
// counter (store_epoch, migration 0067) plus a durable record of the
// addresses minted under each generation (epoch_minted_addresses, migration
// 0069), used to answer whether a previously-minted address is still served
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

// epochAddress is a minted address: a deterministic token over storeID, id,
// and the epoch current at mint time, so re-minting the same id under the
// same epoch always reproduces the same address.
func epochAddress(storeID, id string, epoch int) string {
	return fmt.Sprintf("epch:%s:%s:%d", storeID, id, epoch)
}

// ensureStoreEpochRow lazily initializes store_epoch's singleton row
// (migration 0067; the table starts empty) to epoch 1 on first use, and
// reports the current epoch either way.
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

// upsertEpochMintedAddressInTx writes address's row via an explicit
// exists-check branch (the read already ran in the caller; exists says
// which branch to take) rather than ON DUPLICATE KEY UPDATE, matching
// upsertExpectedRevisionRowInTx's precedent.
func upsertEpochMintedAddressInTx(ctx context.Context, tx DBTX, address, storeID, mintedID string, mintedEpoch int, exists bool) error {
	now := time.Now().UTC()
	if exists {
		if _, err := tx.ExecContext(ctx,
			`UPDATE epoch_minted_addresses SET store_id = ?, minted_id = ?, minted_epoch = ?, minted_at = ? WHERE address = ?`,
			storeID, mintedID, mintedEpoch, now, address,
		); err != nil {
			return fmt.Errorf("epoch CAS: update minted address %s: %w", address, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at) VALUES (?, ?, ?, ?, ?)`,
		address, storeID, mintedID, mintedEpoch, now,
	); err != nil {
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
	epoch, err := ensureStoreEpochRow(ctx, tx)
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

// StillServesInTx reports whether address is still served under storeID's
// CURRENT epoch: an address minted under an earlier epoch is no longer
// served the moment the epoch has moved past it (R20-n), even though the
// row itself is never deleted (ResolveEpochInTx must still be able to
// answer for it). An address from a different store, or one never minted,
// is not served either.
func StillServesInTx(ctx context.Context, tx DBTX, storeID, address string) (bool, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return false, err
	}
	if !found || row.storeID != storeID {
		return false, nil
	}
	epoch, err := ensureStoreEpochRow(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("epoch CAS: still serves %s for %s: %w", address, storeID, err)
	}
	return row.mintedEpoch == epoch, nil
}

// ResolveEpochInTx answers R20's epoch-only restriction for address: Live
// while its minting epoch is still current, GoneReorganization once the
// epoch has moved past it (R20-n — a reorganization, not a retention or
// erasure outcome; RetentionFixture/R17 states are out of scope here), and
// Unknown for an address this store never minted. ProducingStore is always
// storeID: this file has no lineage/replica model to attribute a foreign
// store to (unlike RetentionFixture's cross-store answers).
func ResolveEpochInTx(ctx context.Context, tx DBTX, storeID, address string) (EpochResolveResult, error) {
	row, found, err := readEpochMintedAddressInTx(ctx, tx, address)
	if err != nil {
		return EpochResolveResult{}, err
	}
	if !found || row.storeID != storeID {
		return EpochResolveResult{Restriction: EpochRestrictionUnknown, ProducingStore: storeID}, nil
	}
	epoch, err := ensureStoreEpochRow(ctx, tx)
	if err != nil {
		return EpochResolveResult{}, fmt.Errorf("epoch CAS: resolve %s for %s: %w", address, storeID, err)
	}
	if row.mintedEpoch == epoch {
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
