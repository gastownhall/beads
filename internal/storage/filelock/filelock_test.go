package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.lock")
}

// TestTryLockExcludesASecondHolder is the property every caller depends on:
// two independently constructed Locks over the same path must not both hold it.
// (Each New opens its own descriptor, so this holds even inside one process,
// which is what makes single-process contention tests meaningful.)
func TestTryLockExcludesASecondHolder(t *testing.T) {
	path := lockPath(t)

	first, err := New(path)
	if err != nil {
		t.Fatalf("New (first): %v", err)
	}
	defer first.Close()
	if err := first.TryLock(); err != nil {
		t.Fatalf("first TryLock: %v", err)
	}

	second, err := New(path)
	if err != nil {
		t.Fatalf("New (second): %v", err)
	}
	defer second.Close()
	if err := second.TryLock(); err == nil {
		_ = second.Unlock()
		t.Fatal("second TryLock succeeded while the first holder owns the lock, want an error")
	}

	// ...and the lock is reusable once released, so Unlock really releases.
	if err := first.Unlock(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	if err := second.TryLock(); err != nil {
		t.Fatalf("second TryLock after the first released: %v", err)
	}
	_ = second.Unlock()
}

// TestTryLockClassifiesContentionSeparatelyFromFailure pins the distinction
// callers key their fallback on: only contention is ErrLocked. Everything else
// — the class a mount without working flock(2) produces, where fslock returns
// the raw open/flock errno — must NOT match, or a caller reading "someone else
// won" suppresses its work forever on such a host. The second half is the
// control: without it the assertion would pass for a classifier that answered
// ErrLocked to everything.
func TestTryLockClassifiesContentionSeparatelyFromFailure(t *testing.T) {
	path := lockPath(t)

	holder, err := New(path)
	if err != nil {
		t.Fatalf("New (holder): %v", err)
	}
	defer holder.Close()
	if err := holder.TryLock(); err != nil {
		t.Fatalf("holder TryLock: %v", err)
	}
	defer func() { _ = holder.Unlock() }()

	contender, err := New(path)
	if err != nil {
		t.Fatalf("New (contender): %v", err)
	}
	defer contender.Close()
	switch err := contender.TryLock(); {
	case err == nil:
		_ = contender.Unlock()
		t.Fatal("contender TryLock succeeded against a live holder, want ErrLocked")
	case !errors.Is(err, ErrLocked):
		t.Fatalf("contended TryLock: err = %v, want errors.Is(err, ErrLocked)", err)
	}

	// Control: a lock path that cannot be opened at all (a directory where the
	// lock file belongs — fslock opens it O_CREATE|O_RDWR, and on Windows with
	// FILE_NON_DIRECTORY_FILE) is a locking failure, not contention.
	blocked := filepath.Join(t.TempDir(), "test.lock")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("mkdir blocked lock path: %v", err)
	}
	broken, err := New(blocked)
	if err != nil {
		t.Fatalf("New (broken): %v", err)
	}
	defer broken.Close()
	switch err := broken.TryLock(); {
	case err == nil:
		_ = broken.Unlock()
		t.Fatal("TryLock succeeded on a directory lock path, want a locking failure")
	case errors.Is(err, ErrLocked):
		t.Fatalf("a locking failure was classified as contention: err = %v, want !errors.Is(err, ErrLocked)", err)
	}
}

// TestLockWithContextGivesUpWhenCtxIsDone pins the bounded-wait contract
// internal/metrics relies on to cap how long a contended prune waits.
func TestLockWithContextGivesUpWhenCtxIsDone(t *testing.T) {
	path := lockPath(t)

	holder, err := New(path)
	if err != nil {
		t.Fatalf("New (holder): %v", err)
	}
	defer holder.Close()
	if err := holder.TryLock(); err != nil {
		t.Fatalf("holder TryLock: %v", err)
	}
	defer func() { _ = holder.Unlock() }()

	waiter, err := New(path)
	if err != nil {
		t.Fatalf("New (waiter): %v", err)
	}
	defer waiter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := waiter.LockWithContext(ctx); err == nil {
		_ = waiter.Unlock()
		t.Fatal("LockWithContext acquired a lock held for the whole ctx, want an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("LockWithContext blocked %v past a 50ms ctx, want it bounded by ctx", elapsed)
	}
}

// TestNewOnMissingDirIsNotExist pins the error class callers match on: New
// opens the lock file's parent directory, so a never-created queue dir surfaces
// as os.ErrNotExist rather than an opaque failure. internal/metrics keys its
// "nothing to prune, say nothing" path off exactly this.
func TestNewOnMissingDirIsNotExist(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "never-created", "test.lock"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New on a missing directory: err = %v, want os.ErrNotExist", err)
	}
}

// TestCloseReleasesTheDirectoryHandle pins the reason Close exists. New opens a
// handle to the lock file's parent directory that only Close releases -- Unlock
// closes just the lock file -- so a caller that skips Close leaks one directory
// descriptor per New for the life of the process. The unclosed loop below is
// the control: it must leak where the closed loop does not, otherwise this test
// would pass even if Close did nothing.
func TestCloseReleasesTheDirectoryHandle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor accounting via /proc/self/fd is Linux-only")
	}
	path := lockPath(t)

	openFDs := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("read /proc/self/fd: %v", err)
		}
		return len(entries)
	}

	const iterations = 32

	// Closed: New + full lifecycle + Close must not accumulate descriptors.
	before := openFDs()
	for i := 0; i < iterations; i++ {
		lock, err := New(path)
		if err != nil {
			t.Fatalf("New (closed loop, iteration %d): %v", i, err)
		}
		if err := lock.TryLock(); err != nil {
			t.Fatalf("TryLock (closed loop, iteration %d): %v", i, err)
		}
		if err := lock.Unlock(); err != nil {
			t.Fatalf("Unlock (closed loop, iteration %d): %v", i, err)
		}
		if err := lock.Close(); err != nil {
			t.Fatalf("Close (iteration %d): %v", i, err)
		}
	}
	// Allow a little slack for unrelated runtime descriptors.
	if grew := openFDs() - before; grew > iterations/2 {
		t.Errorf("%d New/Close cycles grew open descriptors by %d, want ~0: Close is not releasing the directory handle", iterations, grew)
	}

	// Control: the same loop WITHOUT Close must visibly leak, which is what
	// makes the assertion above meaningful rather than vacuously true.
	beforeLeak := openFDs()
	leaked := make([]*Lock, 0, iterations)
	for i := 0; i < iterations; i++ {
		lock, err := New(path)
		if err != nil {
			t.Fatalf("New (leak control, iteration %d): %v", i, err)
		}
		leaked = append(leaked, lock)
	}
	grewLeak := openFDs() - beforeLeak
	for _, lock := range leaked {
		_ = lock.Close()
	}
	if grewLeak < iterations {
		t.Errorf("control: %d unclosed New calls grew open descriptors by only %d, want >= %d -- the leak this test guards against is no longer observable, so re-check the assertion above", iterations, grewLeak, iterations)
	}
}
