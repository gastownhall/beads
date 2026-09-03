---
title: Bucket Federation Quickstart
description: Federate a beads database across machines through a GCS or S3 bucket — remote add, seed push, birth the second replica, and pick a sync cadence
---

Point two machines at one object-storage bucket and you have a beads
federation: no server to run, no hosted account, no ports to open. Dolt speaks
GCS and S3 natively, so the bucket *is* the remote — the same role a git
remote plays for a repo.

This is the BYO-cloud path. [DoltHub](/multi-agent/federation) remains the
zero-config default; reach for a bucket when the data must stay in your own
cloud account, or when you already have one.

## What you end up with

```
   machine one                  gs://my-bucket/beads/myproject                machine two
  ┌─────────────┐                    ┌──────────────┐                      ┌─────────────┐
  │ .beads/dolt │ ──── bd sync ────► │    bucket    │ ◄──── bd sync ────── │ .beads/dolt │
  │  (replica)  │ ◄──────────────────│  (no server) │ ─────────────────────►│  (replica)  │
  └─────────────┘                    └──────────────┘                      └─────────────┘
```

Each machine keeps a full local replica and works offline against it; a timer
runs `bd sync` on both ends. Nothing arbitrates between them but the bucket.

## Prerequisites

1. **The Dolt backend** (the only supported backend).
2. **A bucket you can write to**, and credentials on both machines:
   - GCS: Application Default Credentials —
     `gcloud auth application-default login` (or a service account via
     `GOOGLE_APPLICATION_CREDENTIALS`).
   - S3: the standard AWS chain — `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`/
     `AWS_SECRET_ACCESS_KEY`, or an instance role.
