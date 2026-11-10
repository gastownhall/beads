package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// `bd dolt drop-database` is the recovery half of #6099: `bd init` adopting a
// git origin's issue history leaves a database on the Dolt server with no CLI
// path to remove it, and deleting a workspace's .beads directory never
// removes the database from a shared server. This command drops exactly one
// named database — with consent, without purging — so an unwanted adoption
// can be undone without reaching for the `dolt` CLI.

// protectedDropDatabases are the schemas the Dolt server itself needs; no
// invocation may drop them. SHOW DATABASES always lists them, so a name
// taken from there (or guessed) can arrive here.
var protectedDropDatabases = []string{"mysql", "information_schema", "performance_schema"}

// dropDatabaseRefusal is one guard's verdict: what bd refuses to do and the
// actionable way out. Extracted so the guard logic is a pure function with
// direct unit coverage, like shouldPurgeDroppedDatabases.
type dropDatabaseRefusal struct {
	message string
	hint    string
}

// dropDatabaseGuardInput carries everything the guards know about a candidate
// name before any server round-trip: the workspace's own configured database,
// the shared-server global database, and the --database/--global overrides
// for this invocation.
type dropDatabaseGuardInput struct {
	name              string
	workspaceDatabase string
	globalDatabase    string
	globalFlag        bool
	databaseFlag      string
}

// checkDropDatabaseGuard refuses the drops bd must not perform: system
// schemas, the shared-server global database, this invocation's --database
// target, the workspace's own configured database (dropping it would break
// every bd command here), and stale-test names (clean-databases owns those).
// It returns nil when the drop may proceed to the existence probe.
func checkDropDatabaseGuard(in dropDatabaseGuardInput) *dropDatabaseRefusal {
	name := strings.TrimSpace(in.name)
	if name == "" {
		return &dropDatabaseRefusal{
			message: "no database name given",
			hint:    "Usage: bd dolt drop-database <name> — run `dolt sql -q 'SHOW DATABASES'` (or bd dolt status) to list the databases on this server.",
		}
	}
	for _, sys := range protectedDropDatabases {
		if name == sys {
			return &dropDatabaseRefusal{
				message: fmt.Sprintf("refusing to drop the system database %q", name),
				hint:    "mysql, information_schema and performance_schema are required by the Dolt server itself and cannot be dropped.",
			}
		}
	}
	if in.globalFlag {
		return &dropDatabaseRefusal{
			message: "refusing: --global targets the shared global database for this invocation",
			hint:    "drop-database removes a single project database; run without --global.",
		}
	}
	if in.globalDatabase != "" && name == in.globalDatabase {
		return &dropDatabaseRefusal{
			message: fmt.Sprintf("refusing to drop the global shared-server database %q", name),
			hint:    "The global database holds cross-project data for every workspace on the server. Drop the specific project databases you no longer need instead.",
		}
	}
	if in.databaseFlag != "" && name == in.databaseFlag {
		return &dropDatabaseRefusal{
			message: fmt.Sprintf("refusing: %q is the database this invocation targets via --database", name),
			hint:    "Run without --database, or drop it from another workspace.",
		}
	}
	if in.workspaceDatabase != "" && name == in.workspaceDatabase {
		return &dropDatabaseRefusal{
			message: fmt.Sprintf("refusing: %q is the database this workspace is initialized against", name),
			hint:    "Dropping it would break every bd command in this workspace. To abandon this workspace entirely, remove its .beads directory first, then drop the database from another workspace.",
		}
	}
	for _, prefix := range staleDatabasePrefixes {
		if strings.HasPrefix(name, prefix) {
			return &dropDatabaseRefusal{
				message: fmt.Sprintf("refusing: %q looks like a stale test database (prefix %q)", name, prefix),
				hint:    "Use `bd dolt clean-databases` instead — it finds and drops stale test databases in batches, with --dry-run and --purge-dropped.",
			}
		}
	}
	return nil
}

