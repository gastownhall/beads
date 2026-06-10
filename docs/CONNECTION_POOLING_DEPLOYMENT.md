# Connection pooling — deployment, validation, gascity integration

Companion to [CONNECTION_POOLING.md](CONNECTION_POOLING.md) (the design). This
covers how to turn pooling on end-to-end, the real-CLI validation numbers, and
the exact gascity wiring change needed to make it durable.

## Deployment: `bd init --proxied-server` (external)

Pooling only takes effect in **ProxiedServerMode** (the proxy must be in the
path). Provision a workspace whose `metadata.json` records
`dolt_mode=proxied-server` and whose `proxied_server_client_info.json` points at
an already-running external dolt sql-server:

```sh
bd init --proxied-server \
  --proxied-server-external-host 127.0.0.1 \
  --proxied-server-external-port 42188 \
  --proxied-server-external-user root \
  --prefix pt --database pt --non-interactive
# password (if any): export BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD=...
```

Unix socket is supported too (`--proxied-server-external-socket-path`).
Managed-local proxied mode (proxy spawns its own dolt) is intentionally still
rejected — it needs a local `.dolt` lifecycle (`MarkDoltDirCompatible`) that is
out of scope here; external mode fronts an existing server and creates no local
Dolt.

Then enable pooling by exporting, in the environment of every forked `bd`:

```sh
export BEADS_PROXY_POOL_SIZE=4   # 0/unset = transparent (no pooling)
```

The proxy is spawned once per workspace root and reused, so the pool size is
frozen by the first `bd` invocation that starts the proxy. Set it consistently
and kill any pre-existing `bd db-proxy-child` when changing it.

Two `cmd/bd` changes made this usable (both on `feat/connection-pooling`):
- `bd init --proxied-server` external was unfenced (the full
  `runInitProxiedServer` already existed behind a blanket "not yet implemented"
  guard).
- A server-mode store is now routed through the proxy in proxied mode
  (`newProxiedServerRoutedStore`), so the legacy store-based commands
  (list/ready/stats/update/close/...) work — previously only `bd create`
  (uow-based) did; the rest dereferenced a nil store and panicked.

## Real-CLI validation

Two backends were used. **Functional** correctness was checked against the live
gascity-managed dolt on **42188** (create/list/ready/update/close/dep all work
through the proxy; writes commit via `DOLT_COMMIT`). **Quantitative** churn was
measured against a **dedicated quiet dolt on 42199** (no background load, so the
global `Connections` counter is uncontaminated; the 42188 counter is global and
gascity itself generates tens of conns/sec).

Method: kill any proxy, warm once (spawns proxy with the chosen pool size), then
50 sequential `bd list` invocations; read dolt `SHOW GLOBAL STATUS LIKE
'Connections'` before/after.

| Mode | new dolt connections / 50 invocations | per invocation |
|------|---------------------------------------|----------------|
| pooling OFF (`BEADS_PROXY_POOL_SIZE` unset) | 351 | **7.02** |
| pooling ON (`BEADS_PROXY_POOL_SIZE=4`)      | 51  | **1.02** |

→ **~7× fewer new dolt connections** per `bd` invocation. The proxy log confirmed
`connection pooling enabled (maxIdle=4)` with **zero** reset/misalignment errors
(the only logged "errors" are benign readiness probes that dial+close the proxy
before handshaking).

Notes on the numbers:
- The pure-pool ceiling is **0** new connections (see
  `BenchmarkConnectionChurn`: a single raw client → 0 over 50 sessions). The CLI
  residual (~1/invocation) is not a pool defect — it is per-process overhead
  that the pool cannot remove: each `bd` invocation in proxied mode opens both a
  uow-provider connection set (whose `openAndInitSchema` connects twice and
  re-checks schema) and the routed store, and the schema-init connection is the
  remaining ~1 fresh dolt connection that isn't reused across processes.
- OFF is ~7/invocation precisely because of that same dual-connect ×
  schema-init; pooling collapses 6 of the 7.
- A future optimization (out of scope) — making the uow provider lazy so
  read-only commands skip `openAndInitSchema` — would push ON toward ~0.

Container-backed integration tests (`BEADS_TEST_PROXIED_SERVER=1`, real dolt):
`TestProxiedServerExternalCreate` (now unblocked) and
`TestProxiedServerExternalStoreCommands` (list/ready/update/close via the routed
store) both pass.

## gascity integration (Phase 2 — the durable wiring diff)

gascity currently runs `bd` in **direct ServerMode** and **re-asserts
`dolt_mode=server` on every reconcile**, so a manual metadata flip would be
reverted. Three coordinated changes make proxied+pooling stick. All paths are in
`/Users/cstar/rigs/gascity`.

