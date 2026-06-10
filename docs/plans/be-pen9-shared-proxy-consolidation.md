# Consolidate per-scope db-proxy-children into ONE shared proxy (`BackendLocalSharedServer`) — Plan v0.1

**Status:** Draft · **Date:** 2026-06-08 · **Author:** voxist.planner · **Rig:** beads · **Bead:** be-pen9 · **Base:** `feat/connection-pooling`

## Context

Connection-pooling Phases 0 (be-8nd) and 1 (ga-bgub9) are merged and live. The
proxy now pools warm backend connections per scope, but each **scope** (HQ + N
rigs) still spawns its **own** `db-proxy-child`: portharbour measured ~17
children at ~1 GB RAM. They all front the *same* managed dolt, so the
fragmentation is pure overhead — N+1 listeners, N+1 pools, N+1 idle watchers
where one would do.

The decisive question (Karel): **is the warm backend pool DB-pinned?** It is
not. The pool is keyed by `(capabilities, database)`:

- `internal/storage/dbproxy/proxy/pool.go:27` — `backendKey{caps uint32; db string}`
- `internal/storage/dbproxy/proxy/pooledconn.go:39` — `key := backendKey{caps: ch.capabilities, db: ch.database}`

So a **single** `backendPool` already multiplexes many databases onto one
listener, each `(caps, db)` getting its own warm sub-stack. This is **proven
green today**:

- `proxy/multidb_isolation_test.go::TestSpike_MultiDatabaseIsolation` — two DBs
  (`rig_a`, `rig_b`) interleaved through one pool, each reads only its own
  marker, heavy reuse (commit `a3dde55be`).
- `proxy/mysqlwire_spike_test.go::TestSpike_ConnectionCounterFlat` — dolt-side
  `Connections` stays flat across 40 sessions.

What is **missing** is the process-level collapse. The proxy already
spawn-or-reuses by **rootDir**: `GetCreateDatabaseProxyServerEndpoint(rootDir,
opts)` (`proxy/endpoint.go:131`) does `readAndDial(rootDir)` first and only
forks a child when no live proxy answers at that rootDir's pidfile. Today each
scope passes a **per-scope** rootDir (`cmd/bd/proxied_server.go:24`
`proxiedServerRoot = <beadsDir>/proxieddb`), so every scope gets its own lock,
its own pidfile, its own child. Point every proxied scope at **one shared
rootDir** and the `proxy.lock` race / `readAndDial` reuse collapses them to a
single child whose `(caps, db)` pool serves them all.

The backend enum already reserves the slot for this — `proxy/backend.go:18`
`BackendLocalSharedServer = "local-shared-server"` — but it is a **constant with
no dispatch**: `cmd/bd/db_proxy_child.go:103-104` returns
`fmt.Errorf("backend %q: not yet implemented")`, and `endpoint.go`'s validation
switch and `--external-*` flag plumbing have no case for it. This plan wires
it.

## Constraints

- **HARD: validate on a THROWAWAY proxied city, never live portharbour.**
  Changing the proxy topology against the running fleet risks all 18 rigs (same
  discipline as ga-bgub9 T-004). All end-to-end assertions run on a temp city.
- The change is **opt-in**: only scopes explicitly configured for the shared
  backend take the new path. `external` and `local-server` scopes are
  byte-for-byte unchanged (compose with Phase 0/1).
- Dolt, its versioning, and `dolt remote` sync are untouched — the proxy is
  stateless connection plumbing in front of the managed dolt.
- Spike/integration tests are gated `//go:build cgo` and skip when `dolt` is not
  on `PATH`. Raw `go test`/`go vet` need the icu4c CGO flags
  (`-I/-L $(brew --prefix icu4c)`); see [gascity dev loop] memory.
- House style: defer to the rig's existing error-wrapping / logging idioms
  (`fmt.Errorf("...: %w")`, the `[proxy]` logger) — match `endpoint.go` /
  `db_proxy_child.go` as written.

## Proposed approach

