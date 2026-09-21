# Embedding over a proxied-server workspace: observed behaviour at 1.3.1

Last reviewed: 2026-09-21

Read at: `d487b9796` (v1.3.1-rc.1). Because this document describes observed
behaviour rather than a contract, a stale marker makes it wrong, not merely old.
Re-read it against the sources below before trusting it against a later build.

Freshness source: `internal/storage/dbproxy/pidfile/pidfile.go`,
`internal/storage/dbproxy/proxy/` (`endpoint.go`, `server.go`, `shutdown.go`,
`read_status.go`, `backend.go`), `internal/storage/dbproxy/server/doltserver.go`,
`internal/configfile/proxied_server_client_info.go`,
`internal/configfile/configfile.go` (the storage-mode predicates),
`internal/doltserver/physical_root.go`,
`internal/storage/uow/doltserver_provider.go`,
`internal/storage/uow/dolt_sql_provider.go`,
`internal/storage/schema/remote_migrate_gate.go`,
`internal/storage/schema/lock.go`, `internal/storage/storage.go`,
`cmd/bd/db_proxy_child.go`, `cmd/bd/dolt.go`,
`cmd/bd/dolt_proxied_lifecycle.go`, `cmd/bd/migrate.go`, `cmd/bd/main.go`,
`cmd/bd/proxied_server.go`, `cmd/bd/proxy_capability.go`,
`cmd/bd/uow_factory.go`, `beads.go`, `beads_cgo.go`, `beads_nocgo.go`.

## What this document is, and is not

An embedder — an external tool that links bd's Go packages, or one that runs
beside `bd` against the same workspace — ends up leaning on surfaces bd never
published: a PID file's field names, a child process's argv, the shape of a
`--json` payload, whether a Go symbol is exported. This document writes those
down as **observed behaviour at 1.3.1**, so that an embedder can build against
what the code actually does instead of guessing.

It is not a compatibility contract. Nothing here is a published interface with a
deprecation policy behind it. Every surface below is an implementation detail
that bd's own code reaches through, and each one may change in any release,
including a patch release. Four of the eleven are moving in 1.3.1 itself and say
so in place: §10 has landed and is described in its new shape, and §5, §6 and
§11 are decided for 1.3.1 but had not landed at the commit above, so each is
given as before/after with the current behaviour marked as current. Read this as
a snapshot with the commit it was taken at, and re-read it against the source
before an upgrade.

Two things follow for anyone building on this:

- **Prefer the front door.** Where `bd` offers a command or a `--json` payload
  that answers your question, go through it. The control files under the proxied
  root are bd's private lifecycle state; reading them means tracking bd's
  lifecycle work release by release.
- **Fail loudly on a shape you do not recognise.** A missing JSON key, an
  unexpected `schema` number, a new argv flag — treat these as "this bd is not
  the one I was written against", not as a default to paper over.

Everything below was checked by reading the source at `d487b9796`. Where a claim
would need a running proxy to confirm end to end, it says so.

## The topology being described

"Proxied-server" mode puts a small bd-owned TCP proxy between every bd client
and a `dolt sql-server`. The proxy is a separate detached process (`bd
db-proxy-child`), it starts on demand when a command needs the store, it owns
the lifetime of its `dolt` child, and it exits when idle. The workspace's
**proxied root** holds the proxy's control files; it defaults to `.beads/dolt`
and is resolved by `doltserver.ResolveProxiedServerRootPath` from, in order:
`BEADS_PROXIED_SERVER_ROOT_PATH`, the sidecar's `root_path`, then the default.

The control files in that root, as enumerated by `proxy.ControlFilePaths`:

| File | Written by | Holds |
|---|---|---|
| `proxy.pid` | the proxy | the published endpoint record (§1) |
| `proxy.lock` | the proxy | held for the proxy's whole lifetime |
| `proxy.log` | the proxy | proxy trace log |
| `proxy.secret` | the proxy | 64 hex chars authenticating the control port |
| `proxy.spawn` | the spawning parent | in-progress-start marker |
| `proxy.stop-epoch` | `bd dolt stop` | monotonic token that dooms in-flight starts |
| `proxy-child.pid` | the proxy | the `dolt sql-server` record |
| `proxy-child.lock` | the proxy | held while the backend runs |
| `<name>.stale-<unix>` | either | quarantined records, swept after 30 days |

