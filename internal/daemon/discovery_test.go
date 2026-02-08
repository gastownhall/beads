//go:build integration
// +build integration

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestDiscoverDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	workspace := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(workspace, 0755)

	// Start daemon
	dbPath := filepath.Join(workspace, "test.db")
	socketPath := filepath.Join(workspace, "bd.sock")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := rpc.NewServer(socketPath, store, tmpDir, dbPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	<-server.WaitReady()
	defer server.Stop()

	// Test discoverDaemon directly
	daemon := discoverDaemon(socketPath)
	if !daemon.Alive {
		t.Errorf("daemon not alive: %s", daemon.Error)
	}
	if daemon.PID != os.Getpid() {
		t.Errorf("wrong PID: expected %d, got %d", os.Getpid(), daemon.PID)
	}
	if daemon.UptimeSeconds <= 0 {
		t.Errorf("invalid uptime: %f", daemon.UptimeSeconds)
	}
	if daemon.WorkspacePath != tmpDir {
		t.Errorf("wrong workspace: expected %s, got %s", tmpDir, daemon.WorkspacePath)
	}
}

func TestFindDaemonByWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	workspace := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(workspace, 0755)

	// Start daemon
	dbPath := filepath.Join(workspace, "test.db")
	socketPath := filepath.Join(workspace, "bd.sock")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := rpc.NewServer(socketPath, store, tmpDir, dbPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	<-server.WaitReady()
	defer server.Stop()

	// Find daemon by workspace
	daemon, err := FindDaemonByWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("failed to find daemon: %v", err)
	}
	if daemon == nil {
		t.Fatal("daemon not found")
	}
	if !daemon.Alive {
		t.Errorf("daemon not alive: %s", daemon.Error)
	}
	if daemon.WorkspacePath != tmpDir {
		t.Errorf("wrong workspace: expected %s, got %s", tmpDir, daemon.WorkspacePath)
	}
}

func TestCleanupStaleSockets(t *testing.T) {
	tmpDir := t.TempDir()

	// Create stale socket file
	stalePath := filepath.Join(tmpDir, "stale.sock")
	if err := os.WriteFile(stalePath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create stale socket: %v", err)
	}

	daemons := []DaemonInfo{
		{
			SocketPath: stalePath,
			Alive:      false,
		},
	}

	cleaned, err := CleanupStaleSockets(daemons)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}

	// Verify socket was removed
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale socket still exists")
	}
}

func TestWalkWithDepth(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test directory structure
	// tmpDir/
	//   file1.txt
	//   dir1/
	//     file2.txt
	//     dir2/
	//       file3.txt
	//       dir3/
	//         file4.txt

	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "dir1", "dir2", "dir3"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "dir1", "file2.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "dir1", "dir2", "file3.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "dir1", "dir2", "dir3", "file4.txt"), []byte("test"), 0644)

	tests := []struct {
		name      string
		maxDepth  int
		wantFiles int
	}{
		{"depth 0", 0, 1},   // Only file1.txt
		{"depth 1", 1, 2},   // file1.txt, file2.txt
		{"depth 2", 2, 3},   // file1.txt, file2.txt, file3.txt
		{"depth 3", 3, 4},   // All files
		{"depth 10", 10, 4}, // All files (max depth not reached)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var foundFiles []string
			fn := func(path string, info os.FileInfo) error {
				if !info.IsDir() {
					foundFiles = append(foundFiles, path)
				}
				return nil
			}

			err := walkWithDepth(tmpDir, 0, tt.maxDepth, fn)
			if err != nil {
				t.Fatalf("walkWithDepth failed: %v", err)
			}

			if len(foundFiles) != tt.wantFiles {
				t.Errorf("Expected %d files, got %d: %v", tt.wantFiles, len(foundFiles), foundFiles)
			}
		})
	}
}

func TestWalkWithDepth_SkipsHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create hidden directories (should skip)
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".hidden"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "vendor"), 0755)

	// Create .beads directory (should NOT skip)
	os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755)

	// Add files
	os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".hidden", "secret"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "package.json"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".beads", "beads.db"), []byte("test"), 0644)

	var foundFiles []string
	fn := func(path string, info os.FileInfo) error {
		if !info.IsDir() {
			foundFiles = append(foundFiles, filepath.Base(path))
		}
		return nil
	}

	err := walkWithDepth(tmpDir, 0, 5, fn)
	if err != nil {
		t.Fatalf("walkWithDepth failed: %v", err)
	}

	// Should only find beads.db from .beads directory
	if len(foundFiles) != 1 || foundFiles[0] != "beads.db" {
		t.Errorf("Expected only beads.db, got: %v", foundFiles)
	}
}

