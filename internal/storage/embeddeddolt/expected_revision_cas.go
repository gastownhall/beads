//go:build cgo

package embeddeddolt

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
// *EmbeddedDoltStore instead of a wrapper struct. See
// internal/storage/dolt/expected_revision_cas.go's matching header comment.

// CompareAndSetVersion runs R16's whole-of-state per-record CAS write inside
// one transaction, the same way metadataCAS.CompareAndSetKey does on this
// leg: a single attempt, no retry loop. Unlike DoltStore's server-backed
// path, this embedded engine has no separate committing client to race
// against between this call's read and write.
func (s *EmbeddedDoltStore) CompareAndSetVersion(ctx context.Context, plan storage.CompareAndSetVersionPlan) (storage.CompareAndSetVersionResult, error) {
	var result storage.CompareAndSetVersionResult
	if err := s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		r, err := storeops.CompareAndSetVersionInTx(ctx, tx, plan)
		if err != nil {
			return nil, "", err
		}
		result = r
		if !r.Accepted {
			// A refusal changes nothing; the SQL transaction still commits
			// (empty), and no Dolt version-control commit is staged.
			return nil, "", nil
		}
		return storeops.ChangedTables{"expected_revision_records": true},
			fmt.Sprintf("bd: compare-and-set version %s", plan.ID), nil
	}); err != nil {
		return storage.CompareAndSetVersionResult{}, err
	}
	return result, nil
}

// CurrentVersion reports the Address currently current for id.
func (s *EmbeddedDoltStore) CurrentVersion(ctx context.Context, id string) (string, error) {
	var address string
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		address, err = storeops.CurrentExpectedRevisionInTx(ctx, tx, id)
		return err
	}); err != nil {
		return "", err
	}
	return address, nil
}
