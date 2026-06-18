---
id: ready
title: bd ready
slug: /cli-reference/ready
sidebar_position: 30
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc ready`

## bd ready

Show ready work (open issues with no active blockers).

Excludes in_progress, blocked, deferred, and hooked issues. This uses the
GetReadyWork API which applies blocker-aware semantics to find truly claimable work.

Note: 'bd list --ready' uses the same blocker-aware ready-work semantics.

Use --mol to filter to a specific molecule's steps:
  bd ready --mol bd-patrol   # Show ready steps within molecule

Use --gated to find molecules ready for gate-resume dispatch:
  bd ready --gated           # Find molecules where a gate closed

Use --claim to atomically claim the first ready issue matching the filters:
  bd ready --claim --json

This is useful for agents executing molecules to see which steps can run next.

```
bd ready [flags]
```

**Flags:**

```
  -a, --assignee string              Filter by assignee
      --claim                        Atomically claim the first ready issue matching the filters
      --exclude-label strings        Exclude issues that have ANY of these labels
      --exclude-type strings         Exclude issue types from results (comma-separated or repeatable, e.g., --exclude-type=convoy,epic)
      --explain                      Show dependency-aware reasoning for why issues are ready or blocked
      --gated                        Find molecules ready for gate-resume dispatch
      --has-metadata-key string      Filter issues that have this metadata key set
      --include-deferred             Include issues with future defer_until timestamps
      --include-ephemeral            Include ephemeral issues (wisps) in results
  -l, --label strings                Filter by labels (AND: must have ALL). Can combine with --label-any
      --label-any strings            Filter by labels (OR: must have AT LEAST ONE). Can combine with --label
  -n, --limit int                    Maximum issues to show (use 0 for unlimited) (default 100)
      --metadata-field stringArray   Filter by metadata field (key=value, repeatable)
      --mol string                   Filter to steps within a specific molecule
      --mol-type string              Filter by molecule type: swarm, patrol, or work
      --parent string                Filter to descendants of this bead/epic
      --plain                        Display issues as a plain numbered list
      --pretty                       Display issues in a tree format with status/priority symbols (default true)
  -p, --priority int                 Filter by priority
  -s, --sort string                  Sort policy: priority (default), hybrid, oldest (default "priority")
  -t, --type string                  Filter by issue type (task, bug, feature, epic, decision, merge-request). Aliases: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision
  -u, --unassigned                   Show only unassigned issues
```
