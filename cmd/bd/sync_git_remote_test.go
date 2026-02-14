//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// resetConfigForRemoteTest clears viper config state without loading
// the real config.yaml. This prevents sync.mode from config.yaml
// (e.g., dolt-native in production workspace) from overriding test values.
func resetConfigForRemoteTest(t *testing.T) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// mockRemoteStore wraps a MemoryStorage to implement RemoteStorage.
// Tracks Push/Pull/Commit calls for verification.
type mockRemoteStore struct {
	*memory.MemoryStorage

	pushCount  atomic.Int32
	pullCount  atomic.Int32
	commitMsgs []string

	pushErr error // inject Push error
	pullErr error // inject Pull error
}

// Compile-time check: mockRemoteStore implements RemoteStorage.
var _ storage.RemoteStorage = (*mockRemoteStore)(nil)

func newMockRemoteStore() *mockRemoteStore {
	return &mockRemoteStore{
		MemoryStorage: memory.New(""),
	}
}

func (m *mockRemoteStore) Push(_ context.Context) error {
	m.pushCount.Add(1)
	return m.pushErr
}

func (m *mockRemoteStore) Pull(_ context.Context) error {
	m.pullCount.Add(1)
	return m.pullErr
}

func (m *mockRemoteStore) AddRemote(_ context.Context, _, _ string) error {
	return nil
}

// VersionedStorage stubs — dolt-native sync only needs Push/Pull/Commit.

func (m *mockRemoteStore) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return nil, nil
}

func (m *mockRemoteStore) AsOf(_ context.Context, _ string, _ string) (*types.Issue, error) {
	return nil, nil
}

func (m *mockRemoteStore) Diff(_ context.Context, _, _ string) ([]*storage.DiffEntry, error) {
	return nil, nil
}

func (m *mockRemoteStore) Branch(_ context.Context, _ string) error {
	return nil
}

func (m *mockRemoteStore) Merge(_ context.Context, _ string) ([]storage.Conflict, error) {
	return nil, nil
}

func (m *mockRemoteStore) CurrentBranch(_ context.Context) (string, error) {
	return "main", nil
}

func (m *mockRemoteStore) ListBranches(_ context.Context) ([]string, error) {
	return []string{"main"}, nil
}

func (m *mockRemoteStore) Commit(_ context.Context, msg string) error {
	m.commitMsgs = append(m.commitMsgs, msg)
	return nil
}

func (m *mockRemoteStore) GetCurrentCommit(_ context.Context) (string, error) {
	return "abc123", nil
}

func (m *mockRemoteStore) GetConflicts(_ context.Context) ([]storage.Conflict, error) {
	return nil, nil
}

func (m *mockRemoteStore) ResolveConflicts(_ context.Context, _ string, _ string) error {
	return nil
}

// TestDoExportSync_DoltNative_CallsPush verifies that doExportSync in dolt-native
// mode calls store.Commit then store.Push through the RemoteStorage interface,
// and does NOT write JSONL.
func TestDoExportSync_DoltNative_CallsPush(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create .beads directory with empty JSONL
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	// Save and restore globals
	saveAndRestoreGlobals(t)

	// Create mock remote store and set dolt-native mode
	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := mock.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		t.Fatalf("set sync mode: %v", err)
	}

	// Install mock as global store
	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	// Reset viper to prevent config.yaml sync.mode from overriding DB config
	resetConfigForRemoteTest(t)

	// Verify preconditions
	mode := GetSyncMode(ctx, mock)
	if mode != SyncModeDoltNative {
		t.Fatalf("sync mode = %q, want %q", mode, SyncModeDoltNative)
	}
	if !ShouldUseDoltRemote(ctx, mock) {
		t.Fatal("ShouldUseDoltRemote should be true for dolt-native")
	}
	if ShouldExportJSONL(ctx, mock) {
		t.Fatal("ShouldExportJSONL should be false for dolt-native")
	}

	// Run doExportSync
	if err := doExportSync(ctx, jsonlPath, false, false); err != nil {
		t.Fatalf("doExportSync failed: %v", err)
	}

	// Verify Push was called
	if mock.pushCount.Load() != 1 {
		t.Errorf("Push called %d times, want 1", mock.pushCount.Load())
	}

	// Verify Commit was called (doExportSync commits before push)
	if len(mock.commitMsgs) != 1 {
		t.Errorf("Commit called %d times, want 1", len(mock.commitMsgs))
	}

	// Verify JSONL was NOT written (should still be empty)
	content, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	if len(content) > 0 {
		t.Errorf("JSONL should be empty in dolt-native mode, got %d bytes", len(content))
	}
}