Empirical-first (Karel's STEP 1 → 2 → 3), de-risked because the pool half of
STEP 1 is already green:

1. **STEP 1 — confirm process collapse (T-001).** The pool-multiplex hypothesis
   is pre-confirmed (tests above). The residual unknown is topological: two
   scopes at one shared rootDir → one child. Prove it at the
   `GetCreateDatabaseProxyServerEndpoint` reuse seam (simulate the first proxy
   via a written pidfile + listener, exactly like `endpoint_mismatch_test.go`),
   and prove the shared backend's upstream-ID mismatch guard rejects a proxy
   fronting the *wrong* managed dolt. **Gate:** if collapse cannot be shown,
   STOP and re-sling `voxist.platform-architect` (this is STEP 3 territory —
   per-database sub-pools behind one listener — a different design).
2. **STEP 2 — wire `BackendLocalSharedServer` (T-002…T-006).** Dispatch the
   backend to a `server.DatabaseServer` fronting the managed dolt; add endpoint
   validation, upstream-ID, and `--external-*` flag plumbing for it; resolve a
   single machine-wide shared proxy rootDir and have the provider pass it.
3. **STEP 3 — validate + document (T-007…T-008).** Throwaway-city e2e proving
   N+1 → 1, then build/vet/test green and the deployment-doc note.

The likely-lightest wiring (to be confirmed in review, see Open questions): the
shared backend's concrete server is the **same** external-dolt server
(`server.NewExternalDoltServer`) pointed at the managed dolt — the *collapse*
comes entirely from the shared **rootDir**, not from a new server type. The new
backend exists to make that rootDir policy explicit, validated, and
discoverable.

## Micro-tasks

> One bead, one PR. The executor consumes the whole table in a single session.
> First task is the failing test (TDD, architecture §10). `est` is minutes.

| id | description | acceptance | est | slings |
|---|---|---|---|---|
| T-001 | Write failing test for shared-rootDir process collapse + upstream-ID guard, simulating the first proxy via a pidfile (per `endpoint_mismatch_test.go`). | New `proxy/endpoint_shared_test.go::TestSharedRootDir_ReusesOneChild` (second `GetCreateDatabaseProxyServerEndpoint` call, same rootDir, `BackendLocalSharedServer` → returns the same `Endpoint`, no fork) **and** `::TestSharedBackend_RejectsUpstreamMismatch` (pidfile with wrong `UpstreamID` → `ErrUpstreamMismatch`) — both RED. | 5 | — |
| T-002 | Write failing test that `newDatabaseServer(BackendLocalSharedServer, …)` yields a usable server, not the "not yet implemented" error. | `cmd/bd/db_proxy_child_test.go::TestNewDatabaseServer_SharedServer` asserts non-nil server + nil error — RED (`db_proxy_child.go:104`). | 3 | — |
| T-003 | Implement the `BackendLocalSharedServer` case in `newDatabaseServer` to front the managed dolt (reuse `server.NewExternalDoltServer(external)`); read backend password for the shared backend too. | T-002 passes; `db_proxy_child.go:74` password gate also covers `BackendLocalSharedServer`. | 5 | — |
| T-004 | Add the `BackendLocalSharedServer` case to `endpoint.go`'s validation switch (require `LogFilePath` + `External.Validate()`) and make `intendedUpstreamID` return `server.ExternalDoltServerID(opts.External)` for it. | T-001's `TestSharedBackend_RejectsUpstreamMismatch` passes; a shared call missing `External` returns a validation error (assert in `endpoint_shared_test.go`). | 5 | — |
| T-005 | Extend `forkExecChild` to append the `--external-*` args for `BackendLocalSharedServer` (currently gated `== BackendExternal`). | New `proxy/endpoint_args_test.go::TestForkArgs_SharedCarriesExternal` (extract a pure `childArgs(opts, port)` seam; assert it includes `--external-host/-port` for the shared backend) — RED then GREEN. | 5 | — |
| T-006 | Add a machine-wide `SharedProxyRootDir()` resolver and have the proxied UOW provider pass it (not the per-scope `proxieddb`) when the shared backend is selected. | New `..._test.go::TestSharedProxyRootDir_StableAcrossScopes` (two distinct `beadsDir`s resolve to the **same** shared proxy rootDir) — RED then GREEN. | 5 | — |
| T-007 | Validation gate (timeboxed): on a THROWAWAY proxied-shared city, init two scopes and assert exactly one child. | `pgrep -f 'db-proxy-child' \| wc -l` == 1 with both scopes' stores queryable (`bd list` succeeds against each); before/after child count pasted into the bead. NOT run against live portharbour. | 5 | — |
| T-008 | `go build`/`go vet`/dbproxy tests green (icu4c CGO flags); add a beads-side note to `docs/CONNECTION_POOLING_DEPLOYMENT.md`; capture the cross-rig gascity sling. | `go build ./... && go vet ./... && go test ./internal/storage/dbproxy/...` green; deployment-doc Phase-2 section names the new `local-shared-server` backend. | 5 | gascity/voxist.executor (see Open questions: gascity wiring) |

## GDPR data-flow impact

### Data added / removed / relocated
None. This changes **how many proxy processes** front the same managed dolt, not
what data is stored or where. Beads issue data and the dolt store are untouched;
no personal data is read, written, or moved.

### New cross-border transfers (or "none")
None. The shared proxy listens on loopback (`127.0.0.1`) and fronts the same
local managed dolt; no new network egress, no new region.

### Audit-log changes (or "none")
None. Proxy/dolt logging is unchanged (a single shared `proxy.log` rather than N
per-scope logs is fewer files, not different content).

## MDR Class I traceability

Not applicable — not a clinical path. This is beads/gascity orchestration
infrastructure; no `voxmemo` clinical-documentation data crosses it and no
chain-of-evidence metadata is involved.

## Acceptance criteria

- Two proxied scopes against **one shared rootDir** → exactly **ONE**
  `db-proxy-child` serves **both** databases via the `(caps, db)` pool.
- On a multi-scope proxied city, total `db-proxy-children` drops from **N+1 to
  1**; total Dolt connections stay bounded (pool hits dominate).
- Validated on a **throwaway** proxied city, not live portharbour (T-007).
- `go build` + `go vet` + the spike/unit tests green (T-008).
- `external` and `local-server` scopes are unaffected (no regression in
  `endpoint_mismatch_test.go`, `proxy_pool_integration_test.go`).

## Rollback plan

1. **Git-level.** Revert the `gc/be-pen9` PR (single squashed commit on
   `feat/connection-pooling`). Because the backend is opt-in (selected only when
   a scope is configured for `local-shared-server`), reverting removes the
   *option*; all `external`/`local-server`/embedded scopes keep working
   untouched.
2. **Data-level.** None — no schema change, no migration. The only live state is
   a running shared `db-proxy-child`; on revert, kill it (`pkill -f
   db-proxy-child`) and let scopes respawn their per-scope proxies under the
   prior config. Dolt data and `dolt remote` sync are never touched.
3. **Decision criteria.** Trigger rollback if, on the throwaway city, any of:
   (a) a scope reads another scope's store (cross-store bleed — the
   `multidb_isolation` invariant broke); (b) the dolt `Connections` counter is
   **not** bounded (collapse/pool not effective); (c) the single shared proxy
   crashing takes down all scopes without clean spawn-or-reuse recovery
   (blast-radius regression vs. N independent proxies).

