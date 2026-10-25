//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// CurrentEpoch, BumpEpoch, BumpEpochCarrying, MintUnderEpoch, LoseVersion,
// StillServes, Resolve and CurrentAddressFor give this leg R20's
// epoch-transition enforcement
// (gastownhall/beads#5898 revision 9, #6136),
// backed by store_epoch (migration 0067) and epoch_minted_addresses
// (migration 0072). See internal/storage/dolt/epoch_cas.go's matching
// header comment for why these are direct methods on *EmbeddedDoltStore
// rather than a wrapper struct or root-storage-package types.
//
// The writers publish after their SQL transaction commits, and a failed Dolt
// commit is returned (runTransactionWithMessage), so all four meet the
// ordering the server leg is held to. They also return what the server leg
// returns once the SQL commit has landed or may have: runTransactionWithMessage
// wraps every such error in storage.ErrCommitIndeterminate, and a bump hands
// back the epoch its body set (failedBump) and a mint its address, never the
// zero value that would read as nothing applied. Unlike the server leg's mint,
// which logs a failed Dolt commit, this mint returns it, as every write on this
// leg does. A mint must still stage store_epoch (storeops.EpochMintDirtyTables):
// its first use inserts the singleton row.

// CurrentEpoch reports storeID's current epoch generation.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
func (s *EmbeddedDoltStore) CurrentEpoch(ctx context.Context, storeID string) (int, error) {
	var epoch int
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		epoch, err = storeops.CurrentEpochInTx(ctx, tx, storeID)
		return err
	}); err != nil {
		return 0, err
	}
	return epoch, nil
}

// BumpEpoch advances storeID's epoch generation by one for reason. An error
// wrapping storage.ErrCommitIndeterminate comes back with the epoch the bump
// committed, or may have: the caller must not bump again to retry.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
func (s *EmbeddedDoltStore) BumpEpoch(ctx context.Context, storeID, reason string) (int, error) {
	var epoch int
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		epoch, err = storeops.BumpEpochInTx(ctx, tx, storeID, reason)
		if err != nil {
			return nil, "", err
		}
		return storeops.ChangedTables{"store_epoch": true},
			fmt.Sprintf("bd: bump epoch for %s (%s)", storeID, reason), nil
	}); err != nil {
		return failedBump(storeID, epoch, err)
	}
	return epoch, nil
}

// failedBump is what a bump returns when it fails. An error wrapping
// storage.ErrCommitIndeterminate comes from a body that ran to completion and a
// SQL commit that landed or may have (runTransactionWithMessage), so the epoch
// that body set is returned with it, and the caller reads CurrentEpoch to learn
// which. Any other error means nothing landed.
func failedBump(storeID string, epoch int, err error) (int, error) {
	if errors.Is(err, storage.ErrCommitIndeterminate) {
		return epoch, fmt.Errorf("epoch bump for %s may have applied (epoch %d): %w", storeID, epoch, err)
	}
	return 0, err
}

// BumpEpochCarrying is the token-scheme-change bump: BumpEpoch, and also the
// carry of every live address in the database to the new epoch
// (storeops.BumpEpochCarryingInTx). It writes store_epoch and
// epoch_minted_addresses, so it publishes both.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
//
// It fails as BumpEpoch does (failedBump).
//
// CALLERS MUST HOLD THE WORKSPACE GATE (internal/workspacegate) EXCLUSIVELY FOR
// THE BUMP, as a restore does: see storeops.BumpEpochCarryingInTx.
func (s *EmbeddedDoltStore) BumpEpochCarrying(ctx context.Context, storeID, reason string) (int, error) {
	var epoch int
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		epoch, err = storeops.BumpEpochCarryingInTx(ctx, tx, storeID, reason)
		if err != nil {
			return nil, "", err
		}
		return storeops.ChangedTables{"store_epoch": true, "epoch_minted_addresses": true},
			fmt.Sprintf("bd: carry addresses to epoch %d for %s (%s)", epoch, storeID, reason), nil
	}); err != nil {
		return failedBump(storeID, epoch, err)
	}
	return epoch, nil
}

// MintUnderEpoch mints id's address under storeID's current epoch. An error
// wrapping storage.ErrCommitIndeterminate comes back with the address the mint
// committed, or may have; StillServes says whether it did.
//
// Embedded transactions serialize (see recheckBlockedAfterCommit), so a mint
// cannot overlap a BumpEpoch here the way it can on the server leg (see
// TestMintOverlappingABumpLeavesALiveSurvivor in the dolt package).
func (s *EmbeddedDoltStore) MintUnderEpoch(ctx context.Context, storeID, id string) (string, error) {
	var address string
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		address, err = storeops.MintUnderEpochInTx(ctx, tx, storeID, id)
		if err != nil {
			return nil, "", err
		}
		return storeops.EpochMintDirtyTables(),
			fmt.Sprintf("bd: mint %s under epoch for %s", id, storeID), nil
	}); err != nil {
		if errors.Is(err, storage.ErrCommitIndeterminate) {
			return address, err
		}
		return "", err
	}
	return address, nil
}

// StillServes reports whether address is still served under storeID's
// current epoch.
func (s *EmbeddedDoltStore) StillServes(ctx context.Context, storeID, address string) (bool, error) {
	var serves bool
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		serves, err = storeops.StillServesInTx(ctx, tx, storeID, address)
		return err
	}); err != nil {
		return false, err
	}
	return serves, nil
}

// Resolve answers R20's epoch-only restriction for address.
func (s *EmbeddedDoltStore) Resolve(ctx context.Context, storeID, address string) (storeops.EpochResolveResult, error) {
	var result storeops.EpochResolveResult
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = storeops.ResolveEpochInTx(ctx, tx, storeID, address)
		return err
	}); err != nil {
		return storeops.EpochResolveResult{}, err
	}
	return result, nil
}

// CurrentAddressFor reports the address oldAddress now resolves to: the newest
// address of its lineage, or oldAddress itself when nothing carried it. It only
// reads, so it runs on a read-only connection and publishes nothing.
func (s *EmbeddedDoltStore) CurrentAddressFor(ctx context.Context, storeID, oldAddress string) (string, error) {
	var address string
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		address, err = storeops.CurrentAddressForInTx(ctx, tx, storeID, oldAddress)
		return err
	}); err != nil {
		return "", err
	}
	return address, nil
}

// LoseVersion records that storeID no longer serves the Version named by
// address (storeops.LoseVersionInTx). It writes epoch_minted_addresses, so it
// publishes that table, and a failed Dolt commit is returned, wrapping
// storage.ErrCommitIndeterminate: a loss its caller believes recorded must not
// be left unpublished.
func (s *EmbeddedDoltStore) LoseVersion(ctx context.Context, storeID, address string) error {
	return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		if err := storeops.LoseVersionInTx(ctx, tx, storeID, address); err != nil {
			return nil, "", err
		}
		return storeops.ChangedTables{"epoch_minted_addresses": true},
			fmt.Sprintf("bd: lose version %s for %s", address, storeID), nil
	})
}
