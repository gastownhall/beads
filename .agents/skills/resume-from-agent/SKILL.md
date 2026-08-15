---
name: resume-from-agent
description: Resume interrupted work from any known coding-agent session (cross-agent discovery).
argument-hint: "[agent] [session-id]"
disable-model-invocation: true
---

# Resume from agent

Pick up **interrupted work** left in a coding-agent **session** — any harness this machine knows how to read. You are the continuation of that session, not a new agent taking a handoff summary and waiting.

Unlike `/resume-from-claude` (and siblings), this skill **discovers across agents** and picks the latest session for `$PWD` unless you name an agent or session.

Supported stores (when present on disk):

| Agent | Aliases | Root (typical) |
| --- | --- | --- |
| hermes | hermes-agent | `~/.hermes/state.db` |
| dirac | | `~/.dirac/data/tasks` |
| goose | | `~/.local/share/goose/sessions` |
| cursor | cursor-agent | `~/.cursor/projects/.../agent-transcripts` |
| gemini | gemini-cli | `~/.gemini/tmp/.../chats` |
| agy | antigravity, jetski | `~/.gemini/antigravity-cli` (best-effort) |
| claude | claude-code | `~/.claude/projects` |
| pi | | `~/.pi/agent/sessions` |
| codex | | `~/.codex/sessions` |
| opencode | | `~/.local/share/opencode/opencode.db` |
| auggie | augment | *(not observed — reports empty)* |

Research notes: `docs/research/resume-from-agent-session-stores.md` in the jonbaldie/skills repo.

## 1. Resolve and extract the brief

Run the bundled extractor by absolute path — `scripts/extract-session.py` next to this `SKILL.md`:

```bash
python3 /absolute/path/to/resume-from-agent/scripts/extract-session.py --cwd "$PWD" [agent] [session-id]
```

Useful variants:

```bash
# Latest session for this cwd across every known agent
python3 .../extract-session.py --cwd "$PWD"

# Force one agent
python3 .../extract-session.py --cwd "$PWD" hermes
python3 .../extract-session.py --cwd "$PWD" --agent goose

# Pin a session id (with or without agent)
python3 .../extract-session.py --cwd "$PWD" 20260701_134450_4ae33b
python3 .../extract-session.py --cwd "$PWD" dirac <task-id>

# Ranked candidates only
python3 .../extract-session.py --cwd "$PWD" --list
python3 .../extract-session.py --cwd "$PWD" --agent cursor --list

# Escape hatch: explicit transcript/db path
python3 .../extract-session.py --path ~/path/to/session.jsonl
```

- If the user passed an **agent** name and/or **session** id, pass them through (positional args or `--agent`).
- If they passed none, omit both — the script ranks every cwd match by mtime and takes the newest.
- On failure (nothing found, unknown agent, bad path), stop and report the error. Do not invent a session.
- Prefer this skill over the per-agent `/resume-from-*` skills when the user does not know which harness ran last. Use a per-agent skill when they explicitly name one and you only have that skill installed.

**Done when:** the extractor has printed a **brief** and you know `agent`, `session_id`, and (when present) `path` / `cwd`.

## 2. Ground against the live workspace

Treat the brief as a claim about the world, not ground truth. Before acting:

- If the brief's `cwd` differs from `$PWD`, say so once; continue only if the work still belongs here or the user pointed at that session on purpose.
- Read the **files in play** that still matter. Prefer the ones edited most, then the ones named in recent turns.
- Check live git state (`status`, branch, relevant diff) against the brief's `git_branch` (if any) and claimed progress.
- Open any artifact the recent turns depend on (spec, issue, log, failing test output) rather than trusting the transcript's memory of it.
- If **Discovery notes** mention a close runner-up from another agent, glance at `--list` only when the chosen brief looks empty or wrong for the work the user means.

**Done when:** you can state in one short block — goal, what's already done, what's wrong or unfinished, and the immediate next action — and each claim is checked against the live tree or a file you just read.

## 3. Continue the interrupted work

Act on the next action immediately. You are resuming, not reporting.

- Carry forward the session's goal, constraints, and in-flight approach unless the live tree shows they are stale.
- Re-invoke any skill the session was mid-way through, when that skill still applies.
- Skip re-asking for the original goal. Ask only when the brief and the tree genuinely conflict and a wrong guess would be destructive.
- Antigravity/`agy` briefs are lossy (protobuf blobs). Treat them as weak hints; prefer a fuller brief from another agent when both match.

**Done when:** the interrupted work has moved forward in this turn (edit, command, diagnosis step, or a blocked question that only the user can answer) — not when you have merely restated the brief.
