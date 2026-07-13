---
title: Gates
description: Async wait conditions that park a workflow step until the world catches up — a human decision, a timer, or a GitHub run or PR.
---

Some workflow steps can't proceed on code alone: a release needs CI to go
green, a deploy needs a human sign-off, a cleanup should wait 24 hours. A
**gate** is an issue that represents that wait. It blocks a step the same way
any blocker does — the step leaves the ready frontier until the gate closes —
so agents never need to poll or spin.

## How a gate works

A gate is a bead like any other: created open, it blocks its waiters through
a normal dependency edge, and the step becomes ready the moment the gate
closes. Gates close in one of two ways:

- **Manually** — `bd gate resolve <gate-id>` (human gates always close this
  way).
- **Via `bd gate check`** — evaluates open timer and GitHub gates against
  the real world and closes the ones whose condition is met.

```bash
bd gate list                 # open gates
bd gate list --all           # include closed
bd gate show <gate-id>       # details and waiters
bd gate check                # evaluate open gates, close satisfied ones
bd gate check --dry-run      # report without closing
bd gate resolve <gate-id>    # close a gate manually
```

## Gate types

| Type | Waits for | Closed by |
|------|-----------|-----------|
| `human` | a person's decision | `bd gate resolve` only |
| `timer` | a duration after gate creation | `bd gate check` once the timeout elapses |
| `gh:run` | a GitHub Actions workflow to complete successfully | `bd gate check` (uses `gh run view`) |
| `gh:pr` | a pull request to merge | `bd gate check` (uses `gh pr view`) |
| `bead` | a bead in another rig to close | currently unresolvable — multi-rig routing was removed, so `bd gate check` reports these gates as uncheckable |

Timeouts use Go duration syntax: `30m`, `1h`, `24h` (there is no `d` unit —
write `24h`, not `1d`).

## Gates in formulas

A formula step declares a gate with a `[steps.gate]` block. When the formula
is instantiated, bd creates the gate issue and wires it as a blocker of that
step. The schema has four fields: `type`, `id`, `await_id`, and `timeout`.

This is the release gate from beads' own release formula — the step that
waits for the GitHub release workflow:

```toml
[[steps]]
id = "wait-for-ci"
title = "Wait for release workflow"

[steps.gate]
type = "gh:run"
id = "release.yml"       # which workflow to watch
timeout = "30m"          # escalate if it takes longer
```

A human sign-off gate:

```toml
[[steps]]
id = "approve-deploy"
title = "Human approves the deploy"

[steps.gate]
type = "human"
```

And a cooling-off timer:

```toml
[[steps]]
id = "wait-24h"
title = "Let the release bake"

[steps.gate]
type = "timer"
timeout = "24h"
```

Verify what the parser actually understood before pouring — unknown keys in
TOML are dropped silently:

```bash
bd formula show <formula> --json   # inspect the parsed gate blocks
```

## Creating gates outside formulas

`bd gate create` attaches a gate to existing work:

```bash
# Block bd-abc until a PR merges
bd gate create --type=gh:pr --blocks bd-abc --await-id=42

# Block bd-abc until a human resolves the gate
bd gate create --type=human --blocks bd-abc --reason "Design sign-off"

# Add another waiter to an existing gate
bd gate add-waiter <gate-id> <issue-id>
```

## Fan-in: waiting on other steps

Waiting on *other steps* is not a gate — it's a dependency. Use `needs` to
fan in on named steps, and `waits_for` when a step must wait for
dynamically-created children:

```toml
[[steps]]
id = "merge-results"
title = "Merge results"
needs = ["test-a", "test-b"]     # fan-in on named steps

[[steps]]
id = "summarize"
title = "Summarize all spawned work"
waits_for = "all-children"       # or "any-children", or "children-of(step-id)"
```

## Working with gated molecules

```bash
bd ready --gated        # molecules where a gate just closed (ready to resume)
bd blocked              # what's waiting, and on which gates
```

Automation patterns: run `bd gate check` on a schedule (cron, CI, or an
orchestrator loop) so timer and GitHub gates close without a human in the
loop; keep `human` gates for the decisions that should never auto-close.
