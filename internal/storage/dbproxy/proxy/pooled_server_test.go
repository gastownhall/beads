package proxy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

// TestPooledServer_ErrLockHeld verifies the pooled server reuses the same
// rootDir flock contract as the passthrough proxy: if the lock is already held,
// ListenAndServe returns ErrLockHeld (which the child maps to LockHeldExitCode)
// without ever touching Dolt.
func TestPooledServer_ErrLockHeld(t *testing.T) {
	rootDir := t.TempDir()

	// Hold the proxy lock from "another" process.
	lock, err := util.TryLock(filepath.Join(rootDir, LockFileName))
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer lock.Unlock()

	s := NewPooledServer(PooledOpts{
		RootDir:  rootDir,
		Socket:   filepath.Join(rootDir, "pool.sock"),
		DoltAddr: "127.0.0.1:3307",
		PoolSize: 4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = s.ListenAndServe(ctx)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}
}

// TestPooledServer_RequiresSocket verifies that the pooled child surfaces a
// clear error when no client socket is configured.
func TestPooledServer_RequiresSocket(t *testing.T) {
	rootDir := t.TempDir()
	s := NewPooledServer(PooledOpts{
		RootDir:  rootDir,
		DoltAddr: "127.0.0.1:3307",
		PoolSize: 4,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.ListenAndServe(ctx)
	if err == nil {
		t.Fatal("expected error for missing socket, got nil")
	}
	if errors.Is(err, ErrLockHeld) {
		t.Fatalf("unexpected ErrLockHeld; want socket-required error: %v", err)
	}
}
