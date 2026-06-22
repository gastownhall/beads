// Package pool fronts a Dolt sql-server with a query-level MySQL connection
// pooler. See the package overview in pool.go for the design.
//
// # Enabling the pooler
//
// The pooler is OFF by default; bd connects to Dolt exactly as before unless it
// is opted in. To enable it, set in bd's environment:
//
//	BEADS_DOLT_POOL=1            # turn the pooler on (truthy: 1/true/yes/on)
//	BEADS_DOLT_POOL_SIZE=16      # optional: persistent backends (default 16, min 4)
//	BEADS_DOLT_POOL_SOCKET=/path # optional: client socket (default <rootDir>/pool.sock)
//
// Two activation paths consult BEADS_DOLT_POOL, both spawning a db-proxy-child
// in pooled mode (reusing the same lock/pidfile/log/idle-shutdown lifecycle)
// that listens on a pooler unix socket recorded in proxy.pid:
//
//   - SERVER-MODE store path (the town's actual path): the direct dolt store
//     (internal/storage/dolt, newServerMode -> openServerConnection over TCP to
//     BEADS_DOLT_SERVER_HOST/PORT). maybeActivatePooler ensures a SHARED,
//     long-lived pooler keyed to the resolved host:port under
//     ~/.beads/pool/<host>_<port>/ — one pooler per Dolt endpoint, reused by
//     every bd process via the proxy lock/pidfile — and rewrites
//     cfg.ServerSocket to the pooler socket.
//   - uow external-doltserver provider: NewExternalDoltServerUOWProvider
//     resolves a BackendExternalPooled endpoint under the workspace server root.
//
// In both cases bd's DSN is built as a unix-socket DSN (the store layer also
// honors BEADS_DOLT_SERVER_SOCKET for direct connections). Activation is a strict
// no-op unless BEADS_DOLT_POOL is truthy, BEADS_TEST_MODE is unset, and the path
// is plain TCP server mode (no operator socket, not a proxied-server config).
//
// # Routing summary
//
//	bd client  --unix socket-->  pooler (this package)  --K persistent conns-->  Dolt
//
// Regardless of how many short-lived bd processes connect, Dolt sees ~K
// connections (collapsing connection churn; see gt-ye21 / #4292).
//
// # Prepared statements
//
// bd's server-mode DSN does not set interpolateParams, so go-sql-driver sends
// server-side COM_STMT_PREPARE/EXECUTE for parameterized queries. The pooler
// implements these by interpolating the bound parameter values back into the
// SQL text (via vitess GenerateQuery, which SQL-encodes and escapes values) and
// running the result as a plain text query — so prepared statements work
// through the pooler without a server-side prepare on the backend.
package pool
