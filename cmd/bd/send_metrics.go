package main

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
)

var sendMetricsCmd = &cobra.Command{
	Use:    metrics.SendMetricsSubcommand,
	Short:  "Internal: flush queued telemetry events (spawned by bd)",
	Hidden: true,
	Args:   cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		// os.Exit here is intentional: the flusher child must not fall through to
		// main()'s post-command metrics.MaybeSpawnFlusher tail and spawn another
		// send-metrics. MaybeSpawnFlusher also refuses to spawn when EnvIsFlusher
		// is set on this process, so the no-recursion guarantee is structural and
		// not solely dependent on this exit.
		os.Exit(metrics.RunSendMetrics())
	},
}

func init() {
	rootCmd.AddCommand(sendMetricsCmd)
}
