# Beads notify outbox - harness-agnostic seat delivery

## Thesis

Beads owns **time** (due/repeat/sweep) and **delivery ledger** (seat-addressed outbox).
Assignee is a durable **agent identity**: one immutable uuid per agent, minted once at
first registration (`bd seat register --state <path>` with no seat id) and stored in the
seat registry row and the agent's own durable state file. The uuid is never derived from
a pane, tab, or session; the human name lives on the uuid as a mutable alias.
Endpoints are disposable: on every start the agent re-registers under its uuid
(same command, readable state file present), the endpoint re-asserts, and the uuid
never changes. Senders address the uuid (or an alias, resolved exactly); drain resolves
the uuid to the live endpoint at send time and holds the row when it does not resolve.
Re-registration of a minted uuid requires the rebind token from the agent's durable
state, so a foreign shell that merely knows the uuid cannot take the seat over.
A workspace that has not migrated yet still reads its legacy
`.beads/notify/pins.json` (see `bd seat import-pins`), so upgrading never
turns working pins into no-pin holds.
There is no fuzzy search. A missing pin, dead target, or blocked approval dialog leaves
the row queued and marks the seat for parent escalation.
Working targets without verified queue semantics hold until idle and deliver at the next turn boundary.

Firstmate is an orchestrator that may *host* harnesses; it is not a harness and
not the notify bus. No assignee gets a privileged code path in beads.

## Design

```
bd due sweep
  → fire + reschedule + by_assignee
  → enqueue .beads/notify/outbox.jsonl  {seq, seat, issue_id, kind, title}

bd notify drain --seat <assignee> [--exec transport]
  → pending rows for that seat → exact pin (or hold+escalate / hold-until-idle) → ack
```

- **Seat** = `assignee` (empty → `unassigned`)
- **Outbox** = workspace-local under `.beads/notify/`
- **Transports** = native Go herdr delivery (default), or custom `--exec`
- **Attribution & Self-Comment Skip** = actor from `--actor`, else `BEADS_ACTOR`, else the **herdr tab label** (the captain names tabs for purpose, e.g. `portal AMD biller`), else `git config user.name`. Self-comments (actor == assignee) never enqueue outbox rows.
- **No special seats** in beads or contrib scripts
- **Due dedup** = the sweep skips a row when an unacked row already exists for the same seat, issue, and kind; unassigned due beads never enqueue
- **Liveness** = `pending`, `drain`, and the comment-ping drain drop rows whose bead is closed or labelled `tasks-axi-held`, before any hold or delivery decision. Drain acks them, so removing the label restores only pings enqueued afterwards

## Removed anti-patterns

- `publish-wake-*.sh` fan-out by assignee (e.g. hard-coded herdr session)
- Per-name env defaults for particular agents in fleet drop-ins
- Overseer-only bead id files as a second routing plane
- External `--exec` scripts required for standard non-interrupting queue semantics
- Self-ping feedback loops on comment updates (wiseman-c88)

Optional summary publishers may still append one sweep line to a local queue;
that is not per-bead seat delivery.

## CLI

- `bd notify pending [--seat] [--json]`
- `bd notify drain --seat S [--limit N] [--exec CMD] [--no-ack]`
- `bd notify ack --seat S --upto N`
- `bd notify seats`
- `bd notify holds` / `bd notify holds reap [--older-than 14d] [--dry-run|--yes]`
- `bd notify resolve --seat S` - human diagnostic only (fuzzy lookalike is not delivery)
- `bd seat register --state <path> [--alias name] --session S --pane P` - mint once, re-register on every start (default state path: `$BEADS_AGENT_STATE`)
- `bd seat register <seat-id> ...` - operator path for hand-named seats (refused for minted, token-protected uuids)
- `bd seat rename <seat-id> --alias ...` / `bd seat list` / `bd seat delete <seat-id>` (dead rows only; frees a legacy name for a minted uuid) / `bd seat unbind <seat-id>`

## Exact pin delivery

`bd notify drain` native transport:

1. Load `.beads/notify/seats.json` (seat → `{session, pane_id}` or `agent_session_id`),
   falling back to the legacy `pins.json` when `seats.json` is absent.
   The legacy fleet seed in `examples/notify/pins.json` is imported with `bd seat import-pins`;
   explicit endpoint writes go through `scripts/notify/bd-notify-pin`.
2. `herdr session list --json` → running sessions
3. `herdr --session S agent list` → live agents (any harness)
4. Check pinned target status and harness delivery profile:
   - **Target Blocked** (`status == blocked`): approval dialog open. Never type into it; hold row and mark seat for parent escalation (exit 8 semantics).
   - **Target Working** (`status == working`):
     - Verified follow-up queue: submit via harness queue method (`follow_up`).
     - Unverified queue semantics: hold until idle (`hold_until_idle`). Row stays queued, retrying at the next turn boundary without parent escalation.
   - **Target Idle / Done** (`status == idle | done`): submit immediately with Enter prompt (`steer` / `agent prompt`), trivially non-interrupting.
5. If the pinned target is missing or dead, hold the outbox row and mark `.beads/notify/holds.json` for parent escalation (exit 7 semantics).

The notify body (`notify.DefaultPrompt`) is identical across modes.

A lookalike pane (same title, same session name, focused idle pi, …) never
receives the ping. `bd notify resolve` may still print a scored diagnostic
match; drain ignores it.

## Delivery matrix (internal/notify/delivery.go)

| Harness (herdr `agent` label) | State | Delivery Action | How / Semantics |
|---|---|---|---|
| `pi`, `pi-signed` | `working` | `follow_up` | Option/Alt+Enter (`pane send-text` then `pane send-keys alt+enter`, with `ctrl+j` fallback). Queues for pi's next turn boundary, never interrupts in-flight work. |
| `cursor`, `cursor-agent` | `working` | `follow_up` | Tab queue (`pane send-text` then `pane send-keys tab`). Queues after turn on Cursor agent surfaces. |
| `codex` | `working` | `follow_up` | Tab queue (`pane send-text` then `pane send-keys tab`). Queues for next turn in Codex CLI. |
| `claude`, `claude-code` | `working` | `hold_until_idle` | **Deferred**: Enter mid-turn is steering. Row stays queued; delivers when target becomes idle. |
| `agy`, `antigravity` | `working` | `hold_until_idle` | **Deferred**: unverified queue semantics. Row stays queued; delivers when target becomes idle. |
| `gemini`, `kimi`, `omp`, `muse` | `working` | `hold_until_idle` | **Deferred**: unverified queue semantics. Row stays queued; delivers when target becomes idle. |
| Any unverified / unknown harness | `working` | `hold_until_idle` | **Deferred**: default to hold-until-idle until live-verified. |
| Any harness | `idle` / `done` | `steer` | Enter submit (`agent prompt`). Trivially non-interrupting since agent is not in-flight. |
| Any harness | `blocked` | **Escalate** | **Deferred**: approval dialog open. Never type into it; marks parent escalation (`holds.json`, exit 8). |
| Any seat | dead pin / no pin | **Escalate** | **Deferred**: target not live; marks parent escalation (`holds.json`, exit 7 / 5). |

JSON drain rows include `delivery_mode` (`follow_up`, `steer`, or `hold_until_idle`) and `classification`.

## Seat attribution and self-comment skip

Actor identity is resolved in priority order:
1. `--actor` CLI flag
2. `BEADS_ACTOR` environment variable (e.g. `export BEADS_ACTOR=arcs-fm`); unset it to fall back to the herdr tab label inside a named tab
3. `git config user.name`
4. `$USER`

When a comment is added via `bd comment`:
- If `author == assignee`, `notify.CommentAssigneeSeat` returns `"", false` and skips enqueueing.
- If unassigned, skips enqueueing.
- If the bead is closed, skips enqueueing.
- Otherwise enqueues a comment notification for the assignee seat.

## Fleet tooling (`scripts/notify/`)

