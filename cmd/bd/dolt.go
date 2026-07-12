package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
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
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
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
	Use:           "show",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Show current Dolt configuration with connection status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showDoltConfig(true)
	},
}

var doltSetCmd = &cobra.Command{
	Use:           "set <key> <value>",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Set a Dolt configuration value",
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
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt set' is not supported in embedded mode (no Dolt server)")
		}
		key := args[0]
		value := args[1]
		updateConfig, _ := cmd.Flags().GetBool("update-config")
		return setDoltConfig(key, value, updateConfig)
	},
}

var doltTestCmd = &cobra.Command{
	Use:           "test",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Test connection to Dolt server",
	Long: `Test the connection to the configured Dolt server.

This verifies that:
  1. The server is reachable at the configured host:port
  2. The connection can be established

Use this before switching to server mode to ensure the server is running.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt test' is not supported in embedded mode (no Dolt server)")
		}
		return testDoltConnection()
	},
}

// isRemoteNotFoundErr checks whether the error is a Dolt "remote not found"
// error. This typically happens when the remote was added via `dolt remote add`
// (filesystem config) but not via `bd dolt remote add` (which also registers it
// in the SQL server's dolt_remotes table).
func isRemoteNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "remote") && strings.Contains(msg, "not found")
}

// remoteLister is the narrow store surface needed to confirm the structured
// no-remote-configured state.
type remoteLister interface {
	ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error)
}

// persistedRemoteProber is implemented by stores that can check on-disk
// remote persistence (.dolt/repo_state.json) independently of the SQL
// server's dolt_remotes table (server-mode DoltStore).
type persistedRemoteProber interface {
	HasPersistedRemote() bool
}

// isConfirmedNoRemote reports whether a push/pull failure is the benign
// "no remote configured" case that may exit 0. isRemoteNotFoundErr alone is a
// loose string match that also fires on deleted/renamed remote-side repos,
// missing remote branches, and typoed remote names — real sync failures that
// must keep a non-zero exit so agents and CI notice (bd-6dnrw.7). Only an
// actually-empty dolt_remotes table makes the skip safe; if the remotes can't
// be listed, treat the failure as real. An empty table alone is still not
// proof in server mode: a freshly auto-started sql-server can report empty
// dolt_remotes at cold start even though remotes are persisted on disk
// (GH#2118) — the same reason the remote-migrate gate reads repo_state.json
// directly — so the on-disk probe must agree before the skip fires
// (bd-578h9.10).
func isConfirmedNoRemote(ctx context.Context, st remoteLister, err error) bool {
	if !isRemoteNotFoundErr(err) {
		return false
	}
	remotes, listErr := st.ListRemotes(ctx)
	if listErr != nil || len(remotes) > 0 {
		return false
	}
	if prober, ok := st.(persistedRemoteProber); ok && prober.HasPersistedRemote() {
		return false
	}
	return true
}

// isDivergedHistoryErr checks whether the error indicates that local and remote
// Dolt histories have diverged. This happens when independent pushes create
// separate commit histories with no common merge base (e.g., two agents
// bootstrapping from scratch and pushing to the same remote, or a local
// database being re-initialized while the remote retains the old history).
func isDivergedHistoryErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no common ancestor") ||
		strings.Contains(msg, "can't find common ancestor") ||
		strings.Contains(msg, "cannot find common ancestor")
}

// isAncestorPKMismatchErr reports Dolt's hard refusal to merge a table whose
// primary key set differs across the merging histories or in their common
// ancestor. The classification lives in dberrors so the cross-upgrade merge
// test (internal/storage/dolt) can pin it against a real Dolt refusal; see
// dberrors.IsAncestorPKMismatch for the full background (#4259).
func isAncestorPKMismatchErr(err error) bool {
	return dberrors.IsAncestorPKMismatch(err)
}

// ancestorPKMismatchTable extracts the table name from a Dolt
// different-primary-keys merge refusal, or "" if it cannot be determined.
func ancestorPKMismatchTable(err error) string {
	return dberrors.AncestorPKMismatchTable(err)
}

// printAncestorPKMismatchGuidance prints recovery guidance when a Dolt merge
// is refused because a table's primary key set differs across the merging
// histories or in their common ancestor. Unlike row conflicts, this cannot be
// auto-resolved and does not converge on retry; the clones must be
// re-converged through one canonical clone.
func printAncestorPKMismatchGuidance(err error) {
	w := os.Stderr
	table := ancestorPKMismatchTable(err)
	fmt.Fprintln(w, "")
	if table != "" {
		fmt.Fprintf(w, "Dolt refused to merge: table %q has different primary keys across\n", table)
	} else {
		fmt.Fprintln(w, "Dolt refused to merge: a table has different primary keys across")
	}
	fmt.Fprintln(w, "the local and remote histories (or in their common ancestor).")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "This is a schema fork: two clones reshaped the table's primary key")
	fmt.Fprintln(w, "independently, usually by upgrading bd (and so running schema migrations)")
	fmt.Fprintln(w, "separately on each clone while un-synced changes existed on both sides.")
	fmt.Fprintln(w, "Retrying will not help — these histories can no longer be merged.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Recovery (bootstrap from one canonical clone):")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  1. Pick ONE clone as canonical (usually the most complete/up-to-date),")
	fmt.Fprintln(w, "     upgrade bd there, and make the remote authoritative:")
	fmt.Fprintln(w, "       bd dolt push --force")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  2. On EVERY other clone, save local-only work, re-clone, re-apply:")
	fmt.Fprintln(w, "       bd export --all -o /tmp/beads-local.jsonl")
	fmt.Fprintln(w, "       rm -rf .beads/dolt")
	fmt.Fprintln(w, "       bd bootstrap")
	fmt.Fprintln(w, "       bd import /tmp/beads-local.jsonl")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Full playbook (and how to prevent this during upgrades):")
	fmt.Fprintln(w, "  https://github.com/gastownhall/beads/blob/main/docs/RECOVERY.md#pk-fork-refused")
}

// printNoRemoteGuidance prints an informational message (to stdout) when
// push or pull is attempted but no Dolt remote is configured. Exits 0 because
// the absence of a remote is a valid configuration — not an error.
func printNoRemoteGuidance() {
	fmt.Println("No remote is configured — skipping.")
	fmt.Println("")
	fmt.Println("For solo use, pushing is optional — your issues are stored locally")
	fmt.Println("in .beads/ and versioned by Dolt automatically.")
	fmt.Println("")
	fmt.Println("To set up remote sync (for backup or team sharing):")
	fmt.Println("  bd dolt remote add origin <url>")
	fmt.Println("  bd dolt push")
	fmt.Println("")
	fmt.Println("Supported remote URLs:")
	fmt.Println("  • GitHub (via git):   git+ssh://git@github.com/org/repo.git")
	fmt.Println("  • DoltHub:            https://doltremoteapi.dolthub.com/org/repo")
	fmt.Println("  • Azure Blob Storage: az://account.blob.core.windows.net/container/path")
}

func adoptGitOriginRemoteForPush(ctx context.Context, st storage.DoltStorage) (bool, error) {
	remotes, err := st.ListRemotes(ctx)
	if err != nil {
		return false, err
	}
	if len(remotes) > 0 {
		return false, nil
	}
	beadsDir := selectedDoltBeadsDir()
	if beadsDir == "" {
		return false, fmt.Errorf("no active beads workspace")
	}
	originURL, err := gitOriginGetURLForActiveRepo(ctx)
	if err != nil || originURL == "" {
		return false, nil
	}
	remoteURL := normalizeRemoteURL(originURL)
	if err := st.AddRemote(ctx, "origin", remoteURL); err != nil {
		return false, err
	}

	if err := config.SetYamlConfigInDir(beadsDir, "sync.remote", remoteURL); err != nil {
		return false, fmt.Errorf("failed to persist sync.remote to config.yaml: %w", err)
	}
	commitBeadsConfigForActiveRepo(ctx, "bd: update sync.remote")
	return true, nil
}

// printDivergedHistoryGuidance prints recovery guidance when push/pull fails
// due to diverged local and remote histories.
func printDivergedHistoryGuidance(operation string) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Local and remote Dolt histories have diverged.")
	fmt.Fprintln(os.Stderr, "This means the local database and the remote have independent commit")
	fmt.Fprintln(os.Stderr, "histories with no common merge base.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Recovery options:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  1. Keep remote, discard local (recommended if remote is authoritative):")
	fmt.Fprintln(os.Stderr, "       bd bootstrap              # re-clone from remote")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  2. Keep local, overwrite remote (if local is authoritative):")
	fmt.Fprintln(os.Stderr, "       bd dolt push --force       # force-push local history to remote")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  3. Manual recovery (re-initialize local database):")
	fmt.Fprintln(os.Stderr, "       rm -rf .beads/dolt         # delete local Dolt database")
	fmt.Fprintln(os.Stderr, "       bd bootstrap              # re-clone from remote")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Tip: This usually happens when multiple agents independently initialize")
	fmt.Fprintln(os.Stderr, "databases and push to the same remote. Use 'bd bootstrap' to clone an")
	fmt.Fprintln(os.Stderr, "existing remote instead of 'bd init' to avoid divergent histories.")
}

var doltPushCmd = &cobra.Command{
	Use:           "push",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Push commits to Dolt remote",
	Long: `Push local Dolt commits to the configured remote.

Requires a Dolt remote to be configured in the database directory.
For Hosted Dolt, set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD environment
variables for authentication.

Use --force to overwrite remote changes (e.g., when the remote has
uncommitted changes in its working set).

Use --remote to push to a specific named remote instead of the default.
The remote must already exist (see 'bd dolt remote add').`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if config.GetBool("no-push") {
			fmt.Println("skipping push: rig is local-only (no-push: true)")
			return nil
		}
		if isDoltLocalOnly() {
			if jsonOutput {
				if err := outputJSONRaw(map[string]string{"status": "disabled", "reason": "dolt.local-only=true"}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return nil
			}
			fmt.Println("Remote sync is disabled for this project (dolt.local-only=true).")
			fmt.Println("Your issues are stored locally in .beads/.")
			fmt.Println("To re-enable remote sync: bd config unset dolt.local-only")
			return nil
		}
		ctx := context.Background()
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}
		force, _ := cmd.Flags().GetBool("force")
		remote, _ := cmd.Flags().GetString("remote")
		if remote != "" {
			fmt.Printf("Pushing to Dolt remote %q...\n", remote)
			if err := st.PushRemote(ctx, remote, force); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				if isRemoteNotFoundErr(err) {
					fmt.Fprintf(os.Stderr, "\nRemote %q is not configured.\n", remote)
					fmt.Fprintln(os.Stderr, "Use 'bd dolt remote add <name> <url>' to add it.")
					fmt.Fprintln(os.Stderr, "Use 'bd dolt remote list' to see configured remotes.")
				} else if isAncestorPKMismatchErr(err) {
					printAncestorPKMismatchGuidance(err)
				} else if isDivergedHistoryErr(err) {
					printDivergedHistoryGuidance("push --force")
				}
				return SilentExit()
			}
			fmt.Println("Push complete.")
			return nil
		}
		if adopted, err := adoptGitOriginRemoteForPush(ctx, st); err != nil {
			return HandleError("failed to adopt git origin as Dolt remote: %v", err)
		} else if adopted {
			fmt.Println("Configured Dolt remote origin from git origin.")
		}
		fmt.Println("Pushing to Dolt remote...")

		var pushErr error
		if force {
			pushErr = st.ForcePush(ctx)
		} else {
			pushErr = st.Push(ctx)
		}
		if pushErr != nil {
			if isConfirmedNoRemote(ctx, st, pushErr) {
				printNoRemoteGuidance()
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", pushErr)
			if isAncestorPKMismatchErr(pushErr) {
				printAncestorPKMismatchGuidance(pushErr)
			} else if isDivergedHistoryErr(pushErr) {
				op := "push"
				if force {
					op = "push --force"
				}
				printDivergedHistoryGuidance(op)
			}
			return SilentExit()
		}
		fmt.Println("Push complete.")
		return nil
	},
}

var doltPullCmd = &cobra.Command{
	Use:           "pull",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Pull commits from Dolt remote",
	Long: `Pull commits from the configured Dolt remote into the local database.

Requires a Dolt remote to be configured in the database directory.
For Hosted Dolt, set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD environment
variables for authentication.

Use --remote to pull from a specific named remote instead of the default.
The remote must already exist (see 'bd dolt remote add').`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if isDoltLocalOnly() {
			if jsonOutput {
				if err := outputJSONRaw(map[string]string{"status": "disabled", "reason": "dolt.local-only=true"}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return nil
			}
			fmt.Println("Remote sync is disabled for this project (dolt.local-only=true).")
			fmt.Println("Nothing to pull.")
			fmt.Println("To re-enable remote sync: bd config unset dolt.local-only")
			return nil
		}
		ctx := context.Background()
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}
		remote, _ := cmd.Flags().GetString("remote")
		if remote != "" {
			fmt.Printf("Pulling from Dolt remote %q...\n", remote)
			if err := st.PullRemote(ctx, remote); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				if isRemoteNotFoundErr(err) {
					fmt.Fprintf(os.Stderr, "\nRemote %q is not configured.\n", remote)
					fmt.Fprintln(os.Stderr, "Use 'bd dolt remote add <name> <url>' to add it.")
					fmt.Fprintln(os.Stderr, "Use 'bd dolt remote list' to see configured remotes.")
				} else if isAncestorPKMismatchErr(err) {
					printAncestorPKMismatchGuidance(err)
				} else if isDivergedHistoryErr(err) {
					printDivergedHistoryGuidance("pull")
				}
				return SilentExit()
			}
			fmt.Println("Pull complete.")
			return nil
		}
		fmt.Println("Pulling from Dolt remote...")
		if err := st.Pull(ctx); err != nil {
			if isConfirmedNoRemote(ctx, st, err) {
				printNoRemoteGuidance()
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			if isAncestorPKMismatchErr(err) {
				printAncestorPKMismatchGuidance(err)
			} else if isDivergedHistoryErr(err) {
				printDivergedHistoryGuidance("pull")
			}
			return SilentExit()
		}
		fmt.Println("Pull complete.")
		return nil
	},
}

// errExplicitCommitUnsupported returns a typed *storage.ErrUnsupported when the
// open store has no Dolt commit graph (Postgres/MySQL/SQLite), and nil for Dolt.
// The SQL backends deliberately expose a no-op Commit so internal write paths can
// call store.Commit() opportunistically without erroring; an EXPLICIT `bd dolt
// commit` / `bd vc commit` must not ride that no-op and print a fake "Committed."
// with no commit graph behind it. Internal opportunistic auto-commit is skipped
// separately in PostRun for these backends, so gating only the explicit commands
// keeps that path untouched.
func errExplicitCommitUnsupported(st storage.DoltStorage, op string) error {
	ncg, ok := storage.UnwrapStore(st).(storage.NonCommitGraphBackend)
	if !ok || !ncg.CommitGraphUnsupported() {
		return nil
	}
	backend := "non-dolt"
	if beadsDir := selectedDoltBeadsDir(); beadsDir != "" {
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
			if b := cfg.GetBackend(); b != "" && b != configfile.BackendDolt {
				backend = b
			}
		}
	}
	return &storage.ErrUnsupported{Op: op, Backend: backend}
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}
		if err := errExplicitCommitUnsupported(st, "dolt commit"); err != nil {
			return HandleError("%v", err)
		}
		msg, _ := cmd.Flags().GetString("message")
		if msg == "" {
			msg = fmt.Sprintf("bd: dolt commit (auto-commit) by %s", getActor())
		}
		if err := st.Commit(ctx, msg); err != nil {
			if isDoltNothingToCommit(err) {
				fmt.Println("Nothing to commit.")
				return nil
			}
			return HandleError("%v", err)
		}
		commandDidExplicitDoltCommit = true
		fmt.Println("Committed.")
		return nil
	},
}

