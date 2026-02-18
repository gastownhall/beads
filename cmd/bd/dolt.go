//go:build cgo

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	dolt "github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: "setup",
	Short:   "Configure Dolt database settings",
	Long: `Configure and manage Dolt database settings and server lifecycle.

Dolt can run in two modes:
  - embedded: In-process database (default, single-process only)
  - server:   Connect to external dolt sql-server (multi-process, high-concurrency)

Commands:
  bd dolt show         Show current Dolt configuration with connection test
  bd dolt set <k> <v>  Set a configuration value
  bd dolt test         Test server connection
  bd dolt start        Start a Dolt SQL server (background process)
  bd dolt stop         Stop the running Dolt SQL server
  bd dolt commit       Commit pending changes
  bd dolt push         Push commits to Dolt remote
  bd dolt pull         Pull commits from Dolt remote

Configuration keys for 'bd dolt set':
  mode      Connection mode: "embedded" or "server"
  database  Database name (default: issue prefix or "beads")
  host      Server host (default: 127.0.0.1)
  port      Server port (default: 3307)
  user      MySQL user (default: root)

Flags for 'bd dolt set':
  --update-config  Also write to config.yaml for team-wide defaults

Examples:
  bd dolt start                              Start server with configured settings
  bd dolt stop                               Stop the running server
  bd dolt set mode server
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
  mode      Connection mode: "embedded" or "server"
  database  Database name for server mode
  host      Server host (default: 127.0.0.1)
  port      Server port (default: 3307)
  user      MySQL user (default: root)

Use --update-config to also write to config.yaml for team-wide defaults.

Examples:
  bd dolt set mode server
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

var doltStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a Dolt SQL server using configured settings",
	Long: `Start a Dolt SQL server as a background process.

Uses the host, port, and user from your Dolt configuration (see 'bd dolt show').
The server runs in the background and persists after bd exits.

Configuration sources (priority order):
  1. Environment variables (BEADS_DOLT_*)
  2. metadata.json (bd dolt set)
  3. config.yaml (team defaults)`,
	Run: func(cmd *cobra.Command, args []string) {
		startDoltServer()
	},
}

var doltStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running Dolt SQL server",
	Long: `Stop the Dolt SQL server started by 'bd dolt start'.

Sends a graceful shutdown signal (SIGTERM). If the server doesn't stop
within 10 seconds, it is forcefully terminated.`,
	Run: func(cmd *cobra.Command, args []string) {
		stopDoltServer()
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

var doltCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Create a Dolt commit from pending changes",
	Long: `Create a Dolt commit from any uncommitted changes in the working set.

This is useful before push operations that require a clean working set.
Normally, auto-commit handles this after each bd write command, but manual
commit may be needed if auto-commit was off or changes were made externally.

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
			msg = "bd: manual commit (dolt commit)"
		}
		if err := st.Commit(ctx, msg); err != nil {
			errLower := strings.ToLower(err.Error())
			if strings.Contains(errLower, "nothing to commit") || strings.Contains(errLower, "no changes") {
				fmt.Println("Nothing to commit.")
				return
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Committed.")
	},
}

