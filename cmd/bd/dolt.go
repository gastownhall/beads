package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
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
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/ui"
	"golang.org/x/term"
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

Remote management:
  bd dolt remote add <name> <url>   Add a Dolt remote
  bd dolt remote list                List configured remotes
  bd dolt remote remove <name>       Remove a Dolt remote

Configuration keys for 'bd dolt set':
  database  Database name (default: issue prefix or "beads")
  host      Server host (default: 127.0.0.1)
  port      Server port (auto-detected; override with bd dolt set port <N>)
  user      MySQL user (default: root)
  data-dir  Custom dolt data directory (absolute path; default: .beads/dolt)

Flags for 'bd dolt set':
  --update-config  Also write to config.yaml for team-wide defaults

Examples:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject
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
  port      Server port (auto-detected; override with bd dolt set port <N>)
  user      MySQL user (default: root)
  data-dir  Custom dolt data directory (absolute path; default: .beads/dolt)

Use --update-config to also write to config.yaml for team-wide defaults.

Examples:
  bd dolt set database myproject
  bd dolt set host 192.168.1.100
  bd dolt set port 3307 --update-config
  bd dolt set data-dir /home/user/.beads-dolt/myproject`,
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

		if doltserver.IsDaemonManagedFor(beadsDir) {
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
on the next bd command unless auto-start is disabled.

Under Gas Town, the server is managed by the gt daemon and cannot be stopped
via bd. Use 'gt dolt stop' instead.`,
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

		// Write our PID and ensure cleanup on all exit paths
		pidFile := filepath.Join(beadsDir, "dolt-monitor.pid")
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600)
		defer os.Remove(pidFile)

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

		// Handle SIGTERM gracefully (os.Exit doesn't run defers)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-sigCh
			_ = os.Remove(pidFile)
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
			if doltserver.IsDaemonManagedFor(beadsDir) {
				fmt.Fprintf(os.Stderr, "\nUnder Gas Town, use 'gt dolt' commands to manage the server.\n")
			}
			os.Exit(1)
		}

		if len(killed) == 0 {
			fmt.Println("No orphan dolt servers found.")
		} else {
			fmt.Printf("Killed %d orphan dolt server(s): %v\n", len(killed), killed)
		}
	},
}

// staleDatabasePrefixes identifies test/polecat databases that should not persist
// on the production Dolt server. These accumulate from interrupted test runs and
// terminated polecats, wasting server memory.
// - testdb_*: BEADS_TEST_MODE=1 FNV hash of temp paths
// - doctest_*: doctor test helpers
// - doctortest_*: doctor test helpers
// - beads_pt*: gastown patrol_helpers_test.go random prefixes
// - beads_vr*: gastown mail/router_test.go random prefixes
// - beads_t[0-9a-f]*: protocol test random prefixes (t + 8 hex chars)
var staleDatabasePrefixes = []string{"testdb_", "doctest_", "doctortest_", "beads_pt", "beads_vr", "beads_t"}

