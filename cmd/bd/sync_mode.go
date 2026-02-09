package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// Sync mode constants - re-exported from internal/config for backward compatibility.
// These are used with storage.Storage (database) while config.SyncMode* are used
// with viper (config.yaml).
const (
	// SyncModeGitPortable exports to JSONL on push, imports on pull.
	// This is the default mode - works with standard git workflows.
	SyncModeGitPortable = string(config.SyncModeGitPortable)

	// SyncModeRealtime exports to JSONL on every database mutation.
	// Provides immediate persistence but more git noise.
	SyncModeRealtime = string(config.SyncModeRealtime)

	// SyncModeDoltNative uses Dolt remotes for sync, no JSONL writes.
	// Requires Dolt backend and configured Dolt remote.
	SyncModeDoltNative = string(config.SyncModeDoltNative)

	// SyncModeBeltAndSuspenders uses both Dolt remotes AND JSONL.
	// Maximum redundancy - Dolt for versioning, JSONL for git portability.
	SyncModeBeltAndSuspenders = string(config.SyncModeBeltAndSuspenders)

	// SyncModeConfigKey is the database config key for sync mode.
	SyncModeConfigKey = "sync.mode"

	// SyncExportOnConfigKey controls when JSONL export happens.
	SyncExportOnConfigKey = "sync.export_on"

	// SyncImportOnConfigKey controls when JSONL import happens.
	SyncImportOnConfigKey = "sync.import_on"
)

// Trigger constants for export_on and import_on settings.
const (
	// TriggerPush triggers on git push (export) or git pull (import).
	TriggerPush = "push"
	TriggerPull = "pull"

	// TriggerChange triggers on every database mutation (realtime mode).
	TriggerChange = "change"
)

// GetSyncMode returns the configured sync mode, checking config.yaml first (where bd config set writes),
// then falling back to database. This fixes GH#1277 where yaml and database were inconsistent.
func GetSyncMode(ctx context.Context, s storage.Storage) string {
	// First check config.yaml (where bd config set writes for sync.* keys)
	yamlMode := config.GetSyncMode()
	if yamlMode != "" && yamlMode != config.SyncModeGitPortable {
		// Non-default value in yaml takes precedence
		return string(yamlMode)
	}

	// Fall back to database (legacy path)
	mode, err := s.GetConfig(ctx, SyncModeConfigKey)
	if err != nil || mode == "" {
		return SyncModeGitPortable
	}

	// Validate mode using the shared validation
	if !config.IsValidSyncMode(mode) {
		return SyncModeGitPortable
	}

	return mode
}

// SetSyncMode sets the sync mode configuration in the database.
func SetSyncMode(ctx context.Context, s storage.Storage, mode string) error {
	// Validate mode using the shared validation
	if !config.IsValidSyncMode(mode) {
		return fmt.Errorf("invalid sync mode: %s (valid: %s)",
			mode, fmt.Sprintf("%v", config.ValidSyncModes()))
	}

	return s.SetConfig(ctx, SyncModeConfigKey, mode)
}

// GetExportTrigger returns when JSONL export should happen.
func GetExportTrigger(ctx context.Context, s storage.Storage) string {
	trigger, err := s.GetConfig(ctx, SyncExportOnConfigKey)
	if err != nil || trigger == "" {
		// Default based on sync mode
		mode := GetSyncMode(ctx, s)
		if mode == SyncModeRealtime {
			return TriggerChange
		}
		return TriggerPush
	}
	return trigger
}

// GetImportTrigger returns when JSONL import should happen.
func GetImportTrigger(ctx context.Context, s storage.Storage) string {
	trigger, err := s.GetConfig(ctx, SyncImportOnConfigKey)
	if err != nil || trigger == "" {
		return TriggerPull
	}
	return trigger
}

// ShouldExportJSONL returns true if the current sync mode uses JSONL export.
// In dolt-native mode, JSONL is not used — all sync is via Dolt remotes.
// Belt-and-suspenders mode uses both Dolt AND JSONL for maximum redundancy.
//
// Uses GetSyncMode which checks config.yaml first, then falls back to the database.
// This ensures that sync.mode set in config.yaml (e.g. during Dolt migration) is
// respected even if not yet propagated to the database via 'bd sync mode set'.
func ShouldExportJSONL(ctx context.Context, s storage.Storage) bool {
	mode := GetSyncMode(ctx, s)
	return mode != SyncModeDoltNative
}

// ShouldImportJSONL returns true if the current sync mode uses JSONL import.
// In dolt-native mode, there is no JSONL to import — all sync is via Dolt remotes.
// Uses GetSyncMode which checks config.yaml first, then falls back to the database.
func ShouldImportJSONL(ctx context.Context, s storage.Storage) bool {
	mode := GetSyncMode(ctx, s)
	return mode != SyncModeDoltNative
}

// ShouldUseDoltRemote returns true if the current sync mode uses Dolt remotes.
func ShouldUseDoltRemote(ctx context.Context, s storage.Storage) bool {
	mode := GetSyncMode(ctx, s)
	return mode == SyncModeDoltNative || mode == SyncModeBeltAndSuspenders
}

// SyncModeDescription returns a human-readable description of the sync mode.
func SyncModeDescription(mode string) string {
	switch mode {
	case SyncModeGitPortable:
		return "JSONL exported on push, imported on pull"
	case SyncModeRealtime:
		return "JSONL exported on every change"
	case SyncModeDoltNative:
		return "Dolt remotes for sync, no JSONL writes"
	case SyncModeBeltAndSuspenders:
		return "Both Dolt remotes and JSONL"
	default:
		return "unknown mode"
	}
}
