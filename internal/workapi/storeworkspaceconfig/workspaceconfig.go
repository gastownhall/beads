// Package storeworkspaceconfig holds the store-backed implementation of
// issueops.WorkspaceConfig: one shared body that every store-shaped backend's
// WorkspaceConfig accessor hands back.
//
// It is a package of its own for the reason internal/workapi/storereader and
// internal/workapi/storecounter are — see those packages' docs. Down here the
// only importers are the two Dolt store packages, and the
// cmd-bd-role-constructors depguard rule in .golangci.yml makes a front door
// importing it a lint failure rather than a review comment.
package storeworkspaceconfig

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// New returns the workspace settings surface backed by a store handle.
// *DoltStore and *EmbeddedDoltStore answer identically because the difference
// between them is below storage.DoltStorage, not above it.
func New(store storage.DoltStorage) (issueops.WorkspaceConfig, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storeworkspaceconfig.New", Backend: "nil"}
	}
	return &storeWorkspaceConfig{store: store}, nil
}

type storeWorkspaceConfig struct{ store storage.DoltStorage }

var _ issueops.WorkspaceConfig = (*storeWorkspaceConfig)(nil)

func (c *storeWorkspaceConfig) GetSetting(ctx context.Context, req issueops.GetSettingRequest) (issueops.SettingResult, error) {
	key, err := workapi.ValidateSettingKey(req.Key)
	if err != nil {
		return issueops.SettingResult{}, err
	}
	// The KV plane rides in the same table and is not settings. Both the
	// refusal and the answer it gives are workapi's, shared with the
	// unit-of-work body and with the enumeration filter, so where the plane
	// boundary runs is one decision rather than one per door.
	if refused, ok := workapi.FilterSettingsPointRead(key); ok {
		return refused, nil
	}
	value, err := c.store.GetConfig(ctx, key)
	if err != nil {
		return issueops.SettingResult{}, err
	}
	return issueops.SettingResult{Key: key, Value: value}, nil
}

func (c *storeWorkspaceConfig) ListSettings(ctx context.Context, _ issueops.ListSettingsRequest) (issueops.ListSettingsResult, error) {
	stored, err := c.store.GetAllConfig(ctx)
	if err != nil {
		return issueops.ListSettingsResult{}, err
	}
	// The store hands back the whole config table, which holds the KV plane as
	// well as the settings plane. What may be enumerated is workapi's to say,
	// shared with the unit-of-work body so the two doors cannot drift; it also
	// gives the empty-never-nil map the result promises.
	return issueops.ListSettingsResult{Settings: workapi.FilterSettingsEnumeration(stored)}, nil
}

// Set writes one setting through the store's own config verb.
//
// THE PROJECTION IS THE STORE'S, not this body's: both store backends'
// SetConfig already rewrite custom_statuses / custom_types inside the same
// retryable transaction as the row, and drop the process-local caches those
// tables feed. Re-implementing either here would put the projection outside that
// transaction — a row that landed and a table that did not is precisely the
// state the projection exists to prevent. What this body owns is the REFUSALS.
func (c *storeWorkspaceConfig) SetSetting(ctx context.Context, req issueops.SetSettingRequest) (issueops.SetSettingResult, error) {
	value, err := workapi.ValidateSettingWrite(req.Key, req.Value)
	if err != nil {
		return issueops.SetSettingResult{}, err
	}
	if err := c.store.SetConfig(ctx, req.Key, value); err != nil {
		return issueops.SetSettingResult{}, err
	}
	return issueops.SetSettingResult{Key: req.Key, Value: value}, nil
}

func (c *storeWorkspaceConfig) UnsetSetting(ctx context.Context, req issueops.UnsetSettingRequest) (issueops.UnsetSettingResult, error) {
	key, err := workapi.ValidateSettingKey(req.Key)
	if err != nil {
		return issueops.UnsetSettingResult{}, err
	}
	if err := c.store.DeleteConfig(ctx, key); err != nil {
		return issueops.UnsetSettingResult{}, err
	}
	return issueops.UnsetSettingResult{Key: key}, nil
}