var doltCleanDatabasesCmd = &cobra.Command{
	Use:   "clean-databases",
	Short: "Drop stale test/polecat databases from the Dolt server",
	Long: `Identify and drop leftover test and polecat databases that accumulate
on the shared Dolt server from interrupted test runs and terminated polecats.

Stale database prefixes: testdb_*, doctest_*, doctortest_*, beads_pt*, beads_vr*, beads_t*

These waste server memory and can degrade performance under concurrent load.
Use --dry-run to see what would be dropped without actually dropping.`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Connect directly to the Dolt server via config instead of getStore(),
		// which isn't initialized for dolt subcommands (beads-9vt).
		db, cleanup := openDoltServerConnection()
		defer cleanup()

		listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer listCancel()

		rows, err := db.QueryContext(listCtx, "SHOW DATABASES")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing databases: %v\n", err)
			os.Exit(1)
		}
		defer rows.Close()

		var stale []string
		for rows.Next() {
			var dbName string
			if err := rows.Scan(&dbName); err != nil {
				continue
			}
			for _, prefix := range staleDatabasePrefixes {
				if strings.HasPrefix(dbName, prefix) {
					stale = append(stale, dbName)
					break
				}
			}
		}

		if len(stale) == 0 {
			fmt.Println("No stale databases found.")
			return
		}

		fmt.Printf("Found %d stale databases:\n", len(stale))
		for _, name := range stale {
			fmt.Printf("  %s\n", name)
		}

		if dryRun {
			fmt.Println("\n(dry run — no databases dropped)")
			return
		}

		fmt.Println()
		dropped := 0
		failures := 0
		consecutiveTimeouts := 0
		const (
			batchSize         = 5 // Drop this many before pausing
			batchPause        = 2 * time.Second
			backoffPause      = 10 * time.Second
			timeoutThreshold  = 3 // Consecutive timeouts before backoff
			perDropTimeout    = 30 * time.Second
			maxConsecFailures = 10 // Stop after this many consecutive failures
		)

		for i, name := range stale {
			// Circuit breaker: back off when server is overwhelmed
			if consecutiveTimeouts >= timeoutThreshold {
				fmt.Fprintf(os.Stderr, "  ⚠ %d consecutive timeouts — backing off %s\n",
					consecutiveTimeouts, backoffPause)
				time.Sleep(backoffPause)
				consecutiveTimeouts = 0
			}

			// Stop if too many consecutive failures — server is likely unhealthy
			if failures >= maxConsecFailures {
				fmt.Fprintf(os.Stderr, "\n✗ Aborting: %d consecutive failures suggest server is unhealthy.\n", failures)
				fmt.Fprintf(os.Stderr, "  Dropped %d/%d before stopping.\n", dropped, len(stale))
				os.Exit(1)
			}

			// Per-operation timeout: DROP DATABASE can be slow on Dolt
			dropCtx, dropCancel := context.WithTimeout(context.Background(), perDropTimeout)
			// name is from SHOW DATABASES — safe to use in backtick-quoted identifier
			_, err := db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE `%s`", name)) //nolint:gosec // G201: name from SHOW DATABASES
			dropCancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  FAIL: %s: %v\n", name, err)
				failures++
				if isTimeoutError(err) {
					consecutiveTimeouts++
				}
			} else {
				fmt.Printf("  Dropped: %s\n", name)
				dropped++
				failures = 0
				consecutiveTimeouts = 0
			}

			// Rate limiting: pause between batches to let the server breathe
			if (i+1)%batchSize == 0 && i+1 < len(stale) {
				fmt.Printf("  [%d/%d] pausing %s...\n", i+1, len(stale), batchPause)
				time.Sleep(batchPause)
			}
		}
		fmt.Printf("\nDropped %d/%d stale databases.\n", dropped, len(stale))
	},
}

// confirmOverwrite prompts the user to confirm overwriting an existing remote.
// Returns true if the user confirms. Returns true without prompting if stdin is
// not a terminal (non-interactive/CI contexts).
func confirmOverwrite(surface, name, existingURL, newURL string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Printf("  Remote %q already exists on %s: %s\n", name, surface, existingURL)
	fmt.Printf("  Overwrite with: %s\n", newURL)
	fmt.Print("  Overwrite? (y/N): ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// --- Dolt remote management commands ---

var doltRemoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage Dolt remotes",
	Long: `Manage Dolt remotes for push/pull replication.

Subcommands:
  add <name> <url>   Add a new remote
  list               List all configured remotes
  remove <name>      Remove a remote`,
}

var doltRemoteAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Add a Dolt remote (both SQL server and CLI)",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		name, url := args[0], args[1]
		dbPath := st.Path()

		// Check existing remotes on both surfaces
		sqlRemotes, _ := st.ListRemotes(ctx)
		var sqlURL string
		for _, r := range sqlRemotes {
			if r.Name == name {
				sqlURL = r.URL
				break
			}
		}
		cliURL := doltutil.FindCLIRemote(dbPath, name)

		// Prompt for overwrite if either surface already has this remote
		if sqlURL != "" && sqlURL != url {
			if !confirmOverwrite("SQL server", name, sqlURL, url) {
				fmt.Println("Canceled.")
				return
			}
			// Remove existing SQL remote before re-adding
			if err := st.RemoveRemote(ctx, name); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing existing SQL remote: %v\n", err)
				os.Exit(1)
			}
		}
		if cliURL != "" && cliURL != url {
			if !confirmOverwrite("CLI (filesystem)", name, cliURL, url) {
				fmt.Println("Canceled.")
				return
			}
			if err := doltutil.RemoveCLIRemote(dbPath, name); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing existing CLI remote: %v\n", err)
				os.Exit(1)
			}
		}

		// Add to SQL server (skip if already correct)
		if sqlURL != url {
			if err := st.AddRemote(ctx, name, url); err != nil {
				if jsonOutput {
					outputJSONError(err, "remote_add_failed")
				} else {
					fmt.Fprintf(os.Stderr, "Error adding SQL remote: %v\n", err)
				}
				os.Exit(1)
			}
		}

		// Add to CLI filesystem (skip if already correct)
		if cliURL != url {
			if err := doltutil.AddCLIRemote(dbPath, name, url); err != nil {
				// Non-fatal: SQL remote was added successfully
				fmt.Fprintf(os.Stderr, "Warning: SQL remote added but CLI remote failed: %v\n", err)
				fmt.Fprintf(os.Stderr, "Run: cd %s && dolt remote add %s %s\n",
				doltutil.ShellQuote(dbPath), doltutil.ShellQuote(name), doltutil.ShellQuote(url))
			}
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"name": name,
				"url":  url,
			})
		} else {
			fmt.Printf("Added remote %q → %s (SQL + CLI)\n", name, url)
		}
	},
}

var doltRemoteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured Dolt remotes (SQL server + CLI)",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		dbPath := st.Path()

		sqlRemotes, sqlErr := st.ListRemotes(ctx)
		if sqlErr != nil {
			if jsonOutput {
				outputJSONError(sqlErr, "remote_list_failed")
			} else {
				fmt.Fprintf(os.Stderr, "Error listing SQL remotes: %v\n", sqlErr)
			}
			os.Exit(1)
		}

		cliRemotes, cliErr := doltutil.ListCLIRemotes(dbPath)

		// Build unified view
		type unifiedRemote struct {
			Name   string `json:"name"`
			SQLURL string `json:"sql_url,omitempty"`
			CLIURL string `json:"cli_url,omitempty"`
			Status string `json:"status"` // "ok", "sql_only", "cli_only", "conflict"
		}

		seen := map[string]*unifiedRemote{}
		var names []string
		for _, r := range sqlRemotes {
			u := &unifiedRemote{Name: r.Name, SQLURL: r.URL}
			seen[r.Name] = u
			names = append(names, r.Name)
		}
		if cliErr == nil {
			for _, r := range cliRemotes {
				if u, ok := seen[r.Name]; ok {
					u.CLIURL = r.URL
				} else {
					seen[r.Name] = &unifiedRemote{Name: r.Name, CLIURL: r.URL}
					names = append(names, r.Name)
				}
			}
		}

		// Classify each remote
		hasDiscrepancy := false
		var unified []unifiedRemote
		for _, name := range names {
			u := seen[name]
			switch {
			case u.SQLURL != "" && u.CLIURL != "" && u.SQLURL == u.CLIURL:
				u.Status = "ok"
			case u.SQLURL != "" && u.CLIURL != "" && u.SQLURL != u.CLIURL:
				u.Status = "conflict"
				hasDiscrepancy = true
			case u.SQLURL != "" && u.CLIURL == "":
				u.Status = "sql_only"
				hasDiscrepancy = true
			case u.SQLURL == "" && u.CLIURL != "":
				u.Status = "cli_only"
				hasDiscrepancy = true
			}
			unified = append(unified, *u)
		}

		if jsonOutput {
			outputJSON(unified)
			return
		}

		if len(unified) == 0 {
			fmt.Println("No remotes configured.")
			return
		}

		for _, u := range unified {
			url := u.SQLURL
			if url == "" {
				url = u.CLIURL
			}
			switch u.Status {
			case "ok":
				fmt.Printf("%-20s %s\n", u.Name, url)
			case "sql_only":
				fmt.Printf("%-20s %s  %s\n", u.Name, url, ui.RenderWarn("[SQL only]"))
			case "cli_only":
				fmt.Printf("%-20s %s  %s\n", u.Name, url, ui.RenderWarn("[CLI only]"))
			case "conflict":
				fmt.Printf("%-20s %s\n", u.Name, ui.RenderFail("[CONFLICT]"))
				fmt.Printf("%-20s   SQL: %s\n", "", u.SQLURL)
				fmt.Printf("%-20s   CLI: %s\n", "", u.CLIURL)
			}
		}

		if cliErr != nil {
			fmt.Printf("\n%s Could not read CLI remotes: %v\n", ui.RenderWarn("⚠"), cliErr)
		}
		if hasDiscrepancy {
			fmt.Printf("\n%s Remote discrepancies detected. Run 'bd doctor --fix' to resolve.\n", ui.RenderWarn("⚠"))
		}
	},
}

var doltRemoteRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a Dolt remote (both SQL server and CLI)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			fmt.Fprintf(os.Stderr, "Error: no store available\n")
			os.Exit(1)
		}
		name := args[0]
		dbPath := st.Path()

		// Check both surfaces for conflicts
		sqlRemotes, _ := st.ListRemotes(ctx)
		var sqlURL string
		for _, r := range sqlRemotes {
			if r.Name == name {
				sqlURL = r.URL
				break
			}
		}
		cliURL := doltutil.FindCLIRemote(dbPath, name)

		// Refuse removal if URLs conflict — user must resolve first
		forceRemove, _ := cmd.Flags().GetBool("force")
		if sqlURL != "" && cliURL != "" && sqlURL != cliURL && !forceRemove {
			fmt.Fprintf(os.Stderr, "Error: remote %q has conflicting URLs:\n", name)
			fmt.Fprintf(os.Stderr, "  SQL: %s\n  CLI: %s\n", sqlURL, cliURL)
			fmt.Fprintf(os.Stderr, "\nResolve the conflict first. To force remove from both:\n")
			fmt.Fprintf(os.Stderr, "  bd dolt remote remove %s --force\n", name)
			os.Exit(1)
		}

		// Remove from SQL server
		if sqlURL != "" {
			if err := st.RemoveRemote(ctx, name); err != nil {
				if jsonOutput {
					outputJSONError(err, "remote_remove_failed")
				} else {
					fmt.Fprintf(os.Stderr, "Error removing SQL remote: %v\n", err)
				}
				os.Exit(1)
			}
		}

		// Remove from CLI filesystem
		if cliURL != "" {
			if err := doltutil.RemoveCLIRemote(dbPath, name); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: SQL remote removed but CLI remote failed: %v\n", err)
				fmt.Fprintf(os.Stderr, "Run: cd %s && dolt remote remove %s\n",
				doltutil.ShellQuote(dbPath), doltutil.ShellQuote(name))
			}
		}

		if sqlURL == "" && cliURL == "" {
			fmt.Fprintf(os.Stderr, "Error: remote %q not found on either surface\n", name)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"name":    name,
				"removed": true,
			})
		} else {
			fmt.Printf("Removed remote %q (SQL + CLI)\n", name)
		}
	},
}

// isTimeoutError checks if an error is a context deadline exceeded or timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if err == context.DeadlineExceeded {
		return true
	}
	// Check for net.Error timeout (covers TCP and MySQL driver timeouts)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Also catch wrapped context.DeadlineExceeded
	return errors.Is(err, context.DeadlineExceeded)
}

