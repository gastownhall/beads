package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// WorkspaceConfig returns the inner store's workspace-settings surface wrapped
// in this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
func (s *InstrumentedStorage) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	inner, err := s.Unwrap().WorkspaceConfig()
	if err != nil {
		return nil, err
	}
	return s.WrapWorkspaceConfig(inner), nil
}

// WrapWorkspaceConfig instruments guarded workspace-settings access with this
// storage layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapWorkspaceConfig(inner issueops.WorkspaceConfig) issueops.WorkspaceConfig {
	return &instrumentedWorkspaceConfig{storage: s, inner: inner}
}

type instrumentedWorkspaceConfig struct {
	storage *InstrumentedStorage
	inner   issueops.WorkspaceConfig
}

func (c *instrumentedWorkspaceConfig) GetSetting(ctx context.Context, request issueops.GetSettingRequest) (result issueops.SettingResult, err error) {
	ctx, span, started := c.storage.op(ctx, "WorkspaceConfig.GetSetting")
	result, err = c.inner.GetSetting(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}

func (c *instrumentedWorkspaceConfig) ListSettings(ctx context.Context, request issueops.ListSettingsRequest) (result issueops.ListSettingsResult, err error) {
	ctx, span, started := c.storage.op(ctx, "WorkspaceConfig.ListSettings")
	result, err = c.inner.ListSettings(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}

func (c *instrumentedWorkspaceConfig) SetSetting(ctx context.Context, request issueops.SetSettingRequest) (result issueops.SetSettingResult, err error) {
	ctx, span, started := c.storage.op(ctx, "WorkspaceConfig.SetSetting")
	result, err = c.inner.SetSetting(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}

func (c *instrumentedWorkspaceConfig) UnsetSetting(ctx context.Context, request issueops.UnsetSettingRequest) (result issueops.UnsetSettingResult, err error) {
	ctx, span, started := c.storage.op(ctx, "WorkspaceConfig.UnsetSetting")
	result, err = c.inner.UnsetSetting(ctx, request)
	c.storage.done(ctx, span, started, err)
	return result, err
}