func init() {
	doltSetCmd.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltPushCmd.Flags().Bool("force", false, "Force push (overwrite remote changes)")
	doltCommitCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	doltCmd.AddCommand(doltShowCmd)
	doltCmd.AddCommand(doltSetCmd)
	doltCmd.AddCommand(doltTestCmd)
	doltCmd.AddCommand(doltStartCmd)
	doltCmd.AddCommand(doltStopCmd)
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
			result["mode"] = cfg.GetDoltMode()
			result["database"] = cfg.GetDoltDatabase()
			result["host"] = cfg.GetDoltServerHost()
			result["port"] = cfg.GetDoltServerPort()
			result["user"] = cfg.GetDoltServerUser()
			if cfg.IsDoltServerMode() && testConnection {
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
	fmt.Printf("  Mode:     %s\n", cfg.GetDoltMode())
	fmt.Printf("  Database: %s\n", cfg.GetDoltDatabase())

	if cfg.IsDoltServerMode() {
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
		if value != configfile.DoltModeEmbedded && value != configfile.DoltModeServer {
			fmt.Fprintf(os.Stderr, "Error: mode must be '%s' or '%s'\n",
				configfile.DoltModeEmbedded, configfile.DoltModeServer)
			os.Exit(1)
		}
		cfg.DoltMode = value
		yamlKey = "dolt.mode"

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
		fmt.Println("\nYou can now use server mode:")
		fmt.Println("  bd dolt set mode server")
	} else {
		fmt.Printf("%s\n", ui.RenderWarn("✗ Connection failed"))
		fmt.Println("\nMake sure dolt sql-server is running:")
		fmt.Printf("  cd /path/to/dolt/db && dolt sql-server --port=%d\n", port)
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

func startDoltServer() {
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

	dataDir := filepath.Join(beadsDir, "dolt")
	logFile := filepath.Join(beadsDir, "dolt-server.log")
	host := cfg.GetDoltServerHost()
	port := cfg.GetDoltServerPort()
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()

	// Check if server is already running
	if pid := dolt.GetRunningServerPID(dataDir); pid > 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "server_already_running",
				"message": fmt.Sprintf("Dolt server already running (PID %d)", pid),
				"pid":     pid,
				"host":    host,
				"port":    port,
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: Dolt server already running (PID %d)\n", pid)
			fmt.Fprintf(os.Stderr, "Stop it first: bd dolt stop\n")
		}
		os.Exit(1)
	}

	// Check if data directory exists
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "data_dir_not_found",
				"message": fmt.Sprintf("Dolt data directory not found: %s", dataDir),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: Dolt data directory not found: %s\n", dataDir)
			fmt.Fprintf(os.Stderr, "Run 'bd init' first to create the database.\n")
		}
		os.Exit(1)
	}

	if !jsonOutput {
		fmt.Println("Starting Dolt SQL server...")
		fmt.Printf("  Host:     %s\n", host)
		fmt.Printf("  Port:     %d\n", port)
		fmt.Printf("  User:     %s\n", user)
		fmt.Printf("  Database: %s\n", database)
		fmt.Printf("  Data dir: %s\n", dataDir)
		fmt.Printf("  Log file: %s\n", logFile)
		fmt.Println()
		fmt.Print("Waiting for server to accept connections...")
	}

	server := dolt.NewServer(dolt.ServerConfig{
		DataDir:           dataDir,
		SQLPort:           port,
		Host:              host,
		LogFile:           logFile,
		User:              user,
		DisableRemotesAPI: true, // remotesapi only needed for federation
	})

	if err := server.Start(context.Background()); err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "start_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Println() // finish the "Waiting..." line
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintf(os.Stderr, "Check the log file for details: %s\n", logFile)
		}
		os.Exit(1)
	}
	if !jsonOutput {
		fmt.Println() // finish the "Waiting..." line
	}

	pid := dolt.GetRunningServerPID(dataDir)

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":   "started",
			"pid":      pid,
			"host":     host,
			"port":     port,
			"user":     user,
			"database": database,
			"data_dir": dataDir,
			"log_file": logFile,
		})
		return
	}

	fmt.Printf("  %s\n", ui.RenderPass(fmt.Sprintf("✓ Server started (PID %d)", pid)))
	fmt.Println()
	if !cfg.IsDoltServerMode() {
		fmt.Println("To use server mode:")
		fmt.Println("  bd dolt set mode server")
		fmt.Println()
	}
	fmt.Println("To stop the server:")
	fmt.Println("  bd dolt stop")
}

func stopDoltServer() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
		os.Exit(1)
	}

	cfg, _ := configfile.Load(beadsDir)

	dataDir := filepath.Join(beadsDir, "dolt")

	pid := dolt.GetRunningServerPID(dataDir)
	if pid == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "not_running",
				"message": "No Dolt server is running",
			})
		} else {
			fmt.Println("No Dolt server is running.")
		}
		return
	}

	if !jsonOutput {
		fmt.Printf("Stopping Dolt SQL server (PID %d)...\n", pid)
	}

	if err := dolt.StopServerByPID(pid); err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "stop_failed",
				"message": err.Error(),
				"pid":     pid,
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error stopping server: %v\n", err)
		}
		os.Exit(1)
	}

	// Clean up PID file
	pidFile := filepath.Join(dataDir, "dolt-server.pid")
	_ = os.Remove(pidFile)

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status": "stopped",
			"pid":    pid,
		})
		return
	}

	fmt.Printf("%s\n", ui.RenderPass("✓ Server stopped"))

	// Warn if still in server mode
	if cfg != nil && cfg.IsDoltServerMode() {
		fmt.Println()
		fmt.Println("Note: You are still in server mode. bd commands will fail")
		fmt.Println("until the server is restarted or you switch to embedded mode:")
		fmt.Println("  bd dolt set mode embedded")
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
