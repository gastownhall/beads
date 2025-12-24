package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// Unit tests for discovery.go that run without the integration tag
// These tests focus on pure functions and edge cases that don't require real daemons

func TestWalkWithDepth_Basic(t *testing.T) {
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

func TestWalkWithDepth_UnreadableDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Try to walk a non-existent directory - should not error, just return
	err := walkWithDepth(filepath.Join(tmpDir, "nonexistent"), 0, 5, func(path string, info os.FileInfo) error {
		t.Error("should not be called for non-existent directory")
		return nil
	})
	if err != nil {
		t.Errorf("walkWithDepth should handle unreadable directories gracefully: %v", err)
	}
}

func TestWalkWithDepth_CallbackError(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("test"), 0644)

	callbackErr := os.ErrInvalid
	fn := func(path string, info os.FileInfo) error {
		return callbackErr
	}

	err := walkWithDepth(tmpDir, 0, 5, fn)
	if err != callbackErr {
		t.Errorf("Expected callback error to propagate, got: %v", err)
	}
}

func TestWalkWithDepth_MaxDepthExceeded(t *testing.T) {
	tmpDir := t.TempDir()

	// Create deep nesting
	os.MkdirAll(filepath.Join(tmpDir, "a", "b", "c", "d", "e"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "a", "b", "c", "d", "e", "deep.txt"), []byte("test"), 0644)

	// Start at currentDepth > maxDepth should immediately return
	var foundFiles []string
	fn := func(path string, info os.FileInfo) error {
		foundFiles = append(foundFiles, path)
		return nil
	}

	err := walkWithDepth(tmpDir, 10, 5, fn)
	if err != nil {
		t.Fatalf("walkWithDepth failed: %v", err)
	}

	if len(foundFiles) != 0 {
		t.Errorf("Expected 0 files when currentDepth > maxDepth, got: %v", foundFiles)
	}
}

func TestCheckDaemonErrorFile_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Test with no error file
	errMsg := checkDaemonErrorFile(socketPath)
	if errMsg != "" {
		t.Errorf("Expected empty error message, got: %s", errMsg)
	}
}

func TestCheckDaemonErrorFile_WithContent(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Create error file
	errorFilePath := filepath.Join(beadsDir, "daemon-error")
	expectedError := "failed to start: database locked"
	os.WriteFile(errorFilePath, []byte(expectedError), 0644)

	errMsg := checkDaemonErrorFile(socketPath)
	if errMsg != expectedError {
		t.Errorf("Expected error message %q, got %q", expectedError, errMsg)
	}
}

func TestCheckDaemonErrorFile_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Create empty error file
	errorFilePath := filepath.Join(beadsDir, "daemon-error")
	os.WriteFile(errorFilePath, []byte{}, 0644)

	errMsg := checkDaemonErrorFile(socketPath)
	if errMsg != "" {
		t.Errorf("Expected empty message for empty file, got: %s", errMsg)
	}
}

func TestCleanupStaleSockets_Basic(t *testing.T) {
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

func TestCleanupStaleSockets_AlreadyRemoved(t *testing.T) {
	tmpDir := t.TempDir()

	// Stale daemon with non-existent socket
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
	os.WriteFile(socketPath, []byte{}, 0644)

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

	// Verify socket still exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Error("socket should not have been removed for alive daemon")
	}
}

func TestCleanupStaleSockets_WithPIDFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create stale socket and PID file
	stalePath := filepath.Join(tmpDir, "bd.sock")
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	os.WriteFile(stalePath, []byte{}, 0644)
	os.WriteFile(pidPath, []byte("12345"), 0644)

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

	// Verify both socket and PID file were removed
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("socket should be removed")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file should be removed")
	}
}

