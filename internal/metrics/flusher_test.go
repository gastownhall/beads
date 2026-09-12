package metrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dolthub/fslock"
)

// TestPruneUnderLockSkipsWhenAlreadyHeld is the Factor B "bounded, never
// hangs" regression: when another process already holds the eventkit lock
// (an in-flight flush, or a sibling send-metrics child pruning first),
// pruneUnderLock must give up once its ctx expires rather than blocking
// forever, and must not have invoked the underlying prune since it never
// acquired the lock.
func TestPruneUnderLockSkipsWhenAlreadyHeld(t *testing.T) {
	dir := t.TempDir()

	holder, err := fslock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("fslock.New (holder): %v", err)
	}
	if err := holder.TryLock(); err != nil {
		t.Fatalf("holder TryLock: %v", err)
	}
	defer holder.Unlock()

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 0, 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	dropped, freed, err := pruneUnderLock(ctx, dir, time.Now())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("pruneUnderLock() err = nil, want non-nil (lock held by another holder for the whole ctx budget)")
	}
	if called {
		t.Errorf("pruneUnderLock invoked the prune despite never acquiring the lock")
	}
	if dropped != 0 || freed != 0 {
		t.Errorf("pruneUnderLock() = (%d, %d), want (0, 0) on a failed acquisition", dropped, freed)
	}
	if elapsed > time.Second {
		t.Errorf("pruneUnderLock took %v to give up on a 300ms ctx, want well under 1s", elapsed)
	}
}

// TestPruneUnderLockWaitsForReleaseThenRuns pins the other half: a holder
// that releases before the ctx deadline must let pruneUnderLock proceed and
// return the underlying prune's own values, rather than treating transient
// contention as permanent failure.
func TestPruneUnderLockWaitsForReleaseThenRuns(t *testing.T) {
	dir := t.TempDir()

	holder, err := fslock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("fslock.New (holder): %v", err)
	}
	if err := holder.TryLock(); err != nil {
		t.Fatalf("holder TryLock: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = holder.Unlock()
		close(released)
	}()

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 3, 1024
	}

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()

	dropped, freed, err := pruneUnderLock(ctx, dir, time.Now())
	<-released

	if err != nil {
		t.Fatalf("pruneUnderLock() err = %v, want nil once the holder released", err)
	}
	if !called {
		t.Fatal("pruneUnderLock did not invoke the prune after acquiring the lock")
	}
	if dropped != 3 || freed != 1024 {
		t.Errorf("pruneUnderLock() = (%d, %d), want (3, 1024) passed through from the prune", dropped, freed)
	}

	free, err := fslock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("fslock.New (post-check): %v", err)
	}
	if err := free.TryLock(); err != nil {
		t.Fatalf("lock still held after pruneUnderLock returned: %v", err)
	}
	_ = free.Unlock()
}

// TestRunSendMetricsReleasesLockBeforeFlush is the sharpest Factor B
// regression: if pruneUnderLock ever leaked its lock hold past return, the
// FileFlusher's own lock acquisition inside Flush would hit contention and
// silently no-op (treating "someone else is already flushing" as success),
// and RunSendMetrics would wrongly return 0 against an unreachable endpoint.
// Enabling metrics with a real queued event and an unreachable endpoint means
// the ONLY way to observe return code 1 is for Flush to have actually run,
// which requires the lock to have been free when Flush acquired it.
func TestRunSendMetricsReleasesLockBeforeFlush(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".beads", "eventsData")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}
	writeQueuedEventNamed(t, dir, "batch-release-check")

	if _, err := Init("0.0.0-test", true, "http://127.0.0.1:1/collect"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if code := RunSendMetrics(); code != 1 {
		t.Fatalf("RunSendMetrics() = %d, want 1 (Flush must actually run against the unreachable endpoint, proving pruneUnderLock released the lock)", code)
	}
}

// TestRunSendMetricsNormalRunUnaffectedByLocking is the non-regression
// control: with no contention at all, adding the lock around prune must not
// change RunSendMetrics' externally observable behavior, and must not leave
// the lock held once RunSendMetrics returns.
func TestRunSendMetricsNormalRunUnaffectedByLocking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".beads", "eventsData")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 0, 0
	}

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if code := RunSendMetrics(); code != 0 {
		t.Fatalf("RunSendMetrics() = %d, want 0", code)
	}
	if !called {
		t.Fatal("RunSendMetrics did not prune")
	}

	lock, err := fslock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("fslock.New (post-check): %v", err)
	}
	if err := lock.TryLock(); err != nil {
		t.Fatalf("lock still held after RunSendMetrics returned: %v", err)
	}
	_ = lock.Unlock()
}