3. **A Dolt commit identity on every machine** — see
   [Failure modes](#failure-modes); a fresh machine usually has none, and the
   first merge is what discovers it.

## 1. Create the bucket path

One bucket path per beads database. Two databases sharing a path will fight
over the same Dolt history.

```bash
# GCS
gcloud storage buckets create gs://my-bucket --location=us-central1

# S3
aws s3 mb s3://my-bucket
```

No layout to prepare inside it — the first push creates everything.

## 2. Machine one: register the remote and seed it

Run this in the workspace that already holds the database you want to share:

```bash
bd dolt remote add origin gs://my-bucket/beads/myproject
bd dolt push
```

Supported schemes are `gs://`, `s3://` (or Dolt's `aws://`), `az://`,
`dolthub://`, `https://`, `file://`, and git SSH.

Two details worth knowing:

- **Use `bd dolt remote add`, not raw `dolt remote add`.** bd registers the
  remote through the store API so a running Dolt SQL server sees it
  immediately. A remote added with the `dolt` CLI lands in filesystem config
  only, and push/pull then fail with *remote not found* until the server
  restarts.
- **Naming it `origin` also persists `sync.remote`** into `.beads/config.yaml`,
  which is what lets `bd sync` find the remote with no flags. Any other name
  works, but every sync then needs `bd sync --remote <name>`.

Verify:

```bash
bd dolt remote list
```

## 3. Machine two: birth the replica from the bucket

```bash
bd init --remote gs://my-bucket/beads/myproject
```

`bd init --remote` clones the Dolt database from the bucket and persists
`sync.remote`, so machine two is ready to sync immediately. This is a *clone*,
not an import: no JSONL round-trip, no re-keying, and the full commit history
comes with it.

Verify the birth before you trust it:

```bash
bd dolt remote list          # points at the bucket
bd list --status all --json | jq length   # issue-count parity with machine one
```

An issue that machine one closed *after* the last push will still look open
here. That is federation lag, not a bad clone — it arrives on the next sync.

<Note>
Already have a `.beads/` directory on machine two that you want to replace with
the bucket's copy? Don't clone over it. Move it aside first, then run
`bd init --remote`; see [Init Safety](/recovery/init-safety) for the guard
rails.
</Note>

## 4. Pick a sync cadence

`bd sync` is the whole loop — pull, positively check for conflicts, repair
`is_blocked`, push with bounded retry:

```bash
bd sync                  # default remote
bd sync --remote mini    # a specific named remote
bd sync --json           # machine-parseable outcome
```

Run it from a timer on both machines. A 60-second cadence is comfortable at
the scale measured below; the loop is a no-op when nothing changed.

```bash
# cron, every minute
* * * * * cd /path/to/workspace && /usr/local/bin/bd sync --json >> /tmp/bd-sync.log 2>&1
```

A timer branches on the exit code without parsing output:

| Exit | Meaning | What the timer should do |
|------|---------|--------------------------|
| 0 | Synced, or nothing to do | Nothing |
| 1 | Error (transport, auth, storage) | Alert if it repeats |
| 2 | Merge conflict — halted, nothing pushed | **Alert a human**; never auto-resolve |
| 3 | Retries exhausted (push race, or another writer's dirty working set) | Nothing; the next tick retries |
| 4 | Dirty working set is stuck, not busy | **Alert a human**; no later tick will publish |

Three rules for choosing the interval:

- **Staleness is the interval.** Every replica's view of the others is up to
  one full interval old, by construction.
- **Lease TTL and reclaim grace must both exceed the interval.** A lease is
  only meaningful on the replica that granted it, and a reaper on the other
  machine would be judging liveness from data older than the lease. See
  [Leases are per-replica](/multi-agent/federation#leases-are-per-replica),
  and name each replica with `bd config set node_id <name>` so the
  cross-replica reclaim guard arms.
- **A longer interval means more conflicts, not just staler data.**
  `updated_at` is touched by *every* bd mutation, so two replicas editing the
  same issue between syncs conflict even when the fields they changed are
  disjoint. Disjoint edits to *different* issues merge cleanly at any cadence.

## Measured cost

From a production two-machine deployment (laptop + Mac Mini, ~1.3k issues,
~115k chunks, Dolt 2.1.10, GCS remote):

| Operation | Time |
|-----------|------|
| Full cold push (seeding an empty bucket) | 28s |
| Full clone (birthing a replica) | 5s |
| Incremental push | ~4s |
| Pull + merge | ~1s |

A 60-second cadence therefore spends a few seconds per tick, and the steady
state is dominated by no-ops.

## Failure modes

### `bd dolt push` says the remote does not exist

The remote was added with raw `dolt remote add`, so it exists in filesystem
config but not in the SQL server's `dolt_remotes` table. Re-register it:

```bash
bd dolt remote add origin gs://my-bucket/beads/myproject
```

### Pull fails on a fresh machine with no conflicts reported

A machine with no Dolt commit identity cannot author the merge commit a pull
creates, so even a no-op pull fails. It is the most common first-sync failure
on a newly provisioned box:

```bash
dolt config --global --add user.name  "Your Name"
dolt config --global --add user.email "you@example.com"
```

### Auth works in the shell but sync fails in server mode

With an external Dolt SQL server, `CALL DOLT_PUSH/PULL` runs *inside the
server process*, which only has the environment it inherited at startup.
Credentials exported afterwards never reach it. bd detects cloud credentials
matching the remote's scheme (`GOOGLE_*`/`GCS_*` for `gs://`, `AWS_*` for
`s3://`/`aws://`, `AZURE_STORAGE_*` for `az://`) and routes push/pull through
a `dolt` CLI subprocess, which inherits the current environment. If sync still
cannot authenticate, restart the server with the credentials in its
environment.

### The remote is named something other than `origin`

A replica born with `bd init --remote` (or `dolt clone`) names its remote
`origin`; a machine where someone added the remote by hand may have named it
anything. Timers must pass the right `--remote` on each machine — or rename
the remote so both ends match.

### A sync exits 2 (conflict)

`bd sync` halts before recomputing or pushing and never auto-resolves what it
cannot settle safely. Repeated runs keep halting the same way until an
operator resolves the divergence. Inside `.beads/dolt/<db>`:

```bash
dolt sql -q 'select * from dolt_conflicts'   # positive check — do NOT trust pull exit codes
dolt conflicts cat issues                    # base / ours / theirs rows
dolt conflicts resolve --theirs issues       # or --ours
dolt add -A && dolt commit -m 'resolve conflict' && dolt push origin main
```

Then let the timer resume.

### One bucket path, two databases

A bucket path is a single Dolt history. Pointing a second, unrelated beads
database at the same path produces a divergence no merge can reconcile. Give
each database its own path.

## Relationship to `bd federation`

Both paths write Dolt remotes; they differ in what they are for.

| | `bd sync` (this page) | `bd federation sync` |
|---|---|---|
| Target | The workspace's configured remote | Named peer towns |
| Conflicts | Halts (exit 2); no override switch | `--strategy ours\|theirs` available |
| Use for | Replicas of *one* database across machines | Sharing across independent teams/orgs |

Registering the bucket as a named peer instead is one command, and the peer
surface adds sovereignty tiers and topologies:

```bash
bd federation add-peer backup gs://my-bucket/beads-backup
```

See [Federation Setup](/multi-agent/federation) for peers, sovereignty tiers,
and topologies.

## Reference

- [Federation Setup](/multi-agent/federation) — peers, sovereignty, topologies
- [Dolt Architecture](/architecture/dolt) — remotes, push/pull, storage layout
- [`bd dolt`](/cli-reference/dolt) · [`bd init`](/cli-reference/init) ·
  `bd sync --help` for the full sync surface
- [Sync Failures](/recovery/sync-failures) — recovering a wedged sync
