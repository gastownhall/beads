//go:build unix

package setup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// An existing target that is not a regular file must fail the writer before
// any probe or rename: a write-open on a FIFO blocks until a reader appears,
// and a rename would replace the very thing the caller owns nothing about.
func TestAtomicWriteFileRefusesANonRegularTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}

	if err := atomicWriteFile(path, []byte("data")); err == nil {
		t.Fatal("atomicWriteFile replaced a non-regular target instead of refusing")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO was replaced, mode = %v", info.Mode())
	}
	assertNoTempResidue(t, dir)
}
