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
		//
		// That same early exit means this Run returns before Cobra ever reaches
		// PersistentPostRunE in main.go, so --mem-profile/BEADS_MEM_PROFILE/
		// BEADS_MEM_STATS are silently inert for this subcommand unless honored
		// here directly (be-wwy2.2). --mem-profile is registered on
		// rootCmd.PersistentFlags(), so this hidden command inherits it and
		// prints it in --help; pass memProfilePath exactly as PersistentPostRunE
		// does, or the advertised flag would be parsed and then discarded.
		code := metrics.RunSendMetrics()
		writeMemDiagnostics(memProfilePath)
		os.Exit(code)
	},
}

func init() {
	rootCmd.AddCommand(sendMetricsCmd)
}
