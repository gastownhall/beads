package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

const dropDatabaseTimeout = 30 * time.Second

// dropDatabaseResult is the --json shape of bd dolt drop-database.
type dropDatabaseResult struct {
	Dropped              string `json:"dropped"`
	RecoverableViaUndrop bool   `json:"recoverable_via_undrop"`
}

// dropDatabaseRequest is one named drop. confirm is nil when the caller
// cannot prompt; assumeYes is --yes.
type dropDatabaseRequest struct {
	name       string
	configured string
	assumeYes  bool
	confirm    func(name string) (bool, error)
}

// dropDatabaseRefusal is a drop that did not run, with the next step.
type dropDatabaseRefusal struct {
	message string
	hint    string
}

func (r *dropDatabaseRefusal) Error() string { return r.message }

// dropNamedDatabase drops exactly the named database after consent. It never
// purges: Dolt keeps the dropped database recoverable with DOLT_UNDROP until
// something runs DOLT_PURGE_DROPPED_DATABASES, which is server-global.
func dropNamedDatabase(ctx context.Context, conn versioncontrolops.DBConn, req dropDatabaseRequest) (dropDatabaseResult, error) {
	existing, err := listServerDatabases(ctx, conn)
	if err != nil {
		return dropDatabaseResult{}, fmt.Errorf("listing databases: %w", err)
	}
	name := ""
	for _, n := range existing {
		if strings.EqualFold(n, req.name) {
			name = n
			break
		}
	}
	if name == "" {
		return dropDatabaseResult{}, &dropDatabaseRefusal{
			message: fmt.Sprintf("database %s does not exist on this Dolt server", remedy.PrintedValue(req.name)),
			hint:    "Run 'bd dolt status' or SHOW DATABASES to see the databases this server holds.",
		}
	}

	if !req.assumeYes {
		ok := false
		if req.confirm != nil {
			if ok, err = req.confirm(name); err != nil {
				return dropDatabaseResult{}, err
			}
		}
		if !ok {
			return dropDatabaseResult{}, &dropDatabaseRefusal{
				message: fmt.Sprintf("not dropping database %s: no confirmation", remedy.PrintedValue(name)),
				hint:    "Re-run with --yes to confirm.",
			}
		}
	}

	dropCtx, cancel := context.WithTimeout(ctx, dropDatabaseTimeout)
	defer cancel()
	//nolint:gosec // G201: identifier quoted+escaped via doltutil.QuoteIdentifierUnvalidated
	if _, err := conn.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE %s", doltutil.QuoteIdentifierUnvalidated(name))); err != nil {
		return dropDatabaseResult{}, fmt.Errorf("dropping database %s: %w", remedy.PrintedValue(name), err)
	}
	return dropDatabaseResult{Dropped: name, RecoverableViaUndrop: true}, nil
}

func listServerDatabases(ctx context.Context, conn versioncontrolops.DBConn) ([]string, error) {
	listCtx, cancel := context.WithTimeout(ctx, dropDatabaseTimeout)
	defer cancel()
	rows, err := conn.QueryContext(listCtx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// confirmDropDatabase asks once; only y/yes drops. A canceled read returns
// the cancellation so the caller exits 130.
func confirmDropDatabase(ctx context.Context, in io.Reader, closer io.Closer, out io.Writer, name string) (bool, error) {
	fmt.Fprintf(out, "This drops database %s from the Dolt server.\n", remedy.PrintedValue(name))
	fmt.Fprintf(out, "It stays recoverable with CALL DOLT_UNDROP('%s') until the server's dropped databases are purged.\n", name)
	fmt.Fprint(out, "Drop it? [y/N] ")
	line, err := readLineWithContext(ctx, bufio.NewReader(in), closer)
	if err != nil {
		if isCanceled(err) {
			return false, err
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func dropDatabaseReport(res dropDatabaseResult) string {
	return fmt.Sprintf("Dropped database %s.\n"+
		"  Recover it with: CALL DOLT_UNDROP('%s')\n"+
		"  Its disk is reclaimed only by 'bd dolt clean-databases --purge-dropped', which purges\n"+
		"  every dropped database on the server (server-global, irreversible).\n",
		remedy.PrintedValue(res.Dropped), res.Dropped)
}

var doltDropDatabaseCmd = &cobra.Command{
	Use:           "drop-database <name>",
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ExactArgs(1),
	Short:         "Drop one named database from the Dolt server (recoverable via DOLT_UNDROP)",
	Long: `Drop one named database from the Dolt server this workspace uses.

Deleting a workspace's .beads directory does not drop its database on a
shared Dolt server; this command does, for one database you name.

DROP DATABASE only marks the database as dropped: it stays recoverable with
CALL DOLT_UNDROP('<name>') until the server's dropped databases are purged.
This command never purges. 'bd dolt clean-databases --purge-dropped' reclaims
the disk, for every dropped database on the server at once.

Asks for confirmation; pass --yes in scripts and agents.

Examples:
  bd dolt drop-database old_project          # prompts for confirmation
  bd dolt drop-database old_project --yes    # no prompt`,
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
		yes, _ := cmd.Flags().GetBool("yes")
		req := dropDatabaseRequest{name: args[0], configured: cfg.GetDoltDatabase(), assumeYes: yes}
		if !jsonOutput && !isNonInteractiveInit(false) {
			req.confirm = func(name string) (bool, error) {
				return confirmDropDatabase(rootCtx, os.Stdin, os.Stdin, os.Stderr, name)
			}
		}
		run := func(ctx context.Context, conn versioncontrolops.DBConn) error {
			res, err := dropNamedDatabase(ctx, conn, req)
			if err != nil {
				if isCanceled(err) {
					return errCanceled()
				}
				if r, ok := err.(*dropDatabaseRefusal); ok {
					return HandleErrorWithHintRespectJSON(r.message, r.hint)
				}
				return HandleErrorRespectJSON("%v", err)
			}
			if jsonOutput {
				return outputJSON(res)
			}
			fmt.Print(dropDatabaseReport(res))
			return nil
		}

		if usesProxiedServer() {
			return runDoltDropDatabaseProxied(rootCtx, beadsDir, run)
		}
		db, cleanup, err := openDoltServerConnection()
		if err != nil {
			return err
		}
		defer cleanup()
		return run(rootCtx, db)
	},
}

func init() {
	doltDropDatabaseCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt (required in non-interactive use)")
	doltCmd.AddCommand(doltDropDatabaseCmd)
}
