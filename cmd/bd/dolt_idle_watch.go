package main

import (
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
)

// doltIdleWatchCmd is the hidden command the idle watcher runs as: a
// detached re-exec of bd that stops an auto-started dolt sql-server after its
// idle window. Modeled on db-proxy-child: the no-op persistent pre/post runs
// keep it from opening a store or posting metrics, so it is cheap and safe to
// leave running for hours.
var doltIdleWatchCmd = &cobra.Command{
	Use:    "dolt-idle-watch",
	Hidden: true,
	Short:  "Internal: watch an auto-started Dolt server and stop it when idle",
	Long: `dolt-idle-watch runs the idle watcher for one bd-managed dolt
sql-server. bd spawns it itself after an auto-start (see dolt.idle-timeout);
it is not intended to be invoked directly by users.`,
	PersistentPreRun:  func(cmd *cobra.Command, args []string) {},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {},
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts := doltserver.IdleWatchOpts{
			ServerDir: doltIdleWatchServerDir,
			PID:       doltIdleWatchPID,
			Host:      doltIdleWatchHost,
			Port:      doltIdleWatchPort,
			Timeout:   doltIdleWatchTimeout,
		}
		doltserver.RunIdleWatch(cmd.Context(), opts)
		return nil
	},
}

var (
	doltIdleWatchServerDir string
	doltIdleWatchPID       int
	doltIdleWatchHost      string
	doltIdleWatchPort      int
	doltIdleWatchTimeout   time.Duration
)

// spawnIdleWatchProcess re-execs bd itself as the detached dolt-idle-watch
// child for opts. The spawning bd already resolved the idle window, so the
// child reads no configuration. Failures are logged at debug level and
// swallowed: a watcher that failed to spawn only means the server stays up,
// like it always did.
func spawnIdleWatchProcess(opts doltserver.IdleWatchOpts) {
	exe, err := os.Executable()
	if err != nil {
		debug.Logf("idle-watch: resolving bd executable: %v", err)
		return
	}
	// GH#4634, same hazard as the sql-server spawn: the watcher outlives the
	// caller, so descriptors the caller left non-CLOEXEC must not be pinned
	// by it for hours.
	doltserver.SanitizeInheritedFDs()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		debug.Logf("idle-watch: opening null device: %v", err)
		return
	}
	defer devNull.Close()
	cmd := exec.Command(exe, "dolt-idle-watch", //nolint:gosec // G204: exe is bd itself, args are watcher state
		"--server-dir", opts.ServerDir,
		"--pid", strconv.Itoa(opts.PID),
		"--host", opts.Host,
		"--port", strconv.Itoa(opts.Port),
		"--timeout", opts.Timeout.String())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = doltserver.ProcAttrDetached()
	if err := cmd.Start(); err != nil {
		debug.Logf("idle-watch: spawning watcher: %v", err)
	}
	// The child is detached; its lifecycle ends with RunIdleWatch's return.
}

func init() {
	doltIdleWatchCmd.Flags().StringVar(&doltIdleWatchServerDir, "server-dir", "", "directory holding the watched server's state files")
	doltIdleWatchCmd.Flags().IntVar(&doltIdleWatchPID, "pid", 0, "PID of the watched dolt sql-server")
	doltIdleWatchCmd.Flags().StringVar(&doltIdleWatchHost, "host", "", "host of the watched dolt sql-server")
	doltIdleWatchCmd.Flags().IntVar(&doltIdleWatchPort, "port", 0, "port of the watched dolt sql-server")
	doltIdleWatchCmd.Flags().DurationVar(&doltIdleWatchTimeout, "timeout", 0, "idle window after which the server is stopped")
	_ = doltIdleWatchCmd.MarkFlagRequired("server-dir")
	_ = doltIdleWatchCmd.MarkFlagRequired("pid")
	_ = doltIdleWatchCmd.MarkFlagRequired("port")
	_ = doltIdleWatchCmd.MarkFlagRequired("timeout")
	rootCmd.AddCommand(doltIdleWatchCmd)
	doltserver.SetIdleWatchSpawner(spawnIdleWatchProcess)
}
