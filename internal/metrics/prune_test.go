package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeQueueFile creates a file in dir with the given content and mtime.
func writeQueueFile(t *testing.T, dir, name string, size int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(t *testing.T, dir string) map[string]bool {
	t.Helper()
	dirents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, de := range dirents {
		got[de.Name()] = true
	}
	return got
}

func TestPruneTTLDropsStaleBatchesAndOrphanTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "old.evtq", 10, now.Add(-8*24*time.Hour))
	writeQueueFile(t, dir, ".write-orphan", 10, now.Add(-8*24*time.Hour))
	writeQueueFile(t, dir, "young.evtq", 10, now.Add(-time.Hour))
	writeQueueFile(t, dir, ".write-live", 10, now.Add(-time.Minute))

	dropped, freed := pruneQueue(dir, now, 7*24*time.Hour, 100, 1<<20)
	if dropped != 2 || freed != 20 {
		t.Fatalf("dropped=%d freed=%d, want 2/20", dropped, freed)
	}
	got := names(t, dir)
	if got["old.evtq"] || got[".write-orphan"] {
		t.Fatalf("stale files survived: %v", got)
	}
	if !got["young.evtq"] || !got[".write-live"] {
		t.Fatalf("young files pruned: %v", got)
	}
}

func TestPruneCountCapDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// 5 young batches, distinct mtimes, oldest = q0.
	for i := 0; i < 5; i++ {
		writeQueueFile(t, dir, "q"+string(rune('0'+i))+".evtq", 10,
			now.Add(-time.Duration(5-i)*time.Minute))
	}
	dropped, _ := pruneQueue(dir, now, 7*24*time.Hour, 3, 1<<20)
	if dropped != 2 {
		t.Fatalf("dropped=%d, want 2", dropped)
	}
	got := names(t, dir)
	for _, want := range []string{"q2.evtq", "q3.evtq", "q4.evtq"} {
		if !got[want] {
			t.Fatalf("newest survivor %s missing: %v", want, got)
		}
	}
	for _, gone := range []string{"q0.evtq", "q1.evtq"} {
		if got[gone] {
			t.Fatalf("oldest %s survived: %v", gone, got)
		}
	}
}

func TestPruneByteCapDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "a.evtq", 400, now.Add(-3*time.Minute))
	writeQueueFile(t, dir, "b.evtq", 400, now.Add(-2*time.Minute))
	writeQueueFile(t, dir, "c.evtq", 400, now.Add(-time.Minute))

	// 1000-byte cap: dropping only "a" (oldest) brings the total to 800.
	dropped, freed := pruneQueue(dir, now, 7*24*time.Hour, 100, 1000)
	if dropped != 1 || freed != 400 {
		t.Fatalf("dropped=%d freed=%d, want 1/400", dropped, freed)
	}
	got := names(t, dir)
	if got["a.evtq"] || !got["b.evtq"] || !got["c.evtq"] {
		t.Fatalf("wrong survivor set: %v", got)
	}
}

func TestPruneNeverTouchesMarkerOrLock(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ancient := now.Add(-365 * 24 * time.Hour)
	writeQueueFile(t, dir, ".last-flush", 1, ancient)
	writeQueueFile(t, dir, "eventkit.lock", 1, ancient)
	writeQueueFile(t, dir, "unrelated.txt", 1, ancient)

	dropped, _ := pruneQueue(dir, now, 7*24*time.Hour, 0, 0)
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
	got := names(t, dir)
	for _, want := range []string{".last-flush", "eventkit.lock", "unrelated.txt"} {
		if !got[want] {
			t.Fatalf("non-queue file %s pruned", want)
		}
	}
}

func TestPruneCapNeverTakesYoungWriteTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Young emitter temps: a live emitter may hold one between CreateTemp and
	// Rename — cap-deleting it would make that Rename fail and lose a live
	// event. Only TTL (7d) may ever reclaim a .write-* file.
	writeQueueFile(t, dir, ".write-a", 10, now.Add(-time.Second))
	writeQueueFile(t, dir, ".write-b", 10, now.Add(-time.Hour))
	// Enough over-cap batches, all OLDER than the temps, that a buggy
	// implementation counting temps as cap candidates would delete them first.
	for i := 0; i < 4; i++ {
		writeQueueFile(t, dir, "q"+string(rune('0'+i))+".evtq", 10,
			now.Add(-time.Duration(10-i)*time.Hour))
	}
	dropped, _ := pruneQueue(dir, now, 7*24*time.Hour, 1, 1<<20)
	if dropped != 3 {
		t.Fatalf("dropped=%d, want 3 (oldest batches only)", dropped)
	}
	got := names(t, dir)
	if !got[".write-a"] || !got[".write-b"] {
		t.Fatalf("young emitter temp cap-deleted: %v", got)
	}
	if !got["q3.evtq"] {
		t.Fatalf("newest batch should survive: %v", got)
	}
}

func TestPruneMissingDirIsNoop(t *testing.T) {
	dropped, freed := pruneQueue(filepath.Join(t.TempDir(), "nope"), time.Now(), time.Hour, 1, 1)
	if dropped != 0 || freed != 0 {
		t.Fatalf("dropped=%d freed=%d, want 0/0", dropped, freed)
	}
}

func TestPruneWithinCapsIsNoop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "a.evtq", 10, now.Add(-time.Minute))
	dropped, _ := pruneQueue(dir, now, 7*24*time.Hour, 10, 1<<20)
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
	if !names(t, dir)["a.evtq"] {
		t.Fatal("in-cap file pruned")
	}
}
