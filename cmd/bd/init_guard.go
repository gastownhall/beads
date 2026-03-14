package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/gastownhall/beads/internal/ui"
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
func checkDatabaseOnServer(host string, port int, user, password, dbName string) initGuardDBCheck {
	var userPart string
	if password != "" {
		userPart = fmt.Sprintf("%s:%s", user, password)
	} else {
		userPart = user
	}
	dsn := fmt.Sprintf("%s@tcp(%s:%d)/?timeout=5s", userPart, host, port)

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
func initGuardServerMessage(dbName, host string, port int, prefix, syncGitRemote string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s Database %q not found on server at %s:%d.\n", ui.RenderWarn("⚠"), dbName, host, port)
	b.WriteString("The server is running but this database hasn't been created yet.\n")

	b.WriteString("\nDiagnose with:\n")
	b.WriteString("  bd doctor          # check project health\n")
	b.WriteString("  bd dolt status     # inspect Dolt server state\n")

	if syncGitRemote != "" {
		fmt.Fprintf(&b, "\nTip: sync.git-remote is configured (%s).\n", syncGitRemote)
		b.WriteString("If this is a fresh clone, run:\n")
		fmt.Fprintf(&b, "  bd init --prefix %s\n", prefix)
		b.WriteString("to bootstrap from the remote (existing data is preserved).\n")
	} else {
		b.WriteString("\nIf this is a brand-new project, create the database with:\n")
		fmt.Fprintf(&b, "  bd init --prefix %s\n", prefix)
		b.WriteString("\nTo bootstrap from an existing Dolt remote, set sync.git-remote\n")
		b.WriteString("in .beads/config.yaml first, then run bd init.\n")
	}

	b.WriteString("\n⚠  Caution: bd init --force destroys ALL existing issues. Do not\n")
	b.WriteString("use --force unless you are certain the database should be recreated.\n")

	b.WriteString("\nAborting.")
	return errors.New(b.String())
}
