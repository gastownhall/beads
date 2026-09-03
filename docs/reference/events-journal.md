---
title: Events Journal
description: The durable, ordered record of every committed issue mutation that external tooling tails and replays — enabling it, the record contract, resuming after a prune, automatic retention, and what it deliberately does not cover.
---

Something outside beads wants to stay in step with a workspace: a dashboard, a
mirror in another system, an indexer that has to see every state change rather
than the latest one. Polling `bd list` for that means diffing whole snapshots
and still missing anything that changed twice between two reads.

The **events journal** answers it. Every committed issue mutation writes one
ordered record, in the same transaction as the mutation itself, and a consumer
reads those records with a cursor it can resume from. The spirit is a binlog,
not a notification: a record states a change that has already committed, it
carries the resulting state, and it stays readable until you prune it.

The journal is **off by default**, local to one clone, and bounded by default
once it is on: beads keeps the retention floors below and prunes past them
without being asked. Everything below assumes you turned it on for a consumer
that is actually reading it.

## Which of the three event systems do you want?

Beads has three things called events, and they answer different questions.

| System | What it is | Reach for it when |
|---|---|---|
| **Script hooks** | Executable `on_create` / `on_update` / `on_close` scripts in `.beads/hooks/`, run after the mutation and fire-and-forget: asynchronous, output discarded, a failure neither blocks nor retries the write. | A side effect is nice to have — a chat ping, a cache bust — and losing one now and then is acceptable. |
| **Audit history** | The per-issue trail behind `bd history <id> --events`: who changed what, when, with the old and new value of the field. | A person is asking who closed this bead, and when. |
| **Events journal** | One workspace-wide, sequence-ordered stream of committed mutations, replayable from a checkpoint. | A machine is keeping its own copy of the graph in step. |

## Turning it on

```bash
bd config set events-journal true      # this workspace, written to .beads/config.yaml
BD_EVENTS_JOURNAL=1 bd close bd-a1b2   # or per process / operator-wide
```

Records are written only while the journal is enabled. Enabling it does not
backfill what happened before, so a consumer starting today baselines from the
workspace's current state (an [export](/cli-reference/export), or a full read)
and follows the journal from there.

| Key | Default | Effect |
|---|---|---|
| `events-journal` | `false` | Master switch. Off costs nothing; on costs one snapshot write per mutation. |
| `events-journal-retain-days` | `7` | Keep every record younger than this many days. `0` disables the floor. |
| `events-journal-retain-rows` | `100000` | Always keep this many newest records. `0` disables the floor. |
| `events-journal-auto-prune` | `true` | Enforce the floors automatically. `false` leaves deletion to `bd events prune`. |

