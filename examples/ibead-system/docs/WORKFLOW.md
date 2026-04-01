# ibead Workflow — Step-by-Step

This document walks through the complete ibead lifecycle with real terminal output.

---

## Prerequisites

- `bd` CLI installed and initialized
- GitNexus indexed: `npx gitnexus analyze`
- npm scripts wired in `package.json`:
  ```json
  {
    "scripts": {
      "ibead:create":   "node examples/ibead-system/scripts/ibead-create.mjs",
      "ibead:audit":    "node examples/ibead-system/scripts/ibead-audit.mjs",
      "ibead:close":    "node examples/ibead-system/scripts/ibead-close.mjs",
      "ibead:research": "node examples/ibead-system/scripts/ibead-research.mjs"
    }
  }
  ```

---

## Step 1 — Create the Bead

```bash
bd create "Add rate limiting to auth endpoints" --json
```

Output:
```json
{
  "id": "GGV3-abc1",
  "title": "Add rate limiting to auth endpoints",
  "status": "open",
  "issue_type": "task"
}
```

---

## Step 2 — Generate ibeads (Pass 1)

```bash
npm run ibead:create -- --parent GGV3-abc1
```

Output:
```
ibead:create — Pass 1 complete for GGV3-abc1
  Parent: "Add rate limiting to auth endpoints"
  GitNexus query terms: "rate limiting auth endpoints"
  Ibeads created: 4
    GGV3-abc1.1  [session-layer]
    GGV3-abc1.2  [api-contracts]
    GGV3-abc1.3  [ci-coverage]
    GGV3-abc1.4  [routing-layer]

  ⚡ Pass 1 complete. Run ibead:audit after implementation-complete for accurate blast radius.
```

**Task.md now shows:**
```markdown
- [ ] GGV3-abc1: Add rate limiting to auth endpoints
  - [ ] GGV3-abc1.1: [domain: session-layer] splash audit
  - [ ] GGV3-abc1.2: [domain: api-contracts] splash audit
  - [ ] GGV3-abc1.3: [domain: ci-coverage] splash audit
  - [ ] GGV3-abc1.4: [domain: routing-layer] splash audit
```

> These are **approximations**. They become accurate after Pass 2.

---

## Step 3 — Implement

Do your work normally. Commit code. The ibeads are background context — they don't
block implementation.

```bash
# implement rate limiting middleware, routes, tests
git add src/middleware/rate-limit.ts src/routes/auth.ts
git commit -m "feat(auth): add rate limiting middleware"
```

---

## Step 4 — Mark Implementation Complete → Pass 2

```bash
bd update GGV3-abc1 --status implementation-complete
npm run ibead:audit -- --parent GGV3-abc1
```

Output:
```
ibead:audit — Pass 2 complete for GGV3-abc1
  Parent: "Add rate limiting to auth endpoints"
  Changed files analyzed: 7
  Symbols extracted: 4
  Ibeads updated: 4

    GGV3-abc1.1  [session-layer]
      · investigate: SessionService.validateToken — verify no breakage from parent task
      · check: src/middleware/auth.ts — review for impact
      · check: src/services/session.ts — review for impact

    GGV3-abc1.2  [api-contracts]
      · investigate: ApiErrorHandler — does it need new 429 response shape?
      · check: src/routes/auth.ts — review for impact

    GGV3-abc1.3  [ci-coverage]
      · verify: confirm ci-coverage domain unaffected by branch diff
      · investigate: rate-limit test suite — check existing coverage

    GGV3-abc1.4  [routing-layer]
      · check: src/loaders/express.ts — review for impact

  Next: for each ibead, run codex:rescue → complete task list → ibead:close
```

**Task.md now shows:**
```markdown
- [ ] GGV3-abc1: Add rate limiting to auth endpoints
  - [ ] GGV3-abc1.1: [domain: session-layer] splash audit
    - [ ] investigate: SessionService.validateToken — verify no breakage
    - [ ] check: src/middleware/auth.ts — review for impact
  - [ ] GGV3-abc1.2: [domain: api-contracts] splash audit
    - [ ] investigate: ApiErrorHandler — does it need new 429 response shape?
  - [ ] GGV3-abc1.3: [domain: ci-coverage] splash audit
    - [ ] verify: rate-limit test suite coverage
  - [ ] GGV3-abc1.4: [domain: routing-layer] splash audit
    - [ ] check: src/loaders/express.ts — review for impact
```

