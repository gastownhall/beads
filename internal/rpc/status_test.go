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
	socketPath := newTestSocketPath(t)

	store, err := sqlite.New(context.Background(), dbPath)
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

func TestStatusEndpointWithConfig(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store, tmpDir, dbPath)

	// Set config before starting
	server.SetConfig(true, true, false, "10s", "events")

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

	// Verify config fields
	if !status.AutoCommit {
		t.Error("expected AutoCommit to be true")
	}
	if !status.AutoPush {
		t.Error("expected AutoPush to be true")
	}
	if status.LocalMode {
		t.Error("expected LocalMode to be false")
	}
	if status.SyncInterval != "10s" {
		t.Errorf("expected SyncInterval '10s', got '%s'", status.SyncInterval)
	}
	if status.DaemonMode != "events" {
		t.Errorf("expected DaemonMode 'events', got '%s'", status.DaemonMode)
	}
}

func TestStatusEndpointLocalMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store, tmpDir, dbPath)

	// Set config for local mode
	server.SetConfig(false, false, true, "5s", "poll")

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

	// Verify local mode config
	if status.AutoCommit {
		t.Error("expected AutoCommit to be false in local mode")
	}
	if status.AutoPush {
		t.Error("expected AutoPush to be false in local mode")
	}
	if !status.LocalMode {
		t.Error("expected LocalMode to be true")
	}
	if status.SyncInterval != "5s" {
		t.Errorf("expected SyncInterval '5s', got '%s'", status.SyncInterval)
	}
	if status.DaemonMode != "poll" {
		t.Errorf("expected DaemonMode 'poll', got '%s'", status.DaemonMode)
	}
}

func TestStatusEndpointDefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := NewServer(socketPath, store, tmpDir, dbPath)
	// Don't call SetConfig - test default values

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

	// Verify default config (all false/empty when SetConfig not called)
	if status.AutoCommit {
		t.Error("expected AutoCommit to be false by default")
	}
	if status.AutoPush {
		t.Error("expected AutoPush to be false by default")
	}
	if status.LocalMode {
		t.Error("expected LocalMode to be false by default")
	}
	if status.SyncInterval != "" {
		t.Errorf("expected SyncInterval to be empty by default, got '%s'", status.SyncInterval)
	}
	if status.DaemonMode != "" {
		t.Errorf("expected DaemonMode to be empty by default, got '%s'", status.DaemonMode)
	}
}

func TestSetConfigConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)

	store, err := sqlite.New(context.Background(), dbPath)
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

	// Test concurrent SetConfig calls don't race
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			server.SetConfig(n%2 == 0, n%3 == 0, n%4 == 0, "5s", "events")
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify we can still get status (server didn't crash)
	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	status, err := client.Status()
	if err != nil {
		t.Fatalf("status call failed after concurrent SetConfig: %v", err)
	}

	// Just verify the status call succeeded - values will be from last SetConfig
	t.Logf("Final config: AutoCommit=%v, AutoPush=%v, LocalMode=%v",
		status.AutoCommit, status.AutoPush, status.LocalMode)
}
