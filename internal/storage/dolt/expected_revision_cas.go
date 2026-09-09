package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// CompareAndSetVersion and CurrentVersion give this leg R16/R17's
// whole-of-state per-record CAS write, backed by expected_revision_records
// (migration 0069).
//
// UNLIKE MetadataCAS, THIS ROLE HAS NO PUBLIC issueops INTERFACE: the
// conformance contract's own CompareAndSetVersion hook is a bare function
// type (PerRecordCASWrite), deliberately shared with the out-of-scope
// R16.1 GuardedWrite hook rather than modeled as an interface — so there is
// no role accessor to return one of, and these are direct methods on
// *DoltStore instead of a wrapper struct.

// CompareAndSetVersion runs R16's whole-of-state per-record CAS write inside
// ONE transaction, retried the same way metadataCAS.CompareAndSetKey is:
// Dolt has no row locks, so a concurrent writer is caught at commit time and
// withRetryTx re-runs the WHOLE body against the winner's committed row.
func (s *DoltStore) CompareAndSetVersion(ctx context.Context, plan storage.CompareAndSetVersionPlan) (storage.CompareAndSetVersionResult, error) {
	var result storage.CompareAndSetVersionResult
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		r, err := storeops.CompareAndSetVersionInTx(ctx, tx, plan)
		if err != nil {
			return err
		}
		result = r
		if !r.Accepted {
			// A refusal changes nothing; withRetryTx still commits the
			// (empty) SQL transaction.
			return nil
		}
		return s.doltAddAndCommitInTx(ctx, tx, []string{"expected_revision_records"},
			fmt.Sprintf("bd: compare-and-set version %s", plan.ID))
	}); err != nil {
		return storage.CompareAndSetVersionResult{}, err
	}
	return result, nil
}

// CurrentVersion reports the Address currently current for id.
func (s *DoltStore) CurrentVersion(ctx context.Context, id string) (string, error) {
	var address string
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		address, err = storeops.CurrentExpectedRevisionInTx(ctx, tx, id)
		return err
	}); err != nil {
		return "", err
	}
	return address, nil
}