Two topologies share this machinery: **managed-local**, where bd spawns the
`dolt sql-server` itself, and **external**, where the proxy fronts a
`dolt sql-server` somebody else runs. They differ in several of the surfaces
below, and the difference is usually the part an embedder gets wrong.

---

## 1. The `proxy.pid` record

`internal/storage/dbproxy/pidfile/pidfile.go` — one `PidFile` type serves both
records. JSON keys, in declaration order:

```json
{
  "pid": 251973,
  "port": 33825,
  "upstream_id": "…",
  "schema": 2,
  "kind": "db-proxy",
  "birth": "…",
  "root_id": "…",
  "control_port": 41277
}
```

`pid` and `port` are always present. Every other field carries `omitempty`, so a
zero value is absent from the file rather than written as `0` or `""`.

- `port` is the **data port** — the loopback MySQL-protocol endpoint bd clients
  connect to.
- `control_port` is a separate loopback listener that answers an authenticated
  identity challenge using the secret in `proxy.secret`. It is not a data port.
- `kind` is `"db-proxy"` in `proxy.pid` and `"dolt-backend"` in
  `proxy-child.pid` (`pidfile.KindProxy` / `pidfile.KindDoltBackend`).
- `schema` is `2` today (`pidfile.SchemaV2`). `PidFile.ValidateV2` accepts
  `schema >= 2`, so a later bd may raise it without bd's own readers rejecting
  the record; a reader of your own should do the same rather than equality-match.
- `birth` is a platform-specific process-identity token, and `root_id` is the
  SHA-256 of the symlink-resolved absolute proxied root. Treat both as opaque.

`ValidateV2` rejects a record with `schema < 2`, `pid <= 0`, a `port` outside
1–65535, a `control_port` that is neither 0 nor 1–65535, a `kind` other than the
expected one, or an empty `birth`.

**What an embedder can observe, and the trap.** Reading `port` out of
`proxy.pid` is not the same as knowing a proxy is live on it. Before bd adopts a
record (`proxy.readAndDial`) it also verifies `birth` against the live PID,
completes the authenticated challenge on `control_port` and cross-checks every
field of the reply against the file and the workspace, and TCP-probes the data
port. A record left behind by a killed proxy passes a naive parse and points at
a port that may since have been recycled by an unrelated process. If you read
this file at all, verify liveness yourself before connecting — or use `bd dolt
status --json` (§10), which performs that whole check for you.

## 2. The `db-proxy-child` argv

`proxy.forkExecChild` in `internal/storage/dbproxy/proxy/endpoint.go` builds the
argv; `cmd/bd/db_proxy_child.go` declares the flags. The command is hidden and
is not meant to be invoked by hand.

Always present, in this order:

```
bd db-proxy-child --root <abs proxied root> --port <n> --idle-timeout <dur> --backend <kind> --stop-epoch <token>
```

Then, each only when its value is non-empty:

```
--config <path>  --logpath <path>  --dolt-bin <path>  --database <name>
```

And, only when `--backend` is `external` — and then, like the group above, each
only when its own value is set (non-empty string, non-zero number or duration):

```
--external-host <host>  --external-port <n>  --external-socket-path <path>  --external-keep-alive <dur>
```

Both conditions matter, because neither alone predicts the argv. The group is
skipped wholesale on the other two backends, and inside it you never see all
four: `ExternalDoltConfig.Validate` rejects a socket set alongside a host or
port, and refuses a host without a port, so a socket topology carries only
`--external-socket-path` and a TCP one carries `--external-host` *and*
`--external-port` together. `--external-keep-alive` is the one flag whose
absence is a default rather than a gap — the child's flag help documents 30s —
so read a missing one as "unset", not as "off".

`--root`, `--port` and `--backend` are the required flags. `--backend` takes one
of `local-server`, `external`, or `local-shared-server` — the last is declared
but returns "not yet implemented" from `newDatabaseServer`.

Notes on three values an embedder is likely to read:

- **`--root`** is always an absolute path. The provider resolves it with
  `filepath.Abs` before the proxy is opened.
- **`--port 0`** is the normal case, not a literal port: the child binds an
  OS-assigned port and publishes it in `proxy.pid`. A non-zero value is only
  passed when the workspace pins one.
