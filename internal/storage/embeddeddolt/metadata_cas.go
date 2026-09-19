//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// MetadataCAS returns the conditional single-key metadata write for this store.
func (s *EmbeddedDoltStore) MetadataCAS() (issueops.MetadataCAS, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "MetadataCAS", Backend: "nil"}
	}
	return &metadataCAS{store: s}, nil
}

// metadataCAS swaps one metadata key inside one connection's transaction. The
// compare and the write need a TRANSACTION, which storage.DoltStorage does not
// publish, so the sharing with the server-backed store happens at
// issueops.CompareAndSetMetadataKeyInTx. The two differ only in how they reach
// a transaction and in WHEN the version-control entry lands: the server writes
// it inside the write transaction, this one after the SQL commit on a second
// connection.
type metadataCAS struct{ store *EmbeddedDoltStore }

var _ issueops.MetadataCAS = (*metadataCAS)(nil)

// CompareAndSetKey applies the request's transition if the key still holds what
// the caller expected. Validation happens before the connection is opened, so a
// refused request costs no database work.
//
// A SWAP THAT WROTE NOTHING COMPOSES NO COMMIT MESSAGE, which is
// runIssueOperationTxWithMessage's existing "write, but do not version commit"
// signal — the same one a lost race needs, since it has nothing to record.
//
// THE ENTRY LANDS AFTER THE SQL TRANSACTION COMMITS, on a second connection:
// this store has no way to mint a Dolt commit inside its own. "One entry per
// swap" is therefore a steady-state promise here, where the server-backed
// store's is crash-atomic.
func (m *metadataCAS) CompareAndSetKey(ctx context.Context, req issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	plan, err := storage.PlanCompareAndSetKey(req)
	if err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}

	var result issueops.CompareAndSetKeyResult
	if err := m.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		swap, write, err := storeops.CompareAndSetMetadataKeyInTx(ctx, tx, plan)
		if err != nil {
			return nil, "", err
		}
		result = swap
		if len(write.Tables) == 0 {
			// An empty message is runTransaction's "write, but do not version
			// commit" signal: the SQL transaction still commits, which is what an
			// ephemeral swap needs.
			return nil, "", nil
		}
		return write.Tables, fmt.Sprintf("bd: compare-and-set metadata %s.%s", plan.IssueID, plan.Key), nil
	}); err != nil {
		return issueops.CompareAndSetKeyResult{}, err
	}
	return result, nil
}