// showDatabasesNamesFn scans SHOW DATABASES on the live connection. It is a
// seam because versioncontrolops.DBConn returns *sql.Rows, which no in-memory
// fake can fabricate; tests stub the probe itself while the guard, consent
// and DROP paths run for real.
var showDatabasesNamesFn = func(ctx context.Context, conn versioncontrolops.DBConn) ([]string, error) {
	listCtx, cancel := context.WithTimeout(ctx, cleanDatabasesListTimeout)
	defer cancel()
	rows, err := conn.QueryContext(listCtx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return nil, err
		}
		names = append(names, dbName)
	}
	return names, rows.Err()
}

// databaseNameExists confirms the target is a database on this server before
// consent is asked for, so the prompt never offers to drop a name that is
// not there.
func databaseNameExists(ctx context.Context, conn versioncontrolops.DBConn, name string) (bool, error) {
	names, err := showDatabasesNamesFn(ctx, conn)
	if err != nil {
		return false, err
	}
	for _, candidate := range names {
		if candidate == name {
			return true, nil
		}
	}
	return false, nil
}

// dropDatabaseOptions carries everything dropDatabase needs besides the
// connection: the consent mode, the display endpoint, the --json wire shape
// and the confirmation prompt's input.
type dropDatabaseOptions struct {
	assumeYes   bool
	interactive bool
	endpoint    string
	json        bool
	stdin       io.Reader
}

// promptDropDatabaseConsentFn reads the answer to the (y/N) prompt. A seam so
// the answer table is unit-testable; EOF and read errors propagate to the
// caller, which refuses rather than guesses.
var promptDropDatabaseConsentFn = func(reader *bufio.Reader) (bool, error) {
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

// confirmDropDatabase is the consent gate: --yes consents ahead of time; a
// non-interactive run refuses and names the --yes remedy (the same shape as
// dolt remote reset-data); an interactive run is shown the database, the
// server endpoint and the recovery story before the default-NO prompt.
// Decline is "Canceled.", not an error.
func confirmDropDatabase(opts dropDatabaseOptions, name string) (bool, error) {
	if opts.assumeYes {
		return true, nil
	}
	if !opts.interactive {
		return false, HandleErrorWithHint(
			fmt.Sprintf("bd dolt drop-database %q drops a database from this Dolt server (%s)", name, opts.endpoint),
			"Re-run with --yes to confirm.")
	}
	fmt.Printf("This drops database %q from the Dolt server at %s:\n", name, opts.endpoint)
	fmt.Println("  Other workspaces using this database will no longer find it.")
	fmt.Printf("The database stays recoverable via CALL DOLT_UNDROP(%s) until a purge.\n", name)
	fmt.Print("Proceed? (y/N): ")
	granted, err := promptDropDatabaseConsentFn(bufio.NewReader(opts.stdin))
	if err != nil {
		return false, HandleError("reading confirmation: %v", err)
	}
	return granted, nil
}

// dropDatabase is the mode-independent body: guards, existence probe,
// consent, then exactly one DROP DATABASE — never a purge, so DOLT_UNDROP
// recovery stays available. Shared by the direct-server route and the
// proxied-server route.
func dropDatabase(ctx context.Context, conn versioncontrolops.DBConn, opts dropDatabaseOptions, guardIn dropDatabaseGuardInput) error {
	name := strings.TrimSpace(guardIn.name)
	guardIn.name = name
	if refusal := checkDropDatabaseGuard(guardIn); refusal != nil {
		return HandleErrorWithHint(refusal.message, refusal.hint)
	}
	exists, err := databaseNameExists(ctx, conn, name)
	if err != nil {
		return HandleError("listing databases: %v", err)
	}
	if !exists {
		return HandleErrorWithHint(
			fmt.Sprintf("database %q does not exist on this server", name),
			"Run `dolt sql -q 'SHOW DATABASES'` (or bd dolt status) to list the databases on this server.")
	}
	granted, err := confirmDropDatabase(opts, name)
	if err != nil {
		return err
	}
	if !granted {
		fmt.Println("Canceled.")
		return nil
	}
	dropCtx, cancel := context.WithTimeout(ctx, cleanDatabasesDropTimeout)
	defer cancel()
	//nolint:gosec // G201: identifier quoted+escaped via doltutil.QuoteIdentifierUnvalidated
	if _, err := conn.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE %s", doltutil.QuoteIdentifierUnvalidated(name))); err != nil {
		return HandleError("dropping database %q: %v", name, err)
	}
	if opts.json {
		return outputJSON(map[string]any{"dropped": name, "recoverable_via_undrop": true})
	}
	fmt.Printf("Dropped database %q on %s.\n", name, opts.endpoint)
	fmt.Println("Recoverable via CALL DOLT_UNDROP(name) until a purge; bd dolt clean-databases --purge-dropped purges")
	fmt.Println("ALL dropped databases on the server (server-global), not just this one.")
	return nil
}

