---
title: "bd events"
description: "Read and manage the durable events journal"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc events`.

Read and manage the durable events journal (bd_events_journal).

The journal records every committed issue mutation as an ordered, replayable
row. Enable it with 'bd config set events-journal true' (or
BD_EVENTS_JOURNAL=1). Records are emitted only while it is enabled.

Retention is automatic: an enabled journal is bounded to the retention floors
(events-journal-retain-days / -rows, 7 days / 100k rows by default) without
anyone running a command. Disable both floors for an unbounded ledger, or
events-journal-auto-prune for manual control; 'bd events prune' remains for an
earlier, on-demand cut below the floors.

Coverage and scope:
  - Every mutation through bd's normal write paths (create, update, close,
    reopen, delete, claim, dependency add/remove, label add/remove, comment) is
    journaled in the same transaction as the change. Raw DML run through
    'bd sql' bypasses those paths and is NOT journaled — a known non-coverage.
  - The journal is per-branch working-set state (dolt_ignored): it records the
    mutations committed on the writer's active branch. Rows arrive by direct
    write, not by merge, so a consumer must read the journal on the same branch
    the writer commits to; a branch checkout or merge does not carry journal
    rows across branches.
  - For the same reason the journal is per REPLICA. 'bd dolt pull' and the
    changes a merge settles into this clone are not journaled: those rows
    arrived as data, not as local mutations, and nothing on this clone wrote
    them through the mutation seam. A consumer that mirrors a synced workspace
    must re-baseline (a fresh export or a full re-read) after a sync, because
    the journal describes only what THIS clone mutated.
    Each replica also has its OWN seq space, counted from its own first
    mutation. A checkpoint taken against one replica is meaningless against
    another — the same seq names a different record, and a seq above the other
    replica's head reads as "caught up" and stalls forever. Track a checkpoint
    per replica, and re-baseline rather than carry one across.
  - A few writes that happen while a store is being OPENED are unjournaled by
    design: schema migrations and the version reconciliation that runs before
    the workspace's configuration has been applied to the store. They touch
    schema and clone-local metadata, never a bead, so a replaying consumer has
    nothing to apply them to.
  - Dependency records are not symmetric, in two ways.
    Count: a dep_add is emitted for every accepted add, INCLUDING an idempotent
    same-type re-add that only refreshes the edge's metadata. The audit 'events'
    table deduplicates that case and writes nothing; the journal does not. Treat
    dep_add as an upsert of the edge, not as proof the edge is new. A dep_remove
    naming an edge that is already gone emits nothing at all.
    Payload: dep.metadata differs in provenance between the two ops. On dep_add
    it is the value being written, as the caller supplied it; on dep_remove it is
    the raw stored column read back just before the delete. The two can differ
    byte for byte while meaning the same thing, so compare parsed values.

Structural dependency edits — the ones bd wires up itself rather than a 'bd dep'
verb — write no audit event but DO journal, by design: a replaying consumer
needs the edge either way.

```
bd events [command]
```

## bd events export

Print every events journal record from seq 1, in order, as JSON lines.

Equivalent to 'bd events tail --since 0'. Like tail, it FAILS rather than
present a pruned journal's surviving suffix as a complete history.

```
bd events export [flags]
```

**Flags:**

```
      --limit int   maximum number of records to return (0 = no limit)
```

## bd events prune

Delete events journal records with seq less than --before.

Retention is already enforced automatically: after a mutating command commits,
and on a timer in 'bd serve', bd deletes everything the floors below do not
protect. This command is for an EARLIER, on-demand cut BELOW the floors — after
a consumer has durably processed a span you do not want to wait out. It cannot
cut deeper than the floors: shrinking the retained window itself means lowering
them. The journal is clone-local operational state, so pruning never affects
issue data.

Two retention floors compose onto --before and can only reduce what a prune
removes. They bound the automatic prune and this one identically:
  events-journal-retain-days   keep every row younger than N days (default 7)
  events-journal-retain-rows   always keep the newest N rows (default 100000)

Set BOTH floors to 0 for an unbounded ledger: automatic pruning then does
nothing, and this command becomes the only thing that deletes a record. To keep
the floors but own deletion yourself, set events-journal-auto-prune false.

Note the floors are time-based and count-based — they are NOT a consumer
watermark. They protect only the recent window; a consumer that has fallen
further behind than both floors allow will be pruned past and lose records.
Consumers are responsible for tracking their own watermark (the highest seq they
have durably processed) and for sizing the floors to the longest outage they
intend to survive. Pruned history cannot be recovered from the workspace — the
journal is the only local copy. Pruning frees rows, not disk: pair it with
'dolt gc' to reclaim the space, since the table is working-set (dolt_ignored)
state that ordinary Dolt commits never garbage-collect.

```
bd events prune [flags]
```

**Flags:**

```
      --before int   delete records with seq less than this value
```

## bd events tail

Print events journal records with seq greater than --since, in order.

Each line is a JSON record:
  &#123;"seq":N,"ts":"...","op":"create|update|close|delete|dep_add|dep_remove|comment",
   "issue_id":"...","actor":"...","issue":&#123;...|null&#125;,"dep":&#123;"kind":..,"target":..,"metadata":..&#125;,"comment":&#123;...&#125;&#125;

Record contract (stable for external consumers):
  seq       int64   counter-assigned inside the mutation's transaction; gapless,
                    strictly increasing in commit order, never reused or reset
  ts        string  UTC insert time, stamped inside the committing transaction
  op        string  one of the seven ops above
  issue_id  string  the mutated issue's id
  actor     string  the acting identity that performed the mutation, as resolved
                    for the audit-events table (on a comment row: the comment's
                    author). A delete — and the dep_remove rows a cascading
                    delete produces — carries the identity that REQUESTED it.
                    Empty (omitted) only when the path genuinely has no actor:
                    derived maintenance, system cleanup with no request behind
                    it, and rows older than the column. Never user attribution
                    when empty.
  issue     object  full issue state AFTER the mutation; null on delete
  dep       object  &#123;"kind","target","metadata"&#125; for dep_add / dep_remove; omitted otherwise
  comment   object  &#123;"id","author","text","created_at","source"&#125; for comment; omitted otherwise

Poll with the highest seq seen to consume new mutations incrementally, or pass
--follow to keep printing new records as they are committed (Ctrl-C to stop).

Retention boundary: if --since is below the oldest retained record — the prefix
you asked for was pruned — the read FAILS instead of silently skipping ahead or
returning an empty success. With --json the failure carries
&#123;"code":"events_journal_truncated","since":N,"floor":F,"head":H&#125;: floor is the
oldest seq still retained, head the highest ever assigned. Resume from floor-1
to continue with a known gap, or rebuild from a full export.

```
bd events tail [flags]
```

**Flags:**

```
      --follow      keep printing new records as they are committed (Ctrl-C to stop)
      --limit int   maximum number of records to return (0 = no limit)
      --since int   return records with seq greater than this value
```
