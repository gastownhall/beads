# bd dream — Memory Consolidation

`bd dream` is a periodic background pass that asks an LLM to consolidate the
memory store created by `bd remember` / `bd memories`. It identifies duplicate
or overlapping entries, stale references (dates that have passed, removed
code), and low-signal noise, then proposes operations to clean them up.

The design mirrors Claude Code's AutoDream feature: bd has no built-in
scheduler, so triggering is left to cron / launchd / a session-stop hook in
your editor. `bd dream` provides the eligibility gate (`--check`) so the
caller can fire frequently without doing real work most of the time.

## Quickstart

```sh
# One-time setup: provide an Anthropic API key (mirrors bd's other AI features).
export ANTHROPIC_API_KEY=sk-ant-...
# or:  bd config set ai.api_key sk-ant-...

# Show what would change without applying:
bd dream run --dry-run --force

# Apply consolidation now (ignoring the time/churn threshold):
bd dream run --force

# Inspect the last run and threshold state:
bd dream status

# Eligibility-only check, suitable for cron / hooks:
bd dream run --check && bd dream run
```

## Triggering

`bd dream` is a one-shot command. To run it periodically you have several
options; pick the one that fits your environment.

**cron (daily at 03:00):**

```cron
0 3 * * * cd /path/to/repo && bd dream run --check && bd dream run >> ~/.cache/bd-dream.log 2>&1
```

**launchd (macOS):** create a LaunchAgent with `RunAtLoad=false` and
`StartCalendarInterval` set to your preferred cadence; the program is
`bd dream run` (the `--check` gate is built-in if you omit `--force`).

**Editor session-stop hook:** for example, Claude Code's `Stop` hook can
invoke `bd dream run &` after a session ends. The eligibility gate keeps the
hook cheap on most invocations.

## Eligibility threshold

A run is **eligible** when **all** of the following are true:

1. The repo has at least 2 memories.
2. EITHER it has never run before, OR both:
   - At least `dream.min-interval-hours` hours have passed since the last run.
   - The memory count has changed by at least `dream.min-churn` since then.

Defaults are 24 hours and 5 memories, matching AutoDream's "5 sessions over
24 hours" semantics adapted to bd's stateless model. Override per repo:

```sh
bd config set dream.min-interval-hours 12
bd config set dream.min-churn 3
```

`--force` skips the time and churn checks. The "at least 2 memories" floor
always applies — there is nothing to consolidate otherwise.

## What `bd dream run` does

1. Reads all memories (`bd memories --json`).
2. Sends them to the configured AI model (default: `config.ai.model`) with a
   tool-use schema describing three operation types: `forget`, `merge`,
   `update`.
3. Validates the plan against a safety guard: if the model would delete more
   than 50% of the store, the pass aborts.
4. Applies operations via the same storage layer used by `bd forget` /
   `bd remember`.
5. Records `dream.last-run-at`, `dream.last-run-status`,
   `dream.last-run-summary`, and `dream.memory-count-at-last-run` in the
   Dolt config table.

## Operation types

| Action | Effect |
|---|---|
| `forget`  | Delete a memory by key. Used for clearly redundant or obsolete entries. |
| `merge`   | Write a new entry under `new_key` with `new_content` that subsumes 2+ existing memories, then delete the absorbed keys. |
| `update`  | Rewrite an existing memory's content under the same key (typically to fix stale facts). |

The model is instructed to be conservative: when in doubt, leave a memory
alone. The dry-run output shows the proposed reasons so you can audit the
LLM's judgment before applying.

## Configuration keys

All keys live in the Dolt config table (NOT under `kv.`). They sync via
`bd dolt push` like any other dream/sync state.

| Key | Type | Default | Purpose |
|---|---|---|---|
| `dream.min-interval-hours`        | int     | 24 | Minimum hours between runs. |
| `dream.min-churn`                 | int     | 5  | Minimum memory-count delta since last run. |
| `dream.last-run-at`               | RFC3339 | —  | Timestamp of the most recent (attempted) run. |
| `dream.last-run-status`           | string  | —  | `ok`, `skipped:<reason>`, or `error:<msg>`. |
| `dream.last-run-summary`          | string  | —  | Plan summary: e.g. `merged 2, forgot 1, kept 8`. |
| `dream.memory-count-at-last-run`  | int     | —  | Memory count snapshot for churn detection. |

API key resolution mirrors `bd compact`: `ANTHROPIC_API_KEY` env var first,
then `ai.api_key` from config. Model is `config.ai.model` unless overridden
with `--model`.

## JSON output

All `bd dream` subcommands respect `--json`:

```sh
bd dream --json status
bd dream --json run --dry-run --force
bd dream --json run --check
```

`run --check` exits 0 when eligible, 1 when not (with the reasons in the
JSON payload or on stderr).

## Limitations

- Single LLM provider (Anthropic) for now. The provider seam can be
  generalized when a second concrete provider lands.
- No native scheduler — bd has no daemon. Use cron, launchd, or an editor
  hook.
- Memory writes are atomic per operation but not across the full plan; if
  the storage layer fails mid-plan, the pass logs and continues, then the
  next run will pick up the partial state.

## See also

- `bd remember` / `bd memories` / `bd forget` / `bd recall` — the memory
  primitives `bd dream` operates on.
- `bd compact` — the related AI-powered feature for issue summarization,
  whose retry/backoff/audit pattern this command mirrors.
