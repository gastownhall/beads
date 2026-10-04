package metrics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/filelock"
)

// The stateful half of the spawn gate (bd-p6o3y): a detached send-metrics
// child is a full re-exec of the bd binary plus an HTTPS POST, so it must only
// spawn when there is something to upload AND the last attempt is at least
// flushInterval old. These tests drive flusherDue/touchFlushMarker directly —
// calling MaybeSpawnFlusher with every env gate open would fork the test
// binary as a detached child on regression, which is exactly the failure mode
// the extracted decisions exist to keep out of `go test`.

func writeQueuedEvent(t *testing.T, dir string) {
	t.Helper()
	writeQueuedEventNamed(t, dir, "batch1")
}

func writeQueuedEventNamed(t *testing.T, dir, stem string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stem+queuedEventExt), []byte("x"), 0o600); err != nil {
		t.Fatalf("write event file: %v", err)
	}
}

func TestFlusherDueRequiresPendingEvents(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	if flusherDue(dir, now) {
		t.Errorf("flusherDue(empty dir) = true, want false")
	}

	// Non-event litter (the eventkit lock file, our own marker, a stray dir)
	// must not count as pending work.
	if err := os.WriteFile(filepath.Join(dir, "eventkit.lock"), nil, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.evtq"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A directory whose name ends in the event extension is still a directory.
	if flusherDue(dir, now) {
		t.Errorf("flusherDue(litter only) = true, want false")
	}

	writeQueuedEvent(t, dir)
	if !flusherDue(dir, now) {
		t.Errorf("flusherDue(queued event, no marker) = false, want true")
	}
}

// TestFlusherDueOrphanTempsWithoutBatch pins the GH#5712 orphan-only follow-up:
// an eventsData dir holding only orphaned .write-* emitter temps and no .evtq
// batch must still become due once a temp is past pruneTTL, so the prune-only
// child spawns and reclaims it. Before the fix the due predicate matched only
// .evtq batches, so such a dir never scheduled a prune and the temp outlived
// its TTL forever on a disabled machine — the exact leak the review caught. A
// still-fresh temp is NOT yet due, matching pruneQueue's own TTL gate.
func TestFlusherDueOrphanTempsWithoutBatch(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	orphan := filepath.Join(dir, writeTempPrefix+"orphan")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatalf("write orphan temp: %v", err)
	}

	// Fresh orphan temp, no batch: nothing is prune-eligible yet, so not due.
	if flusherDue(dir, now) {
		t.Errorf("flusherDue(fresh orphan temp only) = true, want false")
	}

	// Age it past the prune TTL: now the prune child would reclaim it, so the
	// due predicate must fire even though there is still no .evtq batch.
	stale := now.Add(-pruneTTL - time.Hour)
	if err := os.Chtimes(orphan, stale, stale); err != nil {
		t.Fatalf("chtimes orphan temp: %v", err)
	}
	if !flusherDue(dir, now) {
		t.Errorf("flusherDue(stale orphan temp only) = false, want true (prune-only child, GH#5712)")
	}
}

func TestFlusherDueMissingDirIsNotDue(t *testing.T) {
	if flusherDue(filepath.Join(t.TempDir(), "never-created"), time.Now()) {
		t.Errorf("flusherDue(missing dir) = true, want false")
	}
}

func TestFlusherDueThrottlesByMarkerAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueuedEvent(t, dir)
	marker := filepath.Join(dir, flushMarkerName)

	// Fresh marker: throttled.
	touchFlushMarker(dir)
	if flusherDue(dir, now) {
		t.Errorf("flusherDue(fresh marker) = true, want false")
	}

	// Marker just under the interval: still throttled.
	almost := now.Add(-flushInterval + time.Second)
	if err := os.Chtimes(marker, almost, almost); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if flusherDue(dir, now) {
		t.Errorf("flusherDue(marker %v old) = true, want false", flushInterval-time.Second)
	}

	// Marker past the interval: due again.
	old := now.Add(-flushInterval - time.Second)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if !flusherDue(dir, now) {
		t.Errorf("flusherDue(marker %v old) = false, want true", flushInterval+time.Second)
	}

	// Marker mtime slightly in the future (ordinary timestamp skew — e.g.
	// now was sampled just before the marker write): still throttled.
	nearFuture := now.Add(time.Second)
	if err := os.Chtimes(marker, nearFuture, nearFuture); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if flusherDue(dir, now) {
		t.Errorf("flusherDue(marker slightly in future) = true, want false")
	}

	// Marker mtime far in the future (clock stepped back): due, not
	// suppressed until the wall clock catches up.
	future := now.Add(time.Hour)
	if err := os.Chtimes(marker, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if !flusherDue(dir, now) {
		t.Errorf("flusherDue(marker far in future) = false, want true")
	}
}

// TestHasQueuedEventsAcrossChunks pins the chunked directory scan: a queued
// batch sitting past the first ReadDir(64) chunk must still be found, and a
// large all-litter directory must come back empty rather than false-positive.
func TestHasQueuedEventsAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("litter-%03d.tmp", i)), nil, 0o600); err != nil {
			t.Fatalf("write litter: %v", err)
		}
	}
	if hasQueuedEvents(dir, time.Now()) {
		t.Errorf("hasQueuedEvents(200 litter files) = true, want false")
	}
	// The all-litter pass above is the deterministic pin on the loop: 200
	// entries force at least four 64-entry chunks plus the io.EOF exit. The
	// found-a-match pass is chunk-position-deterministic only where directory
	// enumeration is sorted (e.g. NTFS, which CI exercises); elsewhere the
	// batch may surface in any chunk, and the assertion is just "found".
	writeQueuedEventNamed(t, dir, "zz-last")
	if !hasQueuedEvents(dir, time.Now()) {
		t.Errorf("hasQueuedEvents(batch among 200 litter files) = false, want true")
	}
}

// TestFlusherDueFreshMarkerSkipsQueueScan pins the evaluation order: inside
// the throttle interval the queue must not be scanned at all — the property
// that keeps a backed-up queue (148k entries observed) from taxing every bd
// invocation. The scanQueue seam is stubbed so any scan attempt fails the
// test outright.
func TestFlusherDueFreshMarkerSkipsQueueScan(t *testing.T) {
	dir := t.TempDir()
	writeQueuedEvent(t, dir)
	touchFlushMarker(dir)

	orig := scanQueue
	scanQueue = func(string, time.Time) bool {
		t.Error("flusherDue scanned the queue despite a fresh marker")
		return true
	}
	defer func() { scanQueue = orig }()

	if flusherDue(dir, time.Now()) {
		t.Errorf("flusherDue(fresh marker, queued events) = true, want false")
	}
}

func TestTouchFlushMarkerCreatesThenBumps(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, flushMarkerName)

	touchFlushMarker(dir)
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("marker not created: %v", err)
	}

	// Age the marker, touch again, and the mtime must come back to ~now.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	touchFlushMarker(dir)
	fi, err = os.Stat(marker)
	if err != nil {
		t.Fatalf("stat after touch: %v", err)
	}
	if age := time.Since(fi.ModTime()); age > time.Minute {
		t.Errorf("marker mtime not bumped: %v old", age)
	}
}

// TestFlusherMarkerInertToFileFlusher pins the layout assumption the marker
// relies on: it does not carry the queued-event extension, so the eventkit
// FileFlusher's `*.evtq` scan can never try to parse or delete it.
func TestFlusherMarkerInertToFileFlusher(t *testing.T) {
	if filepath.Ext(flushMarkerName) == queuedEventExt {
		t.Fatalf("flushMarkerName %q must not use the queued-event extension %q", flushMarkerName, queuedEventExt)
	}
}

