package dolt

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pool"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// maybeActivatePooler routes the server-mode store connection through the shared
// connection pooler when BEADS_DOLT_POOL is opted in.
//
// The town's bd processes connect via the DIRECT server-mode path
// (newServerMode -> openServerConnection over TCP to BEADS_DOLT_SERVER_HOST/PORT),
// NOT through the uow external-doltserver provider. So activation must hook in
// here, at the common server-mode connection setup, before the connectivity
// check and DSN building.
//
// When activated, it ensures a SHARED, long-lived pooler child is running for
// the resolved Dolt endpoint (one per host:port, reused across every bd process
// via the proxy lock/pidfile in a stable rootDir) and rewrites cfg.ServerSocket
// to the pooler socket. Everything downstream (the fail-fast dial in
// newServerMode and buildServerDSN) already honors ServerSocket, so the whole
// store transparently flows through the pooler.
//
// It is a no-op (returns nil, store unchanged) unless ALL of the following hold,
// keeping the feature strictly opt-in with zero default behavior change:
//   - BEADS_DOLT_POOL is truthy.
//   - Not test mode (BEADS_TEST_MODE != 1): test traffic must never be pooled
//     onto the production pooler.
//   - The store is plain server mode over TCP: no socket already configured
//     (operator socket is respected), not a proxied-server config, and a real
//     host/port resolved.
//
// On any failure to bring up the pooler it logs a warning and leaves
// cfg.ServerSocket untouched, so bd falls back to the direct connection rather
// than failing the command.
func maybeActivatePooler(cfg *Config) {
	if !pool.EnabledFromEnv() {
		return
	}
	// Safety: never pool test traffic onto the production pooler.
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return
	}
	// Respect an explicitly configured socket / proxied-server path; only the
	// plain TCP server-mode path is hijacked.
	if cfg.ServerSocket != "" || cfg.ProxiedServer {
		return
	}
	if cfg.ServerHost == "" || cfg.ServerPort <= 0 {
		return
	}
	// Never front the production endpoint while in test mode is covered above;
	// here we additionally refuse to pool when the resolved port looks like a
	// throwaway test server is not a concern — only real endpoints reach here.

	socket, err := ensureSharedPooler(cfg.ServerHost, cfg.ServerPort, cfg.ServerUser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: BEADS_DOLT_POOL set but pooler unavailable (%v); using direct connection\n", err)
		return
	}
	cfg.ServerSocket = socket
}

// ensureSharedPooler spawns-or-reuses the shared pooler child for the given Dolt
// endpoint and returns the unix socket clients should connect to. The pooler is
// keyed by host:port so every rig fronting the same Dolt server shares ONE
// pooler process; the proxy lock/pidfile machinery makes concurrent bd
// processes cooperate (first one spawns, the rest reuse).
func ensureSharedPooler(host string, port int, user string) (string, error) {
	rootDir, err := poolerRootDir(host, port)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootDir, config.BeadsDirPerm); err != nil {
		return "", fmt.Errorf("create pooler root %q: %w", rootDir, err)
	}

	socket := pool.SocketFromEnv(rootDir)
	logPath := filepath.Join(rootDir, proxy.LogFileName)
	if user == "" {
		user = "root"
	}

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(rootDir, proxy.OpenOpts{
		Backend:     proxy.BackendExternalPooled,
		LogFilePath: logPath,
		PoolSocket:  socket,
		PoolSize:    pool.SizeFromEnv(),
		IdleTimeout: 0, // long-lived: shared across many short bd invocations
		External: configfile.ExternalDoltConfig{
			Host: host,
			Port: port,
			User: user,
		},
	})
	if err != nil {
		return "", fmt.Errorf("start shared pooler for %s:%d: %w", host, port, err)
	}
	if !ep.IsSocket() {
		return "", fmt.Errorf("pooler endpoint is not a socket: %q", ep.Address())
	}
	return ep.Socket, nil
}

// poolerRootDir returns a stable per-endpoint directory under the user's beads
// home, so all bd processes fronting the same Dolt host:port converge on the
// same pooler (lock/pidfile/socket). Keyed by host_port.
func poolerRootDir(host string, port int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	key := fmt.Sprintf("%s_%d", sanitizeKey(host), port)
	return filepath.Join(home, ".beads", "pool", key), nil
}

// sanitizeKey makes a host string safe for a path segment.
func sanitizeKey(host string) string {
	out := make([]rune, 0, len(host))
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