var doltDropDatabaseYes bool

var doltDropDatabaseCmd = &cobra.Command{
	Use:   "drop-database <name>",
	Short: "Drop one named database from the Dolt server (consent required)",
	Long: `Drop a single named database from the Dolt server.

This is the way to remove a database bd created on the server — for example
one an unwanted 'bd init' adoption of a git origin brought in — because
deleting a workspace's .beads directory never removes the database from a
shared server. It refuses what no invocation should drop: system schemas,
the global shared-server database (beads_global), the database this
workspace is initialized against, and stale test databases ('bd dolt
clean-databases' owns those).

DROP DATABASE only marks a database as dropped; Dolt keeps its directory
under .dolt_dropped_databases/ so it stays recoverable via
CALL DOLT_UNDROP(name) until an explicit purge. This command never purges;
'bd dolt clean-databases --purge-dropped' does (server-global, irreversible).

Requires --yes (or an interactive confirmation) before dropping anything.

Examples:
  bd dolt drop-database old_project    # prompts for confirmation
  bd dolt drop-database old_project --yes    # no prompt (scripts, agents)`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("dolt drop-database")
		beadsDir := selectedDoltBeadsDir()
		if beadsDir == "" {
			return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
		}
		cfg, err := loadDoltBackendConfig(beadsDir)
		if err != nil {
			return HandleError("%v", err)
		}
		if !usesSQLServer() {
			return HandleError("'bd dolt drop-database' is not supported in embedded mode (no Dolt server)")
		}

		guardIn := dropDatabaseGuardInput{
			name:              strings.TrimSpace(args[0]),
			workspaceDatabase: cfg.GetDoltDatabase(),
			globalDatabase:    doltserver.GlobalDatabaseName,
			globalFlag:        globalFlag,
			databaseFlag:      databaseFlag,
		}
		assumeYes, _ := cmd.Flags().GetBool("yes")
		opts := dropDatabaseOptions{
			assumeYes:   assumeYes,
			interactive: term.IsTerminal(int(os.Stdin.Fd())),
			json:        jsonOutput,
			stdin:       os.Stdin,
		}

		if usesProxiedServer() {
			opts.endpoint = "the proxied bd server"
			return runDoltDropDatabaseProxied(rootCtx, beadsDir, opts, guardIn)
		}

		// Connect directly to the Dolt server via config instead of getStore(),
		// which isn't initialized for dolt subcommands (beads-9vt).
		db, cleanup, err := openDoltServerConnection()
		if err != nil {
			return err
		}
		defer cleanup()
		opts.endpoint = fmt.Sprintf("%s:%d", cfg.GetDoltServerHost(), doltserver.DialConfig(beadsDir).Port)
		return dropDatabase(rootCtx, db, opts, guardIn)
	},
}
