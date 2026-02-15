//go:build cgo

package syncbranch

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

func TestGetStoredRemoteSHA(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	// Test getting SHA when not set
	sha, err := getStoredRemoteSHA(ctx, store)
	if err != nil {
		t.Fatalf("getStoredRemoteSHA() error = %v", err)
	}
	if sha != "" {
		t.Errorf("getStoredRemoteSHA() = %q, want empty string", sha)
	}

	// Set a SHA
	testSHA := "abc123def456"
	if err := store.SetConfig(ctx, RemoteSHAConfigKey, testSHA); err != nil {
		t.Fatalf("SetConfig() error = %v", err)
	}

	// Test getting SHA when set
	sha, err = getStoredRemoteSHA(ctx, store)
	if err != nil {
		t.Fatalf("getStoredRemoteSHA() error = %v", err)
	}
	if sha != testSHA {
		t.Errorf("getStoredRemoteSHA() = %q, want %q", sha, testSHA)
	}
}

func TestClearStoredRemoteSHA(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	// Set a SHA first
	testSHA := "abc123def456"
	if err := store.SetConfig(ctx, RemoteSHAConfigKey, testSHA); err != nil {
		t.Fatalf("SetConfig() error = %v", err)
	}

	// Clear it
	if err := clearStoredRemoteSHA(ctx, store); err != nil {
		t.Fatalf("clearStoredRemoteSHA() error = %v", err)
	}

	// Verify it's gone
	sha, err := getStoredRemoteSHA(ctx, store)
	if err != nil {
		t.Fatalf("getStoredRemoteSHA() error = %v", err)
	}
	if sha != "" {
		t.Errorf("SHA should be empty after clear, got %q", sha)
	}
}

func TestForcePushStatus(t *testing.T) {
	// Test forcePushStatus struct
	status := &forcePushStatus{
		Detected:         true,
		StoredSHA:        "abc123",
		CurrentRemoteSHA: "def456",
		Message:          "Force push detected",
		Branch:           "beads-sync",
		Remote:           "origin",
	}

	if !status.Detected {
		t.Error("Expected Detected to be true")
	}
	if status.StoredSHA != "abc123" {
		t.Errorf("StoredSHA = %q, want 'abc123'", status.StoredSHA)
	}
	if status.CurrentRemoteSHA != "def456" {
		t.Errorf("CurrentRemoteSHA = %q, want 'def456'", status.CurrentRemoteSHA)
	}
}

// newTestStoreIntegrity creates a test store for integrity tests
// Note: This is a duplicate of newTestStore from syncbranch_test.go
// but we need it here since tests are in the same package
func newTestStoreIntegrity(t *testing.T) *dolt.DoltStore {
	t.Helper()
	store, err := dolt.New(context.Background(), &dolt.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	return store
}

func TestCheckForcePush_NoStoredSHA(t *testing.T) {
	ctx := context.Background()
	store := newTestStoreIntegrity(t)
	defer store.Close()

	// When no stored SHA exists, checkForcePush should return "first sync" status
	// Note: We can't fully test this without a git repo, but we can test the early return
	status, err := checkForcePush(ctx, store, "/nonexistent", "beads-sync")
	if err != nil {
		t.Fatalf("checkForcePush() error = %v", err)
	}
	if status.Detected {
		t.Error("Expected Detected to be false when no stored SHA")
	}
	if status.StoredSHA != "" {
		t.Errorf("StoredSHA = %q, want empty", status.StoredSHA)
	}
	if status.Message != "No previous sync recorded (first sync)" {
		t.Errorf("Message = %q, want 'No previous sync recorded (first sync)'", status.Message)
	}
}

func TestRemoteSHAConfigKey(t *testing.T) {
	// Verify the config key is what we expect
	if RemoteSHAConfigKey != "sync.remote_sha" {
		t.Errorf("RemoteSHAConfigKey = %q, want 'sync.remote_sha'", RemoteSHAConfigKey)
	}
}
