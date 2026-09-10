package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/memoryapi"
	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

// MemoriesSource is the capability accessor a unit-of-work provider offers for
// the persistent-memory role, the sibling of WorkspaceConfigSource and
// CounterSource.
type MemoriesSource interface {
	Memories() (memoryops.Memories, error)
}

// memories answers memory queries and writes through a unit of work.
type memories struct {
	provider UnitOfWorkProvider
}

// Memories returns the guarded persistent-memory surface for this provider.
func (p *doltSQLProvider) Memories() (memoryops.Memories, error) {
	return NewMemories(p)
}

// NewMemories constructs a public persistent-memory surface backed by provider.
func NewMemories(provider UnitOfWorkProvider) (memoryops.Memories, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new memories: unit-of-work provider must not be nil")
	}
	return &memories{provider: provider}, nil
}

var _ memoryops.Memories = (*memories)(nil)

// This is the genuinely independent body of the three: it composes
// domain.ConfigUseCase where the two store backends compose the shared InTx
// functions in internal/storage/memoryops. What keeps the two routes seeing
// each other's memories is that both encode and decode the kv.memory. prefix
// through that package's StorageKey and MemoriesFromConfig — the one encode and
// the one decode in the tree.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK IS OPENED, for the reason
// workspace_config.go gives: a validation failure raised inside RunTx is
// indistinguishable at the call site from a write that rolled back.

// Remember probes and writes in ONE unit of work.
//
// The shipped proxied path pre-read in a SEPARATE RunTxRead and then opened a
// RunTx to write, so the "Remembered" versus "Updated" verb described a moment
// that had already passed. Nothing moves between the two now.
//
// The probe is GetAllConfig rather than GetConfig because Replaced is about the
// ROW: this seam maps a missing row and a row stored empty to the same "", and
// the map's key set is the only place the difference survives. It is one extra
// query on a table that holds tens of rows, and it is what makes the answer the
// same on this backend as on the other two.
func (m *memories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	storageKey := storagememoryops.StorageKey(key)
	replaced, err := RunTxResult(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) (bool, string, error) {
		cfg := uw.ConfigUseCase()
		all, err := cfg.GetAllConfig(ctx)
		if err != nil {
			return false, "", err
		}
		_, replaced := all[storageKey]
		if err := cfg.SetConfig(ctx, storageKey, req.Content); err != nil {
			return false, "", err
		}
		return replaced, "bd: remember " + key, nil
	})
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	return memoryops.RememberResult{Key: key, Value: req.Content, Replaced: replaced}, nil
}

func (m *memories) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.RecallResult{}, err
	}
	return RunTxRead(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) (memoryops.RecallResult, error) {
		value, err := uw.ConfigUseCase().GetConfig(ctx, storagememoryops.StorageKey(key))
		if err != nil {
			return memoryops.RecallResult{}, err
		}
		return memoryops.RecallResult{Key: key, Value: value, Found: value != ""}, nil
	})
}

// Forget reads the value and deletes the row in ONE unit of work, so the value
// it reports is the one it removed rather than the one a separate earlier read
// saw.
//
// A miss returns an EMPTY commit message, which is how this layer says "commit
// nothing": a Forget that found no memory must leave no Dolt commit behind,
// exactly as it leaves no row change.
func (m *memories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	storageKey := storagememoryops.StorageKey(key)
	result, err := RunTxResult(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) (memoryops.ForgetResult, string, error) {
		cfg := uw.ConfigUseCase()
		previous, err := cfg.GetConfig(ctx, storageKey)
		if err != nil {
			return memoryops.ForgetResult{}, "", err
		}
		if previous == "" {
			return memoryops.ForgetResult{Key: key}, "", nil
		}
		if err := cfg.DeleteConfig(ctx, storageKey); err != nil {
			return memoryops.ForgetResult{}, "", err
		}
		return memoryops.ForgetResult{Key: key, Value: previous, Found: true}, "bd: forget " + key, nil
	})
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	return result, nil
}

func (m *memories) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	return RunTxRead(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) (memoryops.ListResult, error) {
		all, err := uw.ConfigUseCase().GetAllConfig(ctx)
		if err != nil {
			return memoryops.ListResult{}, err
		}
		plane := storagememoryops.MemoriesFromConfig(all)
		return memoryops.ListResult{Memories: memoryapi.FilterMemories(plane, req.Search)}, nil
	})
}
