package dolt

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

func TestAcquireAccessLock_TimesOutWhenHeld(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")
	holder, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire holder lock: %v", err)
	}
	defer holder.Release()

	start := time.Now()
	_, err = AcquireAccessLock(doltDir, true, 120*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, lockfile.ErrLockBusy) {
		t.Fatalf("expected lock busy error, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("lock acquisition exceeded bounded retry window: %v", elapsed)
	}
}

func TestAcquireAccessLock_BoundedRetriesUnderContention(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")
	holder, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire holder lock: %v", err)
	}
	defer holder.Release()

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, acquireErr := AcquireAccessLock(doltDir, false, 150*time.Millisecond)
			errs <- acquireErr
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("contention test hung; lock retries are not bounded")
	}

	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("expected lock contention errors, got nil")
		}
		if !errors.Is(err, lockfile.ErrLockBusy) {
			t.Fatalf("expected lock busy error, got: %v", err)
		}
	}
}

func TestAcquireAccessLock_BoundedTimeoutAcrossReadAndWriteModes(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")
	holder, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire holder lock: %v", err)
	}
	defer holder.Release()

	assertBusyWithinBound := func(name string, write bool) {
		t.Helper()
		start := time.Now()
		_, lockErr := AcquireAccessLock(doltDir, write, 140*time.Millisecond)
		elapsed := time.Since(start)

		if lockErr == nil {
			t.Fatalf("%s: expected lock busy error, got nil", name)
		}
		if !errors.Is(lockErr, lockfile.ErrLockBusy) {
			t.Fatalf("%s: expected lock busy error, got: %v", name, lockErr)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("%s: lock wait exceeded deterministic bound: %v", name, elapsed)
		}
	}

	assertBusyWithinBound("write-mode", true)
	assertBusyWithinBound("read-mode", false)
}

func TestAcquireAccessLock_ConcurrentSharedLocks(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")

	// Multiple goroutines should acquire shared locks simultaneously
	const readers = 8
	locks := make([]*AccessLock, readers)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, readers)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lock, err := AcquireAccessLock(doltDir, false, 2*time.Second)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			locks[idx] = lock
			mu.Unlock()
			errs <- nil
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("shared lock acquisition failed: %v", err)
		}
	}

	// All locks held concurrently — release them
	for _, lock := range locks {
		if lock != nil {
			lock.Release()
		}
	}
}

func TestAcquireAccessLock_ExclusiveBlocksShared(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")

	// Hold exclusive lock
	exclusive, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire exclusive lock: %v", err)
	}
	defer exclusive.Release()

	// Shared lock should time out deterministically
	start := time.Now()
	_, err = AcquireAccessLock(doltDir, false, 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected shared lock to fail while exclusive is held")
	}
	if !errors.Is(err, lockfile.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy, got: %v", err)
	}
	// Should complete near the timeout, not hang indefinitely
	if elapsed > 2*time.Second {
		t.Fatalf("shared lock wait exceeded bound: %v", elapsed)
	}
}

func TestAcquireAccessLock_TimeoutProducesDeterministicError(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")

	holder, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire holder lock: %v", err)
	}
	defer holder.Release()

	_, err = AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Verify error wraps ErrLockBusy
	if !errors.Is(err, lockfile.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy in chain, got: %v", err)
	}

	// Verify error message includes actionable guidance
	errMsg := err.Error()
	if !strings.Contains(errMsg, "bd doctor --fix") {
		t.Fatalf("expected 'bd doctor --fix' in error message, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "exclusive") || !strings.Contains(errMsg, "timeout") {
		t.Fatalf("expected lock type and timeout in error message, got: %s", errMsg)
	}
}

func TestAcquireAccessLock_ReleaseUnblocksWaiter(t *testing.T) {
	t.Parallel()

	doltDir := filepath.Join(t.TempDir(), ".beads", "dolt")

	holder, err := AcquireAccessLock(doltDir, true, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to acquire holder lock: %v", err)
	}

	// Start a goroutine that waits for the lock
	acquired := make(chan struct{})
	go func() {
		waiter, err := AcquireAccessLock(doltDir, true, 2*time.Second)
		if err != nil {
			t.Errorf("waiter failed to acquire lock after release: %v", err)
			return
		}
		waiter.Release()
		close(acquired)
	}()

	// Give the waiter time to start polling
	time.Sleep(100 * time.Millisecond)

	// Release the holder — waiter should succeed
	holder.Release()

	select {
	case <-acquired:
		// Success — waiter acquired the lock after release
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not acquire lock after holder released")
	}
}