The floors bound both prunes: the automatic one and `bd events prune`. See
[Retention and pruning](#retention-and-pruning).

All four are startup settings kept in `config.yaml` rather than database config,
and each has an environment equivalent: `BD_EVENTS_JOURNAL`,
`BD_EVENTS_JOURNAL_RETAIN_DAYS`, `BD_EVENTS_JOURNAL_RETAIN_ROWS`,
`BD_EVENTS_JOURNAL_AUTO_PRUNE`.

## Reading it

```bash
bd events tail --since 0                # every retained record, oldest first
bd events tail --since 4211             # resume from a checkpoint
bd events tail --since 4211 --follow    # ...and keep printing as writes commit (polls once a second; Ctrl-C to stop)
bd events tail --since 4211 --limit 100 # cap one batch
bd events export                        # the whole journal from seq 1 — same as --since 0
```

Output is JSON Lines, one record per line, in sequence order:

```json
{"seq":1,"ts":"2026-01-02T03:04:05Z","op":"create","issue_id":"bd-100","actor":"worker-1","issue":{"id":"bd-100","title":"wire the seam","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T03:00:00Z","updated_at":"2026-01-02T03:04:05Z"}}
{"seq":4,"ts":"2026-01-02T03:04:05Z","op":"update","issue_id":"bd-100","issue":{"id":"bd-100","title":"wire the seam","status":"open","priority":1,"issue_type":"task","is_blocked":true,"created_at":"2026-01-02T03:00:00Z","updated_at":"2026-01-02T03:04:05Z"}}
{"seq":11,"ts":"2026-01-02T03:04:05Z","op":"delete","issue_id":"bd-100","issue":null}
```

A consumer advances its checkpoint to the highest `seq` it has durably
processed, and passes that as the next `--since`.

### Over HTTP

A consumer that already talks to a workspace over `bd serve` reads the same
journal at `GET /v0/beads/events` instead of shelling out:

```bash
curl 'http://127.0.0.1:8080/v0/beads/events?since=4211&limit=500'
```

```json
{
  "records": [
    {"seq":4212,"ts":"2026-01-02T03:04:05Z","op":"create","issue_id":"bd-100","actor":"worker-1","issue":{"id":"bd-100","title":"wire the seam","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T03:00:00Z","updated_at":"2026-01-02T03:04:05Z"}}
  ],
  "head": 4980
}
```

The `records` are the records above — the same fields, the same encoding, the
same [record contract](#the-record-contract) — so an HTTP mirror and a
`bd events export` on the same workspace can be reconciled directly.

- `since` is **required**, and it is the same checkpoint `--since` takes. A
  missing or negative value is a `400`, never a read from the beginning.
- `limit` runs from 1 to 10000 and defaults to 1000. There is no unlimited
  read here: `limit=0` is refused rather than meaning "everything" as it does
  on `GET /v0/beads/issues`.
- `head` is the highest sequence number ever assigned, so a consumer knows
  whether to keep reading or back off. When the last record's `seq` equals
  `head` you are caught up — a full page proves nothing on its own.
- The journal is read-only over HTTP. Pruning stays a workspace decision made
  with `bd events prune` and the retention floors.

<Warning>
Publishing the journal publishes the workspace's **history**, not its current
state: every retained record carries the full issue snapshot as it was at that
mutation, including titles and descriptions since edited and issues since
deleted. Pruning and the retention floors are the only thing that removes a
record — editing or deleting a bead does not redact it from the journal. And
because this is an HTTP read, any process that can reach the address gets that
history without the filesystem permissions on `.beads/` that `bd events tail`
requires. Weigh both before binding a journal-enabled workspace with
`--allow-non-loopback` — which requires `--auth-token-file` (or the explicit
`--insecure-no-auth`), and that token is shared and surface-wide: every client
holding it reads the whole journal.
</Warning>

Two refusals are worth wiring into a consumer before it ships. A checkpoint
below the retained window is a `410 Gone` carrying the same
`events_journal_truncated` code and the same `since` / `floor` / `head` window
[the CLI reports](#resuming-and-the-truncation-error) — with the same ways
forward. And a workspace whose journal is **off** answers `409` with
`events_journal_disabled` rather than an empty page, because a disabled journal
and an empty one look identical in the data and a consumer given the empty page
would poll a workspace that will never produce a record. That 409 is workspace
state, not a missing feature: `events.list` appears in `/v0/beads/context`'s
capabilities on every build, so treat the capability as "this server speaks it"
and the 409 as "not on this workspace". (A workspace that has enabled the
journal on a storage backend with no journal support never gets this far —
`bd serve` refuses to start, the same refusal opening that workspace already
gives.)

The journal is per replica, which matters more over HTTP than on the command
line: a checkpoint is meaningful only against the server URL that issued it.
Track one per server, and re-baseline rather than carry one across.

### Streaming instead of polling

`GET /v0/beads/events:watch` is the same journal, pushed. It answers
`text/event-stream` and holds the connection open, emitting each mutation as it
commits — the HTTP form of `bd events tail --follow`:

```bash
curl -N 'http://127.0.0.1:8080/v0/beads/events:watch?since=4211'
```

```
retry: 3000

id: 4212
data: {"seq":4212,"ts":"2026-01-02T03:04:05Z","op":"create","issue_id":"bd-100","actor":"worker-1","issue":{"id":"bd-100","title":"wire the seam","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T03:00:00Z","updated_at":"2026-01-02T03:04:05Z"}}

: heartbeat
```

Each event's `data` is one line carrying exactly the record the paged read
returns, and `id` is that record's `seq` — the same number `since` takes. The
comment lines are heartbeats, sent every 20 seconds of silence so idle
connections survive intermediaries that drop them; clients ignore comments
automatically.

**Watch or poll?** Poll unless the delay is the point. A poller holds nothing
between requests and can never be refused for capacity; a stream costs a
connection for as long as it is open, and this server holds at most **48** of
them before answering `503` with `events_watch_saturated` and pointing you back
at `GET /v0/beads/events`. That cap sits deliberately below the server's
64-connection limit, so a workspace saturated with streams still has room to
answer polls, mutations and health checks — and room to deliver the `503`
itself. Stream when something is waiting on the mutation — a live mirror, an
agent watching a gate — and poll for anything that can afford an interval.
Neither is faster at draining a backlog.

**Delivery is at-least-once against your own checkpoint.** Within a single
stream each record is sent once, in `seq` order, with no gaps. Duplicates are
possible only across a reconnect — if you resume from an id whose records you
had already applied — so make your consumer idempotent on `seq` and advance
your checkpoint only after your own write lands, exactly as when polling.

**Reconnecting is the normal case, and it is free.** When a stream drops,
reconnect with the standard `Last-Event-ID` header carrying the last `seq` you
processed; it **overrides** `since`. That is what makes a browser's
`EventSource` correct with no extra code — it re-sends the original URL, whose
`since` is as old as your process:

```js
const es = new EventSource('/v0/beads/events:watch?since=0');
es.onmessage = (e) => apply(JSON.parse(e.data));   // e.lastEventId is the seq
es.addEventListener('truncated', (e) => { es.close(); rebaseline(JSON.parse(e.data)); });
```

`since` is still required on every connect — the header is absent on the first
one — and a `Last-Event-ID` that is not a sequence number is a `400` rather than
a silent fall back to `since`, which would start the stream somewhere you did
not ask for.

**Every refusal happens before the stream opens.** The `409` and the `410` above
are the same responses on this route as on the paged read, decided before the
first byte, so a client that got its `200` knows its checkpoint was servable.

<Warning>
There is exactly one failure that can arrive *after* that: a **`truncated`
event**, sent when a prune removes the records the open stream was about to
send. Its `data` is the same `410` body — `events_journal_truncated` with
`since` / `floor` / `head` — and the stream closes immediately after it.

**Treat it as stop-and-re-baseline**, with [the same ways
forward](#resuming-and-the-truncation-error) as the `410`. A consumer that
ignores it does not lose records silently, but it does stall loudly: a bare
`EventSource` will reconnect with the same dead id and earn a connect-time
`410` on every attempt from then on. The stream raises the reconnection delay to
60 seconds first so that loop is slow rather than hot.
</Warning>

The exposure warning above applies identically — a stream is the same history
over the same address and under the same shared credential, held open — and so
does the capability
rule: `events.watch` appears in `/v0/beads/context`'s capabilities on every
build, and says nothing about whether this workspace has a journal.

## The record contract

| Field | Type | Meaning |
|---|---|---|
| `seq` | integer | Assigned inside the mutation's transaction. Gapless, strictly increasing in commit order, never reused and never reset. A rolled-back write burns no sequence number. |
| `ts` | string | UTC insert time, stamped inside the committing transaction. |
| `op` | string | One of the seven operations below. |
| `issue_id` | string | The mutated issue. |
| `actor` | string | The acting identity that performed the mutation, as resolved for the audit-events table; on a `comment` row, the comment's author. Absent when the path has no actor — derived maintenance (`is_blocked` recomputes), deletes (other than a rename's synthetic `delete` row), and rows written before the journal recorded actors. An absent `actor` is never user attribution: read it as "system/unknown", not as a conflicting writer. |
| `issue` | object or null | The issue's full state *after* the mutation; `null` on a delete. |
| `dep` | object | `{"kind","target","metadata"}` on `dep_add` and `dep_remove`; absent otherwise. |
| `comment` | object | `{"id","author","text","created_at","source"}` on `comment`; absent otherwise. |

Six operations are the public vocabulary — the only kinds a downstream event
feed built on the journal may carry:

| `op` | Recorded when |
|---|---|
| `create` | A bead (or wisp) is created. |
| `update` | Any field, label, metadata, claim, unclaim, lease reclaim, promote, defer wake, or derived `is_blocked` flip changes the bead. |
| `close` | A bead is closed. A reopen is an `update`. |
| `delete` | A bead is removed. |
| `dep_add` | A [dependency](/core-concepts/dependencies) edge is written. |
| `dep_remove` | A dependency edge is removed. |

An id rename has no operation of its own. It replays as the operations a
consumer can apply without understanding identity changes: the old edges
removed, the old id deleted, the new id created, the edges re-added under it.

A seventh operation, `comment`, is journaled too. Its `comment` member is the
replayable payload, and `comment.source` is a closed two-value set:
`structured` for a comment someone wrote, `audit` for a comment recorded as an
audit-trail entry.

```json
{"id":"cmt-1","author":"worker-1","text":"picked this up","created_at":"2026-01-02T03:04:05Z","source":"structured"}
```

Anything projecting the journal outward as a public event feed *skips* a
`comment` record rather than faulting on it — the write is already visible in
the issue snapshot beside it.

### The issue snapshot

`issue` is the bead's own row plus its labels plus `is_blocked` — the persisted
readiness projection, included so a dependency change replays without
recomputing the graph. It never inlines dependencies or comments; those arrive
as their own `dep_*` and `comment` records.

It is `null` on a `delete`, and on a dependency record whose source bead was
itself removed by the same cascade.

### Dependency records are not symmetric

A `dep` member names the edge that changed:

```json
{"kind":"blocks","target":"bd-100","metadata":""}
```

**In count.** A `dep_add` is recorded for every accepted add, *including* an
idempotent same-type re-add that only refreshes the edge's metadata. The audit
history deduplicates that case and writes nothing; the journal does not. Treat
`dep_add` as an upsert of the edge, never as proof the edge is new. A
`dep_remove` naming an edge that is already gone records nothing at all.

**In payload.** `dep.metadata` differs in provenance between the two. On a
`dep_add` it is the value being written, exactly as the caller supplied it; on
a `dep_remove` it is the stored column read back just before the delete. The
two can differ byte for byte while meaning the same thing, so compare parsed
values rather than strings.

## Resuming, and the truncation error

If `--since` falls below the oldest retained record — the prefix you asked for
was pruned — the read *fails*. It does not skip ahead to the surviving suffix,
and it does not return an empty success, because both are silent record loss
and indistinguishable from "nothing new" at the cursor.

```bash
bd events tail --since 12 --json
```

```json
{
  "code": "events_journal_truncated",
  "error": "events journal truncated: checkpoint 12 is below the retained window [41..980]; records 13..40 were pruned",
  "floor": 41,
  "head": 980,
  "schema_version": 1,
  "since": 12
}
```

The command exits 1, and the payload carries bd's usual
[`schema_version` envelope](/reference/json-schema). `floor` is the oldest
sequence number still retained, `head` the highest ever assigned. Two ways
forward, and the engine takes neither on your behalf:

- **Accept the gap** — resume from `floor - 1` (`bd events tail --since 40`
  here) and carry on knowing records 13..40 are lost.
- **Re-baseline** — rebuild your copy from the workspace's current state, then
  follow again from `head`. Re-reading a few records is harmless: every record
  carries the full post-mutation snapshot, so applying one twice is the same as
  applying it once.

`bd events export` refuses the same way rather than present a pruned journal's
surviving suffix as a complete history.

Mid-`--follow`, the same failure arrives as **one line of JSON on the stream it
interrupts** — same code, same `since` / `floor` / `head`, same exit status,
compact and without the envelope, because the consumer on the other end is a
line reader:

```json
{"code":"events_journal_truncated","error":"events journal truncated: checkpoint 12 is below the retained window [41..980]; records 13..40 were pruned","floor":41,"head":980,"since":12}
```

A hole in the *middle* of the retained window refuses the same way, with
`since` reporting the last sequence number the read could serve contiguously
from your checkpoint. Nothing bd does produces such a hole — pruning only ever
removes a prefix — but a restored, hand-edited, or half-copied journal table
can, and a consumer must never be handed one silently.

That case has a **third way forward**, and it is worth taking before the other
two: everything between your checkpoint and the reported `since` is intact and
servable, and the refusal did not hand it over. Drain it explicitly by asking
for exactly that span — the same `--since` you already passed, with `--limit`
set to `response.since - your since` — which stops the batch at the hole and
succeeds:

```bash
# refused with since 40 (your checkpoint was 12), floor 61
bd events tail --since 12 --limit 28   # records 13..40, the intact stretch
bd events tail --since 60              # then take the gap, or re-baseline
```

Skipping straight to `floor - 1` loses records you could have had.

## Retention and pruning

The journal is bounded automatically. After a mutating command commits — and on
a timer inside `bd serve` — beads deletes the prefix the two retention floors do
not protect, so an enabled journal settles at "the last 7 days, or the newest
100 000 records, whichever is larger" instead of growing forever.

```bash
bd events prune --before 4000   # an earlier, on-demand cut below the floors
```

Both floors compose onto `--before` and can only ever *reduce* what a prune
removes. Every prune — automatic or asked for — resolves `--before` and both
floors into a single bound and deletes the prefix below it, so a prune can never
leave a hole above a protected record. Automatic pruning is that same
computation with `--before` set past the head: delete everything the floors do
not protect, and nothing else.

`bd events prune` therefore cuts *earlier*, never *deeper*: it removes a
consumed span before the floors would have, and shrinking the retained window
itself means lowering the floors.

| Want | Do |
|---|---|
| A bounded feed | Nothing. It is the default. |
| A different window | Set `events-journal-retain-days` / `events-journal-retain-rows`. |
| An unbounded ledger | Set **both** floors to `0`. Automatic pruning then does nothing at all. |
| Floors respected on reads, deletion under your own control | `bd config set events-journal-auto-prune false`, and run `bd events prune` yourself. |

Maintenance never fails a command. A pass that cannot run is logged and skipped,
it is capped at a few batches per invocation so a long backlog drains over
several commands rather than stalling one, and a throttle keeps it to about one
pass an hour per workspace — or sooner after a large burst of writes.

A pass maintains **the workspace whose command triggered it**. A routed write
(`bd create --repo ../other`) records into the target's journal, but the
retention pass runs against the workspace you ran the command in. A workspace
that is only ever written remotely relies on commands run in it — or on its own
`bd serve` — to stay bounded; if nothing ever runs there, prune it on a schedule
of your own.

<Warning>
The floors are a time and count window, **not a consumer watermark**. A
consumer that has fallen further behind than both floors allow will be pruned
past and lose records — now without anyone running a command to do it. Track
your own watermark, size the floors for the longest outage you intend to
survive, and read [Resuming, and the truncation
error](#resuming-and-the-truncation-error) before you lower them.
</Warning>

Pruned history cannot be recovered from the workspace; the journal is the only
local copy. Pruning frees rows, not disk: pair it with `dolt gc` to actually
reclaim the space, since these are working-set tables that ordinary Dolt commits
never collect.

## What the journal does not cover

Each of these is a deliberate boundary, and each one matters to a consumer that
assumes otherwise.

- **One replica, one sequence space.** Each clone counts from its own first
  mutation. A checkpoint taken against one replica is meaningless against
  another — the same `seq` names a different record, and a `seq` above the
  other replica's head reads as "caught up" and stalls forever. A fresh clone
  starts empty, with `seq` restarting at 1. Track a checkpoint per replica and
  re-baseline rather than carry one across.

  The same reset happens in place if the journal tables are dropped and
  recreated, or if `bd_events_seq` is lost or restored from a backup: `seq`
  begins again at 1, so a parked consumer's checkpoint now sits above the head
  and reads as "caught up" forever. No error is raised, because nothing about
  the new journal looks pruned. Re-baseline any consumer after either.
- **One branch.** The journal records the mutations committed on the writer's
  active branch. Records arrive by direct write, not by merge, so read on the
  same branch the writer commits to; a branch checkout or merge carries no
  records across.
- **[Sync](/core-concepts/sync-concepts) is not journaled.** `bd dolt pull`,
  and the changes a merge settles into this clone, arrived as data — nothing
  here wrote them through the mutation path. A consumer mirroring a synced
  workspace re-baselines after a sync.
- **Raw SQL is not journaled.** DML run through `bd sql` bypasses the write
  paths that record, and is a known non-coverage.
- **Store-open writes are not journaled.** Schema migrations and the version
  reconciliation that runs before the workspace's configuration reaches the
  store touch schema and clone-local metadata, never a bead, so a replaying
  consumer has nothing to apply them to.
- **Compaction and restore are not journaled.** `bd admin compact` rewrites a
  bead's text outside the operation vocabulary, and `bd restore --apply` puts
  it back the same way. A consumer's mirror of a compacted bead goes stale and
  stays stale until that bead's next journaled mutation delivers a fresh
  snapshot.

## How it sits in Dolt

The journal lives in two tables, `bd_events_journal` and `bd_events_seq`, both
registered in `dolt_ignore`. They are working-set state: never versioned, never
staged into a commit, never pushed, pulled, or federated — and, like untracked
files in git, they survive a `dolt reset --hard`.

That is what buys the journal its properties. Versioning these tables would put
a row per mutation into [Dolt's history](/architecture/dolt), and a per-clone
sequence counter on a replicated table would conflict on every merge. The price
is locality: the journal describes what *this* clone mutated, which is exactly
what the boundaries above spell out.
