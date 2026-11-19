# Embedding over a proxied-server workspace

Last reviewed: 2026-11-19

Read at: `e1e1158dd` (`v1.3.1-rc.1-2-ge1e1158dd`), except §8 and the
corrections marked 1.4.0, read at `release/1.4.0` (`ef08cbaf65`, the code
v1.4.0-rc.4 ships). Because this
document describes observed behaviour rather than a contract, a stale marker
makes it wrong, not merely old. Re-read it against the sources below before
trusting it against a later build.

§8 is the exception: it was rewritten when `OpenBestAvailable` gained its
proxied-server arm (GH#8323) and describes that later behaviour, not 1.3.1's.
Unlike the rest of this document, it is backed by tests that run a live proxied
workspace: `TestProxiedServerSDKOpenBestAvailable` (`cmd/bd`, the
doltserver-proxied lane) and the `TestOpenBestAvailableProxied*` refusal tests
at the module root.

Freshness source: `internal/storage/dbproxy/pidfile/pidfile.go`,
`internal/storage/dbproxy/proxy/` (`endpoint.go`, `server.go`, `shutdown.go`,
`read_status.go`, `backend.go`), `internal/storage/dbproxy/server/doltserver.go`,
`internal/configfile/proxied_server_client_info.go`,
`internal/configfile/configfile.go` (the storage-mode predicates),
`internal/doltserver/physical_root.go`,
`internal/storage/uow/doltserver_provider.go`,
`internal/storage/uow/dolt_sql_provider.go`,
`internal/storage/schema/remote_migrate_gate.go`,
`internal/storage/schema/smart_remote_migrate_gate.go`,
`internal/storage/schema/lock.go`, `internal/storage/storage.go`,
`internal/storage/dolt/store.go` (the server-mode shared-gate call site),
`internal/storage/embeddeddolt/` (`store.go`, `cache.go`, `detection.go`),
`cmd/bd/db_proxy_child.go`, `cmd/bd/dolt.go`,
`cmd/bd/dolt_proxied_lifecycle.go`, `cmd/bd/migrate.go`, `cmd/bd/main.go`,
`cmd/bd/proxied_server.go`, `cmd/bd/proxy_capability.go`,
`cmd/bd/remote_migrate_gate.go`, `cmd/bd/store_factory.go`,
`cmd/bd/uow_factory.go`, `internal/proxiedopen/`,
`internal/storage/uowstore/`, `beads.go`, `beads_cgo.go`, `beads_nocgo.go`.

## What this document is, and is not

An embedder — an external tool that links bd's Go packages, or one that runs
beside `bd` against the same workspace — ends up leaning on surfaces bd never
published: a PID file's field names, a child process's argv, the shape of a
`--json` payload, whether a Go symbol is exported. This document writes those
down as **observed behaviour at the commits the header names**, so that an
embedder can build against what the code actually does instead of guessing.

It is not a compatibility contract. Nothing here is a published interface with a
deprecation policy behind it. Every surface below is an implementation detail
that bd's own code reaches through, and each one may change in any release,
including a patch release. Five of the eleven have changed or were planned to:
§7 and §10 changed in 1.3.1, §11's exit handling in 1.3.1 and §5 in
1.4.0-rc.1, each described in its shipped shape; §6's planned change and the
rest of §11's (#6651) have not shipped as of 1.4.0. Read this as a snapshot
with the commits it was taken at, and re-read it against the source before an
upgrade.

Two things follow for anyone building on this:

- **Prefer the front door.** Where `bd` offers a command or a `--json` payload
  that answers your question, go through it. The control files under the proxied
  root are bd's private lifecycle state; reading them means tracking bd's
  lifecycle work release by release.
- **Fail loudly on a shape you do not recognise.** A missing JSON key, an
  unexpected `schema` number, a new argv flag — treat these as "this bd is not
  the one I was written against", not as a default to paper over.

Everything below was checked by reading the source at the commit the header
names for it. Where a claim
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
| `proxy.secret` | the proxy | 64 hex chars authenticating the control port and, since 1.4.0-rc.1, every data-port connection's `BDPROXY1` exchange; rotated on every proxy start |
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
field of the reply against the file and the workspace. Since 1.4.0-rc.1 it does
not probe the data port: the authenticated identity reply is the liveness proof
(§11). A record left behind by a killed proxy passes a naive parse and points at
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
--config <path>  --logpath <path> [--rotate-logpath]  --dolt-bin <path>  --database <name>
```

`--rotate-logpath` (since 1.4.0-rc.1, #8037) follows `--logpath` when bd owns
the log; bd 1.3.x rejects it (§8).

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

## 3. Plain MySQL clients on the data port

Two separate facts combine here, and they live in different files.

**The proxy does not parse what it carries.** `proxyServer.handleConn` in
`internal/storage/dbproxy/proxy/server.go` dials the backend once per
authenticated client connection (per accepted one at 1.3.x) and then runs two
`io.Copy` goroutines, client→backend and backend→client, in an errgroup. There
is no MySQL protocol awareness, no connection pooling and no multiplexing:
after the `BDPROXY1` exchange that opens every data-port connection since
1.4.0-rc.1 (see below), the proxy is a byte pump, and *N* client
connections become *N* backend connections. When either direction finishes, or
the proxy's context is cancelled, both sides are closed. One exception since
1.4.0-rc.1: when an external upstream is not serving (nothing accepts the dial,
or the upstream closes before its greeting), the proxy writes a MySQL ERR
packet to the client itself (`writeUpstreamOutage` in `server.go`,
`UpstreamOutagePacket` in `upstream_error.go`; external backends only).

**The managed-local backend authenticates as `root` with no password.** In
`newManagedProxiedServerUOWProvider` (`internal/proxiedopen/open.go` since
1.4.0-rc.4; `cmd/bd/uow_factory.go` before) bd passes the
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

**What an embedder can observe.** Up to bd 1.3.x, a plain MySQL client
connecting to `127.0.0.1:<port from proxy.pid>` as `root` with no password
reached the same database bd does, because the proxy passed the handshake
through untouched. Since 1.4.0-rc.1 the data port accepts only bd: every
connection opens with a `BDPROXY1` exchange keyed by the owner-only
`proxy.secret` (#7884), so a plain client gets no session (the Go MySQL driver
reports "invalid connection"). An embedder reaches the workspace's live
database through `OpenBestAvailable` (§8); `bd sql` runs ad-hoc SQL.
*Not verified by running at 1.3.x:* that claim was the composition of three
separately-read facts — the credential pair, the generated config's loopback
bind with no users section, and the proxy's lack of protocol handling — not an
observed connection.

At 1.3.x three qualifications applied:

- It followed from the *generated config*, not from a decision to offer an
  authentication posture. A workspace that supplied its own server config, or
  the external topology (where credentials come from `ExternalDoltConfig` and
  the password from that config's environment variable), did not behave this
  way.
- The loopback bind was the whole access control. Anything that could open a
  TCP connection to that port on the host was `root` on the database.
- The **control** port in the same record was not open in this sense: it
  required the `proxy.secret` value.

The proxy dialed the backend per accepted connection and closed the client on
any dial or handshake failure, so a successful connect was the thing to check
first, not a given.

## 4. Open connections, idle exit, and `bd dolt stop`

**Any authenticated connection defers idle exit.** `proxyServer.idleWatcher`
(`internal/storage/dbproxy/proxy/server.go`) returns immediately into a wait
when `idleTimeout <= 0` — that is the "never" case. Otherwise it ticks every
`idleTimeout/4`, with a 1s floor. A tick that sees `activeConns > 0` clears the
armed state; the first tick that sees zero *arms* a timestamp; a later tick then
exits once `timeout` has elapsed since arming. Since 1.4.0-rc.1 `handleConn`
completes the `BDPROXY1` exchange before it increments `activeConns` or dials
the backend, so only an authenticated connection holds off idle exit; at 1.3.x
an accepted TCP connection counted even if it never sent a byte.

Two consequences an embedder should plan for:

- Holding one idle connection open keeps the proxy and its backend alive
  indefinitely. That falls out of how the watcher counts, so it is as much an
  easy way to pin the tree up by accident as a deliberate one — and it is a
  property of the watcher, not an offer.
- Exit is not punctual. Arming costs a tick, so the observed lifetime after the
  last connection closes is longer than the configured timeout — up to roughly
  two ticks longer.

**After idle exit, a provider rediscovers the proxy — releases after 1.3.1.**
At 1.3.1 a unit-of-work provider's pool pins the proxy address it was
constructed with, so once its connections drain and the proxy exits, every
later new connection dials the retired port (or whatever now listens there).
Since 1.4.0-rc.1, `internal/storage/uow/endpoint_rediscovery.go` runs
`proxy.GetCreateDatabaseProxyServerEndpoint` again, with the constructor's
`OpenOpts`, before each new physical connection of both
`NewDoltServerUOWProvider` and `NewExternalDoltServerUOWProvider`. That adopts
a live, authenticated proxy without taking `proxy.lock`, or spawns a
replacement, whose port may differ. Only the address changes: database,
credentials, TLS and timeouts stay as constructed. Bootstrap and reused pooled
connections do not rediscover; overlapping new connections share one lookup,
and nothing is cached between them. A failed lookup fails that connection
with `uow: rediscover proxy endpoint: <cause>` and never falls back to the old
address.

Cancellation is the tradeoff to plan for. A caller whose context ends stops
waiting at once, but the lookup it started keeps running under the proxy's own
startup rules and deadlines (it may still spawn a proxy), because other
connections may be waiting on it. The provider's `Close` waits for a lookup in
flight before returning, so it is not bounded by the caller's context.

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

## 5. The sidecar's `idle_timeout`

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
`idle_timeout`, `external`. Every key but `idle_timeout` carries `omitempty`. A
default `bd init --proxied-server` writes no sidecar at all; with
`--proxied-server-idle-timeout 45s` the sidecar is just:

```json
{"idle_timeout": 45000000000}
```

`port: 0` (the OS-assigned case), a nil `external`, and unset paths are all
absent rather than written as `0`, `null` or `""`. Paths, when present, may be
relative to the `.beads` directory; the `Resolved*Path` helpers on the type are
what bd uses to join them.

`idle_timeout` is a Go `time.Duration` with no custom marshaller, so it is **an
integer number of nanoseconds**, not a duration string: 45s is `45000000000`,
30s is `30000000000`, never is `-1`. bd does not write `0`: since 1.4.0-rc.1
it saves the effective value, so the default is written as `30000000000`. bd
1.3.x left the key out for the default, and bd 1.4 reads a sidecar without it
as 30s.

**(c) The effective value.** `NewDoltServerUOWProvider`
(`internal/storage/uow/doltserver_provider.go`) applies
`if idleTimeout == 0 { idleTimeout = defaultProxyIdleTimeout }`, and
`defaultProxyIdleTimeout` is **30s** (`internal/storage/uow/dolt_sql_provider.go`).
The external provider does the same. So, at the sidecar layer:

| sidecar `idle_timeout` | effective |
|---|---|
| key absent | 30s |
| `0` (bd does not write it) | 30s |
| negative, e.g. `-1` | never |
| positive | as written |

**Changed in 1.4.0-rc.1.** bd writes the effective `idle_timeout` instead of
letting `omitempty` drop it, and reads a sidecar without the key as 30s;
`bd dolt status --json` shows `"30s"` for such a sidecar. Status leaves the key out
when the timeout is never, and when the workspace has no sidecar, which is the
default managed-local case and also runs at 30s (§10). An embedder should read
an absent status key as "ask this bd, do not assume", not as "no timeout".

## 6. `bd migrate schema` on a proxied workspace

**Observed at `e1e1158dd`, and still so in 1.4.0: the verb is permitted, and
on a served database with no remote it migrates. With a remote configured it is
still permitted — it reaches the store open — but the §7 gate refuses the
migration there, because the verb's own consent is not read on that arm.**

Two validators run in the root pre-run before the store open
(`cmd/bd/main.go`). `validateProxyCapabilitiesBeforeProvider`
(`cmd/bd/proxy_capability.go`) refuses flag combinations.
`validateProxyRegistryBeforeProvider` (`cmd/bd/capability_registry.go`)
refuses a command path whose registry row is refused for the workspace's
topology; a path with no row passes. The registry refuses `migrate`,
`migrate sync`, `migrate hooks`, `migrate issues`, `migrate-issues` and
`migrate-personal` with `proxy.migrate.unsupported`, and lists
`migrate schema` as permitted, because "the provider open runs it under the
migration gate". So the verb reaches the store open.

The mechanics matter, because they are not "a command that applies a migration":

- `cmd/bd/main.go` sets `schema.SetSharedMigrateConsent(...)` in the root
  pre-run for this one verb (`isSchemaMigrateVerb` matches `bd migrate schema`
  and deliberately nothing else in the `migrate` tree). That consent satisfies
  the shared-store gate in §7 on **one arm only**. Its single non-test read site
  is `sharedNoRemoteGate`, reached from that gate's `if !hasRemote` branch, and
  the comment there scopes it deliberately: the verb "unlocks ONLY here: with a
  remote configured, #4259's cross-clone fork risk still demands the stronger
  designated-migrator confirmation". When the served database *does* have a
  remote — on the proxied path that is read from its own `dolt_remotes` alone,
  because `dolt_sql_provider.go` calls the gate as
  `CheckSharedStoreMigrateGate(ctx, c, "", nil, dataBehindAdopter(p.readOnly))`
  and so supplies no on-disk
  fallback probe — the gate takes the other arm, which consults only `--force`
  (`bd migrate schema --force` trips `isForcedMigrate`; `bd migrate --force`
  trips it too, but only off a proxied workspace, since the bare verb is
  refused before the store open per the refusal above) and the
  `BD_ALLOW_REMOTE_MIGRATE` environment override.
  Without one of those, a main-lane pending migration is refused at the store
  open, carrying the shared-store reason wherever the smart gate would otherwise
  have auto-migrated. The public twin of this knob,
  `beads.AllowSharedSchemaMigration`, documents the same scope.
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
the proxy relays each authenticated connection (§3) and does not track who
holds one. An embedder
whose process outlives a `bd migrate schema` should treat the schema as changed
beneath it and re-establish its connections; in practice, restart.

**Not changed in 1.4.0.** A refusal by default, with `--force` emitting a
generic warning to close co-resident library clients first, was approved for
1.3.1 and has not shipped. In 1.4.0, running `bd migrate schema` in the
workspace is how an embedder clears the schema gates its open reports (§8).
On a database with a Dolt remote the bare verb refuses, as the mechanics above
explain, and `bd migrate schema --force`, run on one clone of that remote only,
is the form that migrates. With no remote, the bd that migrates warns only that
co-resident bd clients on an older binary will refuse the database. With a
remote, it warns only that one clone should migrate and then run
`bd dolt push`, which proxied-server mode refuses. Neither warning mentions
library clients. An embedder whose process outlives the migration should still
restart.

## 7. The shared-store migration gate — main lane only, new stop in 1.3.1

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

**What changed in 1.3.1: a third refusal shape arrives from underneath.** #6575
(landed at this commit) added `fallbackReasonDataBehind` to
`checkRemoteMigrateGate`. It is not a fourth item in the "two refusals" list
above — that count is the shared form's delta over the remote-backed flow, and it
is unchanged. This stop lives in the flow *both* forms delegate to, so the
shared gate inherits it rather than adding it.

**Whether it can fire is a property of the call site, not of the gate.** The
stop's only input is an ancestry fact the *caller* injects: `localDataBehind`
(`internal/storage/schema/smart_remote_migrate_gate.go`) reads
`FastForwardAdopter.AheadBehind`, and reports "not behind" whenever the adopter
is nil or wires neither callback. Its own comment names "the shared unit-of-work
provider's injection site" as exactly that case, where "routing stays exactly as
it was before this check existed". At this commit there are two production
callers of `CheckSharedStoreMigrateGate`:

- `internal/storage/dolt`'s store open — the **server-mode** open — wires
  `AheadBehind` (added expressly for #6575), so the stop is live there.
- `internal/storage/uow/dolt_sql_provider.go`, the funnel for the proxied CLI
  path and `bd serve`'s own provider, passed `nil` at `e1e1158dd`, so a
  **proxied** workspace reached the shared gate with no ancestry fact at all.
  Since 1.3.1 it passes `dataBehindAdopter(p.readOnly)`, which wires
  `AheadBehind` (the call §6 quotes), and marks the error proxied, so the stop
  is live there too.

At `e1e1158dd` a proxied, remote-backed clone that was data-behind at schema
parity therefore read as level, the smart gate routed to `smartAutoMigrate`, and
the shared arm returned the ordinary `fallbackReasonSharedStore` refusal — the
one whose framing ("the gate would have resolved and was overruled") the
data-behind comment says is *not* true of this state. Since 1.3.1 that clone
gets the data-behind refusal, with proxied-specific remedy text (below).
Embedded mode reaches the stop by a different route: it calls
`CheckRemoteMigrateGateWithAdopt` with a fully-wired adopter
(`internal/storage/embeddeddolt/store.go`), which is why the `OpenForRemoteSync`
carve-out below has anything to carve.

Where the stop *is* live, it fires when schema parity with the cached remote ref
holds but this clone's local HEAD is **missing commits the cached ref has**
(`behind >= 1`, whatever `ahead` is — a strict ancestor only when this clone has
no commits of its own). The clone is behind in data commits it has not pulled,
so it is not the first-mover the smart gate may auto-resolve. The wide predicate
is deliberate: because bd auto-commits every write, `localDataBehind` reasons
that "has local commits AND is behind" is the ordinary multi-machine state, so
stopping only at `ahead == 0` "would leave the larger half of the affected
cohort unprotected". Two details make it matter more than its size suggests to
an embedder classifying gate errors:

- **It is reported on shared stores too.** The `smartDataBehind` /
  `smartDataDiverged` arms set this reason ahead of `fallbackReasonSharedStore`,
  which would ordinarily outrank it. The comment gives the reason: the
  shared-store reason says the gate *would* have resolved and was overruled,
  which is not true here — the gate refuses this state on its own merits.
- **Its options collapse.** `Options()` returns a single `pull-first` option
  whose command is `DataBehindRemedyCommand` (`bd dolt pull`), instead of the
  usual migrate-or-adopt pair; on a shared store a second
  `migrate-shared-after-pulling` option follows it. The `DataDiverged` variant
  is the same reason and the same command; it is the `ahead >= 1` shape — the
  clone also has commits of its own — which is *why* its pull **merges** (and
  can report conflicts) rather than fast-forwarding.

**What an embedder can observe.** This is the one gate refusal with a
one-command operator remedy, and it is the one the gate exports API for:
`(*schema.RemoteMigrateGateError).IsDataBehind()` and the
`schema.DataBehindRemedyCommand` constant. Code that classifies gate errors
should test `IsDataBehind()` before treating a `RemoteMigrateGateError` as
fatal — the doc's own "fail loudly on a shape you do not recognise" rule cuts
the other way here, because this shape *is* recoverable and the exported
predicate exists precisely to say so.

The remedy is itself a store-opening command, so bd carves a path for it:
`isRemoteSyncCommand` in `cmd/bd/main.go` matches `bd dolt pull` alone and sets
`RemoteSyncOpen`, which routes the embedded open to
`embeddeddolt.OpenForRemoteSync` (`cmd/bd/store_factory.go`) — a lenient open
for this gate reason and no other. That carve-out is CLI-only, and an embedder
cannot replicate it in-process: both `embeddeddolt.OpenForRemoteSync` and the
`dolt.Config.RemoteSyncOpen` field live under `internal/`, unimportable from
outside `github.com/steveyegge/beads`, and none of the public open paths
(`OpenBestAvailable`, `Open`, `OpenFromConfig`, `OpenGated`) takes an option
that could request the lenient open. The embedder's remedy is therefore
out-of-process: run `bd dolt pull` (and, per the first observation below,
outside proxied mode), then re-open. A normal in-process open will hit the same
refusal that prescribed the command.

Two observations worth carrying, neither of them this document's to fix:

- On a **proxied** workspace the pull is still the precondition, and the
  workspace still refuses the verb: the capability registry declares
  `refusedPath("dolt pull", "proxy.dolt_pull.unsupported", ...)`
  (`cmd/bd/capability_registry.go`), so the one command a data-behind clone
  needs is declined by that same workspace. Since 1.3.1 the gate says so: on a
  proxied workspace its first option is `pull-first-on-server-host`, which
  says to run the pull on the machine that hosts the database, from a
  workspace with direct access to it, and then retry here. An embedder that
  surfaces gate remedies to users should pass that location on, not just the
  command.
- The shared store's second option, `migrate-shared-after-pulling`, names
  `SharedConsentCommandForced` (`bd migrate schema --force`), because this stop
  is always remote-backed and the bare verb's consent is read only with no
  remote. Unlike the pull, it is runnable from a proxied workspace, so run it
  only after the pull has completed and every co-resident bd client is
  upgraded.

## 8. `OpenBestAvailable` on a proxied-server workspace

`OpenBestAvailable` is the public open path in package `beads` at the module
root. It exists in two build-tagged copies: `beads_cgo.go` (`//go:build cgo`),
which handles embedded Dolt, proxied-server mode, dolt-server mode and
registered extension backends; and `beads_nocgo.go` (`//go:build !cgo`), which
handles all but embedded Dolt and returns an error for that.

**Routing.** Both copies have a proxied-server arm (GH#8323). It is checked
after a registered extension backend and the per-open option refusals, and
before the server and embedded arms. It reads `cfg.IsDoltProxiedServerMode()`,
which looks only at `dolt_mode` in `metadata.json`. Both builds share the arm
(`openProxiedServer` in `beads.go`), so a `CGO_ENABLED=0` embedder opens a
proxied workspace as well as a cgo one.

One open keeps the server arm: an environment that forces server mode
(`BEADS_DOLT_SERVER_MODE=1` or `BEADS_DOLT_SHARED_SERVER`) and names the port
to dial in `BEADS_DOLT_SERVER_PORT` (a positive port, parsed as the server
store parses it). That open dials the named port as a plain MySQL client, as
it did before this arm existed (`routesThroughProxy` in `beads.go`; the
named-port exemption proposed in #8476). The named port must be a plain Dolt
server's: the data port of a proxy started by bd 1.4 accepts only bd, so
naming it fails ("invalid connection"); a pre-1.4 proxy's data port still
answers a plain client until a 1.4 bd replaces that proxy. An embedder that
opens this way usually also pins in its environment what the proxied arm
would ignore: no auto-start, the commit author and the pool bound. Without a
named port, forced server mode would dial a port bd resolves itself, never the
proxy's data port, so that open takes the proxied arm. The legacy
`BEADS_DOLT_PORT` does not count as a named port.

Before this arm existed, a proxied workspace fell through to the embedded arm
(cgo), which created and migrated a second, unrelated database at
`.beads/embeddeddolt` beside the one the proxy serves, or to the
embedded-requires-CGO error (pure). Neither happens now: the open creates no
`.beads/embeddeddolt` and no `beads.db`, whether it succeeds or fails.

**What the open does.** It goes through `internal/proxiedopen`, the same
construction the bd CLI uses, with a root bd command's posture:

- On a team-server workspace (`dolt_team_server` in `metadata.json`) the
  workspace's project identity is asserted against the shared database.
  Other proxied-server workspaces are not checked, by the library or by bd. A
  managed-local root that holds no Dolt database is refused rather than
  initialized.
- A bd proxy already running for the server root is adopted. With none, one is
  started from the `bd` found on `PATH`, so an embedder's open may spawn
  `bd db-proxy-child` from its own `PATH`. With no `bd` on `PATH` the open
  fails with `beads.ErrProxyUnavailable` (match with `errors.Is`); it creates
  no embedded store, starts no proxy, and leaves any stale proxy record and
  orphaned backend alone. It may still create the server root directory and
  write the proxied sql-server config. An open refused later, for example by
  a schema gate, can leave a proxy it started running until the proxy's idle
  exit. The library never starts the proxy from the host executable, because
  the host is not bd.
- A proxy started by a bd older than 1.4 (before the authenticated control
  handshake) cannot answer the library, and the library does not stop it:
  every older bd on the workspace shares that proxy, and the `bd` on `PATH`
  may be too old to replace it. The open fails with
  `beads.ErrProxyUnavailable` and leaves the proxy, its record and its
  backend alone. Upgrading the `bd` on `PATH` and running a bd command in the
  workspace replaces the old proxy, as does waiting for its idle exit. A
  workspace an older bd still uses is usually still at its 1.3.x schema; bd
  then refuses it with a schema version mismatch and the library with
  `beads.RemoteMigrateGateError` until `bd migrate schema` (one-way) migrates
  it. If the database has a Dolt remote, which proxied init adds from the git
  `origin` by default, the bare verb refuses too (§6), and
  `bd migrate schema --force`, run on one clone of that remote only, migrates
  it. The bd CLI itself stops such a proxy and starts its own.
- A managed-local workspace (no external upstream in the sidecar) probes the
  `dolt` binary before it looks for a proxy, so it needs `dolt` on `PATH`
  (or named by `BEADS_DOLT_BIN`) even to adopt a running proxy. Without it
  the open fails with the dolt resolution error, not `ErrProxyUnavailable`.
- A `bd` on `PATH` that is a different build from the one that owns the
  workspace can reject a newer child flag (bd 1.3.x rejects
  `--rotate-logpath`). The open then retries until its deadline, about 15 s,
  and fails with an untyped "timeout waiting for proxy to become ready on its
  OS-assigned port" error; the child's "unknown flag" error is in
  `.beads/dolt/server.log`.
- The events journal is activated under the same workspace switches as the
  CLI's. Versioned history follows only the store's own
  `versioned-history.enabled` row: `BD_VERSIONED_HISTORY_ENABLED` and
  `config.yaml`, which turn it on for bd, do not reach the library. A later
  `SetEventsJournalEnabled` call wins.
- Schema refusals surface as the exported `beads.SchemaSkewError`,
  `beads.RemoteMigrateGateError` and `beads.MigrateConsentError` (match with
  `errors.As`). `AllowSharedSchemaMigration` clears only the shared-database
  gate (`RemoteMigrateGateError`), and only on a database with no Dolt remote;
  the consent gate (`MigrateConsentError`) also needs `BD_ALLOW_MIGRATE=1`.
  `BD_ALLOW_REMOTE_MIGRATE=1` alone clears both, with or without a remote.
  `bd migrate schema` run in the workspace clears both on a database with no
  Dolt remote; with one it refuses (§6), and `bd migrate schema --force`, run
  on one clone of that remote only, clears both.

The returned `Storage` (`internal/storage/uowstore`) runs each call as one
unit of work over the proxy. Its `Close` releases this handle's connections and
leaves the shared proxy running, as a bd command's exit does.

**Reads.** The flat `GetIssue` resolves the issue on either plane and fills
its `Labels`, and the flat `GetIssuesByIDs` reads both planes with their
labels through the same batch read `DoltStore` uses, leaving out an id that
names no row. As on `DoltStore`, the transaction's `GetIssue` fills no labels.

**Creates.** `CreateIssue`, `CreateIssues` and the transaction creates go
through the same domain create use case as the proxied `bd create`, and add
what `DoltStore`'s create adds around it: `PrepareIssueForInsert`, plane
routing by `IsWisp` (so `NoHistory` lands on the wisp plane; the flat single
`CreateIssue` also routes wisp-typed and infrastructure-typed issues), a
refusal for a caller-named ID the other plane holds, `Labels` and `Comments`
persisted with the issue, and for `CreateIssues` the mixed-bucket dependency
check and `Dependencies` persisted with no `dependency_added` event. A flat
create that fails definitely clears the IDs it minted (`DoltStore` in 1.4.0
keeps them); a
commit whose outcome is unknown returns `ErrCommitIndeterminate` (§9), keeps
the IDs, and is never retried.

**Method coverage.** The store serves the issue, label, dependency, comment,
config, local-metadata, ready-work, blocked, statistics and claim methods, the
role accessors, and `RunInTransaction`. A `RunInTransaction` callback runs
at most once and is never replayed. A setup failure before the callback can
be retried. The other `Storage` methods return a typed
`beads.ErrUnsupported` with backend `"proxied-server"` rather than a silent
default. They include the event and provenance reads, the `Iter*` streams,
merge slots and slot state, `GetDependencies`/`GetDependents` and
`GetDependencyTree`, `GetIssuesByLabel`, `GetIssueByExternalRef`,
`SearchIssueSummaries`, `ListWisps`, `RenameLabel`, `MergeMetadata`, and the
`*Checked` update and close variants. The generated list is
`internal/storage/uowstore/unsupported_gen.go`.

Divergences an embedder may notice:

- `EventQuerier` and `DependentQuerier` are not implemented, so `AsEventQuerier`
  and `AsDependentQuerier` return `false`. `BlockedQuerier` and `IssueClaimer`
  are implemented.
- Dependency writes, flat and in a transaction, record no `dependency_added`
  or `dependency_removed` event unless `EmitEvent` is set on the
  `WithOptions` variant, matching `DoltStore`.
- A batch-create dependency whose target does not exist is refused, where
  `DoltStore` skips it. Its `created_by` is the acting user and its
  `created_at` is the write time; `dep.CreatedAt` is not honored.
- Import-shaped creates (an explicit ID over an existing row, preset comments)
  differ from `DoltStore` in ways interactive callers do not reach. The full
  list is the KNOWN DIVERGENCES comment in `internal/storage/uowstore/create.go`:
  - Re-creating an explicit ID over a live row upserts it. That writes another
    `created` events-table row and does not restore an imported lease or drop
    an orphaned one.
  - A create-time comment with no `CreatedAt` is stamped with the current
    time, so several comments in one create can share a second and read back
    out of list order.
  - Two identical create-time comments collapse onto one row but are each
    journaled. An explicit comment ID that is already stored fails with a
    duplicate-key error. `DoltStore` in 1.4.0 fails the same way unless the
    stored comment is identical, which it skips.
  - On such a re-create, an existing dependency edge of a different type fails
    with a type-conflict error. One of the same type with changed metadata or
    thread is rewritten, where `DoltStore` leaves it untouched.
- Dependencies on an external reference are not resolved through routes.
- Dolt commits carry no `--author`: they are attributed to the proxy's SQL
  user, where `DoltStore` takes the author from `GIT_AUTHOR_NAME` and
  `GIT_AUTHOR_EMAIL`, defaulting to `beads <beads@local>`. An embedder that
  needs its own author on each commit keeps using the server arm until the
  proxied arm carries one.
- `ErrCircuitOpen` (§9) is never produced on this arm. The circuit breaker
  belongs to the direct server store.
- `UnderlyingDB()` and `DB()` return the proxy's connection pool as raw-SQL
  escape hatches. Statements on that pool bypass hooks and the events journal.
- Role handles obtained before `Close`, `UnderlyingDB()` and `DB()` refuse
  after `Close`: role calls return an error and the accessors return `nil`.
  None of them panic, and `Close` is safe to race with them.

**Embedders that projected a server endpoint.** An embedder that steers a
proxied workspace onto the server arm by forcing server mode and naming the
port (`BEADS_DOLT_SERVER_MODE=1` with `BEADS_DOLT_SERVER_PORT`) keeps that
route. Forcing server mode without naming a port now takes the proxied arm.
In 1.4.0 the proxied arm already carries the create contract, the
failed-create ID reset and the quiet dependency writes described above; an
embedder that moves to it should check the commit-author divergence first.

**The other public opens are unchanged, and do not go through the proxy.**
`Open`, `OpenFromConfig` and `OpenGated` still construct the direct server
store. On a proxied workspace `Open` does not read the database name from
`metadata.json` and, while the proxy runs, creates an empty `beads` database
on the proxy's backend. While a proxy runs, `OpenFromConfig` and `OpenGated`
dial its backend directly; the proxy does not count those connections and can
idle out under them. With no proxy running, they start a standalone
`dolt sql-server` on the workspace's data directory. It outlives the program
and blocks every bd command until it is killed (`bd dolt stop` does not stop
it). `BEADS_DOLT_AUTO_START=0` makes them fail instead. Embedders should use
`OpenBestAvailable` there.

**`SetEventsJournalEnabled`.** The method is not on the `Storage` interface.
`beads.Storage` aliases `internal/beads.Storage`, which aliases
`internal/storage.Storage`, and the method is declared on a separate optional
interface, `storage.EventsJournalConfigurer` (`internal/storage/storage.go`).
Its contract is per-instance: implementations must not use process-global
state, because one process can hold several stores at once.

Every Dolt arm's store implements it: `dolt.DoltStore`,
`embeddeddolt.EmbeddedDoltStore`, and the proxied arm's `uowstore.Store`, which
forwards to the unit-of-work provider. An embedder reaches it by type
assertion:

```go
store, err := beads.OpenBestAvailable(ctx, beadsDir)
// …
if c, ok := store.(interface{ SetEventsJournalEnabled(bool) }); ok {
    c.SetEventsJournalEnabled(true)
}
```

`storage.EventsJournalConfigurer` is internal and is not re-exported from the
public `beads` package, so the assertion has to be written against a locally
declared single-method interface as above. A store reached through a
registered extension backend (`backends.Lookup`, dispatched ahead of every Dolt
path in both copies of `OpenBestAvailable`) need not implement the method at
all, which is what the `ok` is for.

This is among the more likely surfaces here to move, because it is reached by
assertion rather than by a declared method: no compile error marks its
disappearance.

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
`backend_endpoint`, `idle_timeout`, plus the `schema_version` that bd adds to
its `--json` output. Six are always present: `schema_version`, `mode`, `root`,
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
  "schema_version": 1,
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
  "schema_version": 1,
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
  nanosecond integer in §5 — and it is emitted only when the timeout is `> 0`.
  It is absent when the timeout is never, and on a workspace with no sidecar,
  which a default `bd init --proxied-server` leaves (its proxy runs at the 30s
  default). A sidecar without the key, as bd 1.3.x wrote for the default,
  reports `"30s"` (§5).

The command has no lifecycle side effects, which is why polling it beats parsing
`proxy.pid`: `proxy.ReadStatus` reads the two records and verifies the recorded
processes without starting, stopping or adopting anything. A record that fails
any check reports nothing rather than a PID a caller might act on.

## 11. A clean backend exit

**Up to 1.3.1-rc.1 the proxy did not notice** a `dolt sql-server` child that
stopped cleanly (dolt exits 0 on SIGTERM). `DoltServer.Start` fed `cmd.Wait()`
into an errgroup, which cancels only on a non-nil return. So the proxy kept its
records and listener, and it closed each client after the backend dial failed,
until `bd dolt stop`. This is #6651, reproduced 3/3 against v1.3.0. The trigger
is a signal to the dolt child alone, such as a per-PID supervisor stop or
`kill <dolt pid>`.

**Since 1.3.1 the proxy exits** (#6937). The supervising goroutine reports any
`cmd.Wait()` return as a backend exit (`errBackendExited` in
`internal/storage/dbproxy/server/doltserver.go`), so `DoltServer.Running` turns
false and the proxy's 100ms backend health tick (`proxy/server.go`) stops the
proxy, which removes `proxy.pid`. Observed on 1.4.0-rc.4: after a SIGTERM to
the dolt child alone the proxy was gone within 0.3 s, and
`bd dolt status --json` reported `running: false` and `backend_running: false`.
The next bd command started a new proxy and backend.

Adoption does not probe the backend: `readAndDial` takes the proxy's
authenticated identity reply on its control port as the liveness proof
(`proxy/endpoint.go`). So until the health tick fires, an adopting client can
succeed and then fail its first query. Backend-probing adoption was approved
for 1.3.1 under #6651 and has not shipped. (This window is read from the code,
not run.)

---

## Which of these are most likely to move

Not all eleven carry the same risk, and it is worth saying which is which.

**Already moved:** §5 (since 1.4.0-rc.1 the sidecar writes its effective
`idle_timeout`) and §11's exit handling (since 1.3.1 a clean backend exit stops
the proxy). **Approved for 1.3.1 but not shipped as of 1.4.0:** §6's refusal of
`bd migrate schema`, and §11's backend-probing adoption.

**Likely to move:** §2, the `db-proxy-child` argv. It is a hidden internal
command whose flags exist to serve the parent that spawns it, and flags have
been added to it as topologies were added. §1's `proxy.pid` and the control-file
set are next: they are bd's private lifecycle state, and the lifecycle work that
added `root_id`, `control_port`, the spawn marker and the stop epoch is not
finished. §8 is likely for two reasons: the proxied arm's method coverage is
new and will grow, so a method that returns `ErrUnsupported` today may be served
later, and `SetEventsJournalEnabled` is an assertion-reached optional interface
that can disappear without a compile error anywhere.

**Less likely to move, for a reason:** §9's two error sentinels, because
`errors.Is` on them is the only way a caller can tell those two conditions
apart. §10's payload, because it was *just* reshaped deliberately. §3 already
moved: since 1.4.0-rc.1 the data port requires the BDPROXY1 exchange.

**A caveat on that ranking.** It is a reading of the direction of current work,
not a schedule and not a promise. A surface in the third group can still change
in a patch release; it is where to spend your safety margin, not where to stop
checking.

## Not verified by running

Sections other than §8 were first written by reading the source at
`e1e1158dd`, with no proxy started and no test suite run; the corrections
marked 1.4.0 were read at the release branch, and the few marked observed were
run against a 1.4.0-rc.4 proxy. §8 is backed by
tests (see the top of this document). The claims that would need a
live proxy to confirm end to end, and are therefore inferred from the code:

- The observed idle-exit latency in §4 being longer than the configured timeout
  by up to roughly two watcher ticks.
- That, at 1.3.x, a third-party MySQL client authenticating as `root` with no
  password succeeded against the data port on a managed-local workspace (§3).
  The credentials, the loopback bind and the absence of protocol handling in
  the proxy were all read directly from the source; the end-to-end connection
  was not. Since 1.4.0-rc.1 it does not: observed against a 1.4.0-rc.4 proxy,
  it gets "invalid connection".

Everything else — field names, JSON keys and their types, argv and flag
spellings, exported Go symbols, default values, and which code path decides
what — was read off the files listed in the freshness source above.
