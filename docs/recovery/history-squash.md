---
title: History Bloat
description: Shed reachable Dolt history that dolt gc cannot reclaim
---

Every bead write mints a Dolt commit, and Dolt keeps every byte a reachable
commit references — `dolt gc` only reclaims what nothing points to. A
workspace that has accumulated months of high-frequency writes can grow far
past its live data (gigabytes of storage for a few thousand beads) while
`dolt gc` reclaims nothing, because the entire chain is still reachable from
your branch. This runbook squashes that chain to a single baseline commit so
the old history becomes collectable, without touching your live data.

<Warning>
This procedure rewrites history. Every other clone of the database becomes
unmergeable and must re-clone, and remotes and backups must be re-pointed.
Run it in a fenced window: all writers stopped **on every machine that syncs
this database**, backup verified first.
</Warning>

## Symptoms

- The Dolt data directory (or its remote/backup) is large and growing while
  `bd stats` shows a modest number of beads
- `dolt gc` and `dolt gc --full` reclaim little or nothing
- Cloning or pulling the database is slow far out of proportion to its content

## Diagnosis

Run these inside the Dolt data directory — `.beads/embeddeddolt/<database>/`
in embedded mode, `.beads/dolt/<database>/` in server mode:

```bash
# How big is the store?
du -sh .

# How deep is the history? Thousands of commits with a small live
# dataset means the history, not the data, is the bloat.
dolt log --oneline | wc -l

# Confirm gc has nothing unreachable to collect
dolt gc --full
```

If `dolt gc --full` frees the space, you are done — no squash needed.

## Solution

**Step 1:** Fence and back up. Stop every writer: agents, background
services, and — easiest to miss — any scheduled sync job (cron, launchd,
systemd) that pushes or pulls this database, on *every machine that syncs
it*, not just the one you squash on. List the machines and their sync units
by name and verify each is stopped. One live peer sync undoes the squash on
its next tick: it pulls the new baseline, cross-merges it into its old
chain, and pushes the entire old history back to the fresh remote. In
server mode also stop the server after backing up. The backup is dolt-native
and keeps the full history, so it remains your rollback.

```bash
bd backup sync
bd dolt stop
```

**Step 2:** Squash to a single baseline. From the Dolt data directory,
re-commit the current tree directly on top of the root commit. Keeping the
root as the sole ancestor preserves a valid chain — do not try to "simplify"
further by starting an orphan branch:

```bash
root=$(dolt log --oneline | tail -1 | cut -d' ' -f1)
dolt reset --soft "$root"
dolt add -A
dolt commit -m "history squash: baseline $(date +%F)"
```

**Step 3:** Drop the other refs and collect. Anything still pointing at the
old chain keeps it alive — stale local branches and tags, and also the
*remote-tracking refs* left behind by every past push and fetch, which any
long-lived synced workspace has. Delete them all before collecting; Step 4's
force-push recreates the remote-tracking refs on the new chain:

```bash
dolt branch          # delete stale branches:       dolt branch -D <name>
dolt branch -r       # delete remote-tracking refs: dolt branch -rd <remote>/<branch>
dolt tag             # delete stale tags:           dolt tag -d <name>
dolt gc --full
du -sh .             # verify: the store should now be a fraction of its old size
```

If the size barely moved, a ref still anchors the old chain — re-check
`dolt branch`, `dolt branch -r`, and `dolt tag` for survivors and collect
again. (A failed collection does not endanger Step 4 — the push sends only
what the new baseline references — but this machine keeps the bloat until
the gc succeeds.)

**Step 4:** Re-point remotes and backups. The new history is unrelated to
the old, so the first publish must replace it:

```bash
bd dolt push --force
bd backup remove && bd backup init <path>   # fresh destination, then:
bd backup sync
```

<Warning>
A Dolt remote accumulates chunks monotonically: the force-push re-points the
remote's refs at the squashed chain but deletes nothing, so the *remote's*
storage does not shrink. To reclaim the published side too, rebuild the
remote's data plane from the squashed HEAD with `bd dolt remote reset-data`
(below), or replace the remote with a fresh path/prefix:
`bd dolt remote remove <name>`, `bd dolt remote add <name> <fresh-url>`,
then `bd dolt push --force`. Every other clone must re-clone after a squash
regardless, so replacing the remote costs nothing extra.
</Warning>

