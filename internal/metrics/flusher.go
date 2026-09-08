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

	// hardStop is the process-level backstop for the flusher child. The
	// ctx bound above covers the POST itself, but the child also runs
	// queue pruning and collector teardown outside that context; a lock
	// left wedged by a SIGKILLed parent must not turn the flusher into
	// an immortal orphan. When hardStop fires the child exits in place —
	// worst case a batch is re-sent on the next flush, which the queue's
	// idempotent batching already tolerates.
	hardStop = 60 * time.Second
)

func RunSendMetrics() int {
	// Arm the watchdog before anything else: DataDir, pruning and teardown
	// all run outside the flush ctx and none of them is allowed to wedge
	// this process past hardStop.
	watchdog := time.AfterFunc(hardStop, func() {
		fmt.Fprintln(os.Stderr, "send-metrics: hard stop after 60s (wedged lock or stall); exiting")
		os.Exit(1)
	})
	defer watchdog.Stop()

	dir, err := DataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: %v\n", err)
		return 1
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

	flusher := eventkit.NewFileFlusher(dir, ga)
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := flusher.Flush(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: flush: %v\n", err)
		return 1
	}
	return 0
}
