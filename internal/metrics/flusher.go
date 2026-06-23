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

func RunSendMetrics() int {
	if !Enabled() {
		return 0
	}

	dir, err := DataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "send-metrics: %v\n", err)
		return 1
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
