package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/storage"
	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

// Memories returns the guarded persistent-memory surface for this store.
func (s *DoltStore) Memories() (memoryops.Memories, error) {
	return newMemories(s)
}

// newMemories returns guarded memory operations backed by store.
//
// It is unexported, like newEdgeReader beside it: the shared body is a set of
// InTx functions that need a transaction this store owns, so no front door can
// reach it at all and there is nothing for a depguard entry to deny.
func newMemories(store *DoltStore) (memoryops.Memories, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newMemories", Backend: "nil"}
	}
	return &memories{store: store}, nil
}

type memories struct{ store *DoltStore }

var _ memoryops.Memories = (*memories)(nil)

// Remember validates and derives BEFORE opening the transaction, so a refusal
// cannot be mistaken at the call site for a write that rolled back, and then
// probes and writes inside ONE retryable transaction.
func (m *memories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	var replaced bool
	if err := m.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		replaced, err = storagememoryops.RememberInTx(ctx, tx, key, req.Content)
		return err
	}); err != nil {
		return memoryops.RememberResult{}, err
	}
	return memoryops.RememberResult{Key: key, Value: req.Content, Replaced: replaced}, nil
}

func (m *memories) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.RecallResult{}, err
	}
	var value string
	if err := m.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		value, err = storagememoryops.RecallInTx(ctx, tx, key)
		return err
	}); err != nil {
		return memoryops.RecallResult{}, err
	}
	return memoryops.RecallResult{Key: key, Value: value, Found: value != ""}, nil
}

// Forget reads the value and deletes the row in ONE transaction, so the value
// it reports is the one it removed.
func (m *memories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	var (
		previous string
		found    bool
	)
	if err := m.store.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		previous, found, err = storagememoryops.ForgetInTx(ctx, tx, key)
		return err
	}); err != nil {
		return memoryops.ForgetResult{}, err
	}
	return memoryops.ForgetResult{Key: key, Value: previous, Found: found}, nil
}

func (m *memories) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	var all map[string]string
	if err := m.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		all, err = storagememoryops.ListInTx(ctx, tx)
		return err
	}); err != nil {
		return memoryops.ListResult{}, err
	}
	return memoryops.ListResult{Memories: memoryapi.FilterMemories(all, req.Search)}, nil
}