func init() {
	doltSetCmd.Flags().Bool("update-config", false, "Also write to config.yaml for team-wide defaults")
	doltStopCmd.Flags().Bool("force", false, "Force stop even when managed by Gas Town daemon")
	doltPushCmd.Flags().Bool("force", false, "Force push (overwrite remote changes)")
	doltCommitCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	doltIdleMonitorCmd.Flags().String("beads-dir", "", "Path to .beads directory")
	doltCleanDatabasesCmd.Flags().Bool("dry-run", false, "Show what would be dropped without dropping")
	doltRemoteRemoveCmd.Flags().Bool("force", false, "Force remove even when SQL and CLI URLs conflict")
	doltRemoteCmd.AddCommand(doltRemoteAddCmd)
	doltRemoteCmd.AddCommand(doltRemoteListCmd)
	doltRemoteCmd.AddCommand(doltRemoteRemoveCmd)
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
	doltCmd.AddCommand(doltCleanDatabasesCmd)
	doltCmd.AddCommand(doltRemoteCmd)
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

	// Resolve actual server port for connection testing
	showHost := cfg.GetDoltServerHost()
	dsCfg := doltserver.DefaultConfig(beadsDir)
	showPort := dsCfg.Port

	if jsonOutput {
		result := map[string]interface{}{
			"backend": backend,
		}
		if backend == configfile.BackendDolt {
			result["database"] = cfg.GetDoltDatabase()
			result["host"] = showHost
			result["port"] = showPort
			result["user"] = cfg.GetDoltServerUser()
			if testConnection {
				result["connection_ok"] = testServerConnection(showHost, showPort)
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
	fmt.Printf("  Host:     %s\n", showHost)
	fmt.Printf("  Port:     %d\n", showPort)
	fmt.Printf("  User:     %s\n", cfg.GetDoltServerUser())

	if testConnection {
		fmt.Println()
		if testServerConnection(showHost, showPort) {
			fmt.Printf("  %s\n", ui.RenderPass("✓ Server connection OK"))
		} else {
			fmt.Printf("  %s\n", ui.RenderWarn("✗ Server not reachable"))
		}
	}

	// Show remotes from both surfaces
	doltDir := doltserver.ResolveDoltDir(beadsDir)
	dbName := cfg.GetDoltDatabase()
	dbDir := filepath.Join(doltDir, dbName)
	fmt.Println("\nRemotes:")
	ctx := context.Background()
	st := getStore()
	var sqlRemotes []string
	if st != nil {
		if remotes, err := st.ListRemotes(ctx); err == nil {
			for _, r := range remotes {
				sqlRemotes = append(sqlRemotes, fmt.Sprintf("  %-16s %s", r.Name, r.URL))
			}
		}
	}
	cliRemotes, cliErr := doltutil.ListCLIRemotes(dbDir)
	if len(sqlRemotes) == 0 && (cliErr != nil || len(cliRemotes) == 0) {
		fmt.Println("  (none)")
	} else {
		// Show SQL remotes
		if len(sqlRemotes) > 0 {
			for _, line := range sqlRemotes {
				fmt.Println(line)
			}
		}
		// Flag CLI-only remotes
		if cliErr == nil {
			sqlNames := map[string]bool{}
			if st != nil {
				if remotes, err := st.ListRemotes(ctx); err == nil {
					for _, r := range remotes {
						sqlNames[r.Name] = true
					}
				}
			}
			for _, r := range cliRemotes {
				if !sqlNames[r.Name] {
					fmt.Printf("  %-16s %s  %s\n", r.Name, r.URL, ui.RenderWarn("[CLI only]"))
				}
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

	case "data-dir":
		if value == "" {
			// Allow clearing the custom data dir (revert to default .beads/dolt)
			cfg.DoltDataDir = ""
		} else {
			if !filepath.IsAbs(value) {
				fmt.Fprintf(os.Stderr, "Error: data-dir must be an absolute path\n")
				os.Exit(1)
			}
			cfg.DoltDataDir = value
		}
		yamlKey = "dolt.data-dir"

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown key '%s'\n", key)
		fmt.Fprintf(os.Stderr, "Valid keys: mode, database, host, port, user, data-dir\n")
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
	port := doltserver.DefaultConfig(beadsDir).Port
	addr := fmt.Sprintf("%s:%d", host, port)

	if jsonOutput {
		ok := testServerConnection(host, port)
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

	if testServerConnection(host, port) {
		fmt.Printf("%s\n", ui.RenderPass("✓ Connection successful"))
	} else {
		fmt.Printf("%s\n", ui.RenderWarn("✗ Connection failed"))
		fmt.Println("\nStart the server with: bd dolt start")
		os.Exit(1)
	}

	// Test remote connectivity
	st := getStore()
	if st == nil {
		return
	}
	ctx := context.Background()
	remotes, err := st.ListRemotes(ctx)
	if err != nil || len(remotes) == 0 {
		return
	}
	fmt.Println("\nRemote connectivity:")
	for _, r := range remotes {
		if doltutil.IsSSHURL(r.URL) {
			// Test SSH connectivity by parsing host from URL
			sshHost := extractSSHHost(r.URL)
			if sshHost != "" {
				fmt.Printf("  %s (%s)... ", r.Name, r.URL)
				if testSSHConnectivity(sshHost) {
					fmt.Printf("%s\n", ui.RenderPass("✓ reachable"))
				} else {
					fmt.Printf("%s\n", ui.RenderWarn("✗ unreachable"))
				}
			}
		} else if strings.HasPrefix(r.URL, "https://") || strings.HasPrefix(r.URL, "http://") {
			fmt.Printf("  %s (%s)... ", r.Name, r.URL)
			if testHTTPConnectivity(r.URL) {
				fmt.Printf("%s\n", ui.RenderPass("✓ reachable"))
			} else {
				fmt.Printf("%s\n", ui.RenderWarn("✗ unreachable"))
			}
		} else {
			fmt.Printf("  %s (%s)... skipped (no connectivity test for this scheme)\n", r.Name, r.URL)
		}
	}
}

// serverDialTimeout controls the TCP dial timeout for server connection tests.
// Tests may reduce this to avoid slow unreachable-host hangs in CI.
var serverDialTimeout = 3 * time.Second

func testServerConnection(host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := net.DialTimeout("tcp", addr, serverDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close() // Best effort cleanup
	return true
}

// extractSSHHost extracts the hostname from an SSH URL for connectivity testing.
func extractSSHHost(url string) string {
	// git+ssh://git@github.com/org/repo.git → github.com
	// ssh://git@github.com/org/repo.git → github.com
	// git@github.com:org/repo.git → github.com
	url = strings.TrimPrefix(url, "git+ssh://")
	url = strings.TrimPrefix(url, "ssh://")
	if idx := strings.Index(url, "@"); idx >= 0 {
		url = url[idx+1:]
	}
	// Handle colon-separated (git@host:path) or slash-separated (ssh://host/path)
	if idx := strings.Index(url, ":"); idx >= 0 && !strings.Contains(url[:idx], "/") {
		return url[:idx]
	}
	if idx := strings.Index(url, "/"); idx >= 0 {
		return url[:idx]
	}
	return url
}

// testSSHConnectivity tests if an SSH host is reachable on port 22.
func testSSHConnectivity(host string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "22"), 5*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// httpURLToTCPAddr extracts a TCP dial address (host:port) from an HTTP(S) URL.
// Handles IPv6 addresses correctly (e.g., https://[::1]:8080/path).
func httpURLToTCPAddr(url string) string {
	host := url
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	defaultPort := "443"
	if strings.HasPrefix(url, "http://") {
		defaultPort = "80"
	}
	// Use net.SplitHostPort to correctly handle IPv6 addresses (which
	// contain colons that would otherwise be confused with host:port).
	if h, p, err := net.SplitHostPort(host); err == nil {
		return net.JoinHostPort(h, p)
	}
	// No port in host string. Strip IPv6 brackets if present so
	// JoinHostPort can re-add them correctly.
	h := strings.TrimPrefix(host, "[")
	h = strings.TrimSuffix(h, "]")
	return net.JoinHostPort(h, defaultPort)
}

// testHTTPConnectivity tests if an HTTP(S) URL is reachable via TCP.
func testHTTPConnectivity(url string) bool {
	addr := httpURLToTCPAddr(url)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// openDoltServerConnection opens a direct MySQL connection to the Dolt server
// using config from the beads directory. This bypasses getStore() which isn't
// initialized for dolt subcommands (beads-9vt). Connects without selecting a
// database so callers can operate on all databases (SHOW DATABASES, DROP DATABASE).
func openDoltServerConnection() (*sql.DB, func()) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintln(os.Stderr, "Error: not in a beads repository (no .beads directory found)")
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

	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	user := cfg.GetDoltServerUser()
	password := os.Getenv("BEADS_DOLT_PASSWORD")

	var connStr string
	if password != "" {
		connStr = fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, password, host, port)
	} else {
		connStr = fmt.Sprintf("%s@tcp(%s:%d)/?parseTime=true&timeout=5s",
			user, host, port)
	}

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to Dolt server: %v\n", err)
		os.Exit(1)
	}

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Second)

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		fmt.Fprintf(os.Stderr, "Error: cannot reach Dolt server at %s:%d: %v\n", host, port, err)
		fmt.Fprintln(os.Stderr, "Start the server with: bd dolt start")
		os.Exit(1)
	}

	return db, func() { _ = db.Close() }
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
