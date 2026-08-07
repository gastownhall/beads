# `bd serve` operator runbook

Last reviewed: 2026-08-01 (freshness sources: the operating-envelope constants
in `internal/httpapi/server.go`, and the fields its `event` emitters write)

Running the v0 HTTP surface. For the contract it serves — the operations, the
error vocabulary, the cursor, the loopback posture — see
[design/bd-serve-v0.md](design/bd-serve-v0.md).

Every number below is a constant in `internal/httpapi/server.go` unless stated
otherwise. They are not flags yet; when they become flags the wire contract does
not change.

## Deployment

### Pass an explicit port

```
bd serve --addr 127.0.0.1:7777
```

The default is `--addr 127.0.0.1:0`, which takes an ephemeral port. That is
right for ad-hoc and test use, where the bound address printed on stdout is read
immediately — and it carries **no mutual exclusion**.

Be precise about this, because the tempting shorthand is wrong. "The TCP bind is
the singleton" holds only for an *explicit* port: two serves against one
workspace on `:0` bind two different ephemeral ports and run side by side, with
no way to enumerate them and nothing that fails. On a fixed port the second
process fails to bind, which is the intended behavior and the only mutual
exclusion this command has.

Concurrent serves are data-safe either way — claims are arbitrated in the SQL
server, not in the HTTP process. What you lose without a fixed port is the
ability to know how many servers you are running, and clients' ability to find
the one you meant.

There is no lock file, pid file or discovery file. `bd serve` is
operator-invoked, and on a fixed port the TCP bind *is* the mutual exclusion: a
second instance fails at bind with the operating system's own address-in-use
error. Clients are configured with the address; they do not discover it.

The host in `--addr` must be a numeric IP literal; a DNS name is refused.

### Streams

stdout gets exactly one line, at bind time, and nothing else:

```
bd serve: listening on http://127.0.0.1:7777
```

That is how a caller who asked for an ephemeral port discovers it — read one
line from stdout and stop. Everything else, including the request log, goes to
stderr, so the two can be redirected separately.

### Run it under a supervisor

`bd serve` runs in the foreground and shuts down gracefully on **SIGHUP** as
well as SIGINT and SIGTERM. SIGHUP is in that set on purpose: closing the
terminal of a foreground `bd serve` stops it, rather than leaving an orphan
holding a port and a pool of database connections. A supervisor that
double-forks and expects the child to survive its controlling terminal must
either detach the process from the terminal itself or keep it in the
foreground under the supervisor.

### Binding beyond loopback

`--allow-non-loopback` prints a warning and changes nothing else. There is no
authentication and no TLS: every peer that can reach the address can read every
issue and claim work as any actor. Put an authenticating reverse proxy in front
of it, or do not use the flag.

One behavioral difference follows the flag: `limit=0` (unlimited) is refused on
both list operations with 400 `invalid_argument`. Clients must page.

## Probes

| Probe | Route | What green means |
|---|---|---|
| Liveness | `GET /healthz` | The process is running. |
| Readiness | `GET /v0/beads/ready?limit=1` | The process is running **and** the database answered. |

`/healthz` answers from the process and never touches the database. It stays
green while the database is unreachable, wedged, or idle-stopped — which is what
makes it a correct liveness probe and a useless readiness probe. Do not wire it
to a load balancer's readiness check.

`GET /v0/beads/ready?limit=1` is a real query with a one-row bound. 200 means
ready; 503 means live but not ready, and its `code` says which kind:
`db_unavailable` for a connectivity failure, `busy` for contention or slot
saturation. Both carry `Retry-After`; a probe should treat either as not-ready
and retry, not as a hard failure.

Suggested probe settings: the readiness probe inherits the server's 60s
whole-request deadline as its worst case, so give it a timeout well under that
(2–5s) and let the probe's own failure threshold do the smoothing.

## Detecting a wedge

The failure mode to plan for is a database that stops answering while the
process stays healthy. `/healthz` cannot see it. Three signals can:

