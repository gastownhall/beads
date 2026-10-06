package metrics

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dolthub/eventkit"
	"github.com/steveyegge/beads/internal/storage/filelock"
)

// holdLock takes the eventkit lock in dir the same way production does — through
// internal/storage/filelock, not github.com/dolthub/fslock directly. That keeps
// the filelock package doc's "one place allowed to import fslock" boundary true
// for tests too (depguard runs with `tests: false`, so nothing else would catch
// a direct import here) and gives the new wrapper its first direct exercise.
// The returned release unlocks and closes the lock's directory handle, and is
// safe to call more than once so a test can both release early and defer it.
func holdLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	lock, err := filelock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("filelock.New (holder): %v", err)
	}
	if err := lock.TryLock(); err != nil {
		_ = lock.Close()
		t.Fatalf("holder TryLock: %v", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = lock.Unlock()
			_ = lock.Close()
		})
	}
}

// assertLockFree fails unless the eventkit lock in dir can be acquired, i.e.
// nothing named by after leaked a hold past its return.
func assertLockFree(t *testing.T, dir, after string) {
	t.Helper()
	lock, err := filelock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("filelock.New (post-check): %v", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lock.TryLock(); err != nil {
		t.Fatalf("lock still held after %s returned: %v", after, err)
	}
	_ = lock.Unlock()
}

// writeValidQueuedEvent writes a real, eventkit-parseable queued event batch
// via the same FileEmitter.Send path production code uses. Unlike
// writeQueuedEventNamed's placeholder content (fine for tests that only need
// a file with the right extension to satisfy a filename-based scan), a batch
// eventkit.FileFlusher will actually attempt to Send needs a filename that
// matches the MD5 of its own content: readBatch's CheckFilenameMD5 gate
// silently skips (no error) any file where it doesn't, which is why a fake
// name+content pair never reaches the unreachable endpoint at all.
func writeValidQueuedEvent(t *testing.T, dir string) {
	t.Helper()
	fe, err := eventkit.NewFileEmitter(dir)
	if err != nil {
		t.Fatalf("eventkit.NewFileEmitter: %v", err)
	}
	req := &eventkit.LogEventsRequest{
		DistinctID: "test",
		AppName:    "beads",
		AppVersion: "0.0.0-test",
		Platform:   "test",
		Events: []eventkit.EventRecord{{
			ID:        "1",
			Name:      "test_event",
			StartTime: time.Now(),
			EndTime:   time.Now(),
		}},
	}
	if err := fe.Send(context.Background(), req); err != nil {
		t.Fatalf("FileEmitter.Send: %v", err)
	}
}

// TestPruneUnderLockSkipsWhenAlreadyHeld is the Factor B "bounded, never
// hangs" regression: when another process already holds the eventkit lock
// (an in-flight flush, or a sibling send-metrics child pruning first),
// pruneUnderLock must give up once its ctx expires rather than blocking
// forever, and must not have invoked the underlying prune since it never
// acquired the lock.
func TestPruneUnderLockSkipsWhenAlreadyHeld(t *testing.T) {
	dir := t.TempDir()

	release := holdLock(t, dir)
	defer release()

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
	// Boundary control for the contention classification: here the CALLER's
	// own 300ms budget expires before the 2s lockWait, so this is "the child
	// ran out of time", not "a sibling holds the lock", and it must keep its
	// own error. (Production is the other way round — 30s budget, 2s wait —
	// which is the case the next test covers.)
	if errors.Is(err, errLockContended) {
		t.Errorf("pruneUnderLock() = %v, want the caller's own expired-budget error, not errLockContended: lockWait never fired here", err)
	}
}

// TestPruneUnderLockWaitsForReleaseThenRuns pins the other half: a holder
// that releases before the ctx deadline must let pruneUnderLock proceed and
// return the underlying prune's own values, rather than treating transient
// contention as permanent failure.
func TestPruneUnderLockWaitsForReleaseThenRuns(t *testing.T) {
	dir := t.TempDir()

	// This test's margin is the acquisition bound minus the holder's sleep
	// below, and lockWait (2s in production) leaves only ~1.8s of it — enough
	// that a stalled -race runner would turn ordinary scheduling delay into a
	// red. Widen it through the seam that exists for exactly this; the
	// property under test is "a released lock lets the prune run", not the
	// size of the bound (TestPruneUnderLockGivesUpWellBeforeTheCtxBudget owns
	// that one).
	origWait := lockWait
	t.Cleanup(func() { lockWait = origWait })
	lockWait = 10 * time.Second

	release := holdLock(t, dir)
	released := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		release()
		close(released)
	}()
	defer release()

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

	assertLockFree(t, dir, "pruneUnderLock")
}

