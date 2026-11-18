---
title: "bd rules"
description: "Audit and compact Claude rules"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc rules`.

Audit and compact Claude rules

```
bd rules [command]
```

## bd rules audit

Scan rules for contradictions and merge opportunities

```
bd rules audit [flags]
```

**Flags:**

```
      --path string       Path to rules directory (default ".claude/rules/")
      --threshold float   Jaccard similarity threshold (default 0.6)
```

## bd rules compact

Merge related rules into composites.

A composite keeps only the **Do:** and **Don't:** lines of its sources, plus a
"Source rules:" line naming them. Any other prose is not carried over: the
preview lists every dropped line by source. A rule with no **Do:** or **Don't:**
lines is skipped by --auto and refused by --group.

Without --force, compact only previews: it prints the composite, the dropped
lines, and the files it would back up and delete, and writes nothing. --force
is the consent to apply. It writes the composite, copies each source to
&lt;name&gt;.md.backup, and only then deletes the sources. Backups are not rules,
so audit ignores them.

Compact refuses a group whose composite or any backup path already exists,
including one of its own sources; nothing is written for that group. If a
backup or delete fails, the error names what was written, backed up and
deleted, and every backup is kept.

To undo a merge, delete the composite, then rename each &lt;name&gt;.md.backup back
to &lt;name&gt;.md (a source that was not deleted still has its original name).

Examples:
  bd rules compact --auto                    # Preview the audit's suggested merges
  bd rules compact --auto --force            # Apply them
  bd rules compact --group alpha,beta --force

```
bd rules compact [flags]
```

**Flags:**

```
      --auto            Use audit suggestions to select merge groups
      --dry-run         Preview without applying (the default without --force)
      --force           Write the composite and delete the source files (without this flag, shows preview)
      --group strings   Rule names to merge
      --path string     Path to rules directory (default ".claude/rules/")
```
