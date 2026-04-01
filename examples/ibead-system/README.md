# ibead — Interconnected Bead System

> **Impact-aware task tracking for AI agents using GitNexus blast-radius analysis**

An **ibead** (interconnected bead) is a domain-scoped collateral responsibility marker
automatically generated when a parent bead is created. ibeads make blast radius a
first-class tracked artifact — forcing agents to audit adjacent code domains before
closing a task, not as an afterthought.

---

## The Problem ibeads Solve

Without ibeads, beads track *what to do* but not *what else breaks*. An agent closes
a bead after implementing a feature, but silently leaves behind:

- Broken callers in adjacent services
- Desynchronized API contracts
- CI coverage that doesn't reach the changed surface
- Domain layers that need synchronized updates

ibeads convert GitNexus impact analysis from ad-hoc tooling into tracked, mandatory
work items with a close gate.

---

## Core Concept

```
ibeads ≠ sub-tasks
ibeads = "what will be damaged by this work"
```

A sub-task decomposes the work. An ibead says *this adjacent domain will be affected*.
You don't implement ibeads — you audit them.

---

## Architecture

```
bd create "task title"
      │
      ▼ (automatic)
ibead-create.mjs  ←─── GitNexus Pass 1: query bead title terms
      │                 Finds: 3–5 impacted domains (depth-1)
      │
      ▼ (ibeads written to bd store + Task.md)
      │
      │   [ implementation happens here ]
      │
bd update <id> --status implementation-complete
      │
      ▼ (automatic)
ibead-audit.mjs   ←─── GitNexus Pass 2: impact analysis on actual changed files
      │                 Populates: task_list per ibead
      │                 Updates: Task.md sub-bullets
      │
      ▼ (agent audits each ibead domain)
      │
for each ibead:
    codex:rescue → fix task_list items → lint → type-check
    ibead-close.mjs ──── close gate enforcer
      │
      ▼ (all ibeads closed)
      │
ibead-close.mjs ─────── parent close gate
      │   Checks: all child ibeads closed
      │   Runs: codex:rescue reminder
      │   Runs: lint + type-check
      ▼
bd close <parent>
```

### Two-Pass GitNexus Integration

| Pass | Trigger | Method | Purpose |
|---|---|---|---|
| **Pass 1** | `bd create` | `gitnexus query <title terms>` | Early warning approximation |
| **Pass 2** | `implementation-complete` | `gitnexus impact <symbol> --direction upstream` | Accurate blast radius on changed files |

Pass 1 ibeads are directional approximations created immediately — before implementation
begins — giving agents early awareness of likely blast radius. Pass 2 replaces approximation
with evidence from the actual diff.

---

## File Inventory

```
examples/ibead-system/
├── README.md                    ← this file
├── scripts/
│   ├── ibead-create.mjs         ← Pass 1: generate ibeads from bead title
│   ├── ibead-audit.mjs          ← Pass 2: re-evaluate after implementation
│   ├── ibead-close.mjs          ← close gate enforcer (beads + ibeads)
│   └── ibead-research.mjs       ← batch re-evaluation across all open beads
├── formulas/
│   └── ibead.yaml               ← bd formula template encoding the lifecycle
└── docs/
    ├── ARCHITECTURE.md          ← detailed architecture and design rationale
    └── WORKFLOW.md              ← step-by-step walkthrough with terminal output
```

### Script Responsibilities

| Script | Trigger | Key Operations |
|---|---|---|
| `ibead-create.mjs` | After `bd create` | GitNexus query → extract domains → `bd create` 3–5 child ibeads → write Task.md |
| `ibead-audit.mjs` | After `implementation-complete` | `git diff` → extract symbols → `gitnexus impact` → update ibead metadata → write Task.md sub-bullets |
| `ibead-close.mjs` | Replaces `bd close` | Check all ibeads closed (parent) → codex:rescue reminder → lint → type-check → `bd close` |
| `ibead-research.mjs` | Periodic batch | Query all open beads → re-run GitNexus → add new ibeads → flag stale → summary report |

---

## ibead Schema

ibeads use the standard beads Dolt store. No schema migration, no second system.

```jsonc
{
  "issue_type": "task",
  "labels": ["type:ibead"],
  "parent": "<parent-bead-id>",
  "title": "IB-<parent-short>-<n>: [<domain>] splash audit — <parent title>",
  "metadata": {
    "ibead": true,
    "domain": "session-layer",      // e.g. auth-layer, ci-coverage, api-contracts
    "depth": 1,
    "gitnexus_pass": 1,             // updated to 2 after re-evaluation
    "task_list": [],                // populated by Pass 2
    "parent_title": "...",
    "audited_at": "2026-04-01T..."  // set by Pass 2
  }
}
```

**Storage:** same `bd` Dolt database, `--parent` flag for hierarchy, `type:ibead` label
for filtering. `bd children <parent-id>` returns all ibeads for a given bead.

---

## Close Gate

Both parent beads and ibeads pass through the same mandatory close gate:

