package metrics

import (
	"context"
	"errors"
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

// lockWait bounds how long pruneUnderLock waits to ACQUIRE the eventkit lock,
// as a small fraction of the child's flushTimeout prune budget. Waiting on the
// whole budget is what makes contention expensive: a child that finds a
// sibling's lock held can sit polling for up to 30s, and then either give up
// behind a bare "context deadline exceeded" or win the lock late and repeat
// the stat walk the sibling has just done, on whatever sliver of the budget is
// left. This round needs neither — the sibling holding the lock is doing both
// halves anyway, and the backlog decays on the next child either way — so a
// contended child skips its prune after lockWait and moves on to its Flush,
// which runs on a budget of its own (see RunSendMetrics).
//
// A var, not a const, so a test can shrink it and assert the bound without
// sleeping for it — the same seam idiom as pruneQueueFn above and scanQueue in
// spawn.go.
//
// It bounds ACQUISITION only; ctx bounds the HOLD. Once the lock is taken the
// prune runs on the caller's full ctx rather than a shorter hold budget,
// because a pass that examines fewer entries makes less cap progress, and one
// that never examines more than the caps admit makes none at all (see
// PruneQueue). PruneQueue checks that ctx at every directory chunk, so the
// hold ends at most one chunk of stats, plus that chunk's unlinks, past the
// caller's deadline — far inside flushInterval. "A holder keeps this lock past
// flushInterval", the precondition the declined loser-stamp arm in spawn.go
// was priced against, therefore takes a holder stuck inside a single chunk (a
// hung mount, say), not this package's normal path; for that window every bd
// invocation re-pays the in-band queue scan and then loses its claim.
var lockWait = 2 * time.Second

// errLockContended reports that the queue lock was still held when lockWait
// ran out. A contended round is an expected outcome (a sibling is doing this
// round's work), so it must be distinguishable from a prune that actually
// failed: reported raw it reads as
// "send-metrics: prune: context deadline exceeded", which names neither the
// deadline that fired (lockWait, not the 30s budget) nor the reason.
//
// Who actually reads that line: whoever types `bd send-metrics` by hand. The
// only production caller is the detached child MaybeSpawnFlusher starts with
// cmd.Stderr = nil (see MaybeSpawnFlusher in spawn.go), so every message this
// file writes to stderr — this one, errPrunedWithoutLock below, and the
// prune's own counts — is discarded in the field. Routing the child's output
// somewhere durable is a separate operational change; the distinction still
// earns its keep for the hand-invoked diagnosis, which is the path a user on a
// broken mount is told to run.
var errLockContended = errors.New("sibling holds the queue lock")

// errPrunedWithoutLock reports that the queue lock was unobtainable for a
// reason that is not contention — locking does not work on this host at all —
// and the prune therefore ran WITHOUT it, as it did before this package took
// the lock. It travels back alongside the prune's real counts, so it is a
// disclosure, not a failure: the caller reports it and then reports the counts.
// The raw locking error is wrapped inside it so the operator still learns what
// broke.
var errPrunedWithoutLock = errors.New("pruned without the queue lock")

// pruneUnderLock runs pruneQueueFn under the eventkit lock where that lock can
// be taken, and unlocked (reporting it) where it cannot. Under the lock, a
// concurrently-spawned sibling send-metrics child (Factor B) cannot scan the
// same queue directory at once. The acquisition is bounded by the smaller of
// lockWait and ctx, returning that error if the lock is still held when the
// wait runs out — the caller treats it as "skip this round", not fatal. Once
// acquired, the prune itself gets the caller's full ctx budget. Where the lock
// cannot be taken AT ALL — not held by a sibling, but broken on this host — it
// prunes anyway and reports that it did; see the degrade paragraph below.
//
// A contended round is an expected outcome, not a failure: the child exits 0
// having pruned nothing (and possibly uploaded nothing, since Flush no-ops on a
// held lock), because a sibling is already doing that work. It is reported as
// errLockContended so the caller can say that, rather than printing a bare
// "context deadline exceeded" a reader cannot tell from a real prune failure.
//
// Contention is the ONLY reason to skip the prune. Every other lock error —
// fslock hands back the raw open(2)/flock(2) failure, e.g. ENOLCK/EOPNOTSUPP
// on an NFS home without lockd, a 9p/drvfs $HOME (WSL /mnt/c), some FUSE
// mounts — means locking does not work on this host, and skipping there is
// permanent: this function is PruneQueue's only production caller, so the
// unbounded-queue bound this package exists for (bd-ulfod: 149k files /
// 1.1GB) would be gone on exactly the hosts claimFlush's degraded path spawns
// the child for. So such an error degrades to pruneWithoutLock — base
// behavior (before this wrapper, RunSendMetrics ran pruneQueueFn
// unconditionally, with no lock) — and is reported rather than swallowed. The
// lock's purpose is not lost by choosing this: mutual exclusion here is
// unobtainable for EVERYONE on such a host, because
// eventkit.FileFlusher.Flush takes this same lock and returns every
// non-ErrLocked error verbatim too (fileflusher.go:50-67), so there is no
// concurrently-scanning Flush left to collide with. What comes back is the
// GH#5649 ENOENT interleaving that was the accepted worst case before this
// wrapper existed (see PruneQueue's doc), traded against an unbounded queue.
//
// The lock is always released before returning, never held across the
// caller's subsequent Flush call: eventkit.FileFlusher.Flush acquires this
// exact lock itself with a non-blocking TryLock and silently no-ops (returns
// nil, as if it succeeded) when it observes fslock.ErrLocked. Holding the
// lock in here past return would make every flush after a prune a silent,
// undetectable no-op.
func pruneUnderLock(ctx context.Context, dir string, now time.Time) (int, int64, error) {
	lock, err := filelock.New(filepath.Join(dir, lockFilename))
	if errors.Is(err, os.ErrNotExist) {
		// No queue dir: the emitter never wrote, so there is nothing to prune
		// and nothing to serialize against. fslock opens the lock file's parent
		// dir up front, so the missing-dir case has to be caught here to stay
		// the silent no-op the unlocked prune (and eventkit's own Flush) treat
		// it as, rather than a "send-metrics: prune:" error line on every run.
		return 0, 0, nil
	}
	if err != nil {
		return pruneWithoutLock(ctx, dir, now, err)
	}
	defer func() { _ = lock.Close() }()
	lockCtx, cancelLock := context.WithTimeout(ctx, lockWait)
	defer cancelLock()
	if err := lock.LockWithContext(lockCtx); err != nil {
		// Only a deadline that fired while the CALLER still has budget is
		// contention: that deadline can only be lockWait's. Anything else —
		// the caller's own ctx ending, or a real locking failure, which
		// LockWithContext returns immediately rather than waiting out — keeps
		// its original error.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return 0, 0, fmt.Errorf("%w (waited %v)", errLockContended, lockWait)
		}
		// The caller's own budget ran out: nothing here is broken, and a
		// prune handed an expired ctx would do no work anyway. Report it as
		// the caller's error, not as a host that cannot lock.
		if ctx.Err() != nil {
			return 0, 0, err
		}
		return pruneWithoutLock(ctx, dir, now, err)
	}
	defer func() { _ = lock.Unlock() }()
	dropped, freed := pruneQueueFn(ctx, dir, now)
	return dropped, freed, nil
}

