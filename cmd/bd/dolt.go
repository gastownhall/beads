//go:build cgo

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	Long: `Configure Dolt database settings for server mode connections.

Dolt can run in two modes:
  - embedded: In-process database (default, single-process only)
  - server:   Connect to external dolt sql-server (multi-process, high-concurrency)

Commands:
  bd dolt show         Show current Dolt configuration with connection test
  bd dolt set <k> <v>  Set a configuration value
  bd dolt test         Test server connection

Configuration keys for 'bd dolt set':
  mode      Connection mode: "embedded" or "server"
  database  Database name (default: issue prefix or "beads")
  host      Server host (default: 127.0.0.1)
  port      Server port (default: 3307)
  user      MySQL user (default: root)

Flags for 'bd dolt set':
  --update-config  Also write to config.yaml for team-wide defaults

Examples:
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

func init() {
	doltSetCmd.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltCmd.AddCommand(doltShowCmd)
	doltCmd.AddCommand(doltSetCmd)
	doltCmd.AddCommand(doltTestCmd)
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

// logDoltConfigChange appends an audit entry to .beads/dolt-config.log.
func logDoltConfigChange(beadsDir, key, value string) {
	logPath := filepath.Join(beadsDir, "dolt-config.log")
	actor := os.Getenv("BD_ACTOR")
	if actor == "" {
		actor = "unknown"
	}
	entry := fmt.Sprintf("%s actor=%s key=%s value=%s\n",
		time.Now().UTC().Format(time.RFC3339), actor, key, value)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // best effort
	}
	defer f.Close()
	_, _ = f.WriteString(entry)
}