var doltStartCmd = &cobra.Command{
	Use:           "start",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Start the Dolt SQL server for this project",
	Long: `Start a dolt sql-server for the current beads project.

The server runs in the background on a per-project port derived from the
project path. PID and logs are stored in .beads/.

The server auto-starts transparently when needed, so manual start is rarely
required. Use this command for explicit control or diagnostics.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt start' is not supported in embedded mode (no Dolt server)")
		}
		beadsDir := selectedDoltBeadsDir()
		if beadsDir == "" {
			return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
		}
		serverDir := doltserver.ResolveServerDir(beadsDir)

		state, err := doltserver.Start(serverDir)
		if err != nil {
			if strings.Contains(err.Error(), "already running") {
				fmt.Println(err)
				return nil
			}
			return HandleError("%v", err)
		}

		fmt.Printf("Dolt server started (PID %d, port %d)\n", state.PID, state.Port)
		fmt.Printf("  Data: %s\n", state.DataDir)
		fmt.Printf("  Logs: %s\n", doltserver.LogPath(serverDir))
		if doltserver.IsSharedServerMode() {
			fmt.Println("  Mode: shared server")
		}
		if doltserver.IsDebugMode() {
			fmt.Println("  Debug: on (loglevel=debug, --prof cpu)")
			fmt.Printf("  Profile dir: %s\n", doltserver.DebugProfileDir(beadsDir))
			fmt.Println("  Note: cpu.pprof is written when the server exits cleanly (bd dolt stop).")
		}
		return nil
	},
}

var doltStopCmd = &cobra.Command{
	Use:           "stop",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Stop the Dolt SQL server for this project",
	Long: `Stop the dolt sql-server managed by beads for the current project.

This sends a graceful shutdown signal. The server will restart automatically
on the next bd command unless auto-start is disabled.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt stop' is not supported in embedded mode (no Dolt server)")
		}
		beadsDir := selectedDoltBeadsDir()
		if beadsDir == "" {
			return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
		}
		serverDir := doltserver.ResolveServerDir(beadsDir)
		force, _ := cmd.Flags().GetBool("force")

		if err := doltserver.StopWithForce(serverDir, force); err != nil {
			return HandleError("%v", err)
		}
		fmt.Println("Dolt server stopped.")
		return nil
	},
}

