---
id: rules
title: bd rules
slug: /cli-reference/rules
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc rules` (bd version 1.0.3)

## bd rules

Audit and compact Claude rules

```
bd rules
```

### bd rules audit

Scan rules for contradictions and merge opportunities

```
bd rules audit [flags]
```

**Flags:**

```
      --path string       Path to rules directory (default ".claude/rules/")
      --threshold float   Jaccard similarity threshold (default 0.6)
```

### bd rules compact

Merge related rules into composites

```
bd rules compact [flags]
```

**Flags:**

```
      --auto            Apply audit suggestions
      --dry-run         Preview without applying
      --group strings   Rule names to merge
      --path string     Path to rules directory (default ".claude/rules/")
```
