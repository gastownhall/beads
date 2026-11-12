**Graph plan schema (`--graph`):**

`bd create --graph plan.json` creates a whole issue graph atomically from a
JSON plan. Node field names follow the issue model's JSON tags (the same
names `bd show --json` emits), and every field single-issue `bd create` can
set is addressable from a plan — plus an initial `status` and `pinned`,
which `bd create` has no flags for.

**Edge direction rule:** an edge reads as `bd dep add <from_key> <to_key> --type <type>` —
`from_key` names the *dependent* and `to_key` the dependency it waits on.
`{"from_key": "X", "to_key": "Y", "type": "blocks"}` means X depends on Y
(Y blocks X), exactly like `bd dep add X Y`. `type` defaults to `blocks`.

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
    { "key": "launch", "title": "Launch announced", "type": "event",
      "event_kind": "agent.started", "actor": "agent://a", "target": "bead://b", "payload": "{}",
      "deps": [{"target": "impl", "type": "blocks"}] }
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
- `deps` lists this node's own outgoing dependencies:
  `{"deps": [{"target": "impl", "type": "blocks"}]}` means this node waits on
  `impl` (the same direction rule as an edge: the carrying node is the
  dependent). `target` resolves as a plan key first, then as an existing
  issue ID. There is no `blockedBy`/`dependencies` node field — plans with
  such fields get a warning pointing here.
- `priority` accepts an integer `0`-`4` or the same string forms as the
  `--priority` flag: `"1"`, `"P1"`, `"p1"`. Omitted (or `null`) asks for the
  role's default (P2).
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
- `type` accepts the well-known dependency types plus the documented aliases
  `depends-on` and `blocked-by`, both stored as `blocks` with the direction
  unchanged (like `bd dep add --type`). A type that only respells one of
  those (`blocked_by`, `depends_on`, `Blocks`, …) is refused with the exact
  spelling to use. Any other type (`related-to`, `implements`, …) is stored
  verbatim as a custom type and does not gate `bd ready`.
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

The whole plan applies as ONE atomic transaction (all-or-nothing): a plan is
refused outright if it exceeds the 1000-item batch cap (nodes, edges,
parent links and deferred assignments combined) — split it into multiple
`bd create --graph` calls, each its own transaction. `--dry-run` parses,
warns, and validates the plan without writing anything and prints a preview.

Comments cannot be created from a plan; use `bd comments add` after creation.
Model fields without `bd create` flags (`sender`, `work_type`, `is_template`,
`await_*`, `bonded_from`, `source_*`) are likewise not settable from plans.