var doltStatusCmd = &cobra.Command{
	Use:           "status",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Show Dolt engine status",
	Long: `Show the status of the Dolt engine for the current project.

In embedded mode, reports that the Dolt engine runs in-process and shows
the on-disk data directory. For beads-managed (local) servers, displays
PID, port, and data directory from the local PID file. For externally-
managed servers — a shared server (dolt.shared-server: true), a remote
dolt_server_host, or a local server managed outside bd (dolt.auto-start:
false, e.g. an orchestrator-shared sql-server) — pings the configured
endpoint via SQL and reports reachability, server version, and database.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		beadsDir := selectedDoltBeadsDir()
		if beadsDir == "" {
			return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
		}
		// A non-Dolt backend (postgres/mysql/sqlite) has no Dolt engine at all;
		// report the backend rather than misdescribing an embedded Dolt server
		// (parity with `bd dolt show`, which already special-cases this).
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.GetBackend() != configfile.BackendDolt {
			fmt.Printf("Backend: %s (no Dolt engine)\n", cfg.GetBackend())
			return nil
		}
		if !usesSQLServer() {
			showEmbeddedDoltStatus(beadsDir)
			return nil
		}

		// For externally-managed Dolt servers, the local PID file is
		// meaningless or absent — ping the configured endpoint via SQL
		// instead. Two flavors qualify:
		//   - non-local host (Hosted Dolt, remote shared sql-server, bd-q35w)
		//   - local host with auto-start disabled (an orchestrator or
		//     systemd manages the server lifecycle, be-0eyj)
		//
		// IsAutoStartDisabled reads the active (globally-bound) config and
		// BEADS_DOLT_AUTO_START env, not the per-beadsDir cfg loaded just
		// below. That coupling is intentional and consistent with every
		// other call site of IsAutoStartDisabled in this package — both
		// resolve against the same active workspace at command time.
		cfg, cfgErr := configfile.Load(beadsDir)
		if cfgErr != nil {
			// Don't silently swallow. A corrupted or missing metadata.json
			// would otherwise mask the externally-managed routing for both
			// the remote-host and auto-start-disabled-local cases, falling
			// through to the PID-file path with a misleading "not running"
			// — which is the exact failure mode this PR addresses.
			fmt.Fprintf(os.Stderr, "Warning: cannot load .beads config (%v); falling back to PID-file status path\n", cfgErr)
		}
		if cfg != nil && shouldUseExternalDoltStatus(cfg, doltserver.IsAutoStartDisabled(), doltserver.IsSharedServerMode()) {
			runExternalDoltStatus(beadsDir, cfg)
			return nil
		}

		serverDir := doltserver.ResolveServerDir(beadsDir)

		state, err := doltserver.IsRunning(serverDir)
		if err != nil {
			return HandleError("%v", err)
		}
		renderLocalDoltStatus(state, serverDir)
		return nil
	},
}

// renderLocalDoltStatus writes the bd-managed (local PID-file) status of
// the Dolt server to stdout, honoring jsonOutput. Extracted from the
// doltStatusCmd Run closure so the bd-managed output path is unit-testable
// without requiring a live dolt sql-server (the externally-managed path
// is exercised by TestRunExternalDoltStatus_Unreachable).
func renderLocalDoltStatus(state *doltserver.State, serverDir string) {
	if jsonOutput {
		if err := outputJSON(state); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
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
	if doltserver.IsSharedServerMode() {
		fmt.Println("  Mode: shared server")
	}
	if doltserver.IsDebugMode() {
		fmt.Println("  Debug: on (loglevel=debug, --prof cpu)")
		fmt.Printf("  Profile dir: %s\n", doltserver.DebugProfileDir(serverDir))
	}
	if isDoltLocalOnly() {
		fmt.Println("  Remote sync: disabled (dolt.local-only=true)")
	}
}

// shouldUseExternalDoltStatus reports whether bd dolt status should treat
// the server as externally-managed and probe via SQL instead of consulting
// the local PID file. Returns true when:
//   - shared-server mode is enabled (and not proxied) — a shared server's
//     lifecycle is owned by something other than bd (a Homebrew service,
//     systemd/launchd unit, or a sibling clone), so bd has no PID file for
//     it even when the host is local and auto-start is enabled. Without this
//     branch, status reports "not running" while bd CRUD commands, bd dolt
//     test, and bd dolt show all connect to the server fine (GH#3218). This
//     is checked BEFORE the server-mode guard because shared-server mode
//     wins over a stale metadata.json that still pins dolt_mode="embedded"
//     — mirroring the loadServerMode override in main.go (GH#2946). That
//     stale-metadata case (dolt.shared-server: true in config.yaml, which
//     IsDoltServerMode does not consult) is exactly where the residual
//     GH#3218 bug lived.
//   - dolt_mode=server with a non-local host (Hosted Dolt, remote shared
//     sql-server) — the PID file is on a different machine.
//   - dolt_mode=server with a local host but bd auto-start is disabled —
//     the server lifecycle is owned by something outside bd (e.g. an
//     orchestrator or systemd unit), so no bd PID file exists. Without
//     this branch, status reports "not running" even when bd CRUD
//     commands successfully connect to the server (be-0eyj).
//
// When false, the caller falls back to the PID-file path that reports
// PID, port, log path, and data directory for bd-managed servers.
//
// autoStartDisabled and sharedServerMode are passed in (rather than read
// here) so the predicate is pure and unit-testable without manipulating
// package-level config or process env.
func shouldUseExternalDoltStatus(cfg *configfile.Config, autoStartDisabled, sharedServerMode bool) bool {
	if cfg == nil {
		return false
	}
	// Shared-server mode wins even over an explicit metadata.json
	// dolt_mode="embedded" (loadServerMode override, main.go, GH#2946), so
	// this must precede the IsDoltServerMode guard — otherwise a workspace
	// with dolt.shared-server: true in config.yaml and stale embedded
	// metadata still falls through to the PID-file "not running" path.
	// Proxied-server mode is excluded, matching that override's !psm guard.
	if sharedServerMode && !cfg.IsDoltProxiedServerMode() {
		return true
	}
	if !cfg.IsDoltServerMode() {
		return false
	}
	if !isLocalHost(cfg.GetDoltServerHost()) {
		return true
	}
	return autoStartDisabled
}

// isLocalHost reports whether host refers to this machine. Used to
// distinguish beads-managed local servers from externally-hosted ones.
func isLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return true // empty defaults to local
	}
	switch h {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// runExternalDoltStatus queries an externally-hosted Dolt server and prints
// (or returns, for --json) status. Unlike the local path, there is no PID or
// log file — reachability, version, host/port/database, and TLS mode are the
// user-relevant signals.
func runExternalDoltStatus(beadsDir string, cfg *configfile.Config) {
	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()
	tls := cfg.GetDoltServerTLS()
	password := cfg.GetDoltServerPasswordForPort(port)

	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		TLS:      tls,
		Timeout:  5 * time.Second,
	}.String()

	result := map[string]interface{}{
		"mode":     "external",
		"host":     host,
		"port":     port,
		"user":     user,
		"database": database,
		"tls":      tls,
	}

	db, openErr := sql.Open("mysql", dsn)
	var running bool
	var version string
	var connErr error

	if openErr == nil {
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if pingErr := db.PingContext(ctx); pingErr != nil {
			connErr = pingErr
		} else {
			running = true
			// Best-effort version lookup; don't treat errors as fatal.
			_ = db.QueryRowContext(ctx, "SELECT @@version").Scan(&version)
		}
	} else {
		connErr = openErr
	}

	result["running"] = running
	if version != "" {
		result["version"] = version
	}
	if connErr != nil {
		result["error"] = connErr.Error()
	}

	if jsonOutput {
		if err := outputJSON(result); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	if running {
		fmt.Println("Dolt server: running (external)")
	} else {
		fmt.Println("Dolt server: not reachable (external)")
	}
	fmt.Printf("  Host:     %s\n", host)
	fmt.Printf("  Port:     %d\n", port)
	fmt.Printf("  Database: %s\n", database)
	fmt.Printf("  User:     %s\n", user)
	fmt.Printf("  TLS:      %t\n", tls)
	if version != "" {
		fmt.Printf("  Version:  %s\n", version)
	}
	if connErr != nil {
		fmt.Printf("  Error:    %v\n", connErr)
	}
}

// showEmbeddedDoltStatus reports Dolt engine status when running in
// embedded mode. There is no separate server process; the engine runs
// in-process and data lives at .beads/embeddeddolt/.
func showEmbeddedDoltStatus(beadsDir string) {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	dataDirExists := false
	if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
		dataDirExists = true
	}

	if jsonOutput {
		if err := outputJSON(map[string]interface{}{
			"mode": "embedded",
			// Embedded mode has an active in-process engine, but no
			// separate server process. Use a server-specific field so
			// clients do not read running=false as "Dolt is unavailable".
			"server_running":  false,
			"data_dir":        dataDir,
			"data_dir_exists": dataDirExists,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	fmt.Println("Dolt engine: embedded (in-process, no server)")
	fmt.Printf("  Data: %s\n", dataDir)
	if !dataDirExists {
		fmt.Printf("  %s\n", ui.RenderWarn("Data directory does not exist — run 'bd init' to create it"))
	}
	if isDoltLocalOnly() {
		fmt.Println("  Remote sync: disabled (dolt.local-only=true)")
	}
}

var doltKillallCmd = &cobra.Command{
	Use:   "killall",
	Short: "Kill all orphan Dolt server processes",
	Long: `Find and kill orphan dolt sql-server processes not tracked by the
canonical PID file for the current repo's Dolt data directory.

Under an orchestrator, the canonical server lives at $GT_ROOT/.beads/. Any other
dolt sql-server processes using that shared data directory are considered
orphans and will be killed.

In standalone mode, only dolt sql-server processes using the current
project's Dolt data directory are eligible for cleanup. Other projects'
servers are preserved.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt killall' is not supported in embedded mode (no Dolt server)")
		}
		beadsDir := selectedDoltBeadsDir()
		if beadsDir == "" {
			beadsDir = "." // best effort
		}

		killed, err := doltserver.KillStaleServers(beadsDir)
		if err != nil {
			return HandleError("%v", err)
		}

		if len(killed) == 0 {
			fmt.Println("No orphan dolt servers found.")
		} else {
			fmt.Printf("Killed %d orphan dolt server(s): %v\n", len(killed), killed)
		}
		return nil
	},
}