- **`--idle-timeout`** is a Go duration string. Negative means never, and the
  parent normalises *any* negative value to `proxy.IdleTimeoutNever` (`-1`), so
  the argv you will actually see for "never" is `--idle-timeout -1ns`. The
  child's own flag help states `0 or negative = never shut down`, which matches
  the watcher in §4. This is the third of three different encodings of the same
  setting — see §5 before writing one.

## 3. A loopback root / no-password client on the data port

Two separate facts combine here, and they live in different files.

**The proxy does not parse what it carries.** `proxyServer.handleConn` in
`internal/storage/dbproxy/proxy/server.go` dials the backend once per accepted
client connection and then runs two `io.Copy` goroutines, client→backend and
backend→client, in an errgroup. There is no MySQL protocol awareness, no
connection pooling and no multiplexing: the proxy is a byte pump, and *N* client
connections become *N* backend connections. When either direction finishes, or
the proxy's context is cancelled, both sides are closed.

**The managed-local backend authenticates as `root` with no password.** In
`newManagedProxiedServerUOWProvider` (`cmd/bd/uow_factory.go`) bd passes the
literal `"root"` and `""` to the provider, commented "proxy is loopback-only, no
auth". The generated `dolt sql-server` config (`renderProxiedServerConfig` in
`cmd/bd/proxied_server.go`) binds `127.0.0.1` on a free port, sets log level
`info` and auto-GC archive level `0`, and declares no users section, so Dolt's
default `root` with an empty password applies. The same pair turns up once more
inside the proxy's own process tree, in `DoltServer`'s shutdown-GC connection
(`internal/storage/dbproxy/server/doltserver.go`).

**The readiness probes prove nothing about credentials.** Both of them —
`DoltServer.waitReady` and the proxy's `waitForServerReady`
(`internal/storage/dbproxy/proxy/server.go`) — poll `DatabaseServer.Dial`, which
is a bare `net.Dialer` connect to the backend's host/port or socket, closed again
the moment it succeeds. No handshake is attempted and no credentials are
supplied, so "ready" here means only that something is listening. That is worth
knowing in both directions: it is why the probes cannot be cited as evidence of
the auth posture, and it is the same shallowness §11 turns on.

**What an embedder can observe.** On a managed-local proxied workspace, a
plain MySQL client connecting to `127.0.0.1:<port from proxy.pid>` as `root`
with no password reaches the same database bd does, and the proxy passes the
handshake through untouched. That is what lets an embedder read and write the
workspace's live database without standing up a server of its own.

Three qualifications worth carrying:

