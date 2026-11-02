package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// The lifecycle calls reconcileServerBinary makes. Vars so tests can observe
// and stub them without a dolt sql-server.
var (
	checkServerBinary  = doltserver.CheckServerBinary
	replaceStaleServer = doltserver.ReplaceStaleServer
)

// reconcileServerBinary runs between newServerMode's dial and its first
// application connection (GH#6064). A dolt sql-server keeps running the
// executable it was started from, so after dolt is upgraded the server bd
// started keeps serving the store that clients of the new release write to.
// When the server this open dials is one bd tracks for this workspace and it
// runs a different dolt release than the one bd would launch now, an open
// that may manage the server's lifecycle replaces it on the same port before
// any query, and fails if it cannot.
//
// It returns the port to connect to: cfg.ServerPort, unless the replacement
// had to move.
func reconcileServerBinary(cfg *Config, beadsDir, serverDir string) (int, error) {
	if !serverBinaryCandidate(cfg) {
		return cfg.ServerPort, nil
	}
	serverVersion := serverDoltVersion(cfg)
	check := checkServerBinary(serverDir, cfg.ServerPort, serverVersion)
	refresh := check.Verdict == doltserver.BinaryCurrent && check.Refresh
	if check.Verdict != doltserver.BinaryStale && check.Verdict != doltserver.BinaryCannotReplace && !refresh {
		return cfg.ServerPort, nil
	}
	if !serverBinaryMayRestart(cfg, beadsDir) {
		if !refresh {
			adviseStaleServer(cfg, beadsDir, serverDir, check)
		}
		return cfg.ServerPort, nil
	}
	addr := doltutil.PrintedServerAddr(cfg.ServerHost, cfg.ServerPort)
	if check.Verdict == doltserver.BinaryCannotReplace {
		return 0, cannotReplaceServerError(addr, check, check.Reason)
	}
	// For a reinstall of the same release (refresh) the server stays, and
	// replaceStaleServer only records the executable it was compared with,
	// unless it finds the server stale by the time it holds the lock.
	res, err := replaceStaleServer(serverDir, cfg.ServerPort, serverVersion)
	if err != nil {
		switch {
		case res.Check.Verdict == doltserver.BinaryCannotReplace:
			return 0, cannotReplaceServerError(addr, res.Check, err)
		case res.Check.Verdict != doltserver.BinaryStale && refresh:
			return cfg.ServerPort, nil
		}
		if res.Check.Verdict == doltserver.BinaryStale {
			check = res.Check
		}
		next := "To restart it yourself: bd dolt stop, then bd dolt start"
		if res.Stopped {
			next = fmt.Sprintf("The old server is stopped and none is running; SQL port %d is kept for the next start: bd dolt start", cfg.ServerPort)
		}
		return 0, fmt.Errorf("Dolt server at %s runs dolt %s, but the dolt bd launches (%s) is now %s; "+
			"replacing the server failed, so no query was sent: %w\n\n%s",
			addr, check.RunningVersion, remedy.PrintedValue(check.CandidatePath), check.CandidateVersion,
			remedy.PrintedTextError(err), next)
	}
	if !res.Replaced || res.State == nil {
		return cfg.ServerPort, nil
	}
	fmt.Fprintf(os.Stderr, "Restarted the dolt sql-server on port %d (PID %d → %d): it ran dolt %s, "+
		"and the dolt bd launches (%s) is now %s. Stopping it first attempted to commit uncommitted working-set changes.\n",
		res.State.Port, res.Check.PID, res.State.PID, res.Check.RunningVersion,
		remedy.PrintedValue(res.Check.CandidatePath), res.Check.CandidateVersion)
	return res.State.Port, nil
}

// serverVersionTimeout bounds serverDoltVersion's query, so a server that
// accepts connections but does not answer cannot stall the open.
const serverVersionTimeout = 3 * time.Second