func TestDiscoverDaemons_Registry(t *testing.T) {
	// Test registry-based discovery (no search roots)
	daemons, err := DiscoverDaemons(nil)
	if err != nil {
		t.Fatalf("DiscoverDaemons failed: %v", err)
	}

	// Should return empty list (no daemons running in test environment)
	// Just verify it doesn't error
	_ = daemons
}

func TestDiscoverDaemons_Legacy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow daemon discovery test in short mode")
	}
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Start a test daemon
	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	server := rpc.NewServer(socketPath, store, tmpDir, dbPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	<-server.WaitReady()
	defer server.Stop()

	// Test legacy discovery with explicit search roots
	daemons, err := DiscoverDaemons([]string{tmpDir})
	if err != nil {
		t.Fatalf("DiscoverDaemons failed: %v", err)
	}

	if len(daemons) != 1 {
		t.Fatalf("Expected 1 daemon, got %d", len(daemons))
	}

	daemon := daemons[0]
	if !daemon.Alive {
		t.Errorf("Daemon not alive: %s", daemon.Error)
	}
	if daemon.WorkspacePath != tmpDir {
		t.Errorf("Wrong workspace path: expected %s, got %s", tmpDir, daemon.WorkspacePath)
	}
}

func TestCheckDaemonErrorFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Test 1: No error file exists
	errMsg := checkDaemonErrorFile(socketPath)
	if errMsg != "" {
		t.Errorf("Expected empty error message, got: %s", errMsg)
	}

	// Test 2: Error file exists with content
	errorFilePath := filepath.Join(beadsDir, "daemon-error")
	expectedError := "failed to start: database locked"
	os.WriteFile(errorFilePath, []byte(expectedError), 0644)

	errMsg = checkDaemonErrorFile(socketPath)
	if errMsg != expectedError {
		t.Errorf("Expected error message %q, got %q", expectedError, errMsg)
	}
}

func TestStopDaemon_NotAlive(t *testing.T) {
	daemon := DaemonInfo{
		Alive: false,
	}

	err := StopDaemon(daemon)
	if err == nil {
		t.Error("Expected error when stopping non-alive daemon")
	}
	if err.Error() != "daemon is not running" {
		t.Errorf("Unexpected error message: %s", err.Error())
	}
}

func TestKillAllDaemons_Empty(t *testing.T) {
	results := KillAllDaemons([]DaemonInfo{}, false)
	if results.Stopped != 0 || results.Failed != 0 {
		t.Errorf("Expected 0 stopped and 0 failed, got %d stopped and %d failed", results.Stopped, results.Failed)
	}
	if len(results.Failures) != 0 {
		t.Errorf("Expected empty failures list, got %d failures", len(results.Failures))
	}
}

func TestKillAllDaemons_NotAlive(t *testing.T) {
	daemons := []DaemonInfo{
		{Alive: false, WorkspacePath: "/test", PID: 12345},
	}

	results := KillAllDaemons(daemons, false)
	if results.Stopped != 0 || results.Failed != 0 {
		t.Errorf("Expected 0 stopped and 0 failed for dead daemon, got %d stopped and %d failed", results.Stopped, results.Failed)
	}
}

func TestFindDaemonByWorkspace_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	// Try to find daemon in directory without any daemon
	daemon, err := FindDaemonByWorkspace(tmpDir)
	if err == nil {
		t.Error("Expected error when daemon not found")
	}
	if daemon != nil {
		t.Error("Expected nil daemon when not found")
	}
}

func TestDiscoverDaemon_SocketMissing(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	// Try to discover daemon on non-existent socket
	daemon := discoverDaemon(socketPath)
	if daemon.Alive {
		t.Error("Expected daemon to not be alive for missing socket")
	}
	if daemon.SocketPath != socketPath {
		t.Errorf("Expected socket path %s, got %s", socketPath, daemon.SocketPath)
	}
	if daemon.Error == "" {
		t.Error("Expected error message when daemon not found")
	}
}

func TestCleanupStaleSockets_AlreadyRemoved(t *testing.T) {
	tmpDir := t.TempDir()

	// Create stale daemon with non-existent socket
	stalePath := filepath.Join(tmpDir, "nonexistent.sock")

	daemons := []DaemonInfo{
		{
			SocketPath: stalePath,
			Alive:      false,
		},
	}

	// Should succeed even if socket doesn't exist
	cleaned, err := CleanupStaleSockets(daemons)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (socket didn't exist), got %d", cleaned)
	}
}

func TestCleanupStaleSockets_AliveDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "alive.sock")

	daemons := []DaemonInfo{
		{
			SocketPath: socketPath,
			Alive:      true,
		},
	}

	// Should not remove socket for alive daemon
	cleaned, err := CleanupStaleSockets(daemons)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (daemon alive), got %d", cleaned)
	}
}
