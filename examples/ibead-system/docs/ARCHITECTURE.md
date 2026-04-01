# ibead Architecture

## Design Goals

1. **No second system** — ibeads live in the same beads Dolt store using existing
   fields (`parent`, `labels`, `metadata`). Zero schema migration.
2. **Evidence-based, not speculative** — ibeads are generated from GitNexus impact
   analysis, not manual brainstorming.
3. **Two-pass accuracy** — Pass 1 provides early warning approximations at task-creation
   time. Pass 2 provides accurate blast radius after code lands.
4. **Mandatory audit gate** — parent beads cannot close until all ibeads are audited
   and closed. This is enforced programmatically, not by convention.
5. **Graceful degradation** — if GitNexus is unavailable, keyword-based domain
   extraction provides a lower-fidelity fallback. The workflow continues.

---

## Key Design Decisions

### ibeads ≠ sub-tasks

This distinction was deliberate. Sub-tasks imply the parent *delegated* work to them.
ibeads imply the parent *caused impact* on them. An ibead is not "implement auth tests"
— it is "the ci-coverage domain was affected by the auth change, audit it."

The consequence: ibeads don't add to the implementation scope. They add to the
*audit scope*. Closing an ibead may require zero code changes if the domain is
unaffected — that's a valid and expected outcome.

### Depth-1 by default, no recursive ibead graphs

ibeads are generated at depth-1 (direct callers and importers) only. Deeper analysis
is available via `bead:deepresearch:N` but is opt-in. This prevents:

- Exponential bead inflation from transitive impact chains
- Agents spending more time managing ibeads than doing the work
- False positives from deep transitive dependencies with no practical coupling

The depth-1 constraint was derived from empirical observation: d=1 impacts WILL BREAK,
d=2 LIKELY AFFECTED, d=3+ MAY NEED TESTING. Beads are for d=1 only; d=2+ is judgment.

### Same bd store, no new schema

ibeads are standard beads with:
- `labels: ["type:ibead"]` — for filtering
- `--parent <id>` — for hierarchy (already first-class in beads)
- `metadata: { ibead: true, domain: "...", task_list: [] }` — for ibead-specific data

This means existing beads tooling (`bd children`, `bd query`, `bd list`) works with
ibeads without modification. The ibead system is purely additive.

### Pass 1 approximation is acceptable

Pass 1 ibeads are generated from the bead *title* — not the code. They're directional,
not authoritative. The rationale: early warning has value even when imprecise.

An agent knowing "this task will likely touch the auth-layer, ci-coverage, and
api-contracts domains" from bead creation time can make better architectural decisions
during implementation. Pass 2 then corrects the approximation with evidence.

### codex:rescue as a mandatory pre-close step

`ibead-close.mjs` surfaces a codex:rescue reminder before every close. This is
intentional and non-bypassable (without `--skip-quality`). The reasoning:

codex:rescue provides a second-opinion AI review pass that catches work the primary
agent missed — incomplete error paths, hardening opportunities, edge cases. By making
it mandatory at the close gate (for both beads and ibeads), the system enforces
two-pass review on all completed work, not just flagged work.

---

## Data Flow

