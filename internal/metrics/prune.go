package metrics

import (
	"container/heap"
	"context"
	"os"
	"path/filepath"
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
// context is done, abandons the walk and keeps whatever it already decided
// (GH#5871 — the child advertises a flushTimeout budget, and a spool large
// enough to matter is exactly the one whose per-entry stat walk outruns it).
// Every chunk boundary is a safe place to stop, with no exception: the
// oldest-first caps are applied incrementally as each live entry is seen, via
// a bounded min-heap capped at maxFiles/maxBytes, rather than deferred to a
// pass over the whole listing. A later entry can only compete with — never
// retroactively invalidate — an eviction the heap already made, so a
// truncated walk's evictions are exactly the ones a full walk would have made
// over the same prefix. A queue too large to fully examine within one budget
// converges to the caps across repeated calls instead of within a single one
// (be-wwy2.3 — this replaced an earlier version where an all-young over-cap
// pile made no TTL progress and so was allowed to outrun its own budget to
// the end of the listing just to let the caps fire at all; see GH#5660).
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
// error simply ends the walk, leaving whatever eviction decisions were
// already made for the examined prefix in place — the unexamined remainder is
// left untouched, never guessed at.
type dirChunkReader interface {
	ReadDir(n int) ([]os.DirEntry, error)
}

// queueMinHeap is a container/heap.Interface over queueEntry ordered by
// modTime ascending, so Pop always removes the oldest live entry. Capping it
// at maxFiles/maxBytes during the walk — push, then pop while over either cap
// — bounds the live-candidate set at O(maxFiles) in memory regardless of how
// many entries the directory holds. It is also what makes a streaming,
// incremental cap decision correct: a later entry can only be newer than one
// already evicted, never able to un-evict it, so the entries the heap holds
// at any point are exactly the maxFiles-newest (within maxBytes) of
// everything examined so far — including at a chunk boundary where the walk
// might stop.
type queueMinHeap []queueEntry

func (h queueMinHeap) Len() int           { return len(h) }
func (h queueMinHeap) Less(i, j int) bool { return h[i].modTime.Before(h[j].modTime) }
func (h queueMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *queueMinHeap) Push(x any) {
	*h = append(*h, x.(queueEntry))
}

func (h *queueMinHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
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
	live := &queueMinHeap{}
	heap.Init(live)
	var liveBytes int64

	// evict pops the oldest live entry while either cap is exceeded. Called
	// after every push, so the heap never holds more than maxFiles entries
	// (or maxBytes worth) at any point the walk might stop.
	evict := func() {
		for live.Len() > 0 && (live.Len() > maxFiles || liveBytes > maxBytes) {
			e := heap.Pop(live).(queueEntry)
			liveBytes -= e.size
			if remove(e.path) {
				dropped++
				freed += e.size
			}
		}
	}

	for {
		// One budget check per chunk, so the worst-case overrun is one
		// chunk of stats rather than the whole spool. os.ReadDir is not
		// usable here: it reads AND name-sorts every entry before the caller
		// sees one, which on a backed-up queue is precisely the unbounded
		// prologue this bounds (same reason hasQueuedEvents streams).
		//
		// Unconditional, with no exception for a chunk that dropped nothing:
		// the caps are applied incrementally below as each live entry is
		// pushed, so stopping here never leaves them unapplied the way a
		// deferred whole-listing pass would (be-wwy2.3 / GH#5660).
		if ctx.Err() != nil {
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
				heap.Push(live, queueEntry{filepath.Join(dir, name), fi.ModTime(), fi.Size()})
				liveBytes += fi.Size()
				evict()
			}
		}
		if readErr != nil {
			// Whatever was examined already had its caps applied above via
			// evict(); a non-EOF error just ends the walk here rather than
			// making any decision about the unexamined remainder.
			break
		}
	}
	return dropped, freed
}

// remove deletes path, treating "already gone" as success-shaped: a concurrent
// flusher child may upload-and-delete any batch out from under the prune.
func remove(path string) bool {
	err := os.Remove(path)
	return err == nil
}
