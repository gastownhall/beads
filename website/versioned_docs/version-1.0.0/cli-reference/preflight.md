---
id: preflight
title: bd preflight
slug: /cli-reference/preflight
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc preflight`

## bd preflight

Display a checklist of common pre-PR checks for contributors.

This command helps catch common issues before pushing to CI:
- Tests not run locally
- Lint errors
- Unformatted Go files
- .beads/issues.jsonl pollution
- Stale nix vendorHash
- Version mismatches

Examples:
  bd preflight              # Show checklist
  bd preflight --check      # Run checks automatically
  bd preflight --check --json  # JSON output for programmatic use
  bd preflight --check --skip-lint  # Explicitly skip lint check


```
bd preflight [flags]
```

**Flags:**

```
      --check       Run checks automatically
      --fix         Auto-fix issues where possible (not yet implemented)
      --json        Output results as JSON
      --skip-lint   Skip lint check explicitly
```
