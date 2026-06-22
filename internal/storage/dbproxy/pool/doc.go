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
// When BEADS_DOLT_POOL is truthy, uow.NewExternalDoltServerUOWProvider resolves
// a BackendExternalPooled endpoint instead of the TCP byte-passthrough proxy:
// bd spawns a db-proxy-child in pooled mode (reusing the same lock/pidfile/log/
// idle-shutdown lifecycle), the child listens on the pooler unix socket, and bd
// connects to that socket. The socket path is recorded in proxy.pid and bd's
// DSN is built as a unix-socket DSN (the store layer also honors
// BEADS_DOLT_SERVER_SOCKET for direct connections).
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