// serverDoltVersion asks the server cfg dials for its dolt version, over a
// connection of its own with no database selected. It is how a server bd has
// no launch record for is compared.
func serverDoltVersion(cfg *Config) doltserver.ServerVersionFunc {
	return func() (string, error) {
		db, err := sql.Open("mysql", buildServerDSN(cfg, ""))
		if err != nil {
			return "", err
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), serverVersionTimeout)
		defer cancel()
		v, err := doltversion.ServerVersion(ctx, db)
		if err != nil {
			return "", err
		}
		return v.String(), nil
	}
}

// staleServerAdvised holds the servers (directory and port) this process has
// already warned about, so a command that opens several stores warns once.
var staleServerAdvised sync.Map

// adviseStaleServer warns, once per server, that the tracked server this open
// may not restart runs a dolt since replaced. Nothing is changed: the open
// goes on to use the server as before.
func adviseStaleServer(cfg *Config, beadsDir, serverDir string, check doltserver.BinaryCheck) {
	if _, done := staleServerAdvised.LoadOrStore(fmt.Sprintf("%s:%d", serverDir, cfg.ServerPort), struct{}{}); done {
		return
	}
	running := check.RunningVersion
	if running == "" {
		running = "an earlier release"
	}
	installed := "the installed dolt"
	if check.CandidatePath != "" {
		installed += " (" + remedy.PrintedValue(check.CandidatePath) + ")"
	}
	if check.CandidateVersion != "" {
		installed += " is now " + check.CandidateVersion
	} else {
		installed += " has changed"
	}
	restart := "To restart it on the installed dolt: bd dolt stop, then bd dolt start"
	if doltserver.IsSharedServerModeForDir(beadsDir) {
		restart = "It is the shared server: restarting it disconnects every client and database it serves.\n" +
			"  When that is safe: bd dolt stop, then bd dolt start"
	}
	fmt.Fprintf(os.Stderr, "Warning: the dolt sql-server on port %d runs dolt %s, but %s; this command does not restart it.\n  %s\n",
		cfg.ServerPort, running, installed, restart)
}

func cannotReplaceServerError(addr string, check doltserver.BinaryCheck, reason error) error {
	running := check.RunningVersion
	if running == "" {
		running = "an unknown version"
	}
	return fmt.Errorf("Dolt server at %s runs dolt %s from an executable that has since changed, "+
		"and it cannot be replaced, so no query was sent: %w\n\n"+
		"The server was left running. Fix the dolt installation, or stop the server: bd dolt stop",
		addr, running, remedy.PrintedTextError(reason))
}

// serverBinaryCandidate reports whether this open's endpoint can be the
// tracked local server reconcileServerBinary compares: plain TCP to a host bd
// runs its server at, on a resolved port. A socket, TLS, gateway or proxied
// endpoint is a server bd does not launch, so it is never compared.
func serverBinaryCandidate(cfg *Config) bool {
	if cfg.ServerSocket != "" || cfg.ServerTLS || cfg.Gateway || cfg.ProxiedServer {
		return false
	}
	return cfg.ServerPort > 0 && doltserver.AutoStartReaches(cfg.ServerHost, cfg.ServerSocket)
}

// serverBinaryMayRestart reports whether this open may stop and start the
// workspace's server: the same permission auto-start has, in a workspace
// whose server bd owns, for a command that may change things (not a preview,
// strict --readonly or a frozen command).
func serverBinaryMayRestart(cfg *Config, beadsDir string) bool {
	if cfg.DisableAutoRestart || cfg.Preview || (cfg.ReadOnly && !cfg.ClassifiedRead) {
		return false
	}
	return serverOpenCanAutoStart(cfg) && !doltserver.IsAutoStartDisabled() &&
		doltserver.ResolveServerMode(beadsDir) == doltserver.ServerModeOwned &&
		!doltserver.IsSharedServerModeForDir(beadsDir)
}