// staleDatabasePrefixes lists database name prefixes that
// `bd dolt clean-databases` will drop. This is the cleanup side of the
// test/prod split. Two sibling lists must converge with it (be-avn):
//   - internal/storage/dolt/store.go:testDatabasePrefixes (firewall side)
//   - .gc/system/packs/dolt/formulas/mol-dog-stale-db.toml (city formula)
//
// The firewall list, this cleanup list, and the formula list MUST
// converge — operators rely on consistent semantics across `bd dolt
// clean-databases`, the SQL-side firewall, and `gc dolt cleanup`.
//
// Origin of each prefix:
//   - testdb_     : applyConfigDefaults derives this for BEADS_TEST_MODE=1
//     without an explicit Database (FNV hash of cfg.Path).
//   - beads_test  : convention for hand-written integration tests.
//   - beads_pt    : property-test fixtures.
//   - beads_vr    : version-roundtrip / migration fixtures.
//   - doctest_    : `bd doctor` self-check fixtures.
//   - doctortest_ : older `bd doctor` fixture name (kept for back-compat).
//   - benchdb_    : per-bench scratch DBs (uniqueBenchDBName below,
//     format `benchdb_<pid>_<8 hex>`).
var staleDatabasePrefixes = []string{
	"testdb_",
	"beads_test",
	"beads_pt",
	"beads_vr",
	"doctest_",
	"doctortest_",
	"benchdb_",
}

