package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	// Skip permission checks on Windows as it doesn't support Unix-style file permissions
	skipPermissionChecks := runtime.GOOS == "windows"

	// Create temp directory
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testData := []byte("test content")

	// Write file
	err := atomicWriteFile(testFile, testData)
	if err != nil {
		t.Fatalf("atomicWriteFile failed: %v", err)
	}

	// Verify file exists and has correct content
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(data) != string(testData) {
		t.Errorf("file content mismatch: got %q, want %q", string(data), string(testData))
	}

	// Verify permissions
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	mode := info.Mode()
	if !skipPermissionChecks && mode.Perm() != 0600 {
		t.Errorf("file permissions mismatch: got %o, want %o", mode.Perm(), 0600)
	}

	// Test overwriting existing file
	newData := []byte("updated content")
	err = atomicWriteFile(testFile, newData)
	if err != nil {
		t.Fatalf("atomicWriteFile overwrite failed: %v", err)
	}

	data, err = os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read updated file: %v", err)
	}

	if string(data) != string(newData) {
		t.Errorf("updated file content mismatch: got %q, want %q", string(data), string(newData))
	}

	// Test error case: write to non-existent directory
	badPath := filepath.Join(tmpDir, "nonexistent", "test.txt")
	err = atomicWriteFile(badPath, testData)
	if err == nil {
		t.Error("expected error when writing to non-existent directory")
	}
}

func TestAtomicWriteFile_PreservesSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create target file
	target := filepath.Join(tmpDir, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink
	link := filepath.Join(tmpDir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// Write via symlink
	if err := atomicWriteFile(link, []byte("updated")); err != nil {
		t.Fatalf("atomicWriteFile failed: %v", err)
	}

	// Verify symlink still exists
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("failed to lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced with regular file")
	}

	// Verify target was updated
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read target: %v", err)
	}
	if string(data) != "updated" {
		t.Errorf("target content = %q, want %q", string(data), "updated")
	}
}

func TestDirExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Test existing directory
	if !DirExists(tmpDir) {
		t.Error("DirExists returned false for existing directory")
	}

	// Test non-existing directory
	nonExistent := filepath.Join(tmpDir, "nonexistent")
	if DirExists(nonExistent) {
		t.Error("DirExists returned true for non-existing directory")
	}

	// Test file (not directory)
	testFile := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	if DirExists(testFile) {
		t.Error("DirExists returned true for a file")
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	// Test non-existing file
	if FileExists(testFile) {
		t.Error("FileExists returned true for non-existing file")
	}

	// Create file
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Test existing file
	if !FileExists(testFile) {
		t.Error("FileExists returned false for existing file")
	}

	// Test directory (not file)
	if FileExists(tmpDir) {
		t.Error("FileExists returned true for a directory")
	}
}

func TestEnsureDir(t *testing.T) {
	// Skip permission checks on Windows as it doesn't support Unix-style file permissions
	skipPermissionChecks := runtime.GOOS == "windows"

	tmpDir := t.TempDir()

	// Test creating new directory
	newDir := filepath.Join(tmpDir, "newdir")
	err := EnsureDir(newDir, 0755)
	if err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	if !DirExists(newDir) {
		t.Error("directory was not created")
	}

	// Verify permissions
	info, err := os.Stat(newDir)
	if err != nil {
		t.Fatalf("failed to stat directory: %v", err)
	}

	mode := info.Mode()
	if !skipPermissionChecks && mode.Perm() != 0755 {
		t.Errorf("directory permissions mismatch: got %o, want %o", mode.Perm(), 0755)
	}

	// Test with existing directory (should be no-op)
	err = EnsureDir(newDir, 0755)
	if err != nil {
		t.Errorf("EnsureDir failed on existing directory: %v", err)
	}

	// Test creating nested directories
	nestedDir := filepath.Join(tmpDir, "a", "b", "c")
	err = EnsureDir(nestedDir, 0755)
	if err != nil {
		t.Fatalf("EnsureDir failed for nested directory: %v", err)
	}

	if !DirExists(nestedDir) {
		t.Error("nested directory was not created")
	}
}