The repository includes helper scripts under `scripts/notify/`:
- `bd-notify-pin`: Endpoint inspection and explicit endpoint writes (`set`/`forget`),
  routed through `bd seat register` / `bd seat unbind` as the single registry writer.
  There is no discovery: a target is read off `herdr agent list` or a lock
  owner and written down, never guessed from titles, cwd or focus.
- **One seat, one target**: seats are machine-global but each repo keeps its own
  `.beads/notify/` queue, so a repo may symlink its `seats.json` at the
  machine-canonical map. `bd seat register` (and therefore `bd-notify-pin set`)
  resolves that link and writes through it (mktemp+mv would replace the link and fork the map again - the
  2026-10-02 incident where seat `wiseman` resolved to two different panes).
  Regressed by Test 6 in `test_beads_notify_e2e.sh`.
- `fleet-monitor-beads-notify.sh`: Fleet auditor checking session integrity, outbox health, and non-interrupting delivery compliance.
- `bd-comment-notify`: Wrapper script for commenting and draining.
- `test_beads_notify_e2e.sh`: Shell end-to-end integration test, including the
  guard that no fuzzy resolver and no second transport implementation exists.
  Hermetic by default: a mock `bd` shadows the real binary so the suite never
  writes to a live beads DB; set `BEADS_NOTIFY_E2E_LIVE=1` to opt into a live
  run deliberately.

## Deployment (ponytail)

One service runs sweep then drain: `scripts/notify/bd-due-wake.sh` behind a
`systemd --user` unit. It sleeps until the earliest `due_at` across the
managed workspaces, then runs `bd due sweep` and `bd notify drain --all`, so a
due wake lands within seconds of its due instant instead of on a polling
boundary. A 300-second cap bounds recovery from suspend and late-arriving
dues. The unit runs as machine actor `bd-due-trigger`, the identity the sweep
records its due events under.

Pending outbox rows retry on the next sweep when the pinned target is live.
Unpinned or dead-pinned seats stay queued until a human (or lock owner) pins
the exact herdr id.

## Holds retention and reap (wiseman-mm3l)

`.beads/notify/holds.json` records seats whose drain escalated to a parent
(no-pin, pinned-target-dead, target-blocked). Detection already works; what
was missing is how long those marks live and who clears them.

| Reason | Meaning | Auto-reap? |
|---|---|---|
| `no-pin` | seat has no endpoint | yes, after retention |
| `pinned-target-dead` | pin exists but exact target not live | yes, after retention |
| `target-blocked` | approval dialog open | no (transient) |
| `hold-until-idle` | mid-turn, unverified queue | no (not even written to holds.json for parent escalation) |

**Retention default: 14 days** from `updated_at`. A reaped hold is removed
from `holds.json` and any pending outbox rows for that seat are acked
(they are undeliverable until the seat re-registers; a later fire enqueues
a fresh row).

```text
bd notify holds                     # list
bd notify holds reap --dry-run      # show eligible
bd notify holds reap --yes          # apply (14d default)
bd notify holds reap --older-than 7d --yes
```

A large reap (≥20 holds or ≥100 pending acked) prints a stderr warning so
the parent can notice a registry that has gone stale. Wire the reap into
the due-wake unit or a weekly cron when the backlog is chronic; it is safe
to run dry-run on every drain.

## Prompt playbook (finish-line education)

`notify.DefaultPrompt` always teaches how to end work in beads so harnesses
that only chat or write status files still learn the real finish line:

```text
bd comment <id> "what landed"
bd close <id> --reason "short real reason"
```

Kinds with stronger copy:

| Kind | When | Message emphasis |
|------|------|------------------|
| `due` / `defer` / `manual` | normal fires | short close footer |
| `stale-claim` | assigned open/in_progress gone quiet without close | full playbook: close / delta comment / reassign |
| `progress` | missing comment.progress trail | comment then close |
| `escalate` | escalations | show + comment + close if done |

`comment.progress_required` is mentioned so agents do not bare-close.
