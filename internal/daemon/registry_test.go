package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRegistryBasics(t *testing.T) {
	// Create temporary directory for test registry
	tmpDir := t.TempDir()
	registryPath := filepath.Join(tmpDir, ".beads", "registry.json")

	// Override the registry path for testing (platform-specific)
	homeEnv := "HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "USERPROFILE"
	}
	oldHome := os.Getenv(homeEnv)
	os.Setenv(homeEnv, tmpDir)
	defer os.Setenv(homeEnv, oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Test 1: Registry should start empty
	entries, err := registry.List()
	if err != nil {
		t.Fatalf("Failed to list entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected empty registry, got %d entries", len(entries))
	}

	// Test 2: Register a daemon
	entry := RegistryEntry{
		WorkspacePath: "/test/workspace",
		SocketPath:    "/test/workspace/.beads/bd.sock",
		DatabasePath:  "/test/workspace/.beads/beads.db",
		PID:           12345,
		Version:       "0.19.0",
		StartedAt:     time.Now(),
	}

	if err := registry.Register(entry); err != nil {
		t.Fatalf("Failed to register entry: %v", err)
	}

	// Test 3: Verify registry file was created
	if _, err := os.Stat(registryPath); os.IsNotExist(err) {
		t.Error("Registry file was not created")
	}

	// Test 4: Read back the entry (note: process won't be alive, so List won't return it)
	// Instead, use readEntries to verify it was written
	rawEntries, err := registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 1 {
		t.Errorf("Expected 1 entry in registry, got %d", len(rawEntries))
	}
	if rawEntries[0].WorkspacePath != entry.WorkspacePath {
		t.Errorf("Expected workspace %s, got %s", entry.WorkspacePath, rawEntries[0].WorkspacePath)
	}
	if rawEntries[0].PID != entry.PID {
		t.Errorf("Expected PID %d, got %d", entry.PID, rawEntries[0].PID)
	}

	// Test 5: Register another daemon for same workspace (should replace)
	entry2 := entry
	entry2.PID = 54321
	if err := registry.Register(entry2); err != nil {
		t.Fatalf("Failed to register second entry: %v", err)
	}

	rawEntries, err = registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 1 {
		t.Errorf("Expected 1 entry after replacement, got %d", len(rawEntries))
	}
	if rawEntries[0].PID != 54321 {
		t.Errorf("Expected new PID 54321, got %d", rawEntries[0].PID)
	}

	// Test 6: Unregister
	if err := registry.Unregister(entry2.WorkspacePath, entry2.PID); err != nil {
		t.Fatalf("Failed to unregister: %v", err)
	}

	rawEntries, err = registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 0 {
		t.Errorf("Expected empty registry after unregister, got %d entries", len(rawEntries))
	}
}

func TestRegistryMultipleDaemons(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Register multiple daemons
	for i := 1; i <= 3; i++ {
		entry := RegistryEntry{
			WorkspacePath: filepath.Join("/test", "workspace", string(rune('a'+i-1))),
			SocketPath:    filepath.Join("/test", "workspace", string(rune('a'+i-1)), ".beads/bd.sock"),
			DatabasePath:  filepath.Join("/test", "workspace", string(rune('a'+i-1)), ".beads/beads.db"),
			PID:           10000 + i,
			Version:       "0.19.0",
			StartedAt:     time.Now(),
		}
		if err := registry.Register(entry); err != nil {
			t.Fatalf("Failed to register entry %d: %v", i, err)
		}
	}

	rawEntries, err := registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(rawEntries))
	}
}

func TestRegistryStaleCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Register a daemon with a PID that doesn't exist
	staleEntry := RegistryEntry{
		WorkspacePath: "/test/workspace",
		SocketPath:    "/test/workspace/.beads/bd.sock",
		DatabasePath:  "/test/workspace/.beads/beads.db",
		PID:           99999, // Unlikely to exist
		Version:       "0.19.0",
		StartedAt:     time.Now(),
	}

	if err := registry.Register(staleEntry); err != nil {
		t.Fatalf("Failed to register stale entry: %v", err)
	}

	// List should clean up the stale entry
	daemons, err := registry.List()
	if err != nil {
		t.Fatalf("Failed to list: %v", err)
	}

	// Should return empty since the process doesn't exist
	if len(daemons) != 0 {
		t.Errorf("Expected 0 daemons after cleanup, got %d", len(daemons))
	}

	// Verify registry file was cleaned up
	rawEntries, err := registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 0 {
		t.Errorf("Expected empty registry after cleanup, got %d entries", len(rawEntries))
	}
}

func TestRegistryEmptyArrayNotNull(t *testing.T) {
	tmpDir := t.TempDir()
	registryPath := filepath.Join(tmpDir, ".beads", "registry.json")

	// Override the registry path for testing (platform-specific)
	homeEnv := "HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "USERPROFILE"
	}
	oldHome := os.Getenv(homeEnv)
	os.Setenv(homeEnv, tmpDir)
	defer os.Setenv(homeEnv, oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Clear registry
	if err := registry.Clear(); err != nil {
		t.Fatalf("Failed to clear registry: %v", err)
	}

	// Read the file and verify it's [] not null
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("Failed to read registry file: %v", err)
	}

	content := string(data)
	if content != "[]" && content != "[\n]" {
		t.Errorf("Expected empty array [], got: %s", content)
	}
}

func TestRegistryCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	registryPath := filepath.Join(tmpDir, ".beads", "registry.json")

	homeEnv := "HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "USERPROFILE"
	}
	oldHome := os.Getenv(homeEnv)
	os.Setenv(homeEnv, tmpDir)
	defer os.Setenv(homeEnv, oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Create a corrupted registry file
	os.MkdirAll(filepath.Dir(registryPath), 0755)
	os.WriteFile(registryPath, []byte("invalid json{{{"), 0644)

	// bd-qn5: Corrupted registry should be treated as empty, not an error
	// This allows bd doctor to work gracefully when registry is corrupted
	entries, err := registry.readEntries()
	if err != nil {
		t.Errorf("Expected corrupted registry to be treated as empty, got error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected empty entries for corrupted registry, got %v", entries)
	}
}

// bd-qn5: Test for the specific null bytes case that was reported
func TestRegistryNullBytesFile(t *testing.T) {
	tmpDir := t.TempDir()
	registryPath := filepath.Join(tmpDir, ".beads", "registry.json")

	homeEnv := "HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "USERPROFILE"
	}
	oldHome := os.Getenv(homeEnv)
	os.Setenv(homeEnv, tmpDir)
	defer os.Setenv(homeEnv, oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Create a file with null bytes (the reported error case)
	os.MkdirAll(filepath.Dir(registryPath), 0755)
	os.WriteFile(registryPath, []byte{0, 0, 0, 0}, 0644)

	// Should treat as empty, not error
	entries, err := registry.readEntries()
	if err != nil {
		t.Errorf("Expected null-byte registry to be treated as empty, got error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Expected empty entries for null-byte registry, got %v", entries)
	}
}

func TestRegistryUnregisterNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Unregister from empty registry should succeed
	err = registry.Unregister("/nonexistent/workspace", 99999)
	if err != nil {
		t.Errorf("Unregister should succeed even if entry doesn't exist: %v", err)
	}

	// Verify registry is still empty
	rawEntries, err := registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 0 {
		t.Errorf("Expected empty registry, got %d entries", len(rawEntries))
	}
}

// TestRegistryPathNormalization tests that paths are canonicalized for consistent
// matching across symlinks and case-insensitive filesystems (GH#880).
func TestRegistryPathNormalization(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	// Create a real directory for symlink test
	realPath := filepath.Join(tmpDir, "real-workspace")
	if err := os.MkdirAll(realPath, 0755); err != nil {
		t.Fatalf("Failed to create real workspace: %v", err)
	}

	// Canonicalize realPath too (on macOS, /var -> /private/var)
	if canonReal, err := filepath.EvalSymlinks(realPath); err == nil {
		realPath = canonReal
	}

	// Create a symlink to it
	symlinkPath := filepath.Join(tmpDir, "symlink-workspace")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Skipf("Cannot create symlink (platform limitation): %v", err)
	}

	// Register using the symlink path
	entry := RegistryEntry{
		WorkspacePath: symlinkPath,
		SocketPath:    filepath.Join(symlinkPath, ".beads/bd.sock"),
		DatabasePath:  filepath.Join(symlinkPath, ".beads/beads.db"),
		PID:           12345,
		Version:       "0.43.0",
		StartedAt:     time.Now(),
	}

	if err := registry.Register(entry); err != nil {
		t.Fatalf("Failed to register entry: %v", err)
	}

	// Read back entry - path is stored as-is (PathsEqual handles comparison)
	rawEntries, err := registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(rawEntries))
	}

	// Now register using the real path - should detect as same workspace and replace
	// (PathsEqual handles symlink resolution for comparison)
	entry2 := RegistryEntry{
		WorkspacePath: realPath,
		SocketPath:    filepath.Join(realPath, ".beads/bd.sock"),
		DatabasePath:  filepath.Join(realPath, ".beads/beads.db"),
		PID:           54321,
		Version:       "0.43.0",
		StartedAt:     time.Now(),
	}

	if err := registry.Register(entry2); err != nil {
		t.Fatalf("Failed to register second entry: %v", err)
	}

	// Should still have only 1 entry (replaced, not added)
	rawEntries, err = registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 1 {
		t.Errorf("Expected 1 entry after registering same workspace via different path, got %d", len(rawEntries))
	}
	if rawEntries[0].PID != 54321 {
		t.Errorf("Expected PID 54321, got %d", rawEntries[0].PID)
	}

	// Test unregister using symlink path - should match the canonicalized path
	if err := registry.Unregister(symlinkPath, 54321); err != nil {
		t.Fatalf("Failed to unregister via symlink: %v", err)
	}

	rawEntries, err = registry.readEntries()
	if err != nil {
		t.Fatalf("Failed to read entries: %v", err)
	}
	if len(rawEntries) != 0 {
		t.Errorf("Expected empty registry after unregister, got %d entries", len(rawEntries))
	}
}