// pruneWithoutLock is pruneUnderLock's degraded path for a host where the lock
// itself is unavailable (see pruneUnderLock): prune anyway, exactly as the
// pre-lock code did, rather than letting a broken lock cancel the only bound
// this package puts on the queue. It is the sibling of spawn.go's
// claimWithoutLock, on the same error class and for the same reason.
//
// cause travels back wrapped in errPrunedWithoutLock so the caller can report
// that locking is broken here AND still report what the prune did; returning
// the counts is the point, since a caller told only "error" would log a
// failure for a round that actually bounded the queue.
//
// The wrap names the lock file's full path itself, because cause does not
// reliably carry it: a flock(2) failure — the ENOLCK/EOPNOTSUPP class this
// path exists for — comes back from fslock as a bare errno, an open failure
// names only the file's base name (os.Root reports root-relative paths), and
// on Windows the open returns a bare NTSTATUS. Which file, on which mount, is
// what the operator diagnosing a broken lock needs to know.
func pruneWithoutLock(ctx context.Context, dir string, now time.Time, cause error) (int, int64, error) {
	dropped, freed := pruneQueueFn(ctx, dir, now)
	return dropped, freed, fmt.Errorf("%w: %s: %w", errPrunedWithoutLock, filepath.Join(dir, lockFilename), cause)
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
	// flushTimeout. The prune's own overrun is now at most one chunk of stats,
	// plus the unlinks that chunk's own cap evictions perform: it consults this
	// deadline once per directory chunk and stops at the first boundary past it
	// with no exception, because its caps are applied inline as the walk goes
	// rather than in a pass after the budget check that admitted it
	// (be-wwy2.3 — applying them inline is what moved those unlinks into the
	// budgeted region). What is still un-budgeted is eventkit's own
	// flush prologue, which lists the directory unbounded.
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
	// CONTENDED acquisition just skips this round's prune rather than blocking
	// or double-running — the backlog decays on the next successful child. A
	// host where locking is unavailable is the other case entirely: there the
	// prune runs unlocked (base behavior) and says so, because skipping would
	// be permanent rather than one round.
	dropped, freed, pruneErr := pruneUnderLock(pruneCtx, dir, time.Now())

	// A degraded prune DID run and DID return its counts, so disclose the
	// broken lock and then fall through to the ordinary result line. Reporting
	// it as a failure instead would hide the only work this round did.
	if errors.Is(pruneErr, errPrunedWithoutLock) {
		fmt.Fprintf(os.Stderr, "send-metrics: prune: %v\n", pruneErr)
		pruneErr = nil
	}
	switch {
	case errors.Is(pruneErr, errLockContended):
		// Expected, not a failure — and it has to read as such, since the
		// underlying error is an indistinguishable "context deadline exceeded".
		fmt.Fprintf(os.Stderr, "send-metrics: prune: skipped, %v\n", pruneErr)
	case pruneErr != nil:
		fmt.Fprintf(os.Stderr, "send-metrics: prune: %v\n", pruneErr)
	case dropped > 0:
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
