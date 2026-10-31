---
title: Gates
description: Async wait conditions that park a workflow step until the world catches up — a human decision, a timer, or a GitHub run or PR, or a GitLab pipeline or merge request.
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
- **Via `bd gate check`** — evaluates open timer, GitHub, GitLab, and bead gates
  against the real world and closes the ones whose condition is met.

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
| `gl:pipeline` | a GitLab pipeline to succeed | `bd gate check` (uses `glab api`) |
| `gl:mr` | a GitLab merge request to merge | `bd gate check` (uses `glab api`) |
| `bead` | a bead to close, or to be deleted once a check has seen it — a plain ID names a bead in this rig, the cross-rig form is `<rig>:<bead-id>` | `bd gate check` for plain local IDs; cross-rig values cannot be checked — resolve those manually |

A `bead` gate also closes when its awaited bead is deleted, as long as an
earlier `bd gate check` saw that bead in this rig; the check records the
sighting in the gate's `await_seen` metadata. An await ID that no check has
seen, such as a typo or a bead deleted before the first check, keeps the gate
pending with a diagnostic: correct the await ID or close the gate with
`bd gate resolve`. Renaming the awaited bead with `bd rename` or
`bd rename-prefix` moves the gate's await ID and sighting to the new ID, so a
rename does not count as a deletion, even to a `bd gate check` or `bd close`
that read the gate before the rename; a rename that fails partway leaves the
gate pending without a sighting, never resolved.

Timeouts use Go duration syntax: `30m`, `1h`, `24h` (there is no `d` unit —
write `24h`, not `1d`).

GitHub gates use the current Git repository by default. To evaluate a PR or
workflow run in another repository, set the gate's string `metadata.repo` value
to `OWNER/REPO` or `HOST/OWNER/REPO`. An ad-hoc `gh:run`/`gh:pr` gate created
with `bd gate create` takes that value from `--repo`, or, without the flag,
inherits a valid `metadata.repo` value from the issue it blocks;
`human`/`timer`/`bead` gates do neither (`--repo` is refused on them), since
`metadata.repo` is unrelated, ordinary metadata for those types. `bd gate check`
rejects malformed repository values instead of falling back to the current
repository, and when a PR number does not resolve, the escalation names the
repository it was looked up in. A gate for a PR in another repository whose
blocked issue carries no `metadata.repo` needs `--repo`: without it the number
is looked up in the current repository and escalates as not found on every
check.

```bash
bd gate create --type=gh:pr --blocks bd-abc --await-id=42 --repo=owner/other-repo
```

### GitLab gates

Install and authenticate `glab` for your GitLab instance before checking gates.
A `gl:pipeline` gate uses the pipeline **ID**, not its project-local IID.
Only `success` resolves it. `failed` and `canceled` escalate it; all other
statuses, including unknown statuses, keep it pending. A `gl:mr` gate uses
the merge request's project-local **IID**. `merged` resolves it; `closed`
without merging escalates it. Other states keep it pending.

Set `metadata.repo` or `--repo` to a project path such as
`team/subgroup/project`. To select an explicit host, use
`https://code.example.org/team/subgroup/project`. Without an explicit host,
`glab` selects the authenticated host for the current repository. Without a
project path, it also selects the project from the current GitLab remote.
Host names do not need to contain the word `gitlab`. Ad-hoc gates inherit
`metadata.repo` from the blocked issue when you omit `--repo`.

```bash
bd gate create --type=gl:mr --blocks bd-abc --await-id=7 --repo=team/subgroup/project
bd gate create --type=gl:pipeline --blocks bd-abc
bd gate discover --dry-run
bd gate discover
bd gate check --type=gl
```

Discovery fills an empty pipeline `await_id` with the newest pipeline matching
both the current branch and HEAD commit. It preserves existing IDs and honors
`--dry-run`. Formula gates can also set `repo` for either GitLab type.

`bd close` uses the same GitLab checks: pending and failed conditions require
`--force`. As with GitHub gates, a CLI or API error produces a warning and
allows manual close; `bd gate check` reports that error and leaves the gate open.