// TestClaimFlushExactlyOneWinnerUnderConcurrency is the Factor A regression:
// concurrent bd invocations racing MaybeSpawnFlusher's spawn decision must
// produce exactly one winner, not one spawn attempt per invocation. claimFlush
// is the extracted double-checked-lock decision (TryLock -> re-check the
// marker under the lock -> touchFlushMarker -> Unlock); this drives it
// directly with real goroutines, each opening its own fslock fd on the shared
// dir, so the contention is genuine OS-level flock contention rather than a
// mocked stand-in (each fslock.New opens its own fd even within one process).
func TestClaimFlushExactlyOneWinnerUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	writeQueuedEvent(t, dir)
	now := time.Now()

	const n = 50
	var wins int32
	var wg sync.WaitGroup
	wg.Add(n)
	start := time.Now()
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if claimFlush(dir, now) {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if wins != 1 {
		t.Fatalf("claimFlush winners = %d across %d goroutines, want exactly 1", wins, n)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("claimFlush race took %v, want well under 2s (lock contention must resolve fast, not block)", elapsed)
	}

	// The marker must exist: only the winner stamps it. Deliberately NOT
	// compared against `now` -- on a fresh t.TempDir the marker does not exist
	// yet, so touchFlushMarker takes its os.WriteFile fallback and the mtime
	// comes from the kernel's COARSE clock, which lags time.Now(). A
	// ModTime().Before(now) assertion reds on that lag alone (reproduced at
	// 4/30 runs here, every observed mtime landing on the same sub-millisecond
	// tick), and production is immune to the same skew by design -- flusherDue
	// tolerates negative ages down to -flushInterval. Freshness is already
	// implied by wins == 1 above: every loser reached its decision through the
	// same marker, so a stale stamp would have let a second goroutine win.
	if _, err := os.Stat(filepath.Join(dir, flushMarkerName)); err != nil {
		t.Fatalf("winner did not touch the flush marker: %v", err)
	}
}

// TestClaimFlushWinnerDoesNotRescanTheQueue pins the narrowed under-lock
// re-check: a winning claim asks only the marker question again (markerFresh),
// never the queue question MaybeSpawnFlusher's unlocked flusherDue pre-check
// has already answered. Re-asking it under the lock is the double scan —
// ~250ms per scan on a backed-up spool, paid in an interactive invocation's
// exit tail — that claimFlush's doc describes removing, and no other test
// would notice it coming back, because the claim's outcome is the same either
// way. The scanQueue seam is stubbed so any scan attempt fails the test
// outright, the same idiom as TestFlusherDueFreshMarkerSkipsQueueScan.
func TestClaimFlushWinnerDoesNotRescanTheQueue(t *testing.T) {
	dir := t.TempDir()
	writeQueuedEvent(t, dir)

	orig := scanQueue
	scanQueue = func(string, time.Time) bool {
		t.Error("claimFlush scanned the queue under the lock; the caller's flusherDue pre-check already did")
		return true
	}
	defer func() { scanQueue = orig }()

	if !claimFlush(dir, time.Now()) {
		t.Fatal("claimFlush() = false with no marker and no other holder, want true")
	}
}

