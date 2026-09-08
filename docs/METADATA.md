# Issue Metadata

The `metadata` field on issues accepts arbitrary JSON. Any valid JSON value is stored as-is.

Metadata is the preferred extension point for data that is specific to an
integration, orchestrator, team workflow, or experimental automation. Before
adding first-class fields, commands, or schema changes, check the
[Project Charter](PROJECT_CHARTER.md#schema-boundary).

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

Model and effort values are portable hints, not runtime bindings. The
`execution_reasoning_effort` values above are a canonical advisory scale:
writers should store canonical values rather than runtime-local ones, and a
consumer whose runtime uses a different scale should map the stored value to
its nearest native level instead of dropping the hint. Likewise,
`execution_suggested_model` is a capability-tier suggestion: a consumer on a
different provider should substitute a model of the same tier rather than
ignore the hint.

Parent/orchestrator agents must consume these keys before spawning subagents.
Model and reasoning effort are normally fixed at launch, so reading metadata
after delegation is too late.

Do not add a first-class helper such as `bd show <id> --execution` or
`bd plan <id> --json`. Issue gh-3541 resolved to keep execution hints as
metadata only; the JSON/JQ snippet remains the supported access path.

## Example: Tracker Round-Trip Metadata

Tracker integrations map external issues into beads fields such as title,
status, priority, type, labels, dependencies, and `external_ref`. When an
integration needs to preserve tracker-specific fields that do not belong in the
native beads schema, it can store those fields in issue metadata:

```json
{
  "example_tracker": {
    "board_id": "ENG",
    "sprint_id": 42,
    "remote_type": "story"
  }
}
```

This keeps beads' core issue model stable while allowing the integration to
round-trip fields it understands. Prefer namespaced keys and keep
tracker-specific policy in the integration. If a value becomes broadly useful
to beads itself, revisit whether it deserves a native field.

## Reserved Key Prefixes

| Prefix | Reserved For |
|--------|------------|
| `bd:` | Beads internal use |
| `_` | Internal/private keys |

Avoid these prefixes in user-defined keys to prevent conflicts with future Beads features.

## Related

- [Project Charter](PROJECT_CHARTER.md) - Product scope and schema boundary
- [#1416](https://github.com/gastownhall/beads/issues/1416) - Optional schema enforcement (future)
