package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/ui"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: "setup",
	Short:   "Configure Dolt database settings",
	Long: `Configure and manage Dolt database settings and server lifecycle.

Beads connects to a running dolt sql-server for all database operations.

Commands:
  bd dolt start        Start a local Dolt SQL server
  bd dolt stop         Stop the local Dolt SQL server
  bd dolt show         Show current Dolt configuration with connection test
  bd dolt set <k> <v>  Set a configuration value
  bd dolt test         Test server connection
  bd dolt commit       Commit pending changes
  bd dolt push         Push commits to Dolt remote
  bd dolt pull         Pull commits from Dolt remote

Configuration keys for 'bd dolt set':
  database  Database name (default: issue prefix or "beads")
  host      Server host (default: 127.0.0.1)
  port      Server port (default: 3307)
  user      MySQL user (default: root)

Flags for 'bd dolt set':
  --update-config  Also write to config.yaml for team-wide defaults

Examples:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100 --update-config
  bd dolt test`,
}

var doltShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current Dolt configuration with connection status",
	Run: func(cmd *cobra.Command, args []string) {
		showDoltConfig(true)
	},
}

var doltSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a Dolt configuration value",
	Long: `Set a Dolt configuration value in metadata.json.

Keys:
  database  Database name (default: issue prefix or "beads")
  host      Server host (default: 127.0.0.1)
  port      Server port (default: 3307)
  user      MySQL user (default: root)

Use --update-config to also write to config.yaml for team-wide defaults.

Examples:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100
  bd dolt set port 3307 --update-config`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		value := args[1]
		updateConfig, _ := cmd.Flags().GetBool("update-config")
		setDoltConfig(key, value, updateConfig)
	},
}

var doltTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test connection to Dolt server",
	Long: `Test the connection to the configured Dolt server.

This verifies that:
  1. The server is reachable at the configured host:port
  2. The connection can be established

Use this before switching to server mode to ensure the server is running.`,
	Run: func(cmd *cobra.Command, args []string) {
		testDoltConnection()
	},
}

var doltPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push commits to Dolt remote",
	Long: `Push local Dolt commits to the configured remote.

Requires a Dolt remote to be configured in the database directory.
For Hosted Dolt, set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD environment
variables for authentication.

Use --force to overwrite remote changes (e.g., when the remote has
uncommitted changes in its working set).`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		force, _ := cmd.Flags().GetBool("force")
		fmt.Println("Pushing to Dolt remote...")
		if force {
			if err := st.ForcePush(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := st.Push(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Println("Push complete.")
	},
}

var doltPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull commits from Dolt remote",
	Long: `Pull commits from the configured Dolt remote into the local database.

Requires a Dolt remote to be configured in the database directory.
For Hosted Dolt, set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD environment
variables for authentication.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		fmt.Println("Pulling from Dolt remote...")
		if err := st.Pull(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Pull complete.")
	},
}

var doltStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a local Dolt SQL server",
	Long: `Start a dolt sql-server process for the current beads repository.

The server runs in the background using the configured host, port, and database
settings. A PID file is written to .beads/dolt/dolt-server.pid for lifecycle
management.

If a server is already running (PID file exists and process is alive), this
command exits successfully without starting a second instance.

Requires the 'dolt' CLI to be installed and available in PATH.

Examples:
  bd dolt start            # Start with default settings (127.0.0.1:3307)
  bd dolt start --port 3308  # Start on a custom port`,
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetInt("port")
		startDoltServer(port)
	},
}

var doltStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the local Dolt SQL server",
	Long: `Stop the dolt sql-server started by 'bd dolt start'.

Reads the PID from .beads/dolt/dolt-server.pid, sends SIGTERM to the process,
and removes the PID file. If the server is not running, exits successfully.`,
	Run: func(cmd *cobra.Command, args []string) {
		stopDoltServer()
	},
}

var doltCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Create a Dolt commit from pending changes",
	Long: `Create a Dolt commit from any uncommitted changes in the working set.

This is the primary commit point for batch mode. When auto-commit is set to
"batch", changes accumulate in the working set across multiple bd commands and
are committed together here with a descriptive summary message.

Also useful before push operations that require a clean working set, or when
auto-commit was off or changes were made externally.

For more options (--stdin, custom messages), see: bd vc commit`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		msg, _ := cmd.Flags().GetString("message")
		if msg == "" {
			// No explicit message — use CommitPending which generates a
			// descriptive summary of accumulated changes.
			committed, err := st.CommitPending(ctx, getActor())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if !committed {
				fmt.Println("Nothing to commit.")
				return
			}
		} else {
			if err := st.Commit(ctx, msg); err != nil {
				errLower := strings.ToLower(err.Error())
				if strings.Contains(errLower, "nothing to commit") || strings.Contains(errLower, "no changes") {
					fmt.Println("Nothing to commit.")
					return
				}
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		commandDidExplicitDoltCommit = true
		fmt.Println("Committed.")
	},
}

func init() {
	doltSetCmd.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltPushCmd.Flags().Bool("force", false, "Force push (overwrite remote changes)")
	doltCommitCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	doltStartCmd.Flags().Int("port", 0, "Override server port (default: from config or 3307)")
	doltCmd.AddCommand(doltStartCmd)
	doltCmd.AddCommand(doltStopCmd)
	doltCmd.AddCommand(doltShowCmd)
	doltCmd.AddCommand(doltSetCmd)
	doltCmd.AddCommand(doltTestCmd)
	doltCmd.AddCommand(doltCommitCmd)
	doltCmd.AddCommand(doltPushCmd)
	doltCmd.AddCommand(doltPullCmd)
	rootCmd.AddCommand(doltCmd)
}

func showDoltConfig(testConnection bool) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	backend := cfg.GetBackend()

	if jsonOutput {
		result := map[string]interface{}{
			"backend": backend,
		}
		if backend == configfile.BackendDolt {
			result["database"] = cfg.GetDoltDatabase()
			result["host"] = cfg.GetDoltServerHost()
			result["port"] = cfg.GetDoltServerPort()
			result["user"] = cfg.GetDoltServerUser()
			if testConnection {
				result["connection_ok"] = testServerConnection(cfg)
			}
		}
		outputJSON(result)
		return
	}

	if backend != configfile.BackendDolt {
		fmt.Printf("Backend: %s\n", backend)
		return
	}

	fmt.Println("Dolt Configuration")
	fmt.Println("==================")
	fmt.Printf("  Database: %s\n", cfg.GetDoltDatabase())
	fmt.Printf("  Host:     %s\n", cfg.GetDoltServerHost())
	fmt.Printf("  Port:     %d\n", cfg.GetDoltServerPort())
	fmt.Printf("  User:     %s\n", cfg.GetDoltServerUser())

	if testConnection {
		fmt.Println()
		if testServerConnection(cfg) {
			fmt.Printf("  %s\n", ui.RenderPass("✓ Server connection OK"))
		} else {
			fmt.Printf("  %s\n", ui.RenderWarn("✗ Server not reachable"))
		}
	}

	// Show config sources
	fmt.Println("\nConfig sources (priority order):")
	fmt.Println("  1. Environment variables (BEADS_DOLT_*)")
	fmt.Println("  2. metadata.json (local, gitignored)")
	fmt.Println("  3. config.yaml (team defaults)")
}