// TestClaimFlushLoserLeavesMarkerUntouched pins the other branch of the claim
// decision: a caller that cannot take the lock must report "not claimed" and
// must leave the throttle marker completely alone.
//
// Only a lock holder may stamp the marker. Stamping on the TryLock-failure path
// looks tempting — a live holder is evidence a flush is already underway, so a
// successor could skip the ~250ms in-band queue scan — but it defeats the
// under-lock re-check that makes claimFlush a correct double-checked lock: the
// re-check can no longer tell a real claim from a loser's scribble. Measured
// with losers stamping, a loser's stamp beats the winner's re-check and the
// winner returns false too, so NOTHING spawns and the leftover marker suppresses
// spawns for a full flushInterval. This test fails if that is ever reintroduced:
// flusherDue must still report due, because no claim actually happened.
func TestClaimFlushLoserLeavesMarkerUntouched(t *testing.T) {
	dir := t.TempDir()
	writeQueuedEvent(t, dir)
	now := time.Now()

	// A separate holder stands in for the sibling bd invocation, or the
	// detached child's prune/flush, that already owns the lock.
	defer holdLock(t, dir)()

	if claimFlush(dir, now) {
		t.Error("claimFlush() = true while another holder owns the lock, want false")
	}
	if _, err := os.Stat(filepath.Join(dir, flushMarkerName)); !os.IsNotExist(err) {
		t.Errorf("a losing claimFlush stamped the throttle marker (stat err = %v); only the lock holder may stamp it", err)
	}
	if !flusherDue(dir, now) {
		t.Error("flusherDue() = false after a losing claimFlush: a failed claim must not engage the throttle")
	}
}

// TestClaimFlushDegradesWhenLockingIsUnavailable is the other half of the
// claim's error classification, and the opposite-polarity twin of the loser
// test above: contention means "someone else won", but a lock error that is
// NOT contention means locking does not work here at all, and answering "lost
// the race" to that suppresses the spawn — and with it the prune child, the
// only caller of PruneQueue — permanently and silently on such a host. It also
// leaves the marker unstamped forever, so every bd invocation re-pays the full
// in-band queue scan. The classification degrades that case to the pre-lock
// behavior instead: stamp best-effort and claim.
//
// The failure is injected with a real one rather than a stub: a directory
// where the lock file belongs, which fslock cannot open (O_CREATE|O_RDWR; on
// Windows NtCreateFile with FILE_NON_DIRECTORY_FILE). That stands in for the
// ENOLCK/EOPNOTSUPP class an NFS home without lockd, a 9p/drvfs $HOME or some
// FUSE mounts return, and unlike a stub it proves the real wrapper reports
// that class as something other than contention.
func TestClaimFlushDegradesWhenLockingIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeQueuedEvent(t, dir)
	now := time.Now()

	if err := os.Mkdir(filepath.Join(dir, lockFilename), 0o700); err != nil {
		t.Fatalf("mkdir over the lock path: %v", err)
	}

	// Fixture guard: the injected error must really be a locking failure. If
	// it were contention (or no error at all) the assertions below would be
	// exercising the wrong branch and would pass for the wrong reason.
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

	if !claimFlush(dir, now) {
		t.Fatalf("claimFlush() = false where locking is unavailable (%v), want true: a broken lock must not suppress the spawn — and with it the prune — forever", probeErr)
	}
	if _, err := os.Stat(filepath.Join(dir, flushMarkerName)); err != nil {
		t.Fatalf("degraded claim did not stamp the throttle marker (%v): unstamped, every bd invocation re-pays the full queue scan", err)
	}
	if flusherDue(dir, now) {
		t.Error("flusherDue() = true right after a degraded claim: the throttle must still engage when locking is unavailable, or the spawn rate is unbounded")
	}
}

// TestClaimFlushDegradesWhenTheLockCannotBeConstructed covers the same
// classification one step earlier, at filelock.New rather than TryLock: New
// opens the lock file's parent directory, so it fails on a host where that
// open fails for any reason. MaybeSpawnFlusher's flusherDue pre-check means
// production reaches claimFlush only with a readable queue dir, so this drives
// claimFlush directly; the point is that neither error site may answer "lost
// the race".
func TestClaimFlushDegradesWhenTheLockCannotBeConstructed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")

	if _, err := filelock.New(filepath.Join(dir, lockFilename)); err == nil {
		t.Fatal("fixture: filelock.New succeeded on a missing directory, so this test no longer exercises the New-failure branch")
	}

	if !claimFlush(dir, time.Now()) {
		t.Fatal("claimFlush() = false when the lock could not be constructed, want true (the pre-lock behavior)")
	}
}