// TestPruneUnderLockGivesUpWellBeforeTheCtxBudget pins the acquisition bound:
// a contended child must abandon the lock wait after lockWait rather than
// polling for its whole prune budget. Without the bound the wait runs until
// ctx expires, which is what made contention cost a child its entire 30s
// flushTimeout prune budget — only to skip the prune behind a bare "context
// deadline exceeded", or to win the lock late and re-walk the queue its
// sibling had just pruned on whatever sliver of the budget was left.
func TestPruneUnderLockGivesUpWellBeforeTheCtxBudget(t *testing.T) {
	dir := t.TempDir()

	release := holdLock(t, dir)
	defer release()

	// Shrink the bound so the assertion does not have to sleep for the real
	// one; the property under test is "gives up at lockWait, not at ctx".
	origWait := lockWait
	t.Cleanup(func() { lockWait = origWait })
	lockWait = 50 * time.Millisecond

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 0, 0
	}

	// A budget far larger than lockWait: the old unbounded wait would consume
	// all of it before reporting failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, _, err := pruneUnderLock(ctx, dir, time.Now())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("pruneUnderLock() err = nil, want non-nil (the lock is held for the whole test)")
	}
	if called {
		t.Error("pruneUnderLock invoked the prune despite never acquiring the lock")
	}
	if elapsed > 5*time.Second {
		t.Errorf("pruneUnderLock waited %v against a 50ms lockWait and a 30s ctx, want the wait bounded by lockWait", elapsed)
	}
	if ctx.Err() != nil {
		t.Error("pruneUnderLock consumed the caller's whole ctx budget instead of giving up at lockWait")
	}
	// This is the production shape (wait ≪ budget), so it is the case that
	// must report as contention rather than as a deadline the operator cannot
	// place.
	if !errors.Is(err, errLockContended) {
		t.Errorf("pruneUnderLock() = %v, want errors.Is(err, errLockContended): lockWait expired with the caller's budget intact", err)
	}
}

