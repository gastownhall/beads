//go:build windows

package util

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
	"golang.org/x/sys/windows"
)

func TestTryLockForRemovalWindows(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	long := filepath.Join(root, strings.Repeat("a", 120), strings.Repeat("b", 120), "lock")
	extended := `\\?\` + filepath.Join(root, "extended", "lock")
	if strings.HasPrefix(root, `\\`) {
		extended = `\\?\UNC\` + strings.TrimPrefix(filepath.Join(root, "extended", "lock"), `\\`)
	}
	for _, tc := range []struct{ name, path string }{
		{"ordinary", filepath.Join(root, "ordinary", "lock")},
		{"relative", filepath.Join("relative", "lock")},
		{"unicode", filepath.Join(root, "café-雪", "lock")},
		{"long", long},
		{"extended", extended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := TryLockForRemoval(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if held != nil {
					held.Unlock()
				}
			})
			for _, acquire := range []func(string) (*Lock, error){TryLock, TryLockForRemoval} {
				other, err := acquire(tc.path)
				if other != nil {
					other.Unlock()
				}
				if !lockfile.IsLocked(err) {
					t.Fatalf("competing acquisition = %v, want lock contention", err)
				}
			}
			if err := held.RemoveWhileHeld(); err != nil {
				t.Fatalf("remove while holding lifecycle lock: %v", err)
			}
			for _, acquire := range []func(string) (*Lock, error){TryLock, TryLockForRemoval} {
				other, err := acquire(tc.path)
				if other != nil {
					other.Unlock()
				}
				if err == nil {
					t.Fatal("acquired a lock while original handle was delete-pending")
				}
			}
			held.Unlock()
			held = nil
			if _, err := os.Stat(tc.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retired lock after Unlock: %v", err)
			}
			fresh, err := TryLockForRemoval(tc.path)
			if err != nil {
				t.Fatalf("fresh acquisition after retirement: %v", err)
			}
			fresh.Unlock()
			if _, err := os.Stat(tc.path); err != nil {
				t.Fatalf("Unlock without remove must retain the file: %v", err)
			}
		})
	}
}

func TestTryLockForRemovalWindowsRefusesOrdinaryHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	held, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	other, err := TryLockForRemoval(path)
	if other != nil {
		other.Unlock()
	}
	if !lockfile.IsLocked(err) {
		t.Fatalf("existing lifecycle holder refusal = %v", err)
	}
	if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("ordinary lock deletion behavior changed: %v", err)
	}
}

func TestTryLockForRemovalWindowsRemovalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	held, err := TryLockForRemoval(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if err := held.RemoveWhileHeld(); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("non-sharing reader removal error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := held.RemoveWhileHeld(); err != nil {
		t.Fatalf("remove after releasing reader, lifecycle lock still held: %v", err)
	}
}

func TestTryLockForRemovalWindowsOpenError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(blocker, "lock")} {
		held, err := TryLockForRemoval(path)
		if held != nil {
			held.Unlock()
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("invalid lock target %q error = %v, want PathError", path, err)
		}
	}
	if content, err := os.ReadFile(blocker); err != nil || string(content) != "retain" {
		t.Fatalf("failed open changed blocker: %q, %v", content, err)
	}
}

func TestTryLockForRemovalWindowsRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	path := filepath.Join(root, "lock")
	if err := os.WriteFile(target, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("file symlinks unavailable on this host: %v", err)
		}
		t.Fatal(err)
	}
	held, err := TryLockForRemoval(path)
	if held != nil {
		held.Unlock()
	}
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("reparse lock refusal = %v, want invalid file", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "retain" {
		t.Fatalf("refused lock changed target: %q, %v", content, err)
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("refused lock changed symlink: %q, %v", got, err)
	}
}
