package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// Available backends with descriptions
var availableBackends = []struct {
	Name        string
	Description string
}{
	{"sqlite", "SQLite database (default) - single file, portable, no dependencies"},
	{"dolt", "Dolt database - Git-like versioning for data, SQL interface"},
	{"jsonl", "JSONL only (--no-db mode) - plain text, no database required"},
}

var backendCmd = &cobra.Command{
	Use:     "backend",
	GroupID: "sync",
	Short:   "Manage storage backend configuration",
	Long: `Manage storage backend configuration.

The backend determines how beads stores issue data:
  - sqlite: Default SQLite database (single file, portable)
  - dolt:   Dolt database (Git-like versioning, SQL interface)
  - jsonl:  JSONL only mode (plain text, use with --no-db flag)

The backend is set at initialization time with 'bd init --backend <type>'.
To change backends, use 'bd migrate dolt' or reinitialize.

Commands:
  bd backend list   List available backends
  bd backend show   Show current backend configuration`,
}

var backendListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available storage backends",
	Long: `List all available storage backends and their descriptions.

Available backends:
  sqlite  SQLite database (default) - single file, portable, no dependencies
  dolt    Dolt database - Git-like versioning for data, SQL interface
  jsonl   JSONL only (--no-db mode) - plain text, no database required

The backend is chosen at initialization time:
  bd init                    # Uses sqlite (default)
  bd init --backend dolt     # Uses dolt
  bd init --no-db            # Uses jsonl only`,
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			backends := make([]map[string]string, len(availableBackends))
			for i, b := range availableBackends {
				backends[i] = map[string]string{
					"name":        b.Name,
					"description": b.Description,
				}
			}
			outputJSON(map[string]interface{}{
				"backends": backends,
			})
			return
		}

		fmt.Println("Available backends:")
		for _, b := range availableBackends {
			fmt.Printf("  %-8s %s\n", b.Name, b.Description)
		}
		fmt.Println("\nSet backend at init time: bd init --backend <name>")
		fmt.Println("Migrate to Dolt: bd migrate dolt")
	},
}

var backendShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current backend configuration",
	Long: `Show the current storage backend configuration.

Displays:
  - Current backend type (sqlite, dolt, or jsonl)
  - Backend-specific settings (e.g., Dolt server mode)
  - Database location`,
	Run: func(cmd *cobra.Command, args []string) {
		// Find the beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "Error: not in a beads repository (no .beads directory found)\n")
			os.Exit(1)
		}

		// Check for no-db mode first
		if noDb || isNoDbModeConfigured(beadsDir) {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"backend":     "jsonl",
					"description": "JSONL only (no database)",
					"beads_dir":   beadsDir,
					"jsonl_file":  filepath.Join(beadsDir, "issues.jsonl"),
				})
				return
			}
			fmt.Println("Current backend: jsonl")
			fmt.Println("  Mode: JSONL only (no database)")
			fmt.Printf("  Beads dir: %s\n", beadsDir)
			fmt.Printf("  JSONL file: %s\n", filepath.Join(beadsDir, "issues.jsonl"))
			return
		}

		// Load metadata.json
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
				"backend":   backend,
				"beads_dir": beadsDir,
				"database":  cfg.DatabasePath(beadsDir),
			}

			// Add Dolt-specific info
			if backend == configfile.BackendDolt {
				result["dolt_mode"] = cfg.GetDoltMode()
				if cfg.IsDoltServerMode() {
					result["dolt_server_host"] = cfg.GetDoltServerHost()
					result["dolt_server_port"] = cfg.GetDoltServerPort()
					result["dolt_server_user"] = cfg.GetDoltServerUser()
					result["dolt_database"] = cfg.GetDoltDatabase()
				}
			}

			outputJSON(result)
			return
		}

		// Text output
		fmt.Printf("Current backend: %s\n", backend)

		// Find description
		for _, b := range availableBackends {
			if b.Name == backend {
				fmt.Printf("  Description: %s\n", b.Description)
				break
			}
		}

		fmt.Printf("  Beads dir: %s\n", beadsDir)
		fmt.Printf("  Database: %s\n", cfg.DatabasePath(beadsDir))

		// Dolt-specific info
		if backend == configfile.BackendDolt {
			fmt.Printf("  Dolt mode: %s\n", cfg.GetDoltMode())
			if cfg.IsDoltServerMode() {
				fmt.Printf("  Server: %s:%d\n", cfg.GetDoltServerHost(), cfg.GetDoltServerPort())
				fmt.Printf("  User: %s\n", cfg.GetDoltServerUser())
				fmt.Printf("  Database: %s\n", cfg.GetDoltDatabase())
			}
		}
	},
}

func init() {
	backendCmd.AddCommand(backendListCmd)
	backendCmd.AddCommand(backendShowCmd)
	rootCmd.AddCommand(backendCmd)
}
