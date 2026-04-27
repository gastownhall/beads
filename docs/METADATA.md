# Issue Metadata

The `metadata` field on issues accepts arbitrary JSON. Any valid JSON value is stored as-is.

## Agent Execution Metadata

Automation may store execution hints in issue metadata so agents can make
routing decisions without parsing prose. Agents enacting an issue should read
metadata first, then use description and notes for scope and rationale:

```bash
bd show <id> --json | jq '.[0] | {id,title,metadata,description,notes}'
```

Current execution hint keys:

| Key | Meaning |
|-----|---------|
| `execution_agent_type` | Suggested worker class, such as `explorer`, `worker`, or `mixed`. |
| `execution_suggested_model` | Suggested model for the parent agent or spawned subagent. |
| `execution_reasoning_effort` | Suggested reasoning effort, such as `low`, `medium`, `high`, or `xhigh`. |
| `execution_mode` | Whether work should be local, delegated, or staged between delegated and local execution. |
| `execution_parallel_group` | Grouping hint for work that can run alongside related tasks. |

These keys are advisory metadata, but when present they take precedence over
free-form notes for execution routing. Notes remain useful for rationale,
ownership, and exact prompts.

Parent/orchestrator agents must consume these keys before spawning subagents.
Model and reasoning effort are normally fixed at launch, so reading metadata
after delegation is too late.

Do not add a first-class helper such as `bd show <id> --execution` or
`bd plan <id> --json` yet. Keep using the JSON/JQ snippet until upstream
issue gh-3541 determines whether schedulers or runners need these fields as a
stable CLI surface.

## Reserved Key Prefixes

| Prefix | Reserved For |
|--------|------------|
| `bd:` | Beads internal use |
| `_` | Internal/private keys |

Avoid these prefixes in user-defined keys to prevent conflicts with future Beads features.

## Related

- [#1416](https://github.com/gastownhall/beads/issues/1416) — Optional schema enforcement (future)
