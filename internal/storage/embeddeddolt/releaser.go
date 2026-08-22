//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Releaser returns the claim-release surface for this store.
func (s *EmbeddedDoltStore) Releaser() (issueops.Releaser, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "Releaser", Backend: "nil"}
	}
	return &releaser{store: s}, nil
}

// releaser gives up a claim inside one connection's transaction. The
// classifying read, the release and the post-state snapshot need a
// TRANSACTION, which storage.DoltStorage does not publish, so the sharing with
// the server-backed store happens at issueops.ReleaseIssueInTx. The two differ
// only in how they reach a transaction and in WHEN the version-control entry
// lands: the server writes it inside the write transaction, this one after the
// SQL commit on a second connection.
type releaser struct{ store *EmbeddedDoltStore }

var _ issueops.Releaser = (*releaser)(nil)

// Release gives up the claim on one issue. Validation happens before the
// connection is opened, so a refused request costs no database work.
//
// AN EPHEMERAL RELEASE COMPOSES NO STAGED SET, which is
// runIssueOperationTxWithMessage's existing "write, but do not version commit"
// signal: the SQL transaction still commits and the wisp tables this plane
// ignores are not versioned.
//
// THE ENTRY LANDS AFTER THE SQL TRANSACTION COMMITS, on a second connection:
// this store has no way to mint a Dolt commit inside its own. "One entry per
// release" is therefore a steady-state promise here, where the server-backed
// store's is crash-atomic.
//
// THERE IS NO VERIFY-BY-RE-READ HERE, and its absence is a decision rather than
// an omission. The check the server-backed leg carries exists for a DEGRADED
// SERVER reporting a write that did not land; this store is in-process, and its
// direct UnclaimIssue route has never carried one either.
func (r *releaser) Release(ctx context.Context, req issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	if err := workapi.ValidateReleaseRequest(req); err != nil {
		return issueops.ReleaseResult{}, err
	}

	var result issueops.ReleaseResult
	if err := r.store.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storeops.ChangedTables, string, error) {
		released, write, err := storeops.ReleaseIssueInTx(ctx, tx, req)
		if err != nil {
			return nil, "", err
		}
		result = released
		if len(write.Tables) == 0 {
			return nil, "", nil
		}
		return write.Tables, fmt.Sprintf("bd: unclaim %s", req.IssueID), nil
	}); err != nil {
		return issueops.ReleaseResult{}, err
	}
	return result, nil
}
