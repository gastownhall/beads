//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// DependencyEditor returns the guarded dependency-edge surface for this store.
func (s *EmbeddedDoltStore) DependencyEditor() (issueops.DependencyEditor, error) {
	return NewDependencyEditor(s)
}

// NewDependencyEditor returns a guarded dependency editor backed by store.
func NewDependencyEditor(store *EmbeddedDoltStore) (issueops.DependencyEditor, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewDependencyEditor", Backend: "nil"}
	}
	return &dependencyEditor{store: store}, nil
}

type dependencyEditor struct{ store *EmbeddedDoltStore }

// AddDependencies asserts every edge in ONE transaction with one commit. The
// first refusal rolls the whole thing back, which is what makes the request
// all-or-nothing without any bookkeeping of its own.
func (e *dependencyEditor) AddDependencies(ctx context.Context, request issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	if err := storageissueops.ValidateAddDependenciesRequest(request); err != nil {
		return issueops.AddDependenciesResult{}, err
	}
	message := storageissueops.AddDependenciesCommitMessage(request)

	var result issueops.AddDependenciesResult
	err := e.store.runIssueOperationTx(ctx, message, func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteAddDependencies(ctx, tx, request)
		return tables, err
	})
	if err != nil {
		return issueops.AddDependenciesResult{}, err
	}
	return result, nil
}

// RemoveDependency removes one edge. The message is composed up front because
// a removal names the edge it was asked for; whether an entry is recorded at
// all is decided by the changed-table set, which is empty when no edge existed.
func (e *dependencyEditor) RemoveDependency(ctx context.Context, request issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	if err := storageissueops.ValidateRemoveDependencyRequest(request); err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	message := storageissueops.RemoveDependencyCommitMessage(request.IssueID, request.DependsOnID)

	var result issueops.RemoveDependencyResult
	err := e.store.runIssueOperationTx(ctx, message, func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteRemoveDependency(ctx, tx, request)
		return tables, err
	})
	if err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	return result, nil
}

var _ issueops.DependencyEditor = (*dependencyEditor)(nil)