## Open questions

- **(executor/reviewer) Is a new backend type even necessary?** The collapse
  comes from the shared **rootDir**, not the server type — `BackendExternal` +
  a shared rootDir might suffice. Karel's design and the reserved constant say
  model it as a backend (explicit validation + discoverability + a seam for
  STEP 3 sub-pools). Confirm in PR; if `external`+shared-rootDir is judged
  enough, T-003 collapses to "alias `local-shared-server` to the external
  server" and the value is purely the rootDir policy in T-006.
- **(executor/reviewer) Shared proxy rootDir location + override name.** Under
  `doltserver.SharedServerDir()/proxy`? A new well-known dir? Honor a
  `BEADS_SHARED_PROXY_ROOT_PATH` env (mirroring the existing
  `BEADS_PROXIED_SERVER_ROOT_PATH`)? Pick the name consistent with the existing
  envs in `cmd/bd/proxied_server.go`.
- **(executor/reviewer) Blast radius / SPOF.** One shared proxy fronts ALL
  scopes. Confirm the existing stale-pidfile + child-flock recovery in
  `spawnAndHandoff` (`endpoint.go:198-258`) cleanly handles a shared-proxy
  crash (next scope touch re-spawns one). Raise to `platform-architect` if the
  recovery path needs hardening for the shared topology.
- **(executor/reviewer) Idle timeout.** A shared proxy touched by many scopes
  stays warm trivially, but if all go idle it dies and respawns on next touch.
  The `BEADS_PROXY_IDLE_TIMEOUT` override already exists (`endpoint.go:104`);
  confirm the default suits the shared topology or document the recommended
  value.
