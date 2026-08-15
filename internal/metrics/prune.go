package metrics

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// pruneTTL is how long a queued event batch may wait before it is dropped
	// instead of uploaded. Nothing downstream wants week-old telemetry, and a
	// drain that has fallen a week behind will never catch up by uploading
	// history first (one file per HTTPS round trip; see bd-ulfod forensics:
	// 149k files / 1.1GB of sub-48h backlog on one machine).
	pruneTTL = 7 * 24 * time.Hour

	// maxQueueFiles / maxQueueBytes bound the queue regardless of the
	// drain-vs-emission race. When emission outruns drain (a shared $HOME can
	// emit ~2.3 files/s against a throttled drain of ~0.3-0.4/s) the queue
	// otherwise grows without bound, and every directory scan pays for it.
	// Oldest batches are dropped first: recent telemetry is the only kind
	// worth shipping late.
	maxQueueFiles = 10_000
	maxQueueBytes = 64 << 20 // 64 MiB

	// writeTempPrefix matches the eventkit FileEmitter's CreateTemp pattern
	// (".write-*"). An emitter killed between CreateTemp and Rename strands
	// one forever: no rename will come, and the flusher and the pending-check
	// both ignore non-.evtq names, so only the prune ever reclaims them.
	writeTempPrefix = ".write-"
)

// PruneQueue bounds the queued-event backlog in dir before a flush: event
// batches (and orphaned emitter temp files) older than pruneTTL are deleted,
// and the surviving batches are capped at maxQueueFiles / maxQueueBytes by
// dropping oldest-first. It returns how many files were removed and the bytes
// freed. It runs only in the detached send-metrics child, so its full
// directory scan never lands on an interactive bd invocation.
//
// The prune deliberately runs OUTSIDE eventkit.lock (Flush's TryLock treats
// ErrLocked as "another flusher owns the queue" and silently no-ops, so
// taking the lock here would turn every prune into a skipped flush). The
// worst lock-free interleaving: a rare second child mid-upload sees a file
// this prune deleted and its whole Flush aborts on ENOENT — no event is
// double-sent, the backlog just waits one more spawn interval. ENOENT
// tolerance in eventkit's flush loop is the upstream fix (gastownhall/beads
// GH#5649 lane).
func PruneQueue(dir string, now time.Time) (dropped int, freed int64) {
	return pruneQueue(dir, now, pruneTTL, maxQueueFiles, maxQueueBytes)
}

// queueEntry is one prune-eligible file in the queue directory.
type queueEntry struct {
	path    string
	modTime time.Time
	size    int64
}

// pruneQueue is PruneQueue with the knobs exposed for tests.
func pruneQueue(dir string, now time.Time, ttl time.Duration, maxFiles int, maxBytes int64) (dropped int, freed int64) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		// Missing/unreadable queue dir: nothing to prune.
		return 0, 0
	}

	var live []queueEntry // surviving .evtq batches, candidates for the caps
	var liveBytes int64
	for _, de := range dirents {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		isBatch := filepath.Ext(name) == queuedEventExt
		isOrphanTemp := strings.HasPrefix(name, writeTempPrefix)
		if !isBatch && !isOrphanTemp {
			// Never touch the throttle marker, the flusher lock, or anything
			// else that is not queue payload.
			continue
		}
		fi, err := de.Info()
		if err != nil {
			// Vanished between ReadDir and Info (a concurrent flusher child
			// uploads-and-deletes): already gone, nothing to do.
			continue
		}
		if now.Sub(fi.ModTime()) > ttl {
			if remove(filepath.Join(dir, name)) {
				dropped++
				freed += fi.Size()
			}
			continue
		}
		if isBatch {
			live = append(live, queueEntry{filepath.Join(dir, name), fi.ModTime(), fi.Size()})
			liveBytes += fi.Size()
		}
	}

	if len(live) <= maxFiles && liveBytes <= maxBytes {
		return dropped, freed
	}
	// Drop oldest-first until both caps are satisfied.
	sort.Slice(live, func(i, j int) bool { return live[i].modTime.Before(live[j].modTime) })
	remaining, remainingBytes := len(live), liveBytes
	for _, e := range live {
		if remaining <= maxFiles && remainingBytes <= maxBytes {
			break
		}
		if remove(e.path) {
			dropped++
			freed += e.size
		}
		// A lost race (the file was uploaded-and-deleted concurrently) still
		// shrinks the queue, so it counts against the caps either way.
		remaining--
		remainingBytes -= e.size
	}
	return dropped, freed
}

// remove deletes path, treating "already gone" as success-shaped: a concurrent
// flusher child may upload-and-delete any batch out from under the prune.
func remove(path string) bool {
	err := os.Remove(path)
	return err == nil
}