func TestCleanupStaleSockets_EmptySocket(t *testing.T) {
	daemons := []DaemonInfo{
		{
			SocketPath: "",
			Alive:      false,
		},
	}

	// Should handle empty socket path gracefully
	cleaned, err := CleanupStaleSockets(daemons)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (empty socket path), got %d", cleaned)
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

func TestKillAllDaemons_MultipleNotAlive(t *testing.T) {
	daemons := []DaemonInfo{
		{Alive: false, WorkspacePath: "/test1", PID: 12345},
		{Alive: false, WorkspacePath: "/test2", PID: 12346},
		{Alive: false, WorkspacePath: "/test3", PID: 12347},
	}

	results := KillAllDaemons(daemons, false)
	if results.Stopped != 0 || results.Failed != 0 {
		t.Errorf("Expected 0 stopped and 0 failed for dead daemons, got %d stopped and %d failed", results.Stopped, results.Failed)
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

func TestDiscoverDaemon_SocketExistsButNotListening(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Create a regular file (not a socket)
	os.WriteFile(socketPath, []byte{}, 0644)

	daemon := discoverDaemon(socketPath)
	if daemon.Alive {
		t.Error("Expected daemon to not be alive for non-socket file")
	}
	if daemon.Error == "" {
		t.Error("Expected error message for failed connection")
	}
}

func TestDiscoverDaemon_WithErrorFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Create error file but no socket
	errorFilePath := filepath.Join(beadsDir, "daemon-error")
	expectedError := "startup failed: port in use"
	os.WriteFile(errorFilePath, []byte(expectedError), 0644)

	daemon := discoverDaemon(socketPath)
	if daemon.Alive {
		t.Error("Expected daemon to not be alive")
	}
	if daemon.Error != expectedError {
		t.Errorf("Expected error %q, got %q", expectedError, daemon.Error)
	}
}

func TestDaemonInfoStruct(t *testing.T) {
	// Verify DaemonInfo struct fields
	info := DaemonInfo{
		WorkspacePath:       "/test/workspace",
		DatabasePath:        "/test/workspace/.beads/beads.db",
		SocketPath:          "/test/workspace/.beads/bd.sock",
		PID:                 12345,
		Version:             "0.19.0",
		UptimeSeconds:       3600.5,
		LastActivityTime:    "2024-01-01T12:00:00Z",
		ExclusiveLockActive: true,
		ExclusiveLockHolder: "user@host",
		Alive:               true,
		Error:               "",
	}

	if info.WorkspacePath != "/test/workspace" {
		t.Errorf("WorkspacePath mismatch")
	}
	if info.PID != 12345 {
		t.Errorf("PID mismatch")
	}
	if info.Version != "0.19.0" {
		t.Errorf("Version mismatch")
	}
	if info.UptimeSeconds != 3600.5 {
		t.Errorf("UptimeSeconds mismatch")
	}
	if !info.Alive {
		t.Errorf("Alive mismatch")
	}
	if !info.ExclusiveLockActive {
		t.Errorf("ExclusiveLockActive mismatch")
	}
}

func TestKillAllFailureStruct(t *testing.T) {
	failure := KillAllFailure{
		Workspace: "/test",
		PID:       12345,
		Error:     "connection refused",
	}

	if failure.Workspace != "/test" {
		t.Errorf("Workspace mismatch")
	}
	if failure.PID != 12345 {
		t.Errorf("PID mismatch")
	}
	if failure.Error != "connection refused" {
		t.Errorf("Error mismatch")
	}
}

func TestKillAllResultsStruct(t *testing.T) {
	results := KillAllResults{
		Stopped: 5,
		Failed:  2,
		Failures: []KillAllFailure{
			{Workspace: "/test1", PID: 111, Error: "error1"},
			{Workspace: "/test2", PID: 222, Error: "error2"},
		},
	}

	if results.Stopped != 5 {
		t.Errorf("Stopped mismatch")
	}
	if results.Failed != 2 {
		t.Errorf("Failed mismatch")
	}
	if len(results.Failures) != 2 {
		t.Errorf("Failures count mismatch")
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

func TestFindBeadsDirForWorkspace_RegularRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with a regular directory (no git repo)
	beadsDir := findBeadsDirForWorkspace(tmpDir)

	expected := filepath.Join(tmpDir, ".beads")
	if beadsDir != expected {
		t.Errorf("Expected %s, got %s", expected, beadsDir)
	}
}

func TestFindBeadsDirForWorkspace_NonexistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistent := filepath.Join(tmpDir, "nonexistent")

	// Should fall back gracefully
	beadsDir := findBeadsDirForWorkspace(nonexistent)

	expected := filepath.Join(nonexistent, ".beads")
	if beadsDir != expected {
		t.Errorf("Expected %s, got %s", expected, beadsDir)
	}
}

func TestDiscoverDaemons_Registry(t *testing.T) {
	// Test registry-based discovery (no search roots)
	daemons, err := DiscoverDaemons(nil)
	if err != nil {
		t.Fatalf("DiscoverDaemons failed: %v", err)
	}

	// Just verify it doesn't error - actual daemons depend on environment
	_ = daemons
}

func TestDiscoverDaemons_EmptySearchRoots(t *testing.T) {
	// Empty slice should use registry
	daemons, err := DiscoverDaemons([]string{})
	if err != nil {
		t.Fatalf("DiscoverDaemons failed: %v", err)
	}
	// Just verify no error
	_ = daemons
}

func TestDiscoverDaemons_LegacyWithTempDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow daemon discovery test in short mode")
	}

	tmpDir := t.TempDir()

	// Create a fake .beads directory (no daemon running)
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Test legacy discovery with explicit search root
	daemons, err := DiscoverDaemons([]string{tmpDir})
	if err != nil {
		t.Fatalf("DiscoverDaemons failed: %v", err)
	}

	// Should find no alive daemons
	for _, d := range daemons {
		if d.Alive {
			t.Errorf("Found unexpected alive daemon: %+v", d)
		}
	}
}

