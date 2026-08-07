package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// WorkspaceConfigSource is the capability accessor a unit-of-work provider
// offers for the workspace settings role, the sibling of IssueReaderSource and
// CounterSource.
type WorkspaceConfigSource interface {
	WorkspaceConfig() (publicops.WorkspaceConfig, error)
}

// workspaceConfig answers settings queries and writes through a unit of work.
type workspaceConfig struct {
	provider UnitOfWorkProvider
}

// WorkspaceConfig returns the guarded workspace-settings surface for this
// provider.
func (p *doltSQLProvider) WorkspaceConfig() (publicops.WorkspaceConfig, error) {
	return NewWorkspaceConfig(p)
}

// NewWorkspaceConfig constructs a public workspace-settings surface backed by
// provider.
func NewWorkspaceConfig(provider UnitOfWorkProvider) (publicops.WorkspaceConfig, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new workspace config: unit-of-work provider must not be nil")
	}
	return &workspaceConfig{provider: provider}, nil
}

var _ publicops.WorkspaceConfig = (*workspaceConfig)(nil)

func (c *workspaceConfig) GetSetting(ctx context.Context, req publicops.GetSettingRequest) (publicops.SettingResult, error) {
	key, err := workapi.ValidateSettingKey(req.Key)
	if err != nil {
		return publicops.SettingResult{}, err
	}
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.SettingResult, error) {
		value, err := uw.ConfigUseCase().GetConfig(ctx, key)
		if err != nil {
			return publicops.SettingResult{}, err
		}
		return publicops.SettingResult{Key: key, Value: value}, nil
	})
}

func (c *workspaceConfig) ListSettings(ctx context.Context, _ publicops.ListSettingsRequest) (publicops.ListSettingsResult, error) {
	return RunTxRead(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ListSettingsResult, error) {
		stored, err := uw.ConfigUseCase().GetAllConfig(ctx)
		if err != nil {
			return publicops.ListSettingsResult{}, err
		}
		// Same filter as the store-backed body, for the same reason: the config
		// table holds the KV plane too, and what the settings enumeration may
		// carry is one decision, not one per implementation.
		return publicops.ListSettingsResult{Settings: workapi.FilterSettingsEnumeration(stored)}, nil
	})
}

// Set writes one setting inside one unit of work.
//
// The projection of status.custom and types.custom into their normalized
// tables happens in the config repository this use case sits on
// (internal/storage/domain/db/config.go), so the row and its table land in
// THIS transaction and commit together. Before this role existed a proxied
// `bd config set types.custom` wrote the string, left custom_types holding the
// previous set, and the table-first read kept answering with it forever.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK IS OPENED: a validation failure
// raised inside RunTx would be indistinguishable at the call site from a write
// that rolled back.
func (c *workspaceConfig) SetSetting(ctx context.Context, req publicops.SetSettingRequest) (publicops.SetSettingResult, error) {
	value, err := workapi.ValidateSettingWrite(req.Key, req.Value)
	if err != nil {
		return publicops.SetSettingResult{}, err
	}
	if err := RunTx(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: config set " + req.Key, uw.ConfigUseCase().SetConfig(ctx, req.Key, value)
	}); err != nil {
		return publicops.SetSettingResult{}, err
	}
	return publicops.SetSettingResult{Key: req.Key, Value: value}, nil
}

func (c *workspaceConfig) UnsetSetting(ctx context.Context, req publicops.UnsetSettingRequest) (publicops.UnsetSettingResult, error) {
	key, err := workapi.ValidateSettingKey(req.Key)
	if err != nil {
		return publicops.UnsetSettingResult{}, err
	}
	if err := RunTx(ctx, c.provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: config unset " + key, uw.ConfigUseCase().DeleteConfig(ctx, key)
	}); err != nil {
		return publicops.UnsetSettingResult{}, err
	}
	return publicops.UnsetSettingResult{Key: key}, nil
}
