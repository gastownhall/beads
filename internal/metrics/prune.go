package metrics

import (
	"context"
	"errors"
	"io"
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

	// pruneChunkSize is how many directory entries the prune walks between
	// budget checks. It matches the chunk hasQueuedEvents uses: big enough
	// that the per-chunk check is noise, small enough that an expired
	// deadline is honored within one chunk of stats.
	pruneChunkSize = 64
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
//
// The scan is bounded by ctx: it reads the directory in chunks and, once the
// context is done, abandons the walk and keeps whatever it already deleted
// (GH#5871 — the child advertises a flushTimeout budget, and a spool large
// enough to matter is exactly the one whose per-entry stat walk outruns it).
// The one exception is a pass that has deleted nothing when its budget runs
// out: it finishes the listing anyway, because abandoning it there would also
// skip the oldest-first caps and leave the queue with no bound applied at all
// (see the walk below). That pass can outrun ctx; the walk is otherwise the
// bounded half of a child that used to run ~15 minutes against a 30s budget.
func PruneQueue(ctx context.Context, dir string, now time.Time) (dropped int, freed int64) {
	return pruneQueue(ctx, dir, now, pruneTTL, maxQueueFiles, maxQueueBytes)
}

// queueEntry is one prune-eligible file in the queue directory.
type queueEntry struct {
	path    string
	modTime time.Time
	size    int64
}

// dirChunkReader is the chunked-listing half of *os.File. It is a seam so a
// test can inject a listing that fails part way through: a mid-listing read
// error leaves a PARTIAL prefix, which must never reach the oldest-first cap
// pass below.
type dirChunkReader interface {
	ReadDir(n int) ([]os.DirEntry, error)
}

// pruneQueue is PruneQueue with the knobs exposed for tests.
func pruneQueue(ctx context.Context, dir string, now time.Time, ttl time.Duration, maxFiles int, maxBytes int64) (dropped int, freed int64) {
	f, err := os.Open(dir) // #nosec G304 -- dir is the metrics DataDir, not user input
	if err != nil {
		// Missing/unreadable queue dir: nothing to prune.
		return 0, 0
	}
	defer f.Close()
	return pruneQueueFrom(ctx, f, dir, now, ttl, maxFiles, maxBytes)
}

// pruneQueueFrom is pruneQueue over an already-opened listing of dir.
func pruneQueueFrom(ctx context.Context, r dirChunkReader, dir string, now time.Time, ttl time.Duration, maxFiles int, maxBytes int64) (dropped int, freed int64) {
	var live []queueEntry // surviving .evtq batches, candidates for the caps
	var liveBytes int64
	truncated := false
	for chunk := 0; ; chunk++ {
		// One budget check per chunk, so the worst-case overrun is one
		// chunk of stats rather than the whole spool. os.ReadDir is not
		// usable here: it reads AND name-sorts every entry before the caller
		// sees one, which on a backed-up queue is precisely the unbounded
		// prologue this bounds (same reason hasQueuedEvents streams).
		//
		// Out of budget is not automatically "stop". A truncated walk cannot
		// run the cap pass below, so a pass that also deleted nothing has
		// applied NEITHER drain — and since the next child reopens the
		// directory at offset zero, it would walk the same young prefix and
		// stop in the same place, leaving the file/byte caps inert forever
		// against exactly the young oversized spool they were added for
		// (GH#5660). So the deadline stops the walk only once there is TTL
		// progress to keep (or before the first chunk, where the caller
		// handed over no budget at all and the queue may not even be large).
		// The cost is disclosed: on a big all-young queue this child runs
		// past its budget to the end of the listing, because that is the
		// only pass in which the caps can fire.
		if ctx.Err() != nil && (chunk == 0 || dropped > 0) {
			truncated = true
			break
		}
		dirents, readErr := r.ReadDir(pruneChunkSize)
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
		if readErr != nil {
			// io.EOF means the whole directory was listed and the caps below
			// may run. Anything else ended the listing early, so what we
			// hold is a prefix, not the queue: the cap pass must not see it.
			if !errors.Is(readErr, io.EOF) {
				truncated = true
			}
			break
		}
	}

	if truncated {
		// The caps are an oldest-first decision over the WHOLE queue; a
		// partial listing cannot make it correctly, and guessing from a
		// prefix would drop batches that are not actually the oldest. The
		// TTL deletions above already stand.
		return dropped, freed
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
