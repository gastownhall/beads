package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
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
//
// socket is the transport its repair writes (freshCloneRepairTransport): the
// unix socket to dial, or "" for TCP at host:port. Over TCP alone a
// socket-only server was probed at host:0.
func checkFreshCloneDB(host string, port int, socket, user, password, dbName string, tls bool) freshCloneDBCheck {
	dsn := doltutil.ServerDSN{
		Socket:   socket,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		TLS:      tls,
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
// suggesting the user set sync.remote (FR-020).
// When dbExists is false and syncGitRemote is set, returns StatusWarning
// suggesting bd init to bootstrap from the remote.
func freshCloneServerResult(dbExists bool, dbName, host string, port int, socket, syncRemote string) DoctorCheck {
	if dbExists {
		return DoctorCheck{
			Name:    "Fresh Clone",
			Status:  StatusOK,
			Message: "Database exists on server",
		}
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "%s: database %q not found on server at %s.", freshCloneDetected, dbName, freshCloneServerLocation(host, port, socket))

	fix := "bd bootstrap"
	if syncRemote == "" {
		msg.WriteString(" Run bd bootstrap first as the safe recovery entry point. It may recover existing state or initialize if no prior state can be found. If bootstrap cannot find the expected remote automatically, then set sync.remote in .beads/config.yaml and rerun bd bootstrap.")
		fix = "bd bootstrap"
	} else {
		fmt.Fprintf(&msg, " sync.remote is configured (%s) — run bd bootstrap to recover from the remote, or use --dry-run to inspect the plan first.", remedy.PrintedValue(syncRemote))
	}

	return DoctorCheck{
		Name:    "Fresh Clone",
		Status:  StatusWarning,
		Message: msg.String(),
		Fix:     fix,
	}
}

// freshCloneServerUnreachableResult builds the DoctorCheck for the case where
// dolt_mode=server is configured but the server cannot be reached (TCP refused,
// TLS mismatch, auth failure, etc.). Falling through to the legacy "Fresh clone
// detected (no database)" warning is misleading in server mode because the
// absence of a local database is expected — see GH#35.
//
// The message identifies that we're in server mode, points at the server the
// check dialed (freshCloneServerLocation), surfaces the underlying connection
// error for diagnostics, and suggests connectivity/credential checks rather
// than bd bootstrap (which won't help when the server itself is unreachable).
func freshCloneServerUnreachableResult(dbName, host string, port int, socket string, connErr error) DoctorCheck {
	var msg strings.Builder
	fmt.Fprintf(&msg, "Dolt server unreachable at %s (database %q, server mode configured).", freshCloneServerLocation(host, port, socket), dbName)
	msg.WriteString(" The local database directory is not expected in server mode, so this is not a fresh clone — it's a connectivity or auth problem.")

	var detail strings.Builder
	detail.WriteString("dolt_mode=server is configured but the doctor check could not reach the server.\n")
	detail.WriteString("  In server mode, beads stores data on the Dolt server, so no local .beads/dolt directory is expected.\n")
	if connErr != nil {
		// The connection error repeats the host.
		fmt.Fprintf(&detail, "  Underlying error: %v\n", remedy.PrintedError(connErr))
	}
	detail.WriteString("  Common causes: server not running, wrong host/port, TLS misconfiguration, or invalid credentials.")

	settings := "dolt_server_host, dolt_server_port, dolt_server_tls"
	if socket != "" {
		// The check dialed the socket, which these settings do not name.
		settings = "dolt_server_socket (or BEADS_DOLT_SERVER_SOCKET), " + settings
	}

	return DoctorCheck{
		Name:    "Fresh Clone",
		Status:  StatusWarning,
		Message: msg.String(),
		Detail:  detail.String(),
		Fix: "Verify Dolt server connectivity:\n" +
			"  1. Confirm the server is running and reachable from this host\n" +
			"  2. Check .beads/metadata.json: " + settings + " (a dolt.host in config.yaml outranks dolt_server_host)\n" +
			"  3. Verify credentials in ~/.config/beads/credentials (or BEADS_DOLT_PASSWORD)\n" +
			"  4. Try a CRUD command (e.g. 'bd ready') to confirm the server is usable\n" +
			"  5. Re-run 'bd doctor' once connectivity is restored",
	}
}

// freshCloneRepairTransport is the transport the Fresh Clone check dials ep
// over, the unix socket or "" for TCP: the server its repair
// (fix.FreshCloneImport) writes, so bd doctor --fix never imports
// issues.jsonl into a server the check did not read. That repair opens the
// store through the library open (dolt.NewFromConfigWithOptions), which takes
// its socket from BEADS_DOLT_SERVER_SOCKET alone, not from metadata.json's
// dolt_server_socket as the CLI's store does, and dials it socket first
// (doltserver.SocketTransport). Beside a resolved port the check therefore
// dials a live BEADS_DOLT_SERVER_SOCKET, else TCP at the port, even where
// metadata.json names a live socket: it read that socket's server while the
// repair wrote TCP at the port, so --fix overwrote live rows there with the
// tracked copy (#7874 review). A server only a socket names (no port) is
// read on its socket, the store's transport; bd doctor --fix withholds the
// repair there, as the schema gate's connection (openDoltDB) does not dial a
// socket.
func freshCloneRepairTransport(ep doltserver.Endpoint) string {
	if ep.Resolved() {
		ep.Socket = os.Getenv("BEADS_DOLT_SERVER_SOCKET")
	}
	return ep.Transport()
}

// freshCloneRepairUser is the user the Fresh Clone check logs in to ep as:
// the one its repair (fix.FreshCloneImport) presents there. That repair's
// library store open presents a configured BEADS_DOLT_CREDENTIAL_COMMAND's
// token over the static user (dolt.ApplyGatewayCredential), and a command
// that fails fails the open, so it fails the check too, which never falls
// back to the static user (Endpoint.WithIdentity). The check logged in as
// the static user, which a gateway refuses: it reported the server
// unreachable while bd doctor --fix imported issues.jsonl through that
// gateway over the live rows (#7874 review).
func freshCloneRepairUser(ep doltserver.Endpoint) (string, error) {
	id, err := ep.WithIdentity(context.Background())
	if err != nil {
		return "", fmt.Errorf("resolving dolt credential command: %w", err)
	}
	return id.User, nil
}

// freshCloneDetected begins the message of each Fresh Clone result that
// found the workspace's database missing (FreshCloneDetected).
const freshCloneDetected = "Fresh clone detected"

// FreshCloneDetected reports whether message, a Fresh Clone check's, is a
// fresh-clone verdict: the check found the workspace's database missing, on
// the server it reads or, outside server mode, on disk. That is the one
// result bd doctor --fix repairs by importing issues.jsonl
// (fix.FreshCloneImport). The check also warns when it could not log in to
// the server, or has no address for it, and says that is not a fresh clone;
// --fix ran the import there too, writing the tracked issues over the live
// rows of a database the check never read (#7874 review).
func FreshCloneDetected(message string) bool {
	return strings.HasPrefix(message, freshCloneDetected)
}

// freshCloneServerLocation names the server the Fresh Clone check dialed:
// unix:<socket> when it dialed the socket (freshCloneRepairTransport), else
// host:port. A server with no port is always dialed on its socket, so it
// never names host:0.
func freshCloneServerLocation(host string, port int, socket string) string {
	if socket != "" {
		return remedy.PrintedValue("unix:" + socket)
	}
	return doltutil.PrintedServerAddr(host, port)
}

// freshCloneServerUnresolvedResult builds the DoctorCheck for a server-mode
// workspace whose server has no address (no port resolved and no socket) and
// that no bd command starts (doltserver.Endpoint.BDStartsServer). There is no
// server to look for the database on, and every store open refuses until a
// port resolves, bd bootstrap included, so the check gives the store's advice
// (Endpoint.UnresolvedAdvice) rather than probe 127.0.0.1:0 and report a
// connectivity problem.
func freshCloneServerUnresolvedResult(dbName string, ep doltserver.Endpoint) DoctorCheck {
	return DoctorCheck{
		Name:   "Fresh Clone",
		Status: StatusWarning,
		Message: fmt.Sprintf("No Dolt server port resolved for host %s (database %q, server mode configured), so the database was not looked for.",
			remedy.PrintedValue(ep.Host), dbName),
		Detail: "bd bootstrap and every bd command that opens this workspace's store refuse until a port resolves.",
		Fix:    ep.UnresolvedAdvice(),
	}
}
