# db-proxy connection pooling

## Problem

Multi-agent orchestrators (e.g. gascity/gc) run beads against a persistent
`dolt sql-server` and fork the `bd` binary for **every** agent operation. Each
`bd` process is short-lived: it opens a fresh `*sql.DB`, dials dolt (TCP + MySQL
auth handshake + session setup), runs its query, and tears everything down at
exit. The in-process pool in `internal/storage/dolt/store.go` (`applyPoolLimits`)
never survives the process, so there is **zero** cross-invocation reuse.

Measured on a 1-rig city (7 active agents):

- **71 new connections/sec** to dolt (lifetime `Connections` ≈ 2.33M)
- ~560 queries/sec
- dolt sql-server at 85–170% CPU while a `processlist` snapshot is mostly
  `Sleep`

The CPU is not query work — it is the **per-call connection setup cost** paid
71× per second. This is gascity #1978. Hard constraint: **keep Dolt** (bead
versioning + cross-machine sync via dolt remotes); no migration to the SQLite
coordstore.

## Approach (chosen: pooling proxy)

beads already ships a `db-proxy` (`internal/storage/dbproxy/proxy`) that fronts
the dolt sql-server for lifecycle management (auto-start, idle-stop, multi-writer
coordination). Historically it was a **byte-transparent 1:1 forwarder**: one
backend dial per client, `io.Copy` both ways, `Close` on disconnect — so it did
not reduce connection churn.

The fix makes the proxy a **session-pooling, MySQL-protocol-aware** forwarder:

1. The proxy terminates the client handshake itself (skip-auth — clients are
   loopback-only and already trusted by the proxy's design).
2. It borrows an already-authenticated backend connection from a pool of warm
   connections to dolt.
3. The command phase is forwarded **byte-transparently** (the proven `io.Copy`
   path), so bd's storage / uow / transaction code is unchanged.
4. On client disconnect the proxy sends `COM_RESET_CONNECTION` to the backend
   (purging session variables, open transactions, temp tables, prepared
   statements) and returns it to the pool instead of closing it.

`bd` continues to speak MySQL exactly as before; the only change for a consumer
is selecting ProxiedServerMode and setting one env var.

### Why not a bd daemon (rejected)

A long-lived `bd serve` holding the pool, with the CLI shipping operations over
IPC, was rejected: it requires routing the entire domain layer over an RPC
surface (or reinventing the MySQL wire protocol over a socket), the fallback
path is complex, and transactions still force per-invocation session pinning —
so it yields no advantage over pooling at the proxy while costing far more code.

## Correctness

### Capability-flag parity

Byte-transparent command-phase forwarding requires the capability flags
negotiated **client↔proxy** to equal those negotiated **proxy↔backend** —
result-set encoding depends on flags such as `CLIENT_DEPRECATE_EOF`,
`CLIENT_PROTOCOL_41`, `CLIENT_QUERY_ATTRIBUTES`, `CLIENT_SESSION_TRACK`. A
mismatch would silently corrupt the wire stream. Therefore the pool is **keyed
by `(capabilities, database)`**: the proxy authenticates each backend with
exactly the caps the client negotiated, and only lends a backend to a client
with an identical key. All `bd` clients share one driver and DSN, so they
collapse onto a single key and reuse the same warm connections. A client with an
unexpected key simply causes a fresh dial (still correct, just not reused).

### Session isolation

`COM_RESET_CONNECTION` is the isolation primitive. dolt's go-mysql-server
handler releases all locks, closes the session, creates a fresh one, and
restores the connection's default database. Note that go-sql-driver's
`ResetSession` does **not** send `COM_RESET_CONNECTION` (it only does a liveness
check), which is why the proxy sends the packet itself. If the reset fails the
connection is destroyed rather than reused.

The reset reply is **sequence-checked** (a reply to a fresh command is always
sequence 1). If the proxy instead reads an out-of-sequence packet — e.g. a
client was killed mid-result and unread result packets are still in the stream —
the connection is treated as misaligned and destroyed, never lent out.

### COM_QUIT interception

go-sql-driver sends `COM_QUIT` when it closes a connection. Forwarding that to a
pooled backend would terminate it. The client→backend direction is therefore
frame-aware enough to detect a standalone `COM_QUIT` and reclaim the backend
instead of forwarding it. All other frames pass through verbatim.

## Configuration / opt-in

Pooling is **opt-in** and off by default (`BEADS_PROXY_POOL_SIZE` unset or `0`
preserves the original transparent forwarding). Embedded mode and direct
ServerMode are unaffected.

| Knob | Effect |
|------|--------|
| `BEADS_PROXY_POOL_SIZE=N` | Enable pooling; keep up to `N` warm idle backend connections per `(caps,db)` key. Read by the uow provider when it opens the proxy endpoint. |

Internals (set automatically, listed for reference):

- `proxy.OpenOpts.PoolSize` / `BackendUser` → spawned child flags
  `--pool-size` / `--backend-user`. The backend password is inherited by the
  child via the environment, never passed on the command line.
- `proxy.ProxyOpts.{PoolSize,BackendUser,BackendPassword,PoolConnMaxLifetime}`.
- `PoolConnMaxLifetime` defaults to 0 (unlimited): a short lifetime would
  re-create the churn pooling exists to eliminate.

Because the proxy is spawned once and reused, the pool size is fixed by the
**first** `bd` invocation that starts the proxy; set `BEADS_PROXY_POOL_SIZE`
consistently across a deployment.

## gascity integration

gascity currently points `bd` at its managed dolt sql-server in **ServerMode**
(`BEADS_DOLT_SERVER_HOST`/`PORT`). To benefit from pooling:

1. Switch `bd` to **ProxiedServerMode** with the **external** backend so the
   proxy fronts the existing managed sql-server (rather than starting a second
   dolt). ProxiedServerMode is selected via the workspace's `metadata.json`
   `dolt_mode = proxied-server` (`configfile.IsDoltProxiedServerMode`); the
   runtime path is live at `cmd/bd/main.go` → `newProxiedServerUOWProvider` →
   `newExternalProxiedServerUOWProvider`. The external host/port/user (and
   `BEADS_DOLT_PASSWORD` for auth) configure the backend the proxy connects to.
2. Export **`BEADS_PROXY_POOL_SIZE`** in the environment of every forked `bd`
   (e.g. `2 × max concurrent agents per rig`). The proxy is shared per workspace
   root, so all agents in a rig share one warm pool.

Effect: the per-`bd` connection churn against dolt collapses to the pool size
(steady-state ~0 new connections/sec) instead of one new authenticated
connection per agent operation, removing the connection-setup CPU load on the
dolt sql-server while keeping Dolt and all its versioning/sync semantics intact.

## Validation

`internal/storage/dbproxy/proxy`:

- Unit (`pool_test.go`, `mysqlwire_test.go`): pool reuse/cap/key-isolation/
  drain/eviction/lifetime against a fake backend; wire round-trips. No dolt.
- Integration (`proxy_pool_integration_test.go`, `mysqlwire_spike_test.go`;
  cgo + dolt): real proxyServer — query/transaction/`DOLT_COMMIT` round-trip,
  rollback leaves no row, 8 concurrent clients keep private sessions, and
  session isolation (`@var`, `autocommit`, temp table purged between borrowers
  sharing one backend).
- Benchmark (`BenchmarkConnectionChurn`): dolt-side `Connections` delta over 50
  sequential sessions — **pooling off = 50** (1:1 churn), **pooling on = 0**
  (50 pool hits, 1 dial).
