//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// IssueLifecycle returns the guarded issue-lifecycle surface for this store.
func (s *EmbeddedDoltStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return NewIssueOperations(s)
}

// NewIssueOperations returns guarded issue operations backed by store.
func NewIssueOperations(store *EmbeddedDoltStore) (issueops.Lifecycle, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewIssueOperations", Backend: "nil"}
	}
	return &issueOperations{store: store}, nil
}

type issueOperations struct{ store *EmbeddedDoltStore }

// updateCommitMessage names the updated issue in the Dolt commit message.
func updateCommitMessage(issueID string) string {
	if issueID == "" {
		return "bd: update issue"
	}
	return "bd: update " + issueID
}

func (o *issueOperations) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	snapshot := storageissueops.CloneCreateRequest(request)
	var result issueops.CreateResult
	err := o.store.runIssueOperationTx(ctx, "bd: create issue", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteCreate(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

func (o *issueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	snapshot := storageissueops.CloneUpdateRequest(request)
	var result issueops.UpdateResult
	// Same ID-bearing commit message as the server-backed store, so `bd dolt
	// log` reads the same on both backends.
	err := o.store.runIssueOperationTx(ctx, storageissueops.HistoryEntry(snapshot.Provenance, updateCommitMessage(snapshot.IssueID)), func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteUpdate(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

func (o *issueOperations) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	snapshot := storageissueops.CloneCloseRequest(request)
	var result issueops.CloseResult
	err := o.store.runIssueOperationTx(ctx, "bd: close issue", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteClose(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

func (o *issueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	snapshot := storageissueops.CloneReopenRequest(request)
	var result issueops.ReopenResult
	err := o.store.runIssueOperationTx(ctx, storageissueops.HistoryEntry(snapshot.Provenance, "bd: reopen issue"), func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteReopen(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

var _ issueops.Lifecycle = (*issueOperations)(nil)
