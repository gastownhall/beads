package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTempDirInMemory(t *testing.T) {
	tmpDir := TempDirInMemory(t)

	// Verify directory exists
	if stat, err := os.Stat(tmpDir); err != nil || !stat.IsDir() {
		t.Fatalf("TempDirInMemory() did not create valid directory: %v", err)
	}

	// Verify it's a beads test directory
	if !strings.Contains(filepath.Base(tmpDir), "beads-test") {
		t.Errorf("Expected directory name to contain 'beads-test', got: %s", tmpDir)
	}

	// On Linux CI, verify we're using /dev/shm if available
	if runtime.GOOS == "linux" {
		if stat, err := os.Stat("/dev/shm"); err == nil && stat.IsDir() {
			if !strings.HasPrefix(tmpDir, "/dev/shm") {
				t.Errorf("On Linux with /dev/shm available, expected tmpDir to use it, got: %s", tmpDir)
			} else {
				t.Logf("✓ Using tmpfs ramdisk: %s", tmpDir)
			}
		}
	} else {
		t.Logf("Platform: %s, using standard temp: %s", runtime.GOOS, tmpDir)
	}

	// Verify cleanup happens
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("Test file should exist: %v", err)
	}
}

func TestTempDirInMemory_Cleanup(t *testing.T) {
	var tmpDir string

	// Run in subtest to trigger cleanup
	t.Run("create", func(t *testing.T) {
		tmpDir = TempDirInMemory(t)
		if err := os.WriteFile(filepath.Join(tmpDir, "data"), []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	})

	// After subtest completes, cleanup should have run
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("Expected tmpDir to be cleaned up, but it still exists: %s", tmpDir)
	}
}