- **`event=semaphore_saturated`** — a request waited a second or more for a
  database slot. This is the wedge-detection signal: with no traffic there are no
  saturation events, so a stream of them distinguishes "wedged" from "quiet".
  `outcome=acquired` got its slot in the end; `outcome=abandoned` means the
  client hung up or the request deadline expired while queued, which is the same
  wedge seen from a request that did not live long enough to be shed.
- **`event=semaphore_timeout`** — a request waited the full 10s and was shed as
  503 `busy`.
- **`event=conn_cap_saturated`** — accepted connections reached the 64
  connection cap. Logged once per crossing.

Alert on the readiness probe, and use these to tell a wedged database from a
slow client.

## Connection budget

Size a shared external `dolt sql-server` before pointing several `bd serve`
processes at it. Every `bd serve` process claims, at steady state:

| Consumer | Connections |
|---|---|
| Handler pool, max open (`maxInflight + 4`) | 20 |
| Root command's `DoltStore`, in server / external-server / shared-server mode | 1–2 idle |
| **Per-process worst case** | **~22** |

The shape behind those numbers:

- `maxInflight = 16` bounds handlers that touch the database. Each unit of work
  pins one SQL connection, so that is also the steady-state connection count
  under load.
- The pool is sized `maxInflight + 4 = 20` open, 16 idle, because the semaphore
  bounds *handlers*, not *connections*. A connection poisoned by a failed
  ROLLBACK is replaced, each retry attempt of a committing transaction opens a
  fresh unit of work on a fresh pinned connection, and any semaphore-exempt
  handler that later touches the database escapes the semaphore entirely. The
  four spare slots absorb that.
- In server, external-server and shared-server modes the root command has
  already opened a `DoltStore` that `bd serve` never uses. It stays open for the
  life of the process and holds an idle connection or two against the very
  server this process is about to pool twenty more on. Opening and closing it is
  the root command's business, not one command's — but it is not free, and it
  belongs in the arithmetic.

So `max_connections` on a shared server must cover roughly `22 × (number of bd
serve processes)` plus every other `bd` process pointed at the same server,
each of which claims its own. Idle connections are reclaimed after 5 minutes and
recycled after an hour, so a bursty deployment settles below the worst case —
but size for the worst case.

Those pool limits are applied only if the unit-of-work provider exposes the
knob. If it does not, `bd serve` says so at startup with
`event=pool_limits_unavailable` and runs with an **unbounded** pool rather than
silently pretending. Check for that line before trusting the arithmetic above.

None of this arithmetic applies to a registered backend, which is served from
the store the root command opened rather than from a unit-of-work provider
(`db=roles` on the startup line, against `db=provider` for every Dolt topology).
The pool belongs to that backend, `bd serve` neither owns it nor can reach it,
and no `pool_limits_unavailable` line is emitted — a missing capability would be
reported for a provider that was never asked for. Size that pool wherever the
backend is configured.

`maxConns = 64` bounds *accepted TCP connections*, which is a client-side limit
and not part of the database budget. It exists because the semaphore does not
bound connections: Go spawns a goroutine per connection, and one parked on a
full semaphore still holds its goroutine, file descriptor and buffers. Excess
connections wait in the kernel accept backlog instead of in Go memory.

## Shutdown, and the ambiguous claim

On SIGINT, SIGTERM or SIGHUP the server stops accepting and drains for up to
**20 seconds**. The drain deliberately does *not* cancel in-flight handler
contexts: the budget covers a claim inside its serialization-retry budget plus
its commit, because killing such a connection early would leave the client
unable to tell whether its write landed.

If the drain budget is exceeded, remaining connections are closed and
`event=shutdown_forced` is logged with the count. That is the ambiguous case,
and it is a client-visible one: a claim killed mid-commit may or may not have
landed, and the client saw a dropped connection rather than a response.

**Recovery is a re-claim, and it is safe by construction.** A claim is a
compare-and-set, and a re-claim by the same actor is idempotent: if the first
attempt landed, the second finds the actor already holds it, returns 200 and
writes no commit. If the first did not land, the second claims normally. If a
different actor won in between, the second gets a typed 409 `already_claimed`
carrying that actor in `assignee` — which is the true answer, not an error to
retry through.

So the recovery for an ambiguous shutdown is: re-issue the claim with the same
actor and read the result. Never infer the outcome from the dropped connection,
and never fall back to reading the issue and guessing.

