package fix

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMoveFile_EXDEV_FallsBackToCopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	oldRename := renameFile
	defer func() { renameFile = oldRename }()
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile failed: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected src to be removed, stat err=%v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("dst contents=%q", string(data))
	}
}

func TestMoveFile_EXDEV_CopyFails_LeavesSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	oldRename := renameFile
	oldOpenRW := openFileRW
	defer func() {
		renameFile = oldRename
		openFileRW = oldOpenRW
	}()
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	openFileRW = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ENOSPC}
	}

	err := moveFile(src, dst)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected ENOSPC, got %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("expected src to remain, stat err=%v", err)
	}
}
