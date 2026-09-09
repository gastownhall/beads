package dolt

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
// (migration 0069).
//
// UNLIKE MetadataCAS, THIS ROLE HAS NO PUBLIC issueops INTERFACE: the
// conformance contract's own six epoch hooks are bare function types (see
// backend/conformance.EpochFixture) — so there is no role accessor to
// return one of, and these are direct methods on *DoltStore instead of a
// wrapper struct, the same reasoning expected_revision_cas.go gives for
// CompareAndSetVersion/CurrentVersion.
//
// UNLIKE CompareAndSetVersion, these also have no domain-repository
// exposure: the unit-of-work leg reaches internal/storage/issueops's epoch
// Tx functions directly rather than through the domain issue repository
// (see internal/storage/uow's own epoch wiring), so nothing forces these
// types out of issueops into the root storage package the way
// CompareAndSetVersionPlan/Result were. Method signatures here use plain
// types plus storeops.EpochResolveResult directly; backend/conformance's
// own Address/EpochBumpTrigger/RetentionAnswer vocabulary is translated
// only in this leg's contract-test file, per epoch_cas.go's package doc.
//
// Method names match backend/conformance.EpochFixture's own field names
// verbatim (CurrentVersion/CompareAndSetVersion's precedent).

// CurrentEpoch reports storeID's current epoch generation.
func (s *DoltStore) CurrentEpoch(ctx context.Context, storeID string) (int, error) {
	var epoch int
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		epoch, err = storeops.CurrentEpochInTx(ctx, tx, storeID)
		return err
	}); err != nil {
		return 0, err
	}
	return epoch, nil
}

// BumpEpoch advances storeID's epoch generation by one for reason.
func (s *DoltStore) BumpEpoch(ctx context.Context, storeID, reason string) (int, error) {
	var epoch int
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		epoch, err = storeops.BumpEpochInTx(ctx, tx, storeID, reason)
		if err != nil {
			return err
		}
		return s.doltAddAndCommitInTx(ctx, tx, []string{"store_epoch"},
			fmt.Sprintf("bd: bump epoch for %s (%s)", storeID, reason))
	}); err != nil {
		return 0, err
	}
	return epoch, nil
}

// MintUnderEpoch mints id's address under storeID's current epoch.
func (s *DoltStore) MintUnderEpoch(ctx context.Context, storeID, id string) (string, error) {
	var address string
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		address, err = storeops.MintUnderEpochInTx(ctx, tx, storeID, id)
		if err != nil {
			return err
		}
		return s.doltAddAndCommitInTx(ctx, tx, []string{"epoch_minted_addresses"},
			fmt.Sprintf("bd: mint %s under epoch for %s", id, storeID))
	}); err != nil {
		return "", err
	}
	return address, nil
}

// StillServes reports whether address is still served under storeID's
// current epoch.
func (s *DoltStore) StillServes(ctx context.Context, storeID, address string) (bool, error) {
	var serves bool
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		serves, err = storeops.StillServesInTx(ctx, tx, storeID, address)
		return err
	}); err != nil {
		return false, err
	}
	return serves, nil
}

// Resolve answers R20's epoch-only restriction for address.
func (s *DoltStore) Resolve(ctx context.Context, storeID, address string) (storeops.EpochResolveResult, error) {
	var result storeops.EpochResolveResult
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
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
func (s *DoltStore) CurrentAddressFor(ctx context.Context, storeID, oldAddress string) (string, error) {
	var address string
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		address, err = storeops.CurrentAddressForInTx(ctx, tx, storeID, oldAddress)
		if err != nil {
			return err
		}
		return s.doltAddAndCommitInTx(ctx, tx, []string{"epoch_minted_addresses"},
			fmt.Sprintf("bd: current address for %s (%s)", oldAddress, storeID))
	}); err != nil {
		return "", err
	}
	return address, nil
}