// TestPruneUnderLockKeepsRealLockFailuresDistinct is the other control on that
// classification: a lock that cannot be opened at all is not a sibling doing
// the work, and must not be reported as contention — a caller told "skipped,
// a sibling holds it" would never learn that locking is broken here. It also
// pins the timing assumption the classification rests on: such a failure comes
// back immediately, so it can never be mistaken for an expired lockWait.
//
// It is also this site's DEGRADE pin, and the two properties are one story. A
// real locking failure must be reported, but it must not cost the prune:
// pruneUnderLock is PruneQueue's only production caller, so skipping here is
// permanent on exactly the host class claimFlush's own degrade admits the
// child for, and base pruned those hosts unconditionally. The twin pin
// TestClaimFlushDegradesWhenLockingIsUnavailable injects this identical
// failure and demands the spawn survive it "and with it the prune" — the two
// tests have to agree about whether the prune runs, and until the degrade
// below existed they asserted the opposite of each other on the same fixture.
func TestPruneUnderLockKeepsRealLockFailuresDistinct(t *testing.T) {
	dir := t.TempDir()

	// A directory where the lock file belongs: constructible, unopenable.
	if err := os.Mkdir(filepath.Join(dir, lockFilename), 0o700); err != nil {
		t.Fatalf("mkdir over the lock path: %v", err)
	}

	// Fixture guard, the same one the claimFlush twin uses: the injected error
	// must really be a locking failure. If it were contention (or no error at
	// all) every assertion below would be exercising the wrong branch and
	// would pass for the wrong reason.
	probe, err := filelock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		t.Fatalf("fixture: filelock.New over a directory lock path: %v", err)
	}
	probeErr := probe.TryLock()
	if probeErr == nil {
		_ = probe.Unlock()
	}
	_ = probe.Close()
	if probeErr == nil {
		t.Fatal("fixture: TryLock succeeded on a directory lock path, so this test no longer injects a locking failure")
	}
	if errors.Is(probeErr, filelock.ErrLocked) {
		t.Fatalf("fixture: injected error is contention (%v), not a locking failure", probeErr)
	}

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 4, 2048
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	dropped, freed, err := pruneUnderLock(ctx, dir, time.Now())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("pruneUnderLock() err = nil, want the underlying locking failure reported")
	}
	if errors.Is(err, errLockContended) {
		t.Errorf("pruneUnderLock() = %v, want the raw locking failure, not errLockContended", err)
	}
	if !called {
		t.Error("pruneUnderLock skipped the prune because the lock was unusable; base pruned such hosts unconditionally, so skipping loses the queue bound permanently (see errPrunedWithoutLock)")
	}
	if !errors.Is(err, errPrunedWithoutLock) {
		t.Errorf("pruneUnderLock() = %v, want errors.Is(err, errPrunedWithoutLock): a degraded prune must disclose that it ran unlocked", err)
	}
	// The disclosure has to carry the cause, or the operator learns only that
	// something was skipped, not that locking is broken on this machine.
	// probeErr is that cause: the fixture guard raised it from this same lock
	// file, through the same fslock open.
	if !strings.Contains(err.Error(), probeErr.Error()) {
		t.Errorf("pruneUnderLock() = %q, want the raw locking failure %q kept in the message", err, probeErr)
	}
	// It also has to name the lock file, and only the wrap can: fslock's own
	// errors do not reliably carry the path (see pruneWithoutLock), and this
	// fixture's names at most the base name, so the full path reaches the
	// message only if pruneWithoutLock puts it there.
	if lockPath := filepath.Join(dir, lockFilename); !strings.Contains(err.Error(), lockPath) {
		t.Errorf("pruneUnderLock() = %q, want it to name the lock file %q", err, lockPath)
	}
	if dropped != 4 || freed != 2048 {
		t.Errorf("pruneUnderLock() = (%d, %d), want (4, 2048) passed through from the degraded prune, not zeroed", dropped, freed)
	}
	if elapsed >= lockWait {
		t.Errorf("a locking failure took %v to surface (lockWait = %v), so it can no longer be told apart from an expired wait", elapsed, lockWait)
	}
}

// TestPruneUnderLockDegradesWhenTheLockCannotBeConstructed covers the same
// degrade one error site earlier, at filelock.New rather than
// LockWithContext: New opens the lock file's parent directory, so it fails
// wherever that open fails. A missing directory is the one exception and is
// not this case — nothing was ever emitted, so there is nothing to prune
// (TestPruneUnderLockQuietOnMissingDir owns that). Any other New failure is a
// host that cannot lock, and must cost the prune no more than a TryLock
// failure does. This is the flusher-side twin of
// TestClaimFlushDegradesWhenTheLockCannotBeConstructed.
func TestPruneUnderLockDegradesWhenTheLockCannotBeConstructed(t *testing.T) {
	// A regular file standing where the queue directory belongs: New's
	// OpenRoot on it fails, and not with os.ErrNotExist.
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatalf("fixture: write the file standing in for the queue dir: %v", err)
	}

	// Fixture guard: New must fail, and not with the ENOENT the quiet path
	// swallows, or this test exercises a different branch than it claims.
	if _, err := filelock.New(filepath.Join(dir, lockFilename)); err == nil {
		t.Fatal("fixture: filelock.New succeeded under a non-directory, so this test no longer exercises the New-failure branch")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: New failed with os.ErrNotExist (%v), which is the quiet nothing-to-prune path, not a locking failure", err)
	}

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 1, 512
	}

	dropped, freed, err := pruneUnderLock(context.Background(), dir, time.Now())

	if !called {
		t.Error("pruneUnderLock skipped the prune when the lock could not be constructed, want the pre-lock behavior (prune anyway)")
	}
	if !errors.Is(err, errPrunedWithoutLock) {
		t.Errorf("pruneUnderLock() err = %v, want errors.Is(err, errPrunedWithoutLock)", err)
	}
	if dropped != 1 || freed != 512 {
		t.Errorf("pruneUnderLock() = (%d, %d), want (1, 512) passed through from the degraded prune", dropped, freed)
	}
}

