//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// CurrentEpoch, BumpEpoch, MintUnderEpoch, StillServes, Resolve and
// CurrentAddressFor give this leg R20's epoch-transition enforcement
// (gastownhall/beads#5898 revision 9, this slice: be-x5jqd.4 / #6136),
// backed by store_epoch (migration 0067) and epoch_minted_addresses
// (migration 0069). See internal/storage/dolt/epoch_cas.go's matching
// header comment for why these are direct methods on *EmbeddedDoltStore
// rather than a wrapper struct or root-storage-package types.

// CurrentEpoch reports storeID's current epoch generation.
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

// BumpEpoch advances storeID's epoch generation by one for reason.
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
		return 0, err
	}
	return epoch, nil
}

// MintUnderEpoch mints id's address under storeID's current epoch.
func (s *EmbeddedDoltStore) MintUnderEpoch(ctx context.Context, storeID, id string) (string, error) {
	var address string
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		address, err = storeops.MintUnderEpochInTx(ctx, tx, storeID, id)
		if err != nil {
			return nil, "", err
		}
		return storeops.ChangedTables{"epoch_minted_addresses": true},
			fmt.Sprintf("bd: mint %s under epoch for %s", id, storeID), nil
	}); err != nil {
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

// CurrentAddressFor re-mints oldAddress's underlying id under storeID's
// current epoch.
func (s *EmbeddedDoltStore) CurrentAddressFor(ctx context.Context, storeID, oldAddress string) (string, error) {
	var address string
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		address, err = storeops.CurrentAddressForInTx(ctx, tx, storeID, oldAddress)
		if err != nil {
			return nil, "", err
		}
		return storeops.ChangedTables{"epoch_minted_addresses": true},
			fmt.Sprintf("bd: current address for %s (%s)", oldAddress, storeID), nil
	}); err != nil {
		return "", err
	}
	return address, nil
}