var doltCleanDatabasesCmd = &cobra.Command{
	Use:           "clean-databases",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Drop stale test databases from the Dolt server",
	Long: `Identify and drop leftover test and agent databases that accumulate
on the shared Dolt server from interrupted test runs and terminated agents.

Stale database prefixes: testdb_*, beads_test*, beads_pt*, beads_vr*, doctest_*, doctortest_*, benchdb_*

These waste server memory and can degrade performance under concurrent load.
Use --dry-run to see what would be dropped without actually dropping.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !usesSQLServer() {
			return HandleError("'bd dolt clean-databases' is not supported in embedded mode (no Dolt server)")
		}
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Connect directly to the Dolt server via config instead of getStore(),
		// which isn't initialized for dolt subcommands (beads-9vt).
		db, cleanup, err := openDoltServerConnection()
		if err != nil {
			return err
		}
		defer cleanup()

		listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer listCancel()

		rows, err := db.QueryContext(listCtx, "SHOW DATABASES")
		if err != nil {
			return HandleError("listing databases: %v", err)
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
			return nil
		}

		fmt.Printf("Found %d stale databases:\n", len(stale))
		for _, name := range stale {
			fmt.Printf("  %s\n", name)
		}

		if dryRun {
			fmt.Println("\n(dry run — no databases dropped)")
			return nil
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
				return SilentExit()
			}

			// Per-operation timeout: DROP DATABASE can be slow on Dolt
			dropCtx, dropCancel := context.WithTimeout(context.Background(), perDropTimeout)
			// Escape backticks in database name to prevent SQL injection (` → ``)
			safeName := strings.ReplaceAll(name, "`", "``")
			_, err := db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE `%s`", safeName)) //nolint:gosec // G201: identifier-escaped
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
		return nil
	},
}