- It follows from the *generated config*, not from a decision to offer an
  authentication posture. A workspace that supplies its own server config, or
  the external topology (where credentials come from `ExternalDoltConfig` and
  the password from that config's environment variable), does not behave this
  way.
- The loopback bind is the whole access control. Anything that can open a TCP
  connection to that port on the host is `root` on the database.
- The **control** port in the same record is not open in this sense: it requires
  the `proxy.secret` value.

## 4. Open connections, idle exit, and `bd dolt stop`

**Any accepted connection defers idle exit.** `proxyServer.idleWatcher`
(`internal/storage/dbproxy/proxy/server.go`) returns immediately into a wait
when `idleTimeout <= 0` — that is the "never" case. Otherwise it ticks every
`idleTimeout/4`, with a 1s floor. A tick that sees `activeConns > 0` clears the
armed state; the first tick that sees zero *arms* a timestamp; a later tick then
exits once `timeout` has elapsed since arming. `activeConns` is incremented in
`handleConn`, so an accepted TCP connection counts even if it never sends a
byte.

Two consequences an embedder should plan for:

- Holding one idle connection open keeps the proxy and its backend alive
  indefinitely. That falls out of how the watcher counts, so it is as much an
  easy way to pin the tree up by accident as a deliberate one — and it is a
  property of the watcher, not an offer.
- Exit is not punctual. Arming costs a tick, so the observed lifetime after the
  last connection closes is longer than the configured timeout — up to roughly
  two ticks longer.

**`bd dolt stop` does not wait for clients.** On a proxied workspace (`cmd/bd/dolt.go`)
the command calls `proxy.Shutdown(rootDir)`, which:

1. advances `proxy.stop-epoch`, which makes every start attempt already in
   flight abort terminally rather than retry;
2. acquires `proxy.lock`, re-reads `proxy.pid`, validates it against the
   workspace, and **kills the recorded process** — `procid.Handle.Kill` is
   `SIGKILL` on Linux and macOS, `os.Kill` on Windows;
3. waits only for that *process* to be gone (`max(remaining of 5s, 2s)`), then
   removes the record;
4. repeats steps 2 and 3 for `proxy-child.pid` — the stop epoch is advanced once
   for the whole call, not per record.

There is no drain phase, no notification to connected clients and no wait on
`activeConns` anywhere in that path. An embedder's in-flight query is severed
when the proxy dies. The proxy does install SIGTERM/SIGINT handlers for its own
graceful path, but `bd dolt stop` does not use them, and even that path
force-closes both sides of every connection on context cancellation rather than
draining.

If `Shutdown` refuses because a record's identity cannot be verified,
`bd dolt stop --force` escalates to `proxy.ForceStopUnverified`; the refusal is
recognisable with `proxy.CanForceStopUnverified`.

## 5. The sidecar's `idle_timeout` — changing in 1.3.1

One setting has three encodings, and they disagree about what `0` means. Getting
this wrong is how a workspace ends up with a proxy that never exits, or one that
exits under a long-running embedder.

**(a) The CLI flags.** `bd init --proxied-server-idle-timeout` and the
`--idle-timeout` flag on the proxied-mode migrate verbs share a convention:
omitting the flag selects the built-in default, an explicit `0` means *never*,
and a negative value is refused outright ("must be 0 (never) or a positive
duration"). `resolveMigrateIdleTimeout` and `init.go` both map that explicit `0`
to `proxy.IdleTimeoutNever` before storing it.

**(b) The sidecar file.**
`.beads/proxied_server_client_info.json`, typed by
`configfile.ProxiedServerClientInfo`:

The full key set is `root_path`, `config_path`, `log_path`, `port`,
`idle_timeout`, `external` — and **every one of them carries `omitempty`**, so a
realistic managed-local sidecar with an OS-assigned port and an explicit 45s
timeout is just:

```json
{
  "root_path": "/abs/path/.beads/dolt",
  "idle_timeout": 45000000000
}
```

`port: 0` (the OS-assigned case), a nil `external`, and unset paths are all
absent rather than written as `0`, `null` or `""`. Paths, when present, may be
relative to the `.beads` directory; the `Resolved*Path` helpers on the type are
what bd uses to join them.

`idle_timeout` is a Go `time.Duration` with no custom marshaller, so it is **an
integer number of nanoseconds**, not a duration string: 45s is `45000000000`,
30s is `30000000000`, never is `-1`. Combined with `omitempty`, that means a
value of `0` is written as *no key at all*.

**(c) The effective value.** `NewDoltServerUOWProvider`
(`internal/storage/uow/doltserver_provider.go`) applies
`if idleTimeout == 0 { idleTimeout = defaultProxyIdleTimeout }`, and
`defaultProxyIdleTimeout` is **30s** (`internal/storage/uow/dolt_sql_provider.go`).
The external provider does the same. So, at the sidecar layer:

| sidecar `idle_timeout` | effective |
|---|---|
| key absent | 30s |
| `0` (cannot occur — `omitempty` drops it) | 30s |
| negative, e.g. `-1` | never |
| positive | as written |

**What is changing in 1.3.1.** Writing the *effective* `idle_timeout`
explicitly, rather than letting `omitempty` drop it and relying on the
provider-side default, is approved for 1.3.1. It had not landed at
`d487b9796`, and there is no tracking issue for it at the time of writing.

Before (observed at this commit): a workspace on the default timeout has **no**
`idle_timeout` key in its sidecar, and "default" is therefore indistinguishable
from "unset" to any reader. That also makes the effective value invisible in
`bd dolt status --json`, which only emits `idle_timeout` when the sidecar value
is `> 0` (§10) — so both "default 30s" and "never" render as an absent key.

After: the key is expected to be present with the effective value. An embedder
should read an absent key as "ask this bd, do not assume", not as "no timeout".

## 6. `bd migrate schema` on a proxied workspace — changing in 1.3.1

**Observed at this commit: it is permitted, and it migrates.**

`proxyMaintenanceRefusals` in `cmd/bd/proxy_capability.go` refuses the bare
`migrate` command with code `proxy.migrate.unsupported`, and its `init()`
expands child rows for `migrate hooks` and `migrate issues`. `migrate schema` is
in none of those tables, and `validateProxyMaintenanceBeforeProvider` returns
`nil` for any unmatched multi-word command path — so the verb reaches the store
open.

The mechanics matter, because they are not "a command that applies a migration":

- `cmd/bd/main.go` sets `schema.SetSharedMigrateConsent(...)` in the root
  pre-run for this one verb (`isSchemaMigrateVerb` matches `bd migrate schema`
  and deliberately nothing else in the `migrate` tree). That consent is what
  satisfies the shared-store gate in §7.
- The **provider open then performs the migration**, before `RunE` runs.
- `RunE`'s proxied arm (`reportProxiedSchemaMigrate` in `cmd/bd/migrate.go`)
  therefore only reports, and it reports a **different JSON shape** from the
  direct path:

```json
{
  "status": "current",
  "latest_version": 66,
  "mode": "proxied-server",
  "note": "schema reconciled during provider open"
}
```

(`latest_version` is `schema.LatestVersion()`, shown here illustratively — read
the value, do not hard-code it.) The direct path emits `{"status", "applied",
"latest_version"}`; the proxied arm drops `applied` and adds `mode` and `note`,
so only `status` and `latest_version` are common to both. No applied count is
reported because by the time `RunE` runs the work belongs to an open that has
already returned — there is no number for it to have. An embedder decoding this
payload into one struct across both topologies should treat `applied` as
absent-not-zero on the proxied route, and should not infer "nothing was applied"
from its absence.

**What an embedder can observe.** The DDL lands under co-resident clients that
already hold connections and prepared state, with no coordination of any kind —
the proxy is a byte pump (§3) and has no notion of who is connected. An embedder
whose process outlives a `bd migrate schema` should treat the schema as changed
beneath it and re-establish its connections; in practice, restart.

**What is changing in 1.3.1.** Refusing `bd migrate schema` on a proxied
workspace is approved for 1.3.1: a flat typed refusal by default, with `--force`
emitting a **generic** warning to close co-resident library clients first. The
approved scope is a generic warning with no enumeration of connected clients, for
the reason above — the proxy does not track them, so there is nothing truthful to
list. This had not landed at `d487b9796`, and there is no tracking issue for it
at the time of writing.

So an embedder should expect the verb to stop working on proxied workspaces
rather than build a workflow on it, and should not read the absence of a refusal
today as the absence of one tomorrow.

## 7. The shared-store migration gate looks only at the main lane

`schema.CheckSharedStoreMigrateGate`
(`internal/storage/schema/remote_migrate_gate.go`) is the gate for a database
served to co-resident clients, which includes every proxied workspace. On top of
the remote-backed flow it adds two refusals: with no remote it refuses a silent
in-place migration unless the operator consented (the verb in §6, `--force`, or
the environment override), and with a remote it suppresses the two auto-execute
arms, because on a shared server neither can observe the other clients.

Its decision facts are read from the **main lane only**. The shared arm delegates
to `checkRemoteMigrateGate`, which calls `schema.CurrentVersion` and
`schema.PendingVersions` — both of which resolve to `mainSource`. The
`dolt_ignore`d lane has its own cursor and its own accessors
(`CurrentIgnoredVersion`, `PendingIgnoredVersions`), and the gate consults
neither.

`internal/storage/schema/lock.go` shows where that lands: the gate runs under
the database-scoped migration lock immediately before `MigrateUp`, and
`MigrateUp` reconciles **both** lanes.

**What an embedder can observe.** A main-lane pending migration is what a
refusal is about. An open whose only pending work is on the ignored lane sees
the gate return early (`len(pending) == 0` — nothing to migrate) and then watches
`MigrateUp` apply that ignored-lane work anyway. So "the gate did not refuse"
does not mean "no schema changed under me". Two related carve-outs also let work
through: a fresh database (`current == 0`) always migrates, on the reasoning that
creating a database is consent for its schema, and a fresh-bootstrap heal
authority from the same logical open bypasses the gate entirely.

## 8. `SetEventsJournalEnabled` on `OpenBestAvailable`'s `Storage`

`OpenBestAvailable` is the public open path in package `beads` at the module
root. It exists in two build-tagged copies: `beads_cgo.go` (`//go:build cgo`),
which handles embedded Dolt, dolt-server mode and registered extension
backends; and `beads_nocgo.go` (`//go:build !cgo`), which handles the latter two
and returns an error for embedded Dolt.

**First, a routing fact worth knowing before the interface question.** Neither
copy has a proxied-server arm. Both dispatch on `cfg.IsDoltServerMode()`, and
`configfile.IsDoltServerMode` and `IsDoltProxiedServerMode` are mutually
exclusive — proxied workspaces are explicitly exempt from the host-based
server-mode inference. So on a proxied-server workspace `OpenBestAvailable` falls
through to `embeddeddolt.Open`, and it does not connect through the proxy.

The consequence is not contention over the directory the proxy is serving. It is
that the two paths look at **different directories entirely**. `embeddeddolt`
joins its data directory as `<beadsDir>/embeddeddolt` and does so in three
places — `newStore`, the open cache's `cacheKey`, and `HasRepository` — with no
way for a caller to redirect it. The proxy's `dolt sql-server`, meanwhile,
serves the proxied root, which defaults to `<beadsDir>/dolt`
(`internal/doltserver/physical_root.go`; `Config.DatabasePath`'s fallback is
commented "Always use \"dolt\"").

So an embedder that calls `OpenBestAvailable` on a proxied workspace does not
fight the running server for its files — it silently opens **a separate database**
beside it. `newStore` `MkdirAll`s `.beads/embeddeddolt`, and `initSchema` issues
`CREATE DATABASE IF NOT EXISTS` and migrates, so the open creates and writes a
second database rather than failing. What it contains depends on the workspace's
history: empty on one initialised straight into proxied mode, and whatever
`.beads/embeddeddolt` still holds on one that was ever embedded, since none of
the mode-migration verbs removes that directory. The second case is the nastier
one — stale issues that look real.

Nothing in the open path refuses on that ground and no error comes back. The
store is genuine; it is just not the one the workspace's issues are in. An
embedder that wants the proxied endpoint should connect to the data port (§3)
rather than expect this function to find it.

`SetEventsJournalEnabled(enabled bool)` is **not a method on the `Storage`
interface.** `beads.Storage` aliases `internal/beads.Storage`, which aliases
`internal/storage.Storage`; the method is declared on a separate optional
interface, `storage.EventsJournalConfigurer`
(`internal/storage/storage.go`). Its contract is per-instance: implementations
must not use process-global state, because one process can hold several stores
at once.

The two concrete types `OpenBestAvailable` can return from its Dolt arms do
implement it: `dolt.DoltStore` and `embeddeddolt.EmbeddedDoltStore` each declare
the method. (bd's own proxied path forwards it through the unit-of-work provider,
but that provider is not something `OpenBestAvailable` returns — see the routing
fact above.) An embedder reaches it by type assertion:

```go
store, err := beads.OpenBestAvailable(ctx, beadsDir)
// …
if c, ok := store.(interface{ SetEventsJournalEnabled(bool) }); ok {
    c.SetEventsJournalEnabled(true)
}
```

**Two things an embedder should know.** `storage.EventsJournalConfigurer` is
internal and is not re-exported from the public `beads` package, so the
assertion has to be written against a locally declared single-method interface
as above; there is no exported name to assert against. And a store reached
through a registered extension backend
(`backends.Lookup`, dispatched ahead of every Dolt path in both copies of
`OpenBestAvailable`) need not implement the method at all — which is what the
`ok` is for.

This is among the more likely surfaces here to move, because it is reached by
assertion rather than by a declared method: no compile error marks its
disappearance. No test at the module root asserts that
`OpenBestAvailable`'s result satisfies it, so nothing in CI fails if it stops
doing so.

## 9. `ErrCommitIndeterminate` and `ErrCircuitOpen`

Both are exported from the public `beads` package (`beads.go`) as re-exports of
the values they wrap:

```go
var (
    ErrCircuitOpen         = dolt.ErrCircuitOpen
    ErrCommitIndeterminate = storage.ErrCommitIndeterminate
)
```

Because they are the same error values the internal packages return — not
copies with equal text — `errors.Is(err, beads.ErrCircuitOpen)` and
`errors.Is(err, beads.ErrCommitIndeterminate)` match errors raised internally
and wrapped on the way out.

**What an embedder can observe.** `ErrCircuitOpen` identifies a read or write
rejected because the Dolt circuit breaker is open. `ErrCommitIndeterminate`
identifies a commit whose outcome is unknown — the distinction that lets a
caller decline to retry a write that may already have landed. Match with
`errors.Is`, not on message text — matching on these values is currently the only
discriminator a caller has for those two conditions, so there is nothing else to
write against. That is a statement about today's surface, not about its
longevity; the note at the top of this document still applies.

## 10. `bd dolt status --json` on a proxied workspace — reshaped in 1.3.1

This payload **changed shape in 1.3.1** (#6580, landed at this commit; see the
1.3.1 CHANGELOG entry). Before, on a proxied workspace, `bd dolt status` read
the classic `dolt-server.pid` that proxied mode never writes, and reported the
always-false `{"running": false, "pid": 0, "port": 0}` against a healthy
backend. `pid` and `port` are gone, replaced by the `proxy_*` and `backend_*`
pairs. Direct-server, shared, embedded and externally-managed workspaces keep
the output they had.

The proxied payload is `proxiedDoltStatus` in
`cmd/bd/dolt_proxied_lifecycle.go`:

The full key set is `mode`, `root`, `running`, `proxy_pid`, `proxy_port`,
`backend_managed`, `backend_running`, `backend_pid`, `backend_port`,
`backend_endpoint`, `idle_timeout`. Five are always present: `mode`, `root`,
`running`, `backend_managed` and `backend_running`. None of the three `bool`s
carries `omitempty` — for a bool, `false` is the answer rather than the absence
of one, and `backend_running: false` in the external example below is that rule
in action. The other six (`proxy_pid`, `proxy_port`, `backend_pid`,
`backend_port`, `backend_endpoint`, `idle_timeout`) do carry `omitempty`, so
absent means zero — including `proxy_pid`/`proxy_port` when the proxy is down.

A managed-local workspace with both processes up, whose sidecar carries an
explicit positive timeout:

```json
{
  "mode": "proxied-server",
  "root": "/abs/path/.beads/dolt",
  "running": true,
  "proxy_pid": 251973,
  "proxy_port": 33825,
  "backend_managed": true,
  "backend_running": true,
  "backend_pid": 251980,
  "backend_port": 41163,
  "idle_timeout": "45s"
}
```

The same workspace on an external topology, where bd supervises no dolt:

```json
{
  "mode": "proxied-server",
  "root": "/abs/path/.beads/dolt",
  "running": true,
  "proxy_pid": 251973,
  "proxy_port": 33825,
  "backend_managed": false,
  "backend_running": false,
  "backend_endpoint": "db.internal:3306"
}
```

Reading the fields:

- **`running` describes the proxy**, not the dolt server: the endpoint every bd
  command connects through. It is usually true where the old payload was always
  false.
- **`backend_managed` is `false` on an external proxied topology**, where the
  dolt server is not bd's process to report on and `backend_endpoint` names it
  instead (`host:port`, or `unix:<path>` for a socket). On that topology
  `backend_running` says nothing about the database — distinguishing "the
  backend is down" from "bd never had a backend to report" is exactly what the
  flag is for.
- **`idle_timeout` here is a duration *string*** (`"45s"`), unlike the sidecar's
  nanosecond integer in §5 — and it is emitted only when the sidecar's value is
  `> 0`. Both "default 30s" and "never" therefore render as an absent key today,
  which is why neither example above could show the default. That is the
  observation §5's 1.3.1 change is about.

The command has no lifecycle side effects, which is why polling it beats parsing
`proxy.pid`: `proxy.ReadStatus` reads the two records and verifies the recorded
processes without starting, stopping or adopting anything. A record that fails
any check reports nothing rather than a PID a caller might act on.

## 11. A clean backend exit — changing in 1.3.1

**Observed at this commit: the proxy does not notice.** `DoltServer.Start`
(`internal/storage/dbproxy/server/doltserver.go`) feeds `cmd.Wait()` into an
`errgroup.WithContext`, and an errgroup cancels its context only on a **non-nil**
return. A `dolt sql-server` that stops cleanly returns 0 — its ordinary
shutdown path, not a crash — so:

- the group context is never cancelled;
- `DoltServer.Running` returns `egCtx.Err() == nil`, which stays true;
- the proxy's 100ms backend health tick (`proxy/server.go`) never fires.

The proxy therefore keeps `proxy.pid`, `proxy.lock`, its control port and its
listener, and goes on accepting clients — closing each one after the backend
dial in `handleConn` fails. Adoption does not catch it either: `readAndDial`
verifies the proxy's identity and TCP-probes the *proxy*, never the backend, so
an adopting client succeeds and then fails its first query. The workspace stays
wedged until `bd dolt stop`.

This is #6651, which reports it reproduced 3/3 against v1.3.0. The trigger is a
signal delivered to the dolt child *alone* — a per-PID supervisor stop, or
`kill <dolt pid>`. A `kill -9` of the child and a cgroup-wide SIGTERM are not
affected; the latter signals the proxy too.

**What an embedder can observe.** Reading the code, this state is visible in
`bd dolt status --json` (§10) as `running: true` with `backend_running: false` —
`readBackendRecord` verifies the recorded backend PID's birth token, which a
departed process fails — while a liveness check that only adopts the proxy
reports healthy. That asymmetry is what makes §10 the better probe of the two.
*Not verified by running:* this document was written without starting a proxy,
so the `status` output in that specific wedged state is inferred from the source
rather than observed.

**What is changing in 1.3.1.** Two fixes are approved under #6651: treating
**any** `cmd.Wait()` return as a backend exit, rather than relying on errgroup's
nil-means-keep-going semantics, and making adoption **probe the backend** rather
than only the proxy. Neither had landed at `d487b9796`. After they land, a clean
backend exit is expected to tear the proxy down like any other backend loss, and
adoption is expected to decline a proxy whose backend is gone — so an embedder
should not build recovery logic that depends on the zombie persisting, nor on
adoption succeeding while the backend is absent.

---

## Which of these are most likely to move

Not all eleven carry the same risk, and it is worth saying which is which.

**Most likely to move, and already in motion:** §6 (`bd migrate schema` becomes
a refusal), §11 (clean-backend-exit handling and backend-probing adoption), §5
(the sidecar starts writing its effective `idle_timeout`). All three are decided
for 1.3.1 and described above as before/after.

**Likely to move:** §2, the `db-proxy-child` argv. It is a hidden internal
command whose flags exist to serve the parent that spawns it, and flags have
been added to it as topologies were added. §1's `proxy.pid` and the control-file
set are next: they are bd's private lifecycle state, and the lifecycle work that
added `root_id`, `control_port`, the spawn marker and the stop epoch is not
finished. §8 is likely for a different reason — an assertion-reached optional
interface can disappear without a compile error anywhere.

**Less likely to move, for a reason:** §9's two error sentinels, because
`errors.Is` on them is the only way a caller can tell those two conditions
apart. §10's payload, because it was *just* reshaped deliberately. §3's loopback
byte-pump behaviour, because the proxy's whole design is to not understand the
protocol — though the credentials half of §3 is a property of a generated config
file, which is a much softer thing than the byte-pump half.

**A caveat on that ranking.** It is a reading of the direction of current work,
not a schedule and not a promise. A surface in the third group can still change
in a patch release; it is where to spend your safety margin, not where to stop
checking.

## Not verified by running

This document was written by reading the source at `d487b9796`. No proxy was
started and no test suite was run while writing it. The claims that would need a
live proxy to confirm end to end, and are therefore inferred from the code:

- The exact `bd dolt status --json` output in the §11 wedged state
  (`running: true` with `backend_running: false`).
- The observed idle-exit latency in §4 being longer than the configured timeout
  by up to roughly two watcher ticks.
- That a third-party MySQL client authenticating as `root` with no password
  succeeds against the data port on a managed-local workspace (§3). The
  credentials, the loopback bind and the absence of protocol handling in the
  proxy are all read directly from the source; the end-to-end connection is not.
- That `OpenBestAvailable`'s embedded fall-through (§8) completes and hands back
  a usable but separate store, rather than failing somewhere in the create and
  migrate steps. The *routing* (it takes the embedded arm, because proxied mode
  is exempt from `IsDoltServerMode`) and the *directory* (`embeddeddolt` joins
  `<beadsDir>/embeddeddolt`, the proxy serves `<beadsDir>/dolt`) are read off the
  source and are not in doubt; the open itself was not run against a live
  proxied workspace.

Everything else — field names, JSON keys and their types, argv and flag
spellings, exported Go symbols, default values, and which code path decides
what — was read off the files listed in the freshness source above.