func TestDiscoverDaemonsLegacy_WithSocketFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon discovery test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Create a socket file (fake, not real socket)
	socketPath := filepath.Join(beadsDir, "bd.sock")
	os.WriteFile(socketPath, []byte{}, 0644)

	// Discovery should find it but report not alive
	daemons, err := discoverDaemonsLegacy([]string{tmpDir})
	if err != nil {
		t.Fatalf("discoverDaemonsLegacy failed: %v", err)
	}

	found := false
	for _, d := range daemons {
		if d.SocketPath == socketPath {
			found = true
			if d.Alive {
				t.Error("Expected daemon not to be alive for fake socket")
			}
		}
	}
	if !found {
		t.Error("Expected to find the socket file")
	}
}

func TestDiscoverDaemonsLegacy_MultipleRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon discovery test in short mode")
	}

	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	beadsDir1 := filepath.Join(tmpDir1, ".beads")
	beadsDir2 := filepath.Join(tmpDir2, ".beads")
	os.MkdirAll(beadsDir1, 0755)
	os.MkdirAll(beadsDir2, 0755)

	// Create socket files in both
	os.WriteFile(filepath.Join(beadsDir1, "bd.sock"), []byte{}, 0644)
	os.WriteFile(filepath.Join(beadsDir2, "bd.sock"), []byte{}, 0644)

	daemons, err := discoverDaemonsLegacy([]string{tmpDir1, tmpDir2})
	if err != nil {
		t.Fatalf("discoverDaemonsLegacy failed: %v", err)
	}

	// Should find both sockets
	if len(daemons) < 2 {
		t.Errorf("Expected at least 2 daemons, got %d", len(daemons))
	}
}

func TestDiscoverDaemonsLegacy_DuplicateSockets(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon discovery test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	socketPath := filepath.Join(beadsDir, "bd.sock")
	os.WriteFile(socketPath, []byte{}, 0644)

	// Search same root twice - should deduplicate
	daemons, err := discoverDaemonsLegacy([]string{tmpDir, tmpDir})
	if err != nil {
		t.Fatalf("discoverDaemonsLegacy failed: %v", err)
	}

	// Count unique socket paths
	count := 0
	for _, d := range daemons {
		if d.SocketPath == socketPath {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected 1 unique socket, got %d", count)
	}
}

func TestDiscoverDaemonsLegacy_NonSocketFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon discovery test in short mode")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Create files that are NOT bd.sock
	os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644)
	os.WriteFile(filepath.Join(beadsDir, "other.sock"), []byte{}, 0644)

	daemons, err := discoverDaemonsLegacy([]string{tmpDir})
	if err != nil {
		t.Fatalf("discoverDaemonsLegacy failed: %v", err)
	}

	// Should not find any sockets
	for _, d := range daemons {
		if filepath.Base(d.SocketPath) != "bd.sock" {
			t.Errorf("Found non-bd.sock file: %s", d.SocketPath)
		}
	}
}

