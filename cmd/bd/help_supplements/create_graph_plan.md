**Graph plan schema (`--graph`):**

`bd create --graph plan.json` creates a whole issue graph atomically from a
JSON plan. Node field names follow the issue model's JSON tags (the same
names `bd show --json` emits), and every field single-issue `bd create` can
set is addressable from a plan — plus an initial `status` and `pinned`,
which `bd create` has no flags for:

```json
{
  "commit_message": "optional Dolt commit message",
  "nodes": [
    {
      "key": "api",
      "title": "Design the API",
      "type": "task",
      "description": "…", "design": "…", "acceptance_criteria": "…", "notes": "…",
      "status": "in_progress",
      "priority": 1,
      "assignee": "alice", "assign_after_create": false,
      "owner": "alice@example.com",
      "labels": ["backend"],
      "estimated_minutes": 45,
      "due_at": "2030-01-02T15:04:05Z",
      "defer_until": "2030-01-01T00:00:00Z",
      "spec_id": "bd-spec1", "external_ref": "gh-42",
      "metadata": {"any": "json", "count": 3},
      "metadata_refs": {"tracker": "impl"},
      "id": "bd-a1b2c3",
      "ephemeral": false, "no_history": false,
      "wisp_type": "heartbeat", "mol_type": "swarm",
      "pinned": false
    },
    { "key": "impl", "title": "Implement it", "parent_key": "api" },
    { "key": "gate", "title": "Fanout gate" },
    { "key": "docs", "title": "Document it", "priority": "P3",
      "deps": [{ "target": "impl" }] },
    { "key": "launch", "title": "Launch announced", "type": "event",
      "event_kind": "agent.started", "actor": "agent://a", "target": "bead://b", "payload": "{}" }
  ],
  "edges": [
    { "from_key": "gate", "to_key": "api", "type": "blocks" },
    { "from_key": "gate", "to_key": "impl", "type": "waits-for",
      "gate": "any-children", "spawner_key": "impl" }
  ]
}
```

Node notes:

- `key` is a plan-local symbolic name; the result maps each key to the minted
  issue ID. `id` optionally pins an explicit issue ID instead (must match the
  database prefix, like `bd create --id`, and must not already exist — a plan
  never overwrites an existing issue).
- `parent_key` (a plan node key) or `parent_id` (an existing issue ID) adds a
  `parent-child` dependency on the created node.
- `parent` and `estimate` are accepted aliases for `parent_key` and
  `estimated_minutes`; the canonical field wins when both spellings are set.
- `priority` takes the same forms as `--priority`: an integer `0`-`4`, or
  a string such as `"1"` or `"P1"` (any case). Words like `"high"` are
  refused. A node without one gets the default, P2.
- `deps` lists this node's own dependencies: each entry has a `target` (a
  plan node key, or else an existing issue ID) and an optional `type`
  (default `blocks`). This node is the dependent. Dependencies go in `deps`
  or in `edges`: node fields such as `blockedBy` or `depends_on` are not
  part of the schema and are dropped with a warning.
- `status` sets the initial status (default `open`, or `deferred` when
  `defer_until` is in the future). Nodes created `closed` get `closed_at`
  auto-filled. Timestamps are RFC3339; relative forms like `+6h` are not
  accepted in plans.
- `ephemeral` / `no_history` override the plan-wide `--ephemeral` /
  `--no-history` flags per node (mutually exclusive per node; in
  proxied-server mode the effective storage class must be uniform across the
  plan).
- `event_kind`, `actor`, `target`, and `payload` require `"type": "event"`.
- `metadata` accepts arbitrary JSON values; `metadata_refs` values name other
  node keys and are replaced with their minted IDs after creation.
- Unlike `bd create --parent`, graph children do not inherit parent labels
  and receive flat (non-hierarchical) IDs; plans are explicit artifacts.

Edge notes:

- `type` defaults to `blocks`. `parent_key`/`parent_id` on nodes already
  create `parent-child` edges; don't repeat them in `edges`.
- The same type rules apply to edges and node `deps`. The aliases
  `"blocked-by"` and `"depends-on"` are stored as `blocks` with the
  direction unchanged, as with `bd dep add --type`. A respelling of a
  known type or alias (`"blocked_by"`, `"Blocks"`, `"waits_for"`) is
  refused with the spelling bd recognizes. Other custom types are stored
  verbatim and do not gate `bd ready`.
- `gate` (`all-children` | `any-children`) and `spawner_key`/`spawner_id`
  attach fanout-gate metadata to `waits-for` edges, mirroring
  `--waits-for`/`--waits-for-gate`. The `to` endpoint of a `waits-for` edge
  *is* the spawner (gate evaluation watches its children), so `spawner_key`
  must match `to_key` and `spawner_id` must match `to_id`; they exist to make
  plans self-documenting. Since an explicit `to_id` overrides `to_key` as the
  target, `spawner_key` cannot be combined with `to_id` — use `spawner_id`.
- Edges with ready-work-affecting types (`blocks`, `waits-for`, …) may not
  duplicate or reverse a `parent_key`/`parent_id` relationship, and a parent
  may not be connected to its own child through them.
- `thread_id` threads conversation edges (e.g. `replies-to`).

Plan notes:

- A plan applies as one batch of at most 1000 items: one per node, one per
  parent link, edge and `deps` entry, and one per `assign_after_create`
  node. A larger plan is refused; split it into several plans.
- `--dry-run` validates the plan and prints a preview without writing, but
  it is not a full rehearsal. Only the real apply checks the 1000-item cap,
  that the issue IDs the plan links to exist, cycles through node `deps`,
  and cycles or parent-child blocking paths through stored dependencies.

Comments cannot be created from a plan; use `bd comments add` after creation.
Model fields without `bd create` flags (`sender`, `work_type`, `is_template`,
`await_*`, `bonded_from`, `source_*`) are likewise not settable from plans.
