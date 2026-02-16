package dolt

import (
	"errors"
	"path/filepath"
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
