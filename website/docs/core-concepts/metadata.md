---
id: metadata
title: Issue Metadata
slug: /core-concepts/metadata
---

# Issue Metadata

The `metadata` field on issues accepts arbitrary JSON. Any valid JSON value is stored as-is.

Metadata is the preferred extension point for data that is specific to an
integration, orchestrator, team workflow, or experimental automation. Before
adding first-class fields, commands, or schema changes, check the
[Project Charter](https://github.com/gastownhall/beads/blob/main/docs/PROJECT_CHARTER.md#schema-boundary).

## Example: Agent Execution Metadata

Agent execution hints are one example of using metadata to extend beads without
adding new native database fields. Automation may store these hints so agents
can make routing decisions without parsing prose. Agents enacting an issue
should read metadata first, then use description and notes for scope and
rationale:

```bash
bd show <id> --json | jq '.[0] | {id,title,metadata,description,notes}'
```

The current convention for execution hint keys is:

| Key | Meaning |
|-----|---------|
| `execution_agent_type` | Suggested worker class, such as `explorer`, `worker`, or `mixed`. |
| `execution_suggested_model` | Suggested model for the parent agent or spawned subagent. |
| `execution_reasoning_effort` | Suggested reasoning effort, such as `low`, `medium`, `high`, or `xhigh`. |
| `execution_mode` | Whether work should be local, delegated, or staged between delegated and local execution. |
| `execution_parallel_group` | Grouping hint for work that can run alongside related tasks. |

These keys are advisory metadata, not core issue fields. When a workflow uses
them, they take precedence over free-form notes for execution routing. Notes
remain useful for rationale, ownership, and exact prompts.

Parent/orchestrator agents must consume these keys before spawning subagents.
Model and reasoning effort are normally fixed at launch, so reading metadata
after delegation is too late.

Do not add a first-class helper such as `bd show <id> --execution` or
`bd plan <id> --json` yet. Keep using the JSON/JQ snippet until upstream
issue gh-3541 determines whether schedulers or runners need these fields as a
stable CLI surface.

## Example: Verification Metadata

Local verification queues are another example of metadata as an extension
point. They may store slow-suite state in issue metadata. These keys describe
the validation gate for a specific candidate commit:

| Key | Meaning |
|-----|---------|
| `verify_state` | Verification state: `queued`, `running`, `passed`, or `failed`. |
| `verify_head` | Commit SHA that the verification result applies to. |
| `verify_branch` | Branch name recorded when verification was queued. |
| `verify_cmd` | Command run in the clean verification worktree. |
| `verify_log` | Local path to the verifier log. |
| `verify_result` | Compact result, such as `exit=0` or `exit=1`. |
| `verify_enqueued_at` | UTC timestamp when the verification was queued. |
| `verify_started_at` | UTC timestamp when the verification started. |
| `verify_finished_at` | UTC timestamp when the verification finished. |
| `verify_worktree` | Source or clean verification worktree path. |
| `verify_runner` | Local verifier identity, usually `host:pid`. |

Treat `verify_head` as the source of truth. A passing result gates that exact
commit only; if the branch moves, enqueue verification again.

## Reserved Key Prefixes

| Prefix | Reserved For |
|--------|------------|
| `bd:` | Beads internal use |
| `_` | Internal/private keys |

Avoid these prefixes in user-defined keys to prevent conflicts with future Beads features.

## Related

- [Project Charter](https://github.com/gastownhall/beads/blob/main/docs/PROJECT_CHARTER.md) - Product scope and schema boundary
- [#1416](https://github.com/gastownhall/beads/issues/1416) - Optional schema enforcement (future)
