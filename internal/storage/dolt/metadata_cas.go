package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// MetadataCAS returns the conditional single-key metadata write for this store.
func (s *DoltStore) MetadataCAS() (issueops.MetadataCAS, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "MetadataCAS", Backend: "nil"}
	}
	return &metadataCAS{store: s}, nil
}

// metadataCAS swaps one metadata key inside ONE transaction.
//
// There is no shared constructor package for this role, for the reason
// cycleDetector gives: the compare and the write must see one snapshot, and a
// transaction is not reachable through storage.DoltStorage. The sharing happens
// one level down — this body and the embedded store's are a few lines each
// around issueops.CompareAndSetMetadataKeyInTx, and the unit-of-work leg
// reaches the same function through the domain issue repository — so all three
// legs are ONE body, which the conformance contract's header states.
type metadataCAS struct{ store *DoltStore }

var _ issueops.MetadataCAS = (*metadataCAS)(nil)

// CompareAndSetKey applies the request's transition if the key still holds what
// the caller expected.
//
// VALIDATION HAPPENS BEFORE THE TRANSACTION, which makes the role's "a refusal
// changes nothing" true of the connection as well as of the row.
//
// THE RETRY IS LOAD-BEARING, not defensive. Dolt has no row locks — FOR UPDATE
// is a parse-only no-op — so a concurrent writer is caught at commit time and
// withRetryTx re-runs the WHOLE body against the winner's committed row. That
// re-read is what makes a lost race report the value that actually beat it
// rather than a value from a transaction that never landed.
//
// THE VERSION-CONTROL ENTRY IS ONE PER SWAP THAT WROTE. A lost race and a swap
// over an already-equal value stage nothing, and an ephemeral row's tables are
// ignored by this plane, so the staged-set guard inside doltAddAndCommitInTx
// finds nothing to commit and records none — which is why this leg needs no
// separate wisp path.
func (m *metadataCAS) CompareAndSetKey(ctx context.Context, req issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	plan, err := storage.PlanCompareAndSetKey(req)
	if err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}

	var result issueops.CompareAndSetKeyResult
	if err := m.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		swap, write, err := storeops.CompareAndSetMetadataKeyInTx(ctx, tx, plan)
		if err != nil {
			return err
		}
		result = swap
		if len(write.Tables) == 0 {
			// Nothing was written, or what was written is ephemeral and lives in
			// tables this plane ignores. Either way there is nothing to version;
			// withRetryTx still commits the SQL transaction.
			return nil
		}
		// The swap routes through UpdateIssueInTx, which also writes an
		// EventUpdated row, so stage both tables (mirrors MergeMetadata).
		return m.store.doltAddAndCommitInTx(ctx, tx, []string{"issues", "events"},
			fmt.Sprintf("bd: compare-and-set metadata %s.%s", plan.IssueID, plan.Key))
	}); err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	return result, nil
}
