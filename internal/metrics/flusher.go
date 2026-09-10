package metrics

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dolthub/eventkit"
	ga4tx "github.com/dolthub/eventkit/transport/ga4"
)

const (
	EnvEndpoint = "BEADS_METRICS_ENDPOINT"

	flushTimeout = 30 * time.Second
)

// pruneQueueFn is PruneQueue behind a seam so tests can assert the prune is
// handed the child's real budget — the property GH#5871 turned on.
var pruneQueueFn = PruneQueue

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
	if dropped, freed := pruneQueueFn(pruneCtx, dir, time.Now()); dropped > 0 {
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