---

## Step 5 — Audit and Close Each ibead

For each ibead, trace the code, make any necessary fixes, then close:

```bash
npm run ibead:close -- --bead GGV3-abc1.1
```

Output (passing):
```
ibead:close — Close gate for GGV3-abc1.1
  Title: "IB-abc1-1: [session-layer] splash audit"
  Type: ibead

  ⚡ REQUIRED: codex:rescue must be run before closing.
     Run /codex:rescue in your session if not already done.
     codex:rescue identifies work that was missed or needs hardening.

  Running lint...
  ✓ Lint passed
  Running type-check...
  ✓ Type-check passed

  All gates passed. Closing GGV3-abc1.1...
  ✓ GGV3-abc1.1 closed successfully.
```

Repeat for `.2`, `.3`, `.4`.

---

## Step 6 — Close the Parent Bead

```bash
npm run ibead:close -- --bead GGV3-abc1
```

Output (all ibeads closed):
```
ibead:close — Close gate for GGV3-abc1
  Title: "Add rate limiting to auth endpoints"
  Type: parent bead

  ✓ All ibeads closed (4 total)

  ⚡ REQUIRED: codex:rescue must be run before closing.
     Run /codex:rescue in your session if not already done.

  Running lint...
  ✓ Lint passed
  Running type-check...
  ✓ Type-check passed

  All gates passed. Closing GGV3-abc1...
  ✓ GGV3-abc1 closed successfully.
```

Output (ibeads still open — gate blocks):
```
  ❌ GATE FAILED: Open ibeads must be closed first:
     · GGV3-abc1.2: [domain: api-contracts] splash audit
     · GGV3-abc1.3: [domain: ci-coverage] splash audit

  Run: npm run ibead:close -- --bead <ibead-id>  for each one first.
```

---

## Batch Re-evaluation

When multiple beads have closed and the codebase has shifted:

```bash
npm run ibead:research
```

Output:
```
ibead:research — 2026-04-01

  Beads to scan: 6

  Scanning GGV3-38... ✓ (3 ibeads, +0 new, 0 stale)
  Scanning GGV3-41... ✓ (2 ibeads, +1 new, 0 stale)
  Scanning GGV3-44... ✓ (4 ibeads, +0 new, 1 stale)

ibead:research complete — 2026-04-01
  Beads scanned:              6
  Ibeads added:               1
  Ibeads flagged stale:       1
  Ibeads needing attention:   2

  Action required:
    [new]       [GGV3-41]  new domain "notification-layer" emerged
      → run ibead:audit to populate task list
    IB-44-2     [GGV3-44]  domain no longer in current blast radius
      → review and close if clean
```

---

## Common Patterns

### ibead reveals nothing to fix

This is valid — close the ibead after confirming the domain is clean:

```bash
# domain audit shows no impact
npm run ibead:close -- --bead GGV3-abc1.3
# → lint passes, type-check passes, ibead closes
```

### ibead reveals a pre-existing bug

Track it as a `discovered-from` dependency, don't block the ibead:

```bash
bd create "Fix broken SessionService caller" \
  --deps "discovered-from:GGV3-abc1.1"
# → close GGV3-abc1.1, the new bead tracks the discovered issue
```

### GitNexus index is stale

```bash
npx gitnexus analyze
npm run ibead:audit -- --parent GGV3-abc1
```

### Preview any step

```bash
npm run ibead:create   -- --parent GGV3-abc1 --dry-run
npm run ibead:audit    -- --parent GGV3-abc1 --dry-run
npm run ibead:close    -- --bead   GGV3-abc1 --dry-run
npm run ibead:research -- --dry-run
```

### JSON output for scripting/CI

```bash
npm run ibead:create   -- --parent GGV3-abc1 --json | jq '.ibeads[].id'
npm run ibead:audit    -- --parent GGV3-abc1 --json | jq '.ibeads[].taskList'
npm run ibead:close    -- --bead   GGV3-abc1 --json | jq '.closed'
npm run ibead:research -- --json | jq '.ibeadsNeedingAttention'
```
