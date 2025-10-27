package rpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestStatusEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := filepath.Join(tmpDir, "test.sock")

	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()

	<-server.WaitReady()
	defer server.Stop()

	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
	defer client.Close()

	// Test status endpoint
	status, err := client.Status()
	if err != nil {
		t.Fatalf("status call failed: %v", err)
	}

	// Verify response fields
	if status.Version == "" {
		t.Error("expected version to be set")
	}
	if status.WorkspacePath != tmpDir {
		t.Errorf("expected workspace path %s, got %s", tmpDir, status.WorkspacePath)
	}
	if status.DatabasePath != dbPath {
		t.Errorf("expected database path %s, got %s", dbPath, status.DatabasePath)
	}
	if status.SocketPath != socketPath {
		t.Errorf("expected socket path %s, got %s", socketPath, status.SocketPath)
	}
	if status.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), status.PID)
	}
	if status.UptimeSeconds <= 0 {
		t.Error("expected positive uptime")
	}
	if status.LastActivityTime == "" {
		t.Error("expected last activity time to be set")
	}
	if status.ExclusiveLockActive {
		t.Error("expected no exclusive lock in test")
	}

	// Verify last activity time is recent
	lastActivity, err := time.Parse(time.RFC3339, status.LastActivityTime)
	if err != nil {
		t.Errorf("failed to parse last activity time: %v", err)
	}
	if time.Since(lastActivity) > 5*time.Second {
		t.Errorf("last activity time too old: %v", lastActivity)
	}
}