// TestDoExportSync_BeltAndSuspenders_DoesBoth verifies that belt-and-suspenders
// mode calls both Dolt Push AND exports JSONL.
func TestDoExportSync_BeltAndSuspenders_DoesBoth(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	saveAndRestoreGlobals(t)

	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := mock.SetConfig(ctx, SyncModeConfigKey, SyncModeBeltAndSuspenders); err != nil {
		t.Fatalf("set sync mode: %v", err)
	}

	// Create an issue so JSONL export has something to write
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Belt Test",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := mock.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	resetConfigForRemoteTest(t)

	// Verify preconditions
	if !ShouldUseDoltRemote(ctx, mock) {
		t.Fatal("ShouldUseDoltRemote should be true for belt-and-suspenders")
	}
	if !ShouldExportJSONL(ctx, mock) {
		t.Fatal("ShouldExportJSONL should be true for belt-and-suspenders")
	}

	if err := doExportSync(ctx, jsonlPath, false, false); err != nil {
		t.Fatalf("doExportSync failed: %v", err)
	}

	// Verify Push was called
	if mock.pushCount.Load() != 1 {
		t.Errorf("Push called %d times, want 1", mock.pushCount.Load())
	}

	// Verify JSONL was also written (belt-and-suspenders does both)
	content, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	if len(content) == 0 {
		t.Error("JSONL should be non-empty in belt-and-suspenders mode")
	}
	if !strings.Contains(string(content), "test-1") {
		t.Error("JSONL should contain exported issue test-1")
	}
}

// TestDoExportSync_GitPortable_NoPush verifies that git-portable mode does NOT
// call Dolt Push (it only writes JSONL).
func TestDoExportSync_GitPortable_NoPush(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	saveAndRestoreGlobals(t)

	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	// Default mode is git-portable (no sync.mode config set)

	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	resetConfigForRemoteTest(t)

	// Verify preconditions
	if ShouldUseDoltRemote(ctx, mock) {
		t.Fatal("ShouldUseDoltRemote should be false for git-portable")
	}

	if err := doExportSync(ctx, jsonlPath, false, false); err != nil {
		t.Fatalf("doExportSync failed: %v", err)
	}

	// Verify Push was NOT called
	if mock.pushCount.Load() != 0 {
		t.Errorf("Push called %d times, want 0 for git-portable", mock.pushCount.Load())
	}
}

// TestDoPullFirstSync_DoltNative_CallsPull verifies that doPullFirstSync in
// dolt-native mode calls store.Pull and then returns early (no JSONL merge).
func TestDoPullFirstSync_DoltNative_CallsPull(t *testing.T) {
	ctx := context.Background()

	// Setup git repo (doPullFirstSync checks git state)
	tmpDir, cleanup := setupGitRepo(t)
	defer cleanup()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	// Commit .beads so git status is clean
	_ = exec.Command("git", "add", ".beads").Run()
	_ = exec.Command("git", "commit", "-m", "add beads dir").Run()

	saveAndRestoreGlobals(t)

	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := mock.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		t.Fatalf("set sync mode: %v", err)
	}

	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	resetConfigForRemoteTest(t)

	sbc := &SyncBranchContext{} // no sync branch configured

	// Run pull-first sync
	err := doPullFirstSync(ctx, jsonlPath, false, false, false, true, false, "", false, sbc)
	if err != nil {
		t.Fatalf("doPullFirstSync failed: %v", err)
	}

	// Verify Pull was called
	if mock.pullCount.Load() != 1 {
		t.Errorf("Pull called %d times, want 1", mock.pullCount.Load())
	}

	// Verify JSONL was NOT modified (dolt-native returns early after pull)
	content, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	if len(content) > 0 {
		t.Errorf("JSONL should remain empty in dolt-native mode, got %d bytes", len(content))
	}
}