```
User/Agent creates bead
        │
        ▼
ibead-create.mjs
  1. bd show <parent> --json          → get bead title
  2. extract terms from title          → stop-word filter, 6-term max
  3. npx gitnexus query "<terms>"     → execution flow results
  4. parse domains from output         → process names + file clusters
  5. fallback if < 3 domains           → keyword map from bead title
  6. bd create (ibead) × 3–5          → child beads with type:ibead label
  7. write Task.md                     → indented ibead lines under parent
        │
        ▼
[implementation by agent]
        │
        ▼
bd update <parent> --status implementation-complete
        │
        ▼
ibead-audit.mjs
  1. bd show <parent> --json          → get parent title
  2. bd children <parent> --json      → get all ibeads
  3. git diff origin/main...HEAD      → changed files
  4. extract symbols from changed files → export declarations
  5. npx gitnexus impact <sym> upstream → blast radius per symbol
  6. parse task_list from impact output → investigation items
  7. bd update <ibead> --metadata     → task_list stored per ibead
  8. write Task.md sub-bullets        → investigation items under ibead
        │
        ▼
[agent audits each ibead domain]
        │
        ▼
ibead-close.mjs (per ibead)
  1. bd show <ibead> --json           → verify it's an ibead
  2. surface codex:rescue reminder    → mandatory prompt
  3. npm run lint                     → must pass
  4. npm run type-check               → must pass
  5. bd close <ibead>                 → if all pass
        │
        ▼
ibead-close.mjs (parent bead)
  1. bd children <parent> --json      → check all ibeads closed
  2. block if any open                → list open ibeads, exit 1
  3. surface codex:rescue reminder    → mandatory prompt
  4. npm run lint                     → must pass
  5. npm run type-check               → must pass
  6. bd close <parent>                → if all pass
```

---

## Error Handling and Fallbacks

| Failure | Behavior |
|---|---|
| GitNexus unavailable (Pass 1) | Falls back to keyword-based domain map from bead title |
| GitNexus returns no domains (Pass 1) | Falls back + always adds `ci-coverage` + `api-contracts` as baseline |
| GitNexus unavailable (Pass 2) | task_list populated with generic "investigate <domain>" items |
| `bd show` returns array | Scripts unwrap first element; bd returns `[{...}]` format |
| bd prepends warning text before JSON | `parseJson` uses regex to extract first `{...}` or `[...]` block |
| Changed files contain no extractable symbols | Falls back to domain-name matching against ibead domain labels |
| Lint fails at close gate | Gate blocks, prints first 10 error lines, exits 1 |
| Type-check fails at close gate | Gate blocks, prints first 10 type errors, exits 1 |
| Open ibeads at parent close | Gate blocks, lists all open ibead IDs and titles, exits 1 |

All scripts have 30-second GitNexus timeouts and 120-second npm script timeouts.

---

## bd API Surface Used

The ibead system uses only these `bd` commands:

| Command | Purpose |
|---|---|
| `bd show <id> --json` | Fetch bead details (returns array) |
| `bd children <id> --json` | List child ibeads |
| `bd create <title> --parent <id> --label <l> --metadata <json> --json` | Create ibead |
| `bd update <id> --metadata <json>` | Update ibead task_list |
| `bd close <id>` | Close bead (after gate passes) |
| `bd list --status=open --json` | Batch query for ibead:research |
| `bd list --status=in_progress --json` | Batch query for ibead:research |

No beads internals are accessed. No private APIs. The ibead system is a pure client
built on the public `bd` CLI interface.

---

## Relationship to bd Molecules/Formulas

`formulas/ibead.yaml` encodes the ibead lifecycle as a beads formula. This enables:

```bash
bd formula show ibead           # view the workflow steps
bd mol pour ibead               # instantiate as a molecule
```

The formula is declarative documentation of the workflow — it does not replace the
scripts, but provides a structured representation of the lifecycle that integrates
with bd's native workflow tooling.

---

## Future Extensions

These were explicitly out of scope for v1 but are natural extensions:

**Bead graph visualization** (`bead:graph`): render the ibead dependency graph as a
visual to understand cross-bead impact topology. Would require something like `bd graph`
with ibead filtering.

**Deep research (`bead:deepresearch:N`)**: generate ibeads at depth N. Currently
depth-1 only. Deep research would recursively run GitNexus impact at each hop level,
generating ibeads-for-ibeads up to N levels. Opt-in only.

**Ibead auto-close on evidence**: if Pass 2 produces an empty task_list for an ibead
(no symbols in that domain were impacted), the ibead could auto-close with a note
"domain unaffected by diff." Reduces manual close overhead for clean changes.

**`ibead:watch` live mode**: re-evaluate ibeads on each commit during implementation,
giving real-time feedback on blast radius drift as the branch evolves.