// --- Dolt remote management commands ---

type doltRemoteAddStore interface {
	ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error)
	AddRemote(ctx context.Context, name, url string) error
	RemoveRemote(ctx context.Context, name string) error
}

type doltRemoteAddResult struct {
	Canceled bool
}

type doltRemoteOverwriteConfirmer func(surface, name, existingURL, newURL string) bool

func confirmDoltRemoteOverwrite(surface, name, existingURL, newURL string) bool {
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

func findDoltRemoteURL(remotes []storage.RemoteInfo, name string) string {
	for _, remote := range remotes {
		if remote.Name == name {
			return remote.URL
		}
	}
	return ""
}

func ensureDoltRemote(ctx context.Context, st doltRemoteAddStore, name, url string, confirm doltRemoteOverwriteConfirmer) (doltRemoteAddResult, error) {
	remotes, err := st.ListRemotes(ctx)
	if err != nil {
		return doltRemoteAddResult{}, fmt.Errorf("list existing remotes: %w", err)
	}

	existingURL := findDoltRemoteURL(remotes, name)
	if existingURL == "" {
		if err := st.AddRemote(ctx, name, url); err != nil {
			return doltRemoteAddResult{}, fmt.Errorf("add remote %s: %w", name, err)
		}
		return doltRemoteAddResult{}, nil
	}

	if doltutil.RemoteURLsMatch(existingURL, url) {
		return doltRemoteAddResult{}, nil
	}

	if !confirm("SQL server", name, existingURL, url) {
		return doltRemoteAddResult{Canceled: true}, nil
	}
	if err := st.RemoveRemote(ctx, name); err != nil {
		return doltRemoteAddResult{}, fmt.Errorf("remove existing remote %s: %w", name, err)
	}
	if err := st.AddRemote(ctx, name, url); err != nil {
		return doltRemoteAddResult{}, fmt.Errorf("add remote %s: %w", name, err)
	}
	return doltRemoteAddResult{}, nil
}

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
	Use:           "add <name> <url>",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Add a Dolt remote",
	Args:          cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if isDoltLocalOnly() {
			fmt.Fprintln(os.Stderr, "Error: cannot add Dolt remote: remote sync is disabled (dolt.local-only=true).")
			fmt.Fprintln(os.Stderr, "To re-enable remote sync: bd config unset dolt.local-only")
			return SilentExit()
		}
		allowGitOrigin, _ := cmd.Flags().GetBool("allow-git-origin")
		if doltRemoteMatchesGitOrigin(args[1]) {
			if !allowGitOrigin {
				fmt.Fprintf(os.Stderr, "Error: refusing to add %q as a Dolt remote — this URL matches the git origin.\n", args[1])
				fmt.Fprintln(os.Stderr, "  Hint: use --allow-git-origin to proceed anyway (e.g. monorepo layout).")
				fmt.Fprintln(os.Stderr, "  Hint: or set dolt.local-only=true to disable remote sync entirely.")
				return SilentExit()
			}
			fmt.Fprintf(os.Stderr, "Warning: %q matches the git origin — proceeding because --allow-git-origin is set.\n", args[1])
		}
		ctx := context.Background()
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}
		name, url := args[0], args[1]

		result, err := ensureDoltRemote(ctx, st, name, url, confirmDoltRemoteOverwrite)
		if err != nil {
			if jsonOutput {
				_ = outputJSONError(err, "remote_add_failed")
			} else {
				fmt.Fprintf(os.Stderr, "Error adding remote: %v\n", err)
			}
			return SilentExit()
		}
		if result.Canceled {
			fmt.Println("Canceled.")
			return nil
		}

		if name == "origin" {
			if err := config.SetYamlConfig("sync.remote", url); err != nil {
				return HandleError("failed to persist sync.remote to config.yaml: %v", err)
			}
			if isGitRepo() {
				commitBeadsConfig("bd: update sync.remote")
			}
		}

		if jsonOutput {
			if err := outputJSON(map[string]interface{}{
				"name": name,
				"url":  url,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		} else {
			fmt.Printf("Added remote %q → %s\n", name, url)
		}
		return nil
	},
}

var doltRemoteListCmd = &cobra.Command{
	Use:           "list",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "List configured Dolt remotes",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}

		remotes, err := st.ListRemotes(ctx)
		if err != nil {
			if jsonOutput {
				_ = outputJSONError(err, "remote_list_failed")
			} else {
				fmt.Fprintf(os.Stderr, "Error listing remotes: %v\n", err)
			}
			return SilentExit()
		}

		if jsonOutput {
			if err := outputJSON(formatDoltRemoteListJSON(remotes)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			return nil
		}

		if len(remotes) == 0 {
			fmt.Println("No remotes configured.")
			return nil
		}

		for _, r := range remotes {
			fmt.Printf("%-20s %s\n", r.Name, r.URL)
		}
		return nil
	},
}

type doltRemoteListJSON struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SQLURL string `json:"sql_url,omitempty"`
	CLIURL string `json:"cli_url,omitempty"`
	Status string `json:"status"`
}

func formatDoltRemoteListJSON(remotes []storage.RemoteInfo) []doltRemoteListJSON {
	out := make([]doltRemoteListJSON, 0, len(remotes))
	for _, r := range remotes {
		out = append(out, doltRemoteListJSON{
			Name:   r.Name,
			URL:    r.URL,
			SQLURL: r.URL,
			Status: "ok",
		})
	}
	return out
}