// TestRunSendMetricsReportsContentionDistinctly pins what the operator
// actually sees. pruneUnderLock's doc calls a contended round "an expected
// outcome, not a failure", but the underlying error is
// context.DeadlineExceeded, so reported raw it reads as a prune that broke —
// and names neither the deadline that fired (lockWait, not the 30s budget) nor
// the sibling doing the work.
func TestRunSendMetricsReportsContentionDistinctly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".beads", "eventsData")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}

	defer holdLock(t, dir)()

	origWait := lockWait
	t.Cleanup(func() { lockWait = origWait })
	lockWait = 50 * time.Millisecond

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		t.Error("RunSendMetrics pruned while a sibling held the lock")
		return 0, 0
	}

	// Disabled: the child's only job is the prune, so RunSendMetrics returns
	// right after it and nothing else can write to stderr.
	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	stderr := captureStderr(t, func() {
		if code := RunSendMetrics(); code != 0 {
			t.Errorf("RunSendMetrics() = %d, want 0 (a contended round is not a failure)", code)
		}
	})

	if !strings.Contains(stderr, "skipped, sibling holds the queue lock") {
		t.Errorf("stderr = %q, want it to name the contention", stderr)
	}
	if strings.Contains(stderr, "context deadline exceeded") {
		t.Errorf("stderr = %q, still reports contention as a bare deadline the reader cannot place", stderr)
	}
}

// TestRunSendMetricsReportsTheDegradedPruneWithoutLosingItsCounts pins the
// caller half of the degrade. A prune that ran unlocked did real work, so the
// round is not a failure: the broken lock must be disclosed AND the counts
// must still be reported. Folding it into the generic "prune: %v" error arm
// would print the failure and swallow the only work the child did, which is
// how a maintainer reading logs would conclude the prune never runs here.
func TestRunSendMetricsReportsTheDegradedPruneWithoutLosingItsCounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".beads", "eventsData")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}

	// The same injected locking failure the pruneUnderLock pins use.
	if err := os.Mkdir(filepath.Join(dir, lockFilename), 0o700); err != nil {
		t.Fatalf("mkdir over the lock path: %v", err)
	}

	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	var called bool
	pruneQueueFn = func(context.Context, string, time.Time) (int, int64) {
		called = true
		return 2, 3 << 20
	}

	// Disabled: the child's only job is the prune, so RunSendMetrics returns
	// right after it and nothing else can write to stderr.
	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	stderr := captureStderr(t, func() {
		if code := RunSendMetrics(); code != 0 {
			t.Errorf("RunSendMetrics() = %d, want 0 (a degraded prune still pruned)", code)
		}
	})

	if !called {
		t.Fatal("RunSendMetrics skipped the prune on a host where the lock is unusable")
	}
	if !strings.Contains(stderr, "pruned without the queue lock") {
		t.Errorf("stderr = %q, want it to disclose that the prune ran unlocked", stderr)
	}
	if !strings.Contains(stderr, "pruned 2 queued event file(s)") {
		t.Errorf("stderr = %q, want the degraded prune's counts reported too, not swallowed by the error arm", stderr)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// it wrote. RunSendMetrics writes to os.Stderr directly (it is a process
// entry point, not a library call), so this is the seam available; no test in
// this package runs in parallel, so the swap is safe.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	func() {
		defer func() {
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// TestPruneUnderLockMissingQueueDirIsNoop pins the never-enabled machine's
// case: with no eventsData dir there is nothing to prune, so pruneUnderLock
// must succeed with nothing dropped — keeping RunSendMetrics as silent as the
// unlocked prune was (cmd/bd's TestSendMetricsHonorsMemDiagnostics asserts
// that end to end) — and must not create the dir just to take its lock.
func TestPruneUnderLockMissingQueueDirIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "eventsData")

	dropped, freed, err := pruneUnderLock(context.Background(), dir, time.Now())
	if err != nil {
		t.Fatalf("pruneUnderLock() err = %v, want nil for a missing queue dir", err)
	}
	if dropped != 0 || freed != 0 {
		t.Errorf("pruneUnderLock() = (%d, %d), want (0, 0)", dropped, freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("pruneUnderLock created the queue dir (stat err = %v); a never-enabled machine keeps none", err)
	}
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
	writeValidQueuedEvent(t, dir)

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

	assertLockFree(t, dir, "RunSendMetrics")
}
