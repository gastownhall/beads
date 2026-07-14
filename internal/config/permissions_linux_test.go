//go:build linux

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixBeadsDirPermissions_UnreadableDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(path, 0o111); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o111); err != nil {
		t.Fatal(err)
	}

	fixed, err := FixBeadsDirPermissions(path)
	if err != nil {
		t.Fatalf("FixBeadsDirPermissions() error = %v", err)
	}
	if !fixed {
		t.Fatal("expected unreadable insecure directory to be fixed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != BeadsDirPerm {
		t.Fatalf("permissions after fix = %04o, want %04o", got, BeadsDirPerm)
	}
}

func TestLinuxPathDirHandle_ChmodUnreadableDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(path, 0o111); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o111); err != nil {
		t.Fatal(err)
	}

	handle, err := openBeadsDirPathHandle(path)
	if err != nil {
		t.Fatalf("openBeadsDirPathHandle() error = %v", err)
	}
	defer handle.Close()
	if err := handle.Chmod(BeadsDirPerm); err != nil {
		t.Fatalf("descriptor chmod error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != BeadsDirPerm {
		t.Fatalf("permissions after descriptor chmod = %04o, want %04o", got, BeadsDirPerm)
	}
}
