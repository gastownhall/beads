---
id: backup
title: bd backup
slug: /cli-reference/backup
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc backup`

## bd backup

Back up your beads database for off-machine recovery.

Commands:
  bd backup init &lt;path&gt;    Set up a backup destination (filesystem or DoltHub)
  bd backup sync           Push to configured backup destination
  bd backup restore [path] Restore from a backup directory
  bd backup remove         Remove backup destination
  bd backup status         Show backup status

DoltHub is recommended for cloud backup:
  bd backup init https://doltremoteapi.dolthub.com/&lt;user&gt;/&lt;repo&gt;
  Set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD for authentication.

```
bd backup
```

### bd backup init

Configure a filesystem path or URL as a backup destination.

The path can be a local directory (external drive, NAS, Dropbox folder) or a
DoltHub remote URL. If the destination was previously configured, it is
updated to the new path.

Filesystem examples:
  bd backup add /mnt/usb/beads-backup
  bd backup add ~/Dropbox/beads-backup

DoltHub (recommended for cloud backup):
  bd backup add https://doltremoteapi.dolthub.com/myuser/beads-backup

After adding, run 'bd backup sync' to push your data.

```
bd backup init <path>
```

**Aliases:** add

### bd backup remove

Remove the configured backup destination.

This unregisters the backup remote from Dolt and removes the local
backup configuration. The backup data at the destination is not deleted.

```
bd backup remove
```

**Aliases:** rm

### bd backup restore

Restore the beads database from a Dolt-native backup.

By default, reads from .beads/backup/ (or the configured backup directory).
Optionally specify a path to a directory containing a Dolt backup.

Use --force to overwrite an existing database with the backup contents.

The database must already be initialized (run 'bd init' first if needed).
To initialize and restore in one step, use: bd init &amp;&amp; bd backup restore

```
bd backup restore [path] [flags]
```

**Flags:**

```
      --force   Overwrite existing database with backup contents
```

### bd backup status

Show last backup status

```
bd backup status
```

### bd backup sync

Sync the current beads database to the configured Dolt backup destination.

This pushes the entire database state (all branches, full history) to the
backup location configured with 'bd backup init'.

The backup is atomic — if the sync fails, the previous backup state is preserved.

Run 'bd backup init &lt;path&gt;' first to configure a destination.

```
bd backup sync
```
