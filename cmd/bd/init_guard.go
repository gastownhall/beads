package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/ui"
)

// initGuardDBCheck holds the result of checking whether a database exists on a
// Dolt server. Extracted from checkExistingBeadsDataAt for testability.
type initGuardDBCheck struct {
	Exists    bool // database found via SHOW DATABASES
	Reachable bool // server responded to ping
	Err       error
}

// checkDatabaseOnServer opens a temporary connection to the Dolt server and
// checks whether the named database exists via SHOW DATABASES. The connection
// is closed before returning.
//
// Returns Reachable=false when the server cannot be reached (FR-030), so the
// caller can fall through to existing "already initialized" behavior.
func checkDatabaseOnServer(host string, port int, user, password, dbName string, tls bool) initGuardDBCheck {
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		TLS:      tls,
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return initGuardDBCheck{Reachable: false, Err: err}
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ping first to verify reachability — sql.Open is lazy.
	if err := db.PingContext(ctx); err != nil {
		return initGuardDBCheck{Reachable: false, Err: err}
	}

	// Iterate SHOW DATABASES (not LIKE, to avoid underscore wildcard issues).
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		// Server reachable but query failed — treat as unreachable to avoid
		// false negatives on permissions issues.
		return initGuardDBCheck{Reachable: true, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return initGuardDBCheck{Reachable: true, Err: err}
		}
		if name == dbName {
			return initGuardDBCheck{Exists: true, Reachable: true}
		}
	}
	if err := rows.Err(); err != nil {
		return initGuardDBCheck{Reachable: true, Err: err}
	}

	return initGuardDBCheck{Exists: false, Reachable: true}
}

// initGuardServerMessage builds the error message for the init guard when the
// server is reachable but the database does not exist (FR-010, FR-011).
// Extracted as a pure function for unit testing without a real database.
//
// GH#2363: The message deliberately avoids suggesting `bd init --force` because
// that command destroys all existing issue data.  An AI agent running inside a
// git hook blindly followed the previous suggestion and wiped a production
// database.  Instead we guide the user toward safe diagnostic commands.
func initGuardServerMessage(dbName, host string, port int, prefix, syncRemote string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s Database %q not found on server at %s:%d.\n", ui.RenderWarn("⚠"), dbName, host, port)
	b.WriteString("The server is running but this database hasn't been created yet.\n")

	b.WriteString("\nDiagnose with:\n")
	b.WriteString("  bd doctor          # check project health\n")
	b.WriteString("  bd dolt status     # inspect Dolt server state\n")

	b.WriteString("\nIf this is an existing project, fresh clone, or shared-server recovery, run:\n")
	b.WriteString("  bd bootstrap\n")
	b.WriteString("This is the safe entry point for existing-project recovery and may recover or initialize depending on detected state.\n")

	if syncRemote != "" {
		fmt.Fprintf(&b, "\nTip: sync.remote is configured (%s).\n", syncRemote)
		b.WriteString("Run bd bootstrap to recover from the configured remote, or use --dry-run to inspect the plan first.\n")
	} else {
		b.WriteString("\nIf this is a brand-new project, create the database with:\n")
		fmt.Fprintf(&b, "  bd init --prefix %s\n", prefix)
		b.WriteString("\nIf bd bootstrap cannot find the expected remote automatically, set sync.remote\n")
		b.WriteString("in .beads/config.yaml and re-run bd bootstrap.\n")
	}

	b.WriteString("\n⚠  Caution: bd init --force destroys ALL existing issues. Do not\n")
	b.WriteString("use --force unless you are certain the database should be recreated.\n")

	b.WriteString("\nAborting.")
	return errors.New(b.String())
}

// initAllowRecreateMissing is threaded from --recreate-missing for the
// duration of a single `bd init` invocation (be-5up5). It must only ever be
// set from that explicit per-invocation flag — never from a config key, env
// var, or implied by --force/--reinit-local — because it authorizes the one
// thing this file's guard exists to prevent: creating a fresh database when
// an existing project's configured one is missing or unreachable.
var initAllowRecreateMissing bool

// initGuardMissingServerDBMessage builds the refusal error for be-5up5: an
// existing project (metadata.json carries a project_id, written only by a
// real prior `bd init`) whose configured server-mode database is absent or
// the server could not be reached to confirm. This differs from
// initGuardServerMessage above (used when there is no evidence either way
// about prior initialization, where `bd bootstrap` is a reasonable next
// step): here metadata.json already proves the project was previously
// initialized, so recommending bootstrap would route straight back into the
// same class of silent-empty-database creation this guard exists to stop
// (2026-08-11 fleet-wide data loss). Point at restore-from-export instead,
// and require the explicit --recreate-missing opt-in before bd init will
// create anything.
func initGuardMissingServerDBMessage(dbName, host string, port int, prefix string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s Database %q not found on server at %s:%d (or the server could not be reached to confirm).\n", ui.RenderWarn("⚠"), dbName, host, port)
	b.WriteString("This workspace was already initialized (metadata.json has a project_id from a prior\n")
	b.WriteString("bd init), so this looks like a recovery situation, not a fresh clone: the configured\n")
	b.WriteString("database is missing from the server, or the server could not be reached to check.\n")

	b.WriteString("\nbd init will NOT create an empty database here — that would strand any existing\n")
	b.WriteString("issue data behind a new, empty database of the same name.\n")

	b.WriteString("\nDiagnose with:\n")
	b.WriteString("  bd doctor          # check project health\n")
	b.WriteString("  bd dolt status     # inspect Dolt server state\n")

	b.WriteString("\nTo recover existing data, restore from an export rather than creating fresh:\n")
	b.WriteString("  bd backup restore                  # if a local backup snapshot exists\n")
	b.WriteString("  Check .beads/backup/ for a JSONL export you can import manually.\n")

	b.WriteString("\nIf you are certain no recoverable data exists and want a fresh, empty database at\n")
	b.WriteString("this name, opt in explicitly for this one invocation:\n")
	fmt.Fprintf(&b, "  bd init --recreate-missing --prefix %s\n", prefix)

	b.WriteString("\nAborting.")
	return errors.New(b.String())
}