func setDoltConfig(key, value string, updateConfig bool) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	if cfg.GetBackend() != configfile.BackendDolt {
		fmt.Fprintf(os.Stderr, "Error: not using Dolt backend\n")
		os.Exit(1)
	}

	var yamlKey string

	switch key {
	case "mode":
		fmt.Fprintf(os.Stderr, "Error: mode is no longer configurable; beads always uses server mode\n")
		os.Exit(1)

	case "database":
		if value == "" {
			fmt.Fprintf(os.Stderr, "Error: database name cannot be empty\n")
			os.Exit(1)
		}
		cfg.DoltDatabase = value
		yamlKey = "dolt.database"

	case "host":
		if value == "" {
			fmt.Fprintf(os.Stderr, "Error: host cannot be empty\n")
			os.Exit(1)
		}
		cfg.DoltServerHost = value
		yamlKey = "dolt.host"

	case "port":
		port, err := strconv.Atoi(value)
		if err != nil || port <= 0 || port > 65535 {
			fmt.Fprintf(os.Stderr, "Error: port must be a valid port number (1-65535)\n")
			os.Exit(1)
		}
		cfg.DoltServerPort = port
		yamlKey = "dolt.port"

	case "user":
		if value == "" {
			fmt.Fprintf(os.Stderr, "Error: user cannot be empty\n")
			os.Exit(1)
		}
		cfg.DoltServerUser = value
		yamlKey = "dolt.user"

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown key '%s'\n", key)
		fmt.Fprintf(os.Stderr, "Valid keys: mode, database, host, port, user\n")
		os.Exit(1)
	}

	// Audit log: record who changed what
	logDoltConfigChange(beadsDir, key, value)

	// Save to metadata.json
	if err := cfg.Save(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"key":      key,
			"value":    value,
			"location": "metadata.json",
		}
		if updateConfig {
			result["config_yaml_updated"] = true
		}
		outputJSON(result)
		return
	}

	fmt.Printf("Set %s = %s (in metadata.json)\n", key, value)

	// Also update config.yaml if requested
	if updateConfig && yamlKey != "" {
		if err := config.SetYamlConfig(yamlKey, value); err != nil {
			fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: failed to update config.yaml: %v", err)))
		} else {
			fmt.Printf("Set %s = %s (in config.yaml)\n", yamlKey, value)
		}
	}
}

func testDoltConnection() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	if cfg.GetBackend() != configfile.BackendDolt {
		fmt.Fprintf(os.Stderr, "Error: not using Dolt backend\n")
		os.Exit(1)
	}

	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()
	addr := fmt.Sprintf("%s:%d", host, port)

	if jsonOutput {
		ok := testServerConnection(cfg)
		outputJSON(map[string]interface{}{
			"host":          host,
			"port":          port,
			"connection_ok": ok,
		})
		if !ok {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("Testing connection to %s...\n", addr)

	if testServerConnection(cfg) {
		fmt.Printf("%s\n", ui.RenderPass("✓ Connection successful"))
	} else {
		fmt.Printf("%s\n", ui.RenderWarn("✗ Connection failed"))
		fmt.Println("\nMake sure dolt sql-server is running:")
		fmt.Printf("  bd dolt start\n")
		os.Exit(1)
	}
}

func testServerConnection(cfg *configfile.Config) bool {
	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close() // Best effort cleanup
	return true
}

// doltServerPidFile returns the path to the PID file for the managed dolt server.
func doltServerPidFile(beadsDir string) string {
	return filepath.Join(beadsDir, "dolt", "dolt-server.pid")
}

// isDoltServerRunningByPid checks whether the process recorded in the PID file is alive.
func isDoltServerRunningByPid(pidFile string) (int, bool) {
	data, err := os.ReadFile(pidFile) // #nosec G304 - controlled path
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// Signal 0 checks process existence without actually signaling it.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	err = proc.Signal(syscall.Signal(0))
	return pid, err == nil
}

func startDoltServer(portOverride int) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// Verify dolt CLI is available
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: dolt CLI not found in PATH\n")
		fmt.Fprintf(os.Stderr, "Install dolt: https://docs.dolthub.com/introduction/installation\n")
		os.Exit(1)
	}

	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()
	if portOverride > 0 {
		port = portOverride
	}

	// Check if server already running via PID file
	pidFile := doltServerPidFile(beadsDir)
	if existingPid, alive := isDoltServerRunningByPid(pidFile); alive {
		// Verify it's actually listening on our port
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			_ = conn.Close()
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status":  "already_running",
					"pid":     existingPid,
					"host":    host,
					"port":    port,
					"message": "Dolt server is already running",
				})
			} else {
				fmt.Printf("Dolt server already running (PID %d) on %s\n", existingPid, addr)
			}
			return
		}
		// PID alive but not listening — stale PID file, clean up
		_ = os.Remove(pidFile)
	} else if existingPid > 0 {
		// PID file exists but process dead — clean up
		_ = os.Remove(pidFile)
	}

	// Determine the data directory: .beads/dolt/
	doltDir := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: Dolt data directory not found: %s\n", doltDir)
		fmt.Fprintf(os.Stderr, "Run 'bd init' first to initialize the beads repository.\n")
		os.Exit(1)
	}

	// Build dolt sql-server arguments
	args := []string{
		"sql-server",
		"--host", host,
		"--port", strconv.Itoa(port),
		"--no-auto-commit",
	}

	// Use --data-dir to serve all databases under the dolt directory
	args = append(args, "--data-dir", doltDir)

	cmd := exec.Command(doltBin, args...) // #nosec G204 - doltBin from LookPath
	cmd.Dir = doltDir

	// Redirect server output to a log file
	logPath := filepath.Join(beadsDir, "dolt", "dolt-server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600) // #nosec G304 - controlled path
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot create log file %s: %v\n", logPath, err)
		os.Exit(1)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Start in background
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		fmt.Fprintf(os.Stderr, "Error: failed to start dolt sql-server: %v\n", err)
		os.Exit(1)
	}
	_ = logFile.Close()

	pid := cmd.Process.Pid

	// Write PID file
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create PID file directory: %v\n", err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write PID file: %v\n", err)
	}

	// Detach the child process so it survives after bd exits
	go func() { _ = cmd.Wait() }()

	// Wait for the server to become ready
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ready := false
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			ready = true
			break
		}
	}

	if !ready {
		fmt.Fprintf(os.Stderr, "Warning: server started (PID %d) but not yet accepting connections on %s\n", pid, addr)
		fmt.Fprintf(os.Stderr, "Check log: %s\n", logPath)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":   "started",
			"pid":      pid,
			"host":     host,
			"port":     port,
			"log_file": logPath,
			"pid_file": pidFile,
		})
	} else {
		fmt.Printf("Dolt server started (PID %d) on %s\n", pid, addr)
	}
}