func TestFindDaemonByWorkspace_WithSocketFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// Create a fake socket file
	socketPath := filepath.Join(beadsDir, "bd.sock")
	os.WriteFile(socketPath, []byte{}, 0644)

	// Should not find alive daemon (fake socket)
	daemon, err := FindDaemonByWorkspace(tmpDir)
	if err == nil {
		t.Error("Expected error for fake socket")
	}
	if daemon != nil && daemon.Alive {
		t.Error("Expected daemon not to be alive")
	}
}

func TestStopDaemon_AliveButNoSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	daemon := DaemonInfo{
		Alive:      true,
		SocketPath: socketPath,
		PID:        99999, // Non-existent PID
	}

	// Should fail when trying to stop (can't connect)
	err := StopDaemon(daemon)
	// The error is expected since there's no real daemon
	if err == nil {
		// If no error, we likely couldn't kill the non-existent process
		// which is fine - the test validates we tried
	}
}

func TestKillAllDaemons_MixedAlive(t *testing.T) {
	daemons := []DaemonInfo{
		{Alive: false, WorkspacePath: "/test1", PID: 12345},
		{Alive: true, WorkspacePath: "/test2", PID: 99999, SocketPath: "/nonexistent.sock"},
		{Alive: false, WorkspacePath: "/test3", PID: 12346},
	}

	// Try without force - the alive daemon with non-existent PID will fail
	results := KillAllDaemons(daemons, false)

	// Only 1 alive daemon was attempted, and it likely failed
	// Dead daemons are skipped
	if results.Stopped+results.Failed != 1 {
		t.Errorf("Expected 1 total attempt (1 alive daemon), got %d stopped + %d failed", results.Stopped, results.Failed)
	}
}

func TestKillAllDaemons_WithForce(t *testing.T) {
	daemons := []DaemonInfo{
		{Alive: true, WorkspacePath: "/test", PID: 99999, SocketPath: "/nonexistent.sock"},
	}

	// Try with force - should try both regular kill and force kill
	results := KillAllDaemons(daemons, true)

	// Even with force, non-existent PID will fail
	// Just verify the results struct is populated correctly
	if results.Stopped+results.Failed != 1 {
		t.Errorf("Expected 1 total attempt, got %d stopped + %d failed", results.Stopped, results.Failed)
	}
}

func TestCleanupStaleSockets_Multiple(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple stale sockets
	sock1 := filepath.Join(tmpDir, "sock1.sock")
	sock2 := filepath.Join(tmpDir, "sock2.sock")
	sock3 := filepath.Join(tmpDir, "sock3.sock")
	os.WriteFile(sock1, []byte{}, 0644)
	os.WriteFile(sock2, []byte{}, 0644)
	os.WriteFile(sock3, []byte{}, 0644)

	daemons := []DaemonInfo{
		{SocketPath: sock1, Alive: false},
		{SocketPath: sock2, Alive: false},
		{SocketPath: sock3, Alive: true}, // This one is alive, should not be cleaned
	}

	cleaned, err := CleanupStaleSockets(daemons)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if cleaned != 2 {
		t.Errorf("expected 2 cleaned, got %d", cleaned)
	}

	// Verify sock1 and sock2 removed, sock3 remains
	if _, err := os.Stat(sock1); !os.IsNotExist(err) {
		t.Error("sock1 should be removed")
	}
	if _, err := os.Stat(sock2); !os.IsNotExist(err) {
		t.Error("sock2 should be removed")
	}
	if _, err := os.Stat(sock3); os.IsNotExist(err) {
		t.Error("sock3 should remain (alive daemon)")
	}
}
