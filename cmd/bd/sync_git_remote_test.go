//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// resetConfigForRemoteTest clears viper config state without loading
// the real config.yaml. This prevents sync.mode from config.yaml
// (e.g., dolt-native in production workspace) from overriding test values.
func resetConfigForRemoteTest(t *testing.T) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// NOTE: TestDoExportSync_*, TestDoPullFirstSync_*, and TestRemoteStorageInterfaceCheck
// were removed because they depended on the memory backend's mockRemoteStore pattern
// for Push/Pull error injection. With the dolt-native migration (Phase 7), the global
// store is *dolt.DoltStore which doesn't support mock injection. Sync mode functionality
// is tested through integration tests with real dolt remotes.

// TestShouldUseDoltRemote_ModeSelection verifies the mode -> Dolt remote mapping
// using a real dolt store.
func TestShouldUseDoltRemote_ModeSelection(t *testing.T) {
	ctx := context.Background()
	resetConfigForRemoteTest(t)

	tests := []struct {
		mode    string
		wantUse bool
	}{
		{SyncModeGitPortable, false},
		{SyncModeRealtime, false},
		{SyncModeDoltNative, true},
		{SyncModeBeltAndSuspenders, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			testStore := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
			if err := testStore.SetConfig(ctx, SyncModeConfigKey, tt.mode); err != nil {
				t.Fatalf("set mode: %v", err)
			}

			got := ShouldUseDoltRemote(ctx, testStore)
			if got != tt.wantUse {
				t.Errorf("ShouldUseDoltRemote() = %v, want %v", got, tt.wantUse)
			}
		})
	}
}

// TestShouldExportJSONL_DoltNative_False verifies that JSONL export is disabled
// in dolt-native mode using a real dolt store.
func TestShouldExportJSONL_DoltNative_False(t *testing.T) {
	ctx := context.Background()
	resetConfigForRemoteTest(t)

	tests := []struct {
		mode       string
		wantExport bool
	}{
		{SyncModeGitPortable, true},
		{SyncModeRealtime, true},
		{SyncModeDoltNative, false},
		{SyncModeBeltAndSuspenders, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			testStore := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
			if err := testStore.SetConfig(ctx, SyncModeConfigKey, tt.mode); err != nil {
				t.Fatalf("set mode: %v", err)
			}

			got := ShouldExportJSONL(ctx, testStore)
			if got != tt.wantExport {
				t.Errorf("ShouldExportJSONL() = %v, want %v", got, tt.wantExport)
			}
		})
	}
}
