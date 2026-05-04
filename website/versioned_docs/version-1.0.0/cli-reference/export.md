---
id: export
title: bd export
slug: /cli-reference/export
sidebar_position: 220
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc export` (bd version 1.0.3)

## bd export

Export all issues to JSONL (newline-delimited JSON) format.

Each line is a complete JSON object representing one issue, including its
labels, dependencies, and comments.

This command is for issue export, migration, and interoperability. It does
not produce the JSONL backup snapshot used by 'bd backup restore'. For
supported backup/restore flows, use 'bd backup', 'bd backup export-git',
and 'bd backup restore'.

By default, exports only regular issues (excluding infrastructure beads
like agents, rigs, roles, and messages). Use --all to include everything.

Memories (from 'bd remember') are excluded by default because they may
contain sensitive agent context. Use --include-memories or --all to
include them.

EXAMPLES:
  bd export                              # Export issues to stdout
  bd export -o backup.jsonl              # Export to file
  bd export --include-memories           # Export issues + memories
  bd export --all -o full.jsonl          # Include infra + templates + gates + memories
  bd export --scrub -o clean.jsonl       # Exclude test/pollution records

```
bd export [flags]
```

**Flags:**

```
      --all                Include all records (infra, templates, gates, memories)
      --include-infra      Include infrastructure beads (agents, rigs, roles, messages)
      --include-memories   Include persistent memories (from 'bd remember') in the export
  -o, --output string      Output file path (default: stdout)
      --scrub              Exclude test/pollution records
```