var doltRemoteRemoveCmd = &cobra.Command{
	Use:           "remove <name>",
	Short:         "Remove a Dolt remote",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		name := args[0]

		if usesProxiedServer() {
			return runDoltRemoteRemoveProxied(ctx, name)
		}

		st := getStore()
		if st == nil {
			return HandleError("no store available")
		}

		if err := st.RemoveRemote(ctx, name); err != nil {
			if jsonOutput {
				_ = outputJSONError(err, "remote_remove_failed")
			} else {
				fmt.Fprintf(os.Stderr, "Error removing remote: %v\n", err)
			}
			return SilentExit()
		}

		if name == "origin" {
			if current := config.GetYamlConfig("sync.remote"); current != "" {
				if err := config.UnsetYamlConfig("sync.remote"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to clear sync.remote from config.yaml: %v\n", err)
				}
				if isGitRepo() {
					commitBeadsConfig("bd: clear sync.remote")
				}
			}
		}

		if jsonOutput {
			if err := outputJSON(map[string]interface{}{
				"name":    name,
				"removed": true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		} else {
			fmt.Printf("Removed remote %q\n", name)
		}
		return nil
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
	doltStopCmd.Flags().Bool("force", false, "Force stop the server")
	doltPushCmd.Flags().Bool("force", false, "Force push (overwrite remote changes)")
	doltPushCmd.Flags().String("remote", "", "Push to a specific named remote instead of the default")
	doltPullCmd.Flags().String("remote", "", "Pull from a specific named remote instead of the default")
	doltCommitCmd.Flags().StringP("message", "m", "", "Commit message (default: auto-generated)")
	doltCleanDatabasesCmd.Flags().Bool("dry-run", false, "Show what would be dropped without dropping")
	doltRemoteAddCmd.Flags().Bool("allow-git-origin", false, "Allow adding a Dolt remote whose URL matches the git origin (proceed with a warning instead of aborting)")
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
	doltCmd.AddCommand(doltKillallCmd)
	doltCmd.AddCommand(doltCleanDatabasesCmd)
	doltCmd.AddCommand(doltRemoteCmd)
	rootCmd.AddCommand(doltCmd)
}

func selectedDoltBeadsDir() string {
	beadsDir := ""
	if os.Getenv("BEADS_DIR") != "" {
		beadsDir = beads.FindBeadsDir()
	}
	if beadsDir == "" {
		beadsDir = selectedNoDBBeadsDir(nil)
	}
	if beadsDir == "" {
		return ""
	}
	prepareSelectedNoDBContext(beadsDir)
	return beadsDir
}

func showDoltConfig(testConnection bool) error {
	beadsDir := selectedDoltBeadsDir()
	if beadsDir == "" {
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return HandleError("loading config: %v", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	backend := cfg.GetBackend()
	embedded := !usesSQLServer()

	// Resolve actual server port for connection testing
	showHost := cfg.GetDoltServerHost()
	dsCfg := doltserver.DefaultConfig(beadsDir)
	showPort := dsCfg.Port
	embeddedDataDir := filepath.Join(beadsDir, "embeddeddolt")

	if jsonOutput {
		result := map[string]interface{}{
			"backend": backend,
		}
		if backend == configfile.BackendDolt {
			result["database"] = cfg.GetDoltDatabase()
			result["embedded"] = embedded
			if embedded {
				result["data_dir"] = embeddedDataDir
			} else {
				result["host"] = showHost
				result["port"] = showPort
				result["user"] = cfg.GetDoltServerUser()
				result["shared_server"] = doltserver.IsSharedServerMode()
				if testConnection {
					result["connection_ok"] = testServerConnection(showHost, showPort)
				}
			}
		}
		if err := outputJSON(result); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return nil
	}

	if backend != configfile.BackendDolt {
		fmt.Printf("Backend: %s\n", backend)
		return nil
	}

	fmt.Println("Dolt Configuration")
	fmt.Println("==================")
	fmt.Printf("  Database: %s\n", cfg.GetDoltDatabase())
	if embedded {
		fmt.Println("  Mode:     embedded (in-process Dolt engine)")
		fmt.Printf("  Data:     %s\n", embeddedDataDir)
	} else {
		fmt.Printf("  Host:     %s\n", showHost)
		fmt.Printf("  Port:     %d\n", showPort)
		fmt.Printf("  User:     %s\n", cfg.GetDoltServerUser())
		if doltserver.IsSharedServerMode() {
			fmt.Println("  Mode:     shared server")
			if sharedDir, err := doltserver.SharedServerDir(); err == nil {
				fmt.Printf("  Server:   %s\n", sharedDir)
			}
		} else {
			fmt.Println("  Mode:     per-project")
		}

		if testConnection {
			fmt.Println()
			if testServerConnection(showHost, showPort) {
				fmt.Printf("  %s\n", ui.RenderPass("✓ Server connection OK"))
			} else {
				fmt.Printf("  %s\n", ui.RenderWarn("✗ Server not reachable"))
			}
		}
	}

	fmt.Println("\nRemotes:")
	ctx := context.Background()
	st := getStore()
	var remotes []storage.RemoteInfo
	if st != nil {
		remotes, _ = st.ListRemotes(ctx)
	}
	if len(remotes) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, r := range remotes {
			fmt.Printf("  %-16s %s\n", r.Name, r.URL)
		}
	}

	// Show config sources
	fmt.Println("\nConfig sources (priority order):")
	fmt.Println("  1. Environment variables (BEADS_DOLT_*)")
	fmt.Println("  2. metadata.json (local, gitignored)")
	fmt.Println("  3. config.yaml (team defaults)")
	return nil
}

func setDoltConfig(key, value string, updateConfig bool) error {
	beadsDir := selectedDoltBeadsDir()
	if beadsDir == "" {
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return HandleError("loading config: %v", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	if cfg.GetBackend() != configfile.BackendDolt {
		return HandleError("not using Dolt backend")
	}

	var yamlKey string

	switch key {
	case "mode":
		// Mode will be configurable again when embedded Dolt support returns.
		// For now, server mode is required (embedded driver not yet re-integrated).
		return HandleError("mode is not yet configurable; embedded mode is coming soon")

	case "database":
		if value == "" {
			return HandleError("database name cannot be empty")
		}
		cfg.DoltDatabase = value
		yamlKey = "dolt.database"

	case "host":
		if value == "" {
			return HandleError("host cannot be empty")
		}
		cfg.DoltServerHost = value
		yamlKey = "dolt.host"

	case "port":
		port, err := strconv.Atoi(value)
		if err != nil || port <= 0 || port > 65535 {
			return HandleError("port must be a valid port number (1-65535)")
		}
		cfg.DoltServerPort = port
		yamlKey = "dolt.port"

	case "socket":
		// Empty value clears the socket (reverts to TCP host/port).
		cfg.DoltServerSocket = value
		yamlKey = "dolt.socket"

	case "user":
		if value == "" {
			return HandleError("user cannot be empty")
		}
		cfg.DoltServerUser = value
		yamlKey = "dolt.user"

	case "data-dir":
		// GH#2438: In server mode, data-dir has no effect on which database
		// the server connects to. Setting it silently switches the local
		// resolution path without affecting the running server, causing
		// commands to operate on the wrong (often empty) database.
		if value != "" && cfg.IsDoltServerMode() {
			fmt.Fprintf(os.Stderr, "Error: setting data-dir in server mode is not supported (GH#2438).\n")
			fmt.Fprintf(os.Stderr, "In server mode, the database is determined by the 'database' config key,\n")
			fmt.Fprintf(os.Stderr, "not the local data directory. Setting data-dir would silently disconnect\n")
			fmt.Fprintf(os.Stderr, "from the configured database '%s'.\n", cfg.GetDoltDatabase())
			fmt.Fprintf(os.Stderr, "\nTo change which database to use:\n")
			fmt.Fprintf(os.Stderr, "  bd dolt set database <name>\n")
			return SilentExit()
		}
		if value == "" {
			// Allow clearing the custom data dir (revert to default .beads/dolt)
			cfg.DoltDataDir = ""
		} else {
			if !filepath.IsAbs(value) {
				return HandleError("data-dir must be an absolute path")
			}
			cfg.DoltDataDir = value
			// Absolute paths are machine-specific and won't be persisted to
			// metadata.json (which is committed to git). Use the env var for
			// persistence across sessions. (GH#2251)
			fmt.Fprintf(os.Stderr, "Note: absolute paths are not saved to metadata.json (it propagates via git).\n")
			fmt.Fprintf(os.Stderr, "For persistence, add to your shell profile:\n")
			fmt.Fprintf(os.Stderr, "  export BEADS_DOLT_DATA_DIR=%s\n", value)
		}
		yamlKey = "dolt.data-dir"

	case "shared-server":
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" {
			return HandleError("shared-server must be 'true' or 'false'")
		}
		// shared-server is yaml-only (not stored in metadata.json)
		if err := config.SetYamlConfig("dolt.shared-server", lower); err != nil {
			return HandleError("setting shared-server: %v", err)
		}
		if jsonOutput {
			if err := outputJSON(map[string]interface{}{
				"key":      "shared-server",
				"value":    lower,
				"location": "config.yaml",
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			return nil
		}
		if lower == "true" {
			fmt.Println("Shared server mode enabled.")
			fmt.Println("All projects will use a single Dolt server at ~/.beads/shared-server/.")
			fmt.Println("Each project's data remains isolated in its own database.")
		} else {
			fmt.Println("Shared server mode disabled. Each project will use its own Dolt server.")
		}
		return nil

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown key '%s'\n", key)
		fmt.Fprintf(os.Stderr, "Valid keys: database, host, port, socket, user, data-dir, shared-server\n")
		return SilentExit()
	}

	// Audit log: record who changed what
	logDoltConfigChange(beadsDir, key, value)

	// Save to metadata.json
	if err := cfg.Save(beadsDir); err != nil {
		return HandleError("saving config: %v", err)
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
		if err := outputJSON(result); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return nil
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
	return nil
}

func testDoltConnection() error {
	beadsDir := selectedDoltBeadsDir()
	if beadsDir == "" {
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return HandleError("loading config: %v", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	if cfg.GetBackend() != configfile.BackendDolt {
		return HandleError("not using Dolt backend")
	}

	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	addr := fmt.Sprintf("%s:%d", host, port)

	if jsonOutput {
		ok := testServerConnection(host, port)
		if err := outputJSON(map[string]interface{}{
			"host":          host,
			"port":          port,
			"connection_ok": ok,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		if !ok {
			return SilentExit()
		}
		return nil
	}

	fmt.Printf("Testing connection to %s...\n", addr)

	if testServerConnection(host, port) {
		fmt.Printf("%s\n", ui.RenderPass("✓ Connection successful"))
	} else {
		fmt.Printf("%s\n", ui.RenderWarn("✗ Connection failed"))
		fmt.Println("\nStart the server with: bd dolt start")
		return SilentExit()
	}

	// Test remote connectivity
	st := getStore()
	if st == nil {
		return nil
	}
	ctx := context.Background()
	remotes, err := st.ListRemotes(ctx)
	if err != nil || len(remotes) == 0 {
		return nil
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
	return nil
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
func openDoltServerConnection() (*sql.DB, func(), error) {
	beadsDir := selectedDoltBeadsDir()
	if beadsDir == "" {
		return nil, nil, HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, nil, HandleError("loading config: %v", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	user := cfg.GetDoltServerUser()
	password := os.Getenv("BEADS_DOLT_PASSWORD")

	connStr := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		TLS:      cfg.GetDoltServerTLS(),
	}.String()

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, nil, HandleError("connecting to Dolt server: %v", err)
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
		return nil, nil, SilentExit()
	}

	return db, func() { _ = db.Close() }, nil
}

// doltServerPidFile returns the path to the PID file for the managed dolt server.
// logDoltConfigChange appends an audit entry to .beads/dolt-config.log.
// Includes the beadsDir path for debugging worktree config pollution (bd-la2cl).
func logDoltConfigChange(beadsDir, key, value string) {
	logPath := filepath.Join(beadsDir, "dolt-config.log")
	actor := os.Getenv("BEADS_ACTOR")
	if actor == "" {
		actor = os.Getenv("BD_ACTOR") // deprecated fallback
	}
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
