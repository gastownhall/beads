package dolt

import (
	"context"
	"database/sql"
	"fmt"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// CurrentEpoch, BumpEpoch, BumpEpochCarrying, MintUnderEpoch, LoseVersion,
// StillServes, Resolve and CurrentAddressFor give this leg R20's
// epoch-transition enforcement
// (gastownhall/beads#5898 revision 9, #6136),
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
// PUBLICATION ORDER. The four writers
// commit their SQL transaction FIRST and stage and Dolt-commit afterwards,
// never from inside the open transaction. doltAddAndCommitInTx builds the Dolt
// commit from the transaction's BEGIN-time snapshot, so under concurrent
// writers it writes every row changed in the meantime back to its old value,
// and store_epoch and epoch_minted_addresses are append-only: a row lost that
// way is never rewritten (the HAZARD block on that helper).

// CurrentEpoch reports storeID's current epoch generation.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
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
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
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

// BumpEpochCarrying is the token-scheme-change bump: BumpEpoch, and also the
// carry of every live address in the database to the new epoch
// (storeops.BumpEpochCarryingInTx). It writes store_epoch and
// epoch_minted_addresses, so it publishes both. It surfaces a failed Dolt
// commit exactly as BumpEpoch does, and the bump has applied in that case: the
// epoch returned is the new one and the caller must not bump again to retry.
//
// The epoch counter is per database, not per store. storeID appears only in
// error text; one bump advances the epoch seen through every storeID that
// shares this database.
//
// CALLERS MUST HOLD THE WORKSPACE GATE (internal/workspacegate) EXCLUSIVELY FOR
// THE BUMP, as a restore does: see storeops.BumpEpochCarryingInTx.
func (s *DoltStore) BumpEpochCarrying(ctx context.Context, storeID, reason string) (int, error) {
	var epoch int
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		epoch, err = storeops.BumpEpochCarryingInTx(ctx, tx, storeID, reason)
		return err
	}); err != nil {
		return 0, err
	}
	if err := s.doltAddAndCommitPostTx(ctx, []string{"store_epoch", "epoch_minted_addresses"},
		fmt.Sprintf("bd: carry addresses to epoch %d for %s (%s)", epoch, storeID, reason)); err != nil {
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
// and commits an address minted under the epoch the bump replaced. That address
// is a Live survivor: a restore or reinit bump writes no address rows, and an
// address's status is read from its own row. TestMintOverlappingABumpLeavesALiveSurvivor
// records that interleaving, and TestAMintAfterAConcurrentLossNeverRevivesTheLostAddress
// the one that must never bring a lost address back.
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

// CurrentAddressFor reports the address oldAddress now resolves to: the newest
// address of its lineage, or oldAddress itself when nothing carried it. It only
// reads, so it runs on this leg's read path and publishes nothing.
func (s *DoltStore) CurrentAddressFor(ctx context.Context, storeID, oldAddress string) (string, error) {
	var address string
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
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
// publishes that table, and unlike a mint it RETURNS a failed Dolt commit: a
// loss its caller believes recorded must not be left unpublished. The loss has
// applied in that case, and replaying it is a no-op.
func (s *DoltStore) LoseVersion(ctx context.Context, storeID, address string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return storeops.LoseVersionInTx(ctx, tx, storeID, address)
	}); err != nil {
		return err
	}
	if err := s.doltAddAndCommitPostTx(ctx, []string{"epoch_minted_addresses"},
		fmt.Sprintf("bd: lose version %s for %s", address, storeID)); err != nil {
		return fmt.Errorf("loss of %s for %s recorded but its Dolt commit failed: %w", address, storeID, err)
	}
	return nil
}