`bd dolt remote reset-data` replaces a remote's data plane in place with a
fresh store holding only live chunks. In server mode, restart the Dolt
server right before it (`bd dolt stop`, then `bd dolt start`; restart a
server bd does not run yourself), and do not push or pull that remote in
between:

```bash
bd dolt remote reset-data <name> --yes
```

That is how to shrink a *git-backed* remote (issue data riding your code
remote under `refs/dolt/data`), where there is no fresh path to pick — the
storage is the git repository itself, and its manifest lists every
historical table file as current, so the full old store stays reachable
even after the force-push. reset-data replaces the remote's Dolt data refs
with the fresh store; code branches are untouched. On a native `file://`
remote it clears the store directory and pushes again. It refuses a cloud
or hosted remote (`aws://`, `gs://`, `dolthub://`, ...): replace that one as
above.

<Warning>
A `file://` remote whose URL does not end in `.git` (`bd dolt remote list`
shows `file://`, not `git+file://`) and whose directory is a git repository, a
checkout or a bare repository, is not a native file store: Dolt keeps its
files among the repository's own. Do not run reset-data on such a remote; replace it with a
fresh URL as above. Releases up to and including v1.3.2 clear such a
checkout, `.git` and every working file included, and report a reset that
leaves a bare repository's old table files in place; later releases refuse
both.
</Warning>

The restart matters: a running sql-server keeps its view of every git
remote it has used, the force-push above included. Releases up to and
including v1.3.2 delete the remote's Dolt data refs and then force-push.
Once the refs are deleted under that view, the push through the server
hangs until a read timeout or reports success having written nothing, and
the remote is left without its Dolt data; running reset-data again right
after a restart rebuilds it. Deleting the refs by hand
(`git push origin :refs/dolt/data`) and force-pushing runs into the same
trap, so use reset-data.

Through a server, releases after v1.3.2 never delete the data refs first:
reset-data pushes the fresh store to a temporary ref and then moves the
data ref to it, so the remote keeps its data until the new store is
complete, and the restart before it does no harm. The move leaves the
server's view of the remote stale instead: until the server restarts, it
takes the pre-reset store's table files for present on the remote, so a
push through it can write a manifest naming files the remote no longer
holds, or report a branch as up to date that the remote lacks. So those
releases restart bd's own server after the move, on the same port (in
shared-server mode, the shared server). Like `bd dolt stop`, that restart
commits any uncommitted changes in every database the server serves and
briefly interrupts them, which for the shared server includes other
workspaces' databases. They do not restart any other server, nor bd's own
while auto-start is off (`dolt.auto-start: false`,
`BEADS_DOLT_AUTO_START=0`) or, outside shared-server mode, while
`BEADS_DOLT_SERVER_MODE=1` or `dolt_server_port` declares it externally
managed. reset-data then exits non-zero once the reset is done, naming the
setting for bd's own server, and `--json` sets `server_restart_required`:
restart that server after reset-data returns, before the next push or pull
of that remote, and do not run reset-data again.

**Step 5:** Verify, then re-clone everywhere else. On this machine:

```bash
bd doctor
bd list -n 5
```

Every other clone of this database must be re-created from the squashed
remote. Old clones must not pull: the two chains still share the root, so a
pull can "succeed" as a cross-merge that re-anchors the entire old history —
and a later push resurrects the bloat on the squashed remote.

Treat sync jobs and writers as separate switches. Re-enable each machine's
sync job only after *that machine* has re-cloned — verified, not assumed —
and unfence writers last; unfencing writers must never implicitly restart a
peer's sync job. One peer syncing from its old clone re-uploads the entire
pre-squash store to the fresh remote.

The first push from a re-cloned machine can be far larger than a routine
sync. If that machine's scheduled sync job enforces a timeout, the capped
push can die mid-upload on every tick, orphaning partial uploads on the
remote — run one manual `bd dolt push` (no timeout) right after re-cloning
and hand back to the schedule once it completes.

## Prevention

High-frequency coordination state lives in unversioned tables precisely so
routine agent traffic does not mint history — claim leases and
[wisps](/workflows/wisps) —
so bloat at this scale usually means something is writing versioned tables
in a tight loop. Find and fix that writer, watch data-directory growth over time, and
run `dolt gc` periodically so unreachable garbage never accumulates on top
of reachable history — with an occasional `dolt gc --full`, since a default
pass is generational and never revisits data an earlier pass moved to the old
generation.