- **(cross-rig sling — NOT routed from here) gascity production wiring.**
  Realizing N+1 → 1 *in production* needs the gascity side to select the shared
  backend, per `docs/CONNECTION_POOLING_DEPLOYMENT.md` §1-3 (paths in
  `/Users/cstar/rigs/gascity`): `cmd/gc/beads_provider_lifecycle.go` (emit the
  shared-proxied mode), `examples/bd/assets/scripts/gc-beads-bd.sh` (init +
  env), `cmd/gc/bd_env.go` (project pool size). This is a separate `ga-` bead;
  per the planner hard rule it must route to **`gascity/voxist.executor`**, not
  any planner. Sling-ready text:
  > **Title:** Wire gascity to select the shared db-proxy backend (N+1 → 1)
  > **Body:** With beads be-pen9 landed (`local-shared-server` backend +
  > shared proxy rootDir), switch gascity's proxied-scope wiring to select it so
  > all scopes collapse onto one shared proxy. Apply `CONNECTION_POOLING_DEPLOYMENT.md`
  > §1 (`beads_provider_lifecycle.go` → emit shared-proxied + client info), §2
  > (`gc-beads-bd.sh` init/env), §3 (`bd_env.go` pool-size projection). Validate
  > on a throwaway city: `pgrep -f db-proxy-child | wc -l` → 1. Base: a beads
  > release that includes be-pen9.
  > **Depends on:** be-pen9 (beads side) merged + released.

## Status

All micro-tasks green; one PR opens the accumulated per-task commits on `gc/be-pen9`.

- [x] T-001 — failing test: shared-rootDir reuse + upstream-ID guard   ✅ green at `be89a1897`
- [x] T-002 — failing test: `newDatabaseServer(BackendLocalSharedServer)` usable   ✅ green at `002316c49`
- [x] T-003 — dispatch `BackendLocalSharedServer` → `NewExternalDoltServer`; password gate covers it; obsolete `…StillStubbed` test replaced   ✅ green at `002316c49`
- [x] T-004 — `intendedUpstreamID` + endpoint validation switch cover the shared backend (`External` required)   ✅ green at `be89a1897`
- [x] T-005 — extracted pure `childArgs(rootDir, opts, port)` seam; shared backend carries `--external-*` via `backendCarriesExternal`   ✅ green at `c2753121e`
- [x] T-006 — `doltserver.SharedProxyRootDir()` machine-wide resolver + opt-in `BEADS_SHARED_PROXY` rootDir chokepoint in `resolveProxiedServerRootPath`   ✅ green at `587f3c9bc`
- [x] T-007 — THROWAWAY proxied-shared city (NOT live portharbour): two external-proxied scopes → **1** db-proxy-child (before 0 → init A 1 → init B still 1; both stores queryable). N+1→1 confirmed; live fleet untouched. Evidence in the bead.
- [x] T-008 — `go build ./...` + `go vet ./...` + `go test ./internal/storage/dbproxy/...` green; deployment-doc §4 (`local-shared-server`) added; cross-rig gascity sling captured as `ga-mozik` (gascity store, gated on be-pen9 release, unrouted by design)   ✅ green at `70a019042`

### Resolved open questions

- **New backend type necessary?** Kept as the plan's lightest wiring: `local-shared-server` dispatches to `NewExternalDoltServer` (same server type); the collapse is the shared rootDir. The distinct backend buys explicit validation, the upstream-ID guard, and `--external-*` fork plumbing — worth it for discoverability and a STEP-3 seam.
- **Shared rootDir location + override.** `doltserver.SharedProxyRootDir()` → `~/.beads/shared-server/proxy/`, override `BEADS_SHARED_PROXY_ROOT_PATH` (mirrors `BEADS_SHARED_SERVER_DIR`). Scope opt-in is `BEADS_SHARED_PROXY` (truthy), OFF by default so existing proxied scopes are byte-for-byte unchanged.
- **Blast radius / idle timeout.** Unchanged from the existing proxy: `BEADS_PROXY_IDLE_TIMEOUT` keeps a shared proxy warm; spawn-or-reuse recovery in `endpoint.go` is identical (one rootDir). No hardening needed for the opt-in beads side; production blast-radius tuning rides the gascity sling (`ga-mozik`).