### 1. `cmd/gc/beads_provider_lifecycle.go` — write proxied-server, not server

`normalizeCanonicalBdScopeFiles` / `normalizeCanonicalBdScopeFilesForInit` (and
the `DoltMode: "server"` literal around line 1435) re-normalize each scope's
`metadata.json` to `dolt_mode=server` on every reconcile. Change them to emit
`dolt_mode=proxied-server` and to write `proxied_server_client_info.json` with
an `external` block (host/port/user) pointing at the managed dolt (42188),
instead of (or in addition to) the server-mode host/port. This is the load-
bearing change: without it the scope is flipped back to server mode and the
proxy is bypassed.

### 2. `examples/bd/assets/scripts/gc-beads-bd.sh` — init + env

- `run_bd_init_pinned` (≈ line 2189) runs
  `bd init --server --server-host H --server-port P`. Switch to:
  `bd init --proxied-server --proxied-server-external-host H
  --proxied-server-external-port P --proxied-server-external-user "$DOLT_USER"`,
  and pass the password via `BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD` (not
  `BEADS_DOLT_PASSWORD`).
- `run_bd_pinned` (≈ line 2179) exports `BEADS_DOLT_SERVER_HOST/PORT/USER/
  PASSWORD` for every `bd` call, which forces direct server mode. For proxied
  scopes, stop exporting `BEADS_DOLT_SERVER_*`/`BEADS_DOLT_SERVER_MODE` (they
  select direct ServerMode and would shadow proxied mode) and instead export
  `BEADS_PROXY_POOL_SIZE` (e.g. `2 × max concurrent agents per rig`) plus
  `BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD` when the server requires auth.

### 3. `cmd/gc/bd_env.go` — inject the pool size into projected env

The bd command runners (`bdCommandRunnerForCity/Rig`,
`controlBdCommandRunnerFor*`) and the canonical-target env projector
(`applyCanonicalDoltTargetEnv`, ≈ line 256) build the env map for every `bd`
invocation. Add `env["BEADS_PROXY_POOL_SIZE"] = <n>` there so both agent and
**controller** bd calls front the pool. (The controller + order loop alone
generates ~13 conns/sec even with no agents, so pooling helps the controller,
not just agents.)

### 4. `local-shared-server` — collapse N+1 proxy children into one (be-pen9)

Sections 1–3 give every scope its **own** `db-proxy-child` (HQ + N rigs ⇒ N+1
children, ~1 GB RAM measured on portharbour). The `local-shared-server` backend
collapses them onto **one** shared child. The pool is already keyed by
`(capabilities, database)`, so a single proxy multiplexes every scope's database;
the collapse comes entirely from pointing every scope at **one shared proxy
rootDir** (the parent's spawn-or-reuse is keyed by rootDir).

On the beads side (landed in be-pen9):

- `proxy.BackendLocalSharedServer` (`local-shared-server`) is now a dispatchable
  backend — it fronts the managed dolt through the same external-server
  mechanism, carries the `--external-*` args across the fork, and participates
  in the upstream-ID guard (a shared proxy pointed at the wrong managed dolt is
  rejected, never silently reused).
- `doltserver.SharedProxyRootDir()` resolves the machine-wide shared rootDir
  (`~/.beads/shared-server/proxy/`, override with `BEADS_SHARED_PROXY_ROOT_PATH`).
- **Opt-in via `BEADS_SHARED_PROXY=1`**: when set, every proxied scope resolves
  its proxy rootDir to `SharedProxyRootDir()` instead of its per-scope
  `.beads/proxieddb`. **OFF by default**, so existing per-scope proxied scopes
  are byte-for-byte unchanged.

gascity wiring (the cross-rig sling that realizes the collapse in production):
export `BEADS_SHARED_PROXY=1` in the projected env (`cmd/gc/bd_env.go`, alongside
the section-3 pool-size injection) for every proxied scope, keeping the section-1
`external` block pointing all scopes at the **same** managed dolt (same
host/port ⇒ same upstream ID ⇒ they share one child). Validate on a **throwaway**
city, never live portharbour: `pgrep -f db-proxy-child | wc -l` → 1 with each
scope's store still queryable. (be-pen9 T-007 confirmed N+1→1 on a throwaway
two-scope city: both scopes resolved to one rootDir and one child served both.)

### Why this matches the measured problem

The managed dolt config (`/Users/cstar/portharbour/.gc/runtime/packs/dolt/
dolt-config.yaml`) already documents the symptom — short-lived per-call
connections piling up in `Sleep`, `read_timeout` cut to 30s to reap them — and
gascity #1978 measured 71 new conns/sec. Routing every scope through the pooled
proxy collapses that to roughly the pool size in steady state while keeping
Dolt, its versioning, and `dolt remote` sync untouched.