func stopDoltServer() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	pidFile := doltServerPidFile(beadsDir)
	pid, alive := isDoltServerRunningByPid(pidFile)

	if pid == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "not_running",
				"message": "No Dolt server PID file found",
			})
		} else {
			fmt.Println("No Dolt server running (no PID file found).")
		}
		return
	}

	if !alive {
		// Process already dead, just clean up
		_ = os.Remove(pidFile)
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "not_running",
				"pid":     pid,
				"message": "Server process already exited, cleaned up PID file",
			})
		} else {
			fmt.Printf("Server process (PID %d) already exited. Cleaned up PID file.\n", pid)
		}
		return
	}

	// Send SIGTERM for graceful shutdown
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not find process %d: %v\n", pid, err)
		_ = os.Remove(pidFile)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not stop server (PID %d): %v\n", pid, err)
		_ = os.Remove(pidFile)
		os.Exit(1)
	}

	// Wait for the process to exit (up to 10 seconds)
	stopped := false
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			stopped = true
			break
		}
	}

	_ = os.Remove(pidFile)

	if !stopped {
		// Force kill if graceful shutdown didn't work
		_ = proc.Signal(syscall.SIGKILL)
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "force_killed",
				"pid":     pid,
				"message": "Server did not stop gracefully, sent SIGKILL",
			})
		} else {
			fmt.Printf("Server (PID %d) did not stop gracefully, sent SIGKILL.\n", pid)
		}
		return
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":  "stopped",
			"pid":     pid,
			"message": "Dolt server stopped",
		})
	} else {
		fmt.Printf("Dolt server stopped (PID %d).\n", pid)
	}
}

// logDoltConfigChange appends an audit entry to .beads/dolt-config.log.
// Includes the beadsDir path for debugging worktree config pollution (bd-la2cl).
func logDoltConfigChange(beadsDir, key, value string) {
	logPath := filepath.Join(beadsDir, "dolt-config.log")
	actor := os.Getenv("BD_ACTOR")
	if actor == "" {
		actor = "unknown"
	}
	entry := fmt.Sprintf("%s actor=%s key=%s value=%s beads_dir=%s\n",
		time.Now().UTC().Format(time.RFC3339), actor, key, value, beadsDir)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // best effort
	}
	defer f.Close()
	_, _ = f.WriteString(entry)
}
