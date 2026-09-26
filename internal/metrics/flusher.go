package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dolthub/eventkit"
	ga4tx "github.com/dolthub/eventkit/transport/ga4"
	"github.com/steveyegge/beads/internal/storage/filelock"
)

const (
	EnvEndpoint = "BEADS_METRICS_ENDPOINT"

	flushTimeout = 30 * time.Second

	// lockFilename is the eventkit.FileFlusher's own lock file name.
	// pruneUnderLock and claimFlush take this same fslock (not a second,
	// independent one) so a prune and a Flush against the same dir can never
	// run concurrently, and a spawn decision can never race a sibling's.
	lockFilename = eventkit.DefaultLockFilename
)

// pruneQueueFn is PruneQueue behind a seam so tests can assert the prune is
// handed the child's real budget — the property GH#5871 turned on.
var pruneQueueFn = PruneQueue

// pruneUnderLock runs pruneQueueFn while holding the eventkit lock, so a
// concurrently-spawned sibling send-metrics child (Factor B) cannot scan the
// same queue directory at once. LockWithContext bounds the wait by ctx rather
// than blocking indefinitely, returning ctx.Err() if the lock is still held
// when ctx is done — the caller treats that as "skip this round", not fatal.
//
// The lock is always released before returning, never held across the
// caller's subsequent Flush call: eventkit.FileFlusher.Flush acquires this
// exact lock itself with a non-blocking TryLock and silently no-ops (returns
// nil, as if it succeeded) when it observes fslock.ErrLocked. Holding the
// lock in here past return would make every flush after a prune a silent,
// undetectable no-op.
func pruneUnderLock(ctx context.Context, dir string, now time.Time) (int, int64, error) {
	lock, err := filelock.New(filepath.Join(dir, lockFilename))
	if err != nil {
		return 0, 0, err
	}
	if err := lock.LockWithContext(ctx); err != nil {
		return 0, 0, err
	}
	defer func() { _ = lock.Unlock() }()
	dropped, freed := pruneQueueFn(ctx, dir, now)
	return dropped, freed, nil
}

func RunSendMetrics() int {
	dir, err := DataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: %v\n", err)
		return 1
	}

	// The prune used to run before any context existed, so the expensive half
	// of the child — a stat per queued file — had no deadline at all, and
	// children on a backed-up spool were observed alive for ~15 minutes
	// against this 30s advertised bound (GH#5871). Giving it a budget bounds
	// the part that ran away. It does not make the child's total wall clock
	// flushTimeout: the prune may still overrun (see PruneQueue, which
	// finishes its listing when stopping would leave the queue unbounded),
	// eventkit's own flush prologue lists the directory unbounded, and the
	// prune's cap-unlink pass runs after the budget check that admitted it.
	pruneCtx, cancelPrune := context.WithTimeout(context.Background(), flushTimeout)
	defer cancelPrune()

	// Bound the queue before flushing: TTL out stale batches and orphaned
	// emitter temps, cap the rest drop-oldest (bd-ulfod: an unbounded queue
	// reached 149k files / 1.1GB when emission outran the throttled drain).
	// Out-of-band by construction — this child is already detached.
	//
	// Locked (Factor B): an in-flight Flush (this process or a sibling child)
	// or another concurrently-spawned prune holds the same eventkit lock, so
	// two children can no longer scan the same queue directory at once. A
	// failed acquisition just skips this round's prune rather than blocking
	// or double-running — the backlog decays on the next successful child.
	if dropped, freed, err := pruneUnderLock(pruneCtx, dir, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: prune: %v\n", err)
	} else if dropped > 0 {
		fmt.Fprintf(os.Stderr, "send-metrics: pruned %d queued event file(s), freed %.1f MB\n",
			dropped, float64(freed)/(1<<20))
	}

	// With telemetry disabled this child exists only for the prune above:
	// nothing may be POSTed, but the backlog an earlier enabled configuration
	// queued still has to decay. The old ordering (enabled check first)
	// stranded eventsData forever on exactly the machine that just opted out —
	// 2M+ files / 15.8GB observed on one control VM (GH#5712).
	if !Enabled() {
		return 0
	}

	ga, err := ga4tx.New(ga4tx.Config{Endpoint: Endpoint()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: ga4: %v\n", err)
		return 1
	}

	// The upload gets its own full budget rather than the prune's remainder.
	// Sharing one would let a slow-but-successful prune hand the flush an
	// already-spent context, so the child would prune, upload nothing, and
	// exit 1 — and since a prune slow enough to do that is exactly what a
	// backed-up spool produces, the uploads would never resume. Two budgets
	// keep the two halves independent: neither can starve the other.
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), flushTimeout)
	defer cancelFlush()

	flusher := eventkit.NewFileFlusher(dir, ga)
	if err := flusher.Flush(flushCtx); err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: flush: %v\n", err)
		return 1
	}
	return 0
}
