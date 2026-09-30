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
// (migration 0071).
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
//
// PUBLICATION ORDER. The three writers
// commit their SQL transaction FIRST and stage and Dolt-commit afterwards,
// never from inside the open transaction. doltAddAndCommitInTx builds the Dolt
// commit from the transaction's BEGIN-time snapshot, so under concurrent
// writers it writes every row changed in the meantime back to its old value,
// and store_epoch and epoch_minted_addresses are append-only: a row lost that
// way is never rewritten (the HAZARD block on that helper).

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
//
// A Dolt commit that fails after the SQL commit is RETURNED, where the mint
// paths below log it and carry on: a bump is rare and
// operator-driven, and its caller (the restore trigger) must not report a
// restore complete while the bump is committed but unpublished. In that case
// the bump HAS applied: the epoch returned is the new one, the row is durable
// in the working set and rides the next Dolt commit, and the caller must not
// bump again to retry.
func (s *DoltStore) BumpEpoch(ctx context.Context, storeID, reason string) (int, error) {
	var epoch int
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		epoch, err = storeops.BumpEpochInTx(ctx, tx, storeID, reason)
		return err
	}); err != nil {
		return 0, err
	}
	if err := s.doltAddAndCommitPostTx(ctx, []string{"store_epoch"},
		fmt.Sprintf("bd: bump epoch for %s (%s)", storeID, reason)); err != nil {
		return epoch, fmt.Errorf("epoch bump for %s applied (epoch %d) but its Dolt commit failed: %w", storeID, epoch, err)
	}
	return epoch, nil
}

// MintUnderEpoch mints id's address under storeID's current epoch.
//
// It publishes like every issue mutation, through
// runIssueOperationTxWithMessage: a Dolt commit that fails after the SQL commit
// is logged, not returned. The mint is durable in the working set and rides the
// next Dolt commit, and mints are frequent and automated, so failing one over a
// missing history commit would only turn a harmless gap into caller errors.
//
// A mint whose transaction overlaps a BumpEpoch reads the epoch before the bump
// and can commit an address the bump has already voided. That is left as it is;
// TestMintOverlappingABumpCommitsAnAddressThatIsAlreadyGone records why.
func (s *DoltStore) MintUnderEpoch(ctx context.Context, storeID, id string) (string, error) {
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
// current epoch. It mints, so it publishes, and handles a failed Dolt commit,
// exactly as MintUnderEpoch does.
func (s *DoltStore) CurrentAddressFor(ctx context.Context, storeID, oldAddress string) (string, error) {
	var address string
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		var err error
		address, err = storeops.CurrentAddressForInTx(ctx, tx, storeID, oldAddress)
		if err != nil {
			return nil, "", err
		}
		return storeops.EpochMintDirtyTables(),
			fmt.Sprintf("bd: current address for %s (%s)", oldAddress, storeID), nil
	}); err != nil {
		return "", err
	}
	return address, nil
}
