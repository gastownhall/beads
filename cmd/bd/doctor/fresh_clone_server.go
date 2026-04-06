package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// freshCloneDBCheck holds the result of checking whether a database exists on
// a Dolt server during the fresh clone doctor check.
type freshCloneDBCheck struct {
	Exists    bool  // database found via SHOW DATABASES
	Reachable bool  // server responded to ping
	Err       error // connection or query error
}

// checkFreshCloneDB opens a temporary connection to the Dolt server and checks
// whether the named database exists via SHOW DATABASES. The connection is
// closed before returning. Returns Reachable=false when the server cannot be
// reached, so the caller can skip the server-mode check (FR-030).
func checkFreshCloneDB(host string, port int, user, password, dbName string) freshCloneDBCheck {
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return freshCloneDBCheck{Reachable: false, Err: err}
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ping first to verify reachability — sql.Open is lazy.
	if err := db.PingContext(ctx); err != nil {
		return freshCloneDBCheck{Reachable: false, Err: err}
	}

	// Iterate SHOW DATABASES (not LIKE, to avoid underscore wildcard issues).
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return freshCloneDBCheck{Reachable: true, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return freshCloneDBCheck{Reachable: true, Err: err}
		}
		if name == dbName {
			return freshCloneDBCheck{Exists: true, Reachable: true}
		}
	}
	if err := rows.Err(); err != nil {
		return freshCloneDBCheck{Reachable: true, Err: err}
	}

	return freshCloneDBCheck{Exists: false, Reachable: true}
}

// freshCloneServerResult builds the DoctorCheck for server-mode fresh clone
// detection. Pure function — testable without a real database.
//
// When dbExists is true, returns StatusOK (FR-021).
// When dbExists is false and syncGitRemote is empty, returns StatusWarning
// suggesting the user set sync.git-remote (FR-020).
// When dbExists is false and syncGitRemote is set, returns StatusWarning
// suggesting bd init to bootstrap from the remote.
func freshCloneServerResult(dbExists bool, dbName, host string, port int, syncGitRemote string) DoctorCheck {
	if dbExists {
		return DoctorCheck{
			Name:    "Fresh Clone",
			Status:  StatusOK,
			Message: "Database exists on server",
		}
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "Fresh clone detected: database %q not found on server at %s:%d.", dbName, host, port)

	fix := "bd bootstrap"
	if syncGitRemote == "" {
		msg.WriteString(" Run bd bootstrap first as the safe recovery entry point. It may recover existing state or initialize if no prior state can be found. If bootstrap cannot find the expected remote automatically, then set sync.git-remote in .beads/config.yaml and rerun bd bootstrap.")
		fix = "bd bootstrap"
	} else {
		fmt.Fprintf(&msg, " sync.git-remote is configured (%s) — run bd bootstrap to recover from the remote, or use --dry-run to inspect the plan first.", syncGitRemote)
	}

	return DoctorCheck{
		Name:    "Fresh Clone",
		Status:  StatusWarning,
		Message: msg.String(),
		Fix:     fix,
	}
}
