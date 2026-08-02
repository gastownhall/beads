---
title: Reference
description: Lookup material — configuration, git integration, JSON contracts, observability, troubleshooting, and the FAQ.
---

Lookup material for beads: the pages here specify contracts and edge cases
rather than teach concepts (that's [How Beads Works](/core-concepts/index)).

## Pages in this section

- [Configuration](/reference/configuration) — every config key, environment
  variable, default, and the precedence between them.
- [Git Integration](/reference/git-integration) — hooks, `refs/dolt/data`,
  role detection, and branchless (jujutsu) workflows.
- [Git Worktrees Guide](/reference/worktrees) — how beads behaves across
  worktrees sharing one repository.
- [Protected Branches](/reference/protected-branches) — sync-branch mode for
  repos where direct pushes to main are blocked.
- [Advanced Features](/reference/advanced) — rename, merge, compaction, and
  other power-user operations.
- [JSON Output Schema Contract](/reference/json-schema) — the stability
  contract behind every `--json` flag.
- [Observability (OpenTelemetry)](/reference/observability) — traces and
  metrics bd can emit, and how to point them at a collector.
- [Troubleshooting](/reference/troubleshooting) — symptom-first fixes for
  common failures (deeper runbooks live in [Recovery](/recovery/index)).
- [Antivirus False Positives](/reference/antivirus) — Windows AV heuristics
  and binary verification.
- [FAQ](/reference/faq) — beads vs other trackers, and the questions
  everyone asks in week one.
- [CLI Reference](/cli-reference/index) — the generated page-per-command
  reference, nested below as a collapsible group.