// TestDoPullFirstSync_DoltNative_PullError verifies that dolt-native mode
// propagates Pull errors (except "no remote" warnings).
func TestDoPullFirstSync_DoltNative_PullError(t *testing.T) {
	ctx := context.Background()

	tmpDir, cleanup := setupGitRepo(t)
	defer cleanup()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	_ = exec.Command("git", "add", ".beads").Run()
	_ = exec.Command("git", "commit", "-m", "add beads dir").Run()

	saveAndRestoreGlobals(t)

	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := mock.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		t.Fatalf("set sync mode: %v", err)
	}

	// Inject a non-remote error
	mock.pullErr = os.ErrPermission

	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	resetConfigForRemoteTest(t)

	sbc := &SyncBranchContext{}

	err := doPullFirstSync(ctx, jsonlPath, false, false, false, true, false, "", false, sbc)
	if err == nil {
		t.Fatal("expected error from Pull, got nil")
	}
	if !strings.Contains(err.Error(), "dolt pull failed") {
		t.Errorf("expected 'dolt pull failed' error, got: %v", err)
	}
}

// TestDoExportSync_DoltNative_NoRemote verifies that dolt-native mode handles
// "no remote configured" gracefully (warns but doesn't fail).
func TestDoExportSync_DoltNative_NoRemote(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	saveAndRestoreGlobals(t)

	mock := newMockRemoteStore()
	if err := mock.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := mock.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		t.Fatalf("set sync mode: %v", err)
	}

	// Inject "no remote" error — doExportSync checks if error contains "remote"
	// to decide whether to warn vs fail.
	mock.pushErr = &remoteNotConfiguredError{}

	store = mock
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	resetConfigForRemoteTest(t)

	err := doExportSync(ctx, jsonlPath, false, false)
	if err != nil {
		t.Fatalf("doExportSync should not fail for missing remote, got: %v", err)
	}

	// Push was attempted
	if mock.pushCount.Load() != 1 {
		t.Errorf("Push called %d times, want 1", mock.pushCount.Load())
	}
}

// remoteNotConfiguredError is a test error whose message contains "remote"
// to trigger the graceful handling in doExportSync.
type remoteNotConfiguredError struct{}

func (e *remoteNotConfiguredError) Error() string {
	return "no remote configured"
}

// TestShouldUseDoltRemote_ModeSelection verifies the mode → Dolt remote mapping
// using the in-memory mock (no Dolt required).
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
			mock := newMockRemoteStore()
			if err := mock.SetConfig(ctx, SyncModeConfigKey, tt.mode); err != nil {
				t.Fatalf("set mode: %v", err)
			}

			got := ShouldUseDoltRemote(ctx, mock)
			if got != tt.wantUse {
				t.Errorf("ShouldUseDoltRemote() = %v, want %v", got, tt.wantUse)
			}
		})
	}
}

// TestShouldExportJSONL_DoltNative_False verifies that JSONL export is disabled
// in dolt-native mode using the mock (no Dolt required).
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
			mock := newMockRemoteStore()
			if err := mock.SetConfig(ctx, SyncModeConfigKey, tt.mode); err != nil {
				t.Fatalf("set mode: %v", err)
			}

			got := ShouldExportJSONL(ctx, mock)
			if got != tt.wantExport {
				t.Errorf("ShouldExportJSONL() = %v, want %v", got, tt.wantExport)
			}
		})
	}
}

// TestRemoteStorageInterfaceCheck verifies that storage.AsRemote correctly
// detects RemoteStorage implementations.
func TestRemoteStorageInterfaceCheck(t *testing.T) {
	// mockRemoteStore should implement RemoteStorage
	mock := newMockRemoteStore()
	if !storage.IsRemote(mock) {
		t.Error("mockRemoteStore should implement RemoteStorage")
	}
	rs, ok := storage.AsRemote(mock)
	if !ok || rs == nil {
		t.Error("AsRemote should succeed for mockRemoteStore")
	}

	// Plain MemoryStorage should NOT implement RemoteStorage
	mem := memory.New("")
	if storage.IsRemote(mem) {
		t.Error("MemoryStorage should NOT implement RemoteStorage")
	}
	_, ok = storage.AsRemote(mem)
	if ok {
		t.Error("AsRemote should fail for MemoryStorage")
	}
}
