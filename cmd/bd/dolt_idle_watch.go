package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
)

var (
	doltIdleWatchServerDir string
	doltIdleWatchPID       int
	doltIdleWatchHost      string
	doltIdleWatchPort      int
	doltIdleWatchTimeout   time.Duration
)

var doltIdleWatchCmd = &cobra.Command{
	Use:    "dolt-idle-watch",
	Hidden: true,
	Short:  "Internal: stop an auto-started dolt sql-server once it is idle",
	Long: `dolt-idle-watch watches one dolt sql-server that bd auto-started and stops
it once it has had no client for --timeout (dolt.idle-timeout). bd launches it
beside each auto-started server; it is not intended to be invoked directly.`,

	PersistentPreRun:  func(cmd *cobra.Command, args []string) {},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {},

	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		outcome, err := doltserver.RunIdleWatch(ctx, doltserver.IdleWatchOpts{
			ServerDir: doltIdleWatchServerDir,
			PID:       doltIdleWatchPID,
			Host:      doltIdleWatchHost,
			Port:      doltIdleWatchPort,
			Timeout:   doltIdleWatchTimeout,
		})
		debug.Logf("dolt-idle-watch: %s", outcome)
		return err
	},
}

// spawnDoltIdleWatcher is doltserver.IdleWatchSpawner for the bd binary: it
// re-executes this bd as the hidden dolt-idle-watch command. main() installs
// it, so test binaries never spawn copies of themselves.
func spawnDoltIdleWatcher(opts doltserver.IdleWatchOpts) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	return doltserver.SpawnIdleWatchProcess(exe, opts)
}

func init() {
	doltIdleWatchCmd.Flags().StringVar(&doltIdleWatchServerDir, "server-dir", "", "server directory holding dolt-server.pid")
	doltIdleWatchCmd.Flags().IntVar(&doltIdleWatchPID, "pid", 0, "PID of the dolt sql-server to watch")
	doltIdleWatchCmd.Flags().StringVar(&doltIdleWatchHost, "host", "", "host the server listens on")
	doltIdleWatchCmd.Flags().IntVar(&doltIdleWatchPort, "port", 0, "SQL port of the server")
	doltIdleWatchCmd.Flags().DurationVar(&doltIdleWatchTimeout, "timeout", 0, "idle window before the server is stopped")
	_ = doltIdleWatchCmd.MarkFlagRequired("server-dir")
	_ = doltIdleWatchCmd.MarkFlagRequired("pid")
	_ = doltIdleWatchCmd.MarkFlagRequired("port")
	_ = doltIdleWatchCmd.MarkFlagRequired("timeout")
	rootCmd.AddCommand(doltIdleWatchCmd)
}
