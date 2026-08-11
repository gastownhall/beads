package metrics

import (
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
	if err := os.WriteFile(filepath.Join(dir, "batch1"+queuedEventExt), []byte("x"), 0o600); err != nil {
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