```
1. bd update <id> --status implementation-complete
   → triggers Pass 2 GitNexus scan (parent beads)
   → surfaces codex:rescue reminder

2. codex:rescue runs → complete all identified work

3. npm run lint && npm run type-check  ← must pass clean

4. (parent beads only) all child ibeads must be closed

5. ibead-close.mjs executes bd close <id>
```

If any step fails, the gate blocks with a diagnostic message and exits non-zero.

---

## codex:rescue Integration

`ibead-close.mjs` surfaces a mandatory codex:rescue prompt before every close operation:

```
⚡ REQUIRED: codex:rescue must be run before closing.
   Run /codex:rescue in your session if not already done.
   codex:rescue identifies work that was missed or needs hardening.
```

codex:rescue is a secondary AI review pass (via the Codex CLI) that catches missed
edge cases, incomplete error handling, and hardening opportunities. By wiring it into
the ibead close gate, no task or ibead can be closed without having gone through
this second-opinion review.

---

## GitNexus Dependencies

The ibead system depends on [GitNexus](https://github.com/gitnexus/gitnexus) for
code intelligence:

| GitNexus command | Used by | Purpose |
|---|---|---|
| `npx gitnexus query "<terms>"` | `ibead-create.mjs` | Find execution flows matching bead title terms |
| `npx gitnexus impact <symbol> --direction upstream` | `ibead-audit.mjs` | Blast radius: direct callers and importers |
| `npx gitnexus detect-changes --scope compare --base-ref main` | `ibead-audit.mjs` | Branch diff symbol inventory |

**Fallback behavior:** if GitNexus is unavailable or returns no results, the scripts
fall back to keyword-based domain extraction from the bead title. This produces
lower-fidelity ibeads but preserves the workflow structure. All GitNexus calls have
30-second timeouts and degrade gracefully.

**Index freshness:** GitNexus must be indexed for accurate Pass 2 results. If the
index is stale, run `npx gitnexus analyze` before `ibead-audit.mjs`. A `PostToolUse`
hook on `git commit` can keep the index automatically fresh.

---

## npm Scripts (add to package.json)

```json
{
  "scripts": {
    "ibead:create":   "node scripts/ibead-create.mjs",
    "ibead:audit":    "node scripts/ibead-audit.mjs",
    "ibead:close":    "node scripts/ibead-close.mjs",
    "ibead:research": "node scripts/ibead-research.mjs"
  }
}
```

---

## Depth Control

| Mode | Depth | Command |
|---|---|---|
| Default | 1 (direct callers only) | `ibead-create.mjs` |
| Deep research | N hops | `bead:deepresearch:N` (e.g. `bead:deepresearch:5`) |

ibeads never generate their own ibeads. Recursive ibead graphs are opt-in via
`deepresearch` only, preventing exponential bead inflation.

---

## `ibead:research` — Batch Re-evaluation

As other beads are closed and code shifts, the blast radius of open beads changes.
`ibead-research.mjs` performs a full re-evaluation:

1. Queries all `open` + `in_progress` beads (excluding ibeads)
2. Re-runs GitNexus for each
3. Adds new ibeads where domains have expanded
4. Flags existing ibeads where the domain is no longer in blast radius (potentially stale)
5. Outputs a structured summary report

```
ibead:research complete — 2026-04-01
  Beads scanned:              8
  Ibeads added:               4  (bead-38: +2, bead-41: +2)
  Ibeads flagged stale:       1  (IB-35-3: domain no longer in blast radius)
  Ibeads needing attention:   5
```

**Recommended cadence:** run `ibead:research` at session start when 3 or more beads
have been closed since the last pass.

---

## Quick Reference

```bash
# Task starts
bd create "title" && npm run ibead:create -- --parent <id>

# Implementation complete
bd update <id> --status implementation-complete
npm run ibead:audit -- --parent <id>

# Audit each ibead domain, then close
npm run ibead:close -- --bead <ibead-id>   # repeat per ibead

# Close parent (gate verifies all ibeads closed)
npm run ibead:close -- --bead <parent-id>

# Periodic maintenance
npm run ibead:research

# Preview without writing
npm run ibead:create  -- --parent <id> --dry-run
npm run ibead:audit   -- --parent <id> --dry-run
npm run ibead:close   -- --bead   <id> --dry-run
npm run ibead:research           --dry-run

# JSON output for scripting
npm run ibead:create -- --parent <id> --json
```

---

## Real-World Test Results

Validated against a production codebase (GGV3 / GearGrind tactical marketplace,
38K+ symbols, 70K+ relationships in GitNexus):

- Pass 1 correctly identified `[components]`, `[services]`, `[store]`, `[api]`
  as impacted domains for "ibead system integration test"
- Pass 2 (dry-run) populated 5 specific file-level investigation items per ibead
  from the branch diff
- Close gate correctly blocked parent with 4 open ibeads, printed each ibead ID
- `ibead:research` correctly scanned a single bead: 0 stale, 0 new, clean summary

---

## Related

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — Design decisions and trade-offs
- [docs/WORKFLOW.md](docs/WORKFLOW.md) — Full walkthrough with terminal output examples
- [formulas/ibead.yaml](formulas/ibead.yaml) — bd formula template
- [GitNexus](https://github.com/gitnexus/gitnexus) — Code intelligence dependency
