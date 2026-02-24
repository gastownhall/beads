package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/ui"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: "setup",
	Short:   "Configure Dolt database settings",
	Long: `Configure and manage Dolt database settings and server lifecycle.

Beads uses a dolt sql-server for all database operations. The server is
auto-started transparently when needed. Use these commands for explicit
control or diagnostics.

Server lifecycle:
  bd dolt start        Start the Dolt server for this project
  bd dolt stop         Stop the Dolt server for this project
  bd dolt status       Show Dolt server status

Configuration:
  bd dolt show         Show current Dolt configuration with connection test
  bd dolt set <k> <v>  Set a configuration value
  bd dolt test         Test server connection

Version control:
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

var doltStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Dolt SQL server for this project",
	Long: `Start a dolt sql-server for the current beads project.

The server runs in the background on a per-project port derived from the
project path. PID and logs are stored in .beads/.

The server auto-starts transparently when needed, so manual start is rarely
required. Use this command for explicit control or diagnostics.`,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
			os.Exit(1)
		}
		serverDir := doltserver.ResolveServerDir(beadsDir)

		if doltserver.IsDaemonManaged() {
			// Check if daemon's server is already accepting connections
			cfg := doltserver.DefaultConfig(serverDir)
			addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
			conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				fmt.Printf("Dolt server already running on port %d (managed by Gas Town daemon)\n", cfg.Port)
				return
			}
			fmt.Fprintf(os.Stderr, "Warning: Dolt server is normally managed by the Gas Town daemon,\n"+
				"but no server found on port %d. Starting one.\n\n", cfg.Port)
		}

		state, err := doltserver.Start(serverDir)
		if err != nil {
			if strings.Contains(err.Error(), "already running") {
				fmt.Println(err)
				return
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Dolt server started (PID %d, port %d)\n", state.PID, state.Port)
		fmt.Printf("  Data: %s\n", state.DataDir)
		fmt.Printf("  Logs: %s\n", doltserver.LogPath(serverDir))
	},
}

var doltStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Dolt SQL server for this project",
	Long: `Stop the dolt sql-server managed by beads for the current project.

This sends a graceful shutdown signal. The server will restart automatically
on the next bd command unless auto-start is disabled.`,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
			os.Exit(1)
		}
		serverDir := doltserver.ResolveServerDir(beadsDir)
		force, _ := cmd.Flags().GetBool("force")

		if err := doltserver.StopWithForce(serverDir, force); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Dolt server stopped.")
	},
}

var doltStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Dolt server status",
	Long: `Show the status of the dolt sql-server for the current project.

Displays whether the server is running, its PID, port, and data directory.`,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
			os.Exit(1)
		}
		serverDir := doltserver.ResolveServerDir(beadsDir)

		state, err := doltserver.IsRunning(serverDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(state)
			return
		}

		if state == nil || !state.Running {
			cfg := doltserver.DefaultConfig(serverDir)
			fmt.Println("Dolt server: not running")
			fmt.Printf("  Expected port: %d\n", cfg.Port)
			return
		}

		fmt.Println("Dolt server: running")
		fmt.Printf("  PID:  %d\n", state.PID)
		fmt.Printf("  Port: %d\n", state.Port)
		fmt.Printf("  Data: %s\n", state.DataDir)
		fmt.Printf("  Logs: %s\n", doltserver.LogPath(serverDir))
	},
}

var doltIdleMonitorCmd = &cobra.Command{
	Use:    "idle-monitor",
	Short:  "Run idle monitor (internal, not for direct use)",
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir, _ := cmd.Flags().GetString("beads-dir")
		if beadsDir == "" {
			beadsDir = beads.FindBeadsDir()
		}
		if beadsDir == "" {
			os.Exit(1)
		}

		// Write our PID
		_ = os.WriteFile(filepath.Join(beadsDir, "dolt-monitor.pid"),
			[]byte(strconv.Itoa(os.Getpid())), 0600)

		// Parse idle timeout from config
		idleTimeout := doltserver.DefaultIdleTimeout
		if v := config.GetYamlConfig("dolt.idle-timeout"); v != "" {
			if v == "0" {
				// Disabled
				return
			}
			if d, err := time.ParseDuration(v); err == nil {
				idleTimeout = d
			}
		}

		// Handle SIGTERM gracefully
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-sigCh
			_ = os.Remove(filepath.Join(beadsDir, "dolt-monitor.pid"))
			os.Exit(0)
		}()

		doltserver.RunIdleMonitor(beadsDir, idleTimeout)
	},
}

var doltKillallCmd = &cobra.Command{
	Use:   "killall",
	Short: "Kill all orphan Dolt server processes",
	Long: `Find and kill orphan dolt sql-server processes not tracked by the
canonical PID file.

Under Gas Town, the canonical server lives at $GT_ROOT/.beads/. Any other
dolt sql-server processes are considered orphans and will be killed.

In standalone mode, all dolt sql-server processes are killed except the
one tracked by the current project's PID file.`,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			beadsDir = "." // best effort
		}

		killed, err := doltserver.KillStaleServers(beadsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(killed) == 0 {
			fmt.Println("No orphan dolt servers found.")
		} else {
			fmt.Printf("Killed %d orphan dolt server(s): %v\n", len(killed), killed)
		}
	},
}

func init() {
	doltSetCmd.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltStopCmd.Flags().Bool("force", false, "Force stop even when managed by Gas Town daemon")
	doltPushCmd.Flags().Bool("force", false, "Force push (overwrite remote changes)")
	doltCommitCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	doltIdleMonitorCmd.Flags().String("beads-dir", "", "Path to .beads directory")
	doltCmd.AddCommand(doltShowCmd)
	doltCmd.AddCommand(doltSetCmd)
	doltCmd.AddCommand(doltTestCmd)
	doltCmd.AddCommand(doltCommitCmd)
	doltCmd.AddCommand(doltPushCmd)
	doltCmd.AddCommand(doltPullCmd)
	doltCmd.AddCommand(doltStartCmd)
	doltCmd.AddCommand(doltStopCmd)
	doltCmd.AddCommand(doltStatusCmd)
	doltCmd.AddCommand(doltIdleMonitorCmd)
	doltCmd.AddCommand(doltKillallCmd)
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
		fmt.Println("\nStart the server with: bd dolt start")
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
