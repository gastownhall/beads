package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if hasQueuedEvents(dir) {
		t.Errorf("hasQueuedEvents(200 litter files) = true, want false")
	}
	// The all-litter pass above is the deterministic pin on the loop: 200
	// entries force at least four 64-entry chunks plus the io.EOF exit. The
	// found-a-match pass is chunk-position-deterministic only where directory
	// enumeration is sorted (e.g. NTFS, which CI exercises); elsewhere the
	// batch may surface in any chunk, and the assertion is just "found".
	writeQueuedEventNamed(t, dir, "zz-last")
	if !hasQueuedEvents(dir) {
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
	scanQueue = func(string) bool {
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
