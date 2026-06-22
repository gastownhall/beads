---
id: attachment
title: bd attachment
slug: /cli-reference/attachment
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc attachment`

## bd attachment

Manage issue file attachments

```
bd attachment
```

**Aliases:** attachments

### bd attachment add

Attach a file to an issue

```
bd attachment add [issue-id] [path]
```

### bd attachment copy

Copy an attachment out to a file or directory

```
bd attachment copy [issue-id] [filename-or-hash] [target] [flags]
```

**Flags:**

```
  -f, --force   Overwrite the target file if it exists
```

### bd attachment fsck

Find unreachable attachment files

```
bd attachment fsck
```

### bd attachment list

List attachments for an issue

```
bd attachment list [issue-id]
```

### bd attachment prune

Remove unreachable attachment files

```
bd attachment prune [flags]
```

**Flags:**

```
  -n, --dry-run   Show unreachable files without removing them
```

### bd attachment remove

Remove an attachment from an issue

```
bd attachment remove [issue-id] [filename-or-hash]
```

**Aliases:** rm