The API fields follow the [GitLab pipelines API](https://docs.gitlab.com/api/pipelines/)
and [merge requests API](https://docs.gitlab.com/api/merge_requests/).
Host and current-project selection follow [glab api](https://docs.gitlab.com/cli/api/).

### Known limitations: multi-rig and proxied-server topologies

Bead gates and prefix routing (`routes.jsonl`) do not work in every
topology. Two limitations to expect, both tracked in
[#5861](https://github.com/gastownhall/beads/issues/5861):

- **Cross-rig bead gates never resolve on their own.** A `<rig>:<bead-id>`
  await value reports `cannot be checked (multi-rig routing removed)` and
  stays pending regardless of the awaited bead's status. Close it with
  `bd gate resolve`.
- **Prefix routing cannot open a proxied-server target rig.** A routed
  lookup into a rig that is itself in proxied-server mode fails with
  `proxy server store needs to be uow provider`, so an all-proxied
  shared-server topology cannot resolve routed targets at all.

## Gates in formulas

A formula step declares a gate with a `[steps.gate]` block. When the formula
is instantiated, bd creates a companion gate issue `<parent>.gate-<step>`
next to the step. The schema has five fields: `type`, `id`, `await_id`,
`timeout`, and `repo`.

How the companion is wired depends on the step's own `type`:

- **Entry-gate** (any step not declared `type = "gate"`): the gate blocks the
  step. The step's work starts after the gate resolves.
- **Exit-gate** (a step declared `type = "gate"`): the step becomes a `task`
  that holds the preparation work and blocks the gate. A step that lists
  this step in `depends_on` or `needs` waits for both the task and the gate.
  A `waits_for` on this step waits for the children bonded under the task,
  and for the gate.

`bd gate check` and `bd gate resolve` close a gate without waiting for its
blockers, so an exit-gate can close while its task is still open; a step
that lists the exit-gate step in `depends_on` or `needs` still waits for the
task. Under either form, a `timer` gate's timeout and `gh:*` gate checks
count from when the molecule is poured, not from when the gate's blockers
close: a timer exit-gate's wait runs alongside its task, not after it.

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

For a `gh:run` or `gh:pr` gate that watches another repository, set `repo`
the same way a `metadata.repo` value works for an ad-hoc gate — `OWNER/REPO`
or `HOST/OWNER/REPO`. Malformed values are rejected when the gate is checked:

```toml
[[steps]]
id = "wait-for-downstream"
title = "Wait for downstream release"

[steps.gate]
type = "gh:run"
id = "release.yml"
repo = "org/downstream-repo"   # check gh:run against this repo, not the current one
```

`repo` accepts a `{{var}}` placeholder (e.g. `repo = "{{gate_repo}}"`); for a
formula persisted with `bd cook --persist`, the placeholder is substituted
when the proto is later poured with `bd mol pour --var gate_repo=...`, the
same as every other var-bearing step field (`title`, `description`, `design`,
`acceptance_criteria`, `notes`, `assignee`, `labels`, `await_id`, and the
string values inside `metadata`).

`bd gate discover` (auto-discovery of a `gh:run` gate's run ID) requires a
workflow name hint (`await_id`/`id`, not left blank) for a gate targeting
another repository — without one, the local commit/branch heuristics that
narrow a same-repo match don't apply across repos, so nothing but the
workflow name can identify the right run. A cross-repo gate discovery also
ignores the local checkout's branch unless `--branch` is passed explicitly;
an auto-detected local branch has no relationship to the target repo's
branches.

A human sign-off gate, as an exit-gate whose task prepares the decision:

```toml
[[steps]]
id = "approve-deploy"
title = "Prepare the deploy for human approval"
type = "gate"

[steps.gate]
type = "human"

[[steps]]
id = "deploy"
title = "Deploy"
needs = ["approve-deploy"]   # waits for the prep task and for bd gate resolve
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
id = "fan-out"
title = "Spawn a worker per shard"

[[steps]]
id = "summarize"
title = "Summarize all spawned work"
needs = ["fan-out"]              # the spawner whose children to wait for
waits_for = "all-children"       # or "any-children", or "children-of(fan-out)"
```

`all-children` and `any-children` wait for the children of `needs[0]`, so a step
using them must declare `needs`; `children-of(step-id)` names the spawner itself
and does not. A bare gate with neither is rejected when the formula is cooked,
because it would wait for no one.

## Working with gated molecules

```bash
bd ready --gated        # molecules where a gate just closed (ready to resume)
bd blocked              # what's waiting, and on which gates
```

Automation patterns: run `bd gate check` on a schedule (cron, CI, or an
orchestrator loop) so timer and GitHub gates close without a human in the
loop; keep `human` gates for the decisions that should never auto-close.
