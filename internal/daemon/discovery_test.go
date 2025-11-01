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
	store, err := sqlite.New(dbPath)
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
	store, err := sqlite.New(dbPath)
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
		{"depth 0", 0, 1},        // Only file1.txt
		{"depth 1", 1, 2},        // file1.txt, file2.txt
		{"depth 2", 2, 3},        // file1.txt, file2.txt, file3.txt
		{"depth 3", 3, 4},        // All files
		{"depth 10", 10, 4},      // All files (max depth not reached)
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
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Start a test daemon
	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")
	store, err := sqlite.New(dbPath)
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
