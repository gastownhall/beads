//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/storage"
	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

// Memories returns the guarded persistent-memory surface for this store.
func (s *EmbeddedDoltStore) Memories() (memoryops.Memories, error) {
	return newMemories(s)
}

// newMemories returns guarded memory operations backed by store. It is
// unexported for the reason the server-backed sibling gives: the accessor above
// is the door, because that is where each decorator adds its layer.
func newMemories(store *EmbeddedDoltStore) (memoryops.Memories, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newMemories", Backend: "nil"}
	}
	return &memories{store: store}, nil
}

type memories struct{ store *EmbeddedDoltStore }

var _ memoryops.Memories = (*memories)(nil)

// Remember validates and derives before the connection is taken, then probes
// and writes on ONE connection — the embedded engine's version of the single
// transaction the server-backed store gets from withRetryTx.
func (m *memories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	var replaced bool
	if err := m.store.withConn(ctx, true, func(tx *sql.Tx) error {
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
	if err := m.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		value, err = storagememoryops.RecallInTx(ctx, tx, key)
		return err
	}); err != nil {
		return memoryops.RecallResult{}, err
	}
	return memoryops.RecallResult{Key: key, Value: value, Found: value != ""}, nil
}

func (m *memories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	var (
		previous string
		found    bool
	)
	if err := m.store.withConn(ctx, true, func(tx *sql.Tx) error {
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
	if err := m.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		all, err = storagememoryops.ListInTx(ctx, tx)
		return err
	}); err != nil {
		return memoryops.ListResult{}, err
	}
	return memoryops.ListResult{Memories: memoryapi.FilterMemories(all, req.Search)}, nil
}
