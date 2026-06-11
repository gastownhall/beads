# Attachment Storage Policy

Attachment support uses a split storage model:

- Attachment metadata is stored in the Dolt database.
- Attachment bytes are stored as plain files under `.beads/attachments/<BEADID>/<sha256>`.
- Attachment files are local-only in the MVP. Dolt sync, Dolt backups, and JSONL exports do not copy the plain files in `.beads/attachments`.

This keeps large binary payloads out of Dolt history while still making the
relationship between a bead and its attachments versioned metadata.

## What Syncs

`bd dolt push`, `bd dolt pull`, and Dolt-native `bd backup` move the database
state. For attachments, that means they carry attachment metadata rows such as
the bead ID, content hash, original filename, MIME type, size, and storage path.

They do not move the file bytes under `.beads/attachments`. After a metadata-only
sync or restore on another machine, an attachment can be listed even when the
local file is absent. `bd show` and `bd attachment list` report those entries as
missing instead of hiding them or failing the whole command.

`bd export` and `.beads/issues.jsonl` remain issue-table exports. They are not a
restorable database backup and they do not include attachment bytes.

## Backing Up Bytes

If attachment bytes matter, back up `.beads/attachments` with your repository or
machine backup tooling in addition to using `bd dolt push` or `bd backup sync`
for the Dolt database.

For example, a complete project backup needs both:

```sh
bd backup sync
rsync -a .beads/attachments/ /backup/beads-attachments/
```

Restore the Dolt database first, then restore `.beads/attachments` into the
same `.beads` directory. Run `bd attachment fsck` afterward to find files that
are present on disk but no longer referenced by any bead, and `bd attachment
prune` to remove those unreachable files when the report looks correct.

## Git and Git LFS

Git does not automatically track `.beads/attachments`. Many repositories ignore
`.beads/*`, including this repository. If a team wants attachment bytes in git,
that must be an explicit project policy.

For Git LFS, opt in by tracking the attachment path deliberately:

```gitattributes
.beads/attachments/** filter=lfs diff=lfs merge=lfs -text
```

The repository also needs matching `.gitignore` exceptions for `.beads/attachments`
before git can see those files. This is intentionally not enabled by `bd init`
because it changes repository size, clone behavior, and LFS hosting requirements.