The same reasoning covers a client that times out, hangs up, or is restarted
mid-claim. A client hanging up is not counted as a server fault:
`event=request` records `code=client_closed` and no `request_error` is emitted,
so an impatient caller does not spike the signal an operator alerts on.

## Request log

One structured line per request on **stderr**, prefixed `bd serve: ` and a UTC
timestamp from the standard logger:

```
bd serve: 2026/08/01 05:17:42 event=request request_id=8f3a1c07-000042 op=listReadyWork method=GET path=/v0/beads/ready status=200 code="" duration_ms=12.480 sem_wait_ms=0.031 uow_ms=11.902 conns=1 remote_addr=127.0.0.1:54321
```

Fields, in order:

| Field | Meaning |
|---|---|
| `request_id` | Correlation id, `<per-process random prefix>-<sequence>`. Echoed in the `request_id` member of any problem body. |
| `op` | The spec's `operationId`. |
| `method`, `path`, `status` | As sent and answered. |
| `code` | The problem `code`, or `""` on success. |
| `duration_ms` | Whole request, milliseconds with three decimals. |
| `sem_wait_ms` | Time spent waiting for a database slot. |
| `uow_ms` | Time spent holding the unit of work. |
| `conns` | Live accepted connections at that moment — watch this climb toward 64. |
| `remote_addr` | Which client; on loopback the port identifies the local process. |
| `refused` | Present only when the request was refused for a specific value: the offending value. |

The per-process random prefix means ids from two servers, or two runs of one
server, never collide in a shared log.

Values are quoted whenever they contain a space, `"`, `=` or any control
character, and an empty value renders as `""`. That is not cosmetic: without it
a caller-supplied value — a `Host` header, a rejected parameter, an error
message — could forge fields or whole lines, and an unquoted C1 control such as
U+009B is a CSI introducer that would drive the operator's terminal.

Other events on the same stream:

| Event | When |
|---|---|
| `startup` | Bound address, mode, workspace, database, host allowlist, capabilities. |
| `limits` | The operating envelope this build compiled in. Log it, then compare against this page. |
| `request_error` | Accompanies a ≥500, carrying the real error the body withholds. Join on `request_id`. Not emitted when the client hung up (see above). |
| `pool_limits_unavailable` | The unit-of-work provider does not expose the pool knob, so the limits below are *not* applied and the pool is unbounded. Worth alerting on; it changes the connection budget. |
| `panic` | A panicking handler: the value, the stack, and the same `request_id`. |
| `semaphore_saturated`, `semaphore_timeout`, `conn_cap_saturated` | See [Detecting a wedge](#detecting-a-wedge). |
| `shutdown_start`, `shutdown_complete`, `shutdown_forced` | The drain. |

A 5xx body carries a fixed static detail by design, so `request_id` is the
client's only handle on the one line that has the real error. When a user
reports a 500, ask for the `request_id` and grep for it — the `request` line
gives the shape, the `request_error` line gives the cause.

## Refusals to expect at startup

| Message | Cause |
|---|---|
| `bd serve requires a Dolt SQL server; this workspace uses embedded Dolt` | Permanent. The embedded backend commits outside the SQL transaction on a separate connection, so this server's per-request atomicity would be a lie there. Refused by `serveDatabaseSource`, which is the only thing refusing it — see "Workspace modes" in the design doc. |
| `bd serve is unavailable under strict readonly` | `--readonly`, or `readonly` in config. Every server this command binds publishes the issue-claim operation and the advertised capability set is a property of the build, not of the flags on the process that started it — so the alternatives were a server advertising a claim it always fails (the store source, where the read-only open reaches the claimer) or a `--readonly` that quietly bought nothing (the provider source, which builds its own writable connection). Drop the flag to serve. |
| `host must be a numeric IP literal, not a name — use 127.0.0.1 rather than localhost` | `--addr` was given a DNS name. |
| `binds beyond loopback; bd serve has no authentication, so this requires --allow-non-loopback` | A non-loopback `--addr` without the flag. |
| `address already in use` | The fixed-port mutual exclusion working as intended: a second server is already on that port. |
