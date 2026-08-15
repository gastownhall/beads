---
name: resume-from-codex
description: Resume interrupted work from a Codex CLI session transcript.
argument-hint: "[session-id]"
disable-model-invocation: true
---

# Resume from Codex

Pick up **interrupted work** left in a Codex **session**. You are the continuation of that session, not a new agent taking a handoff summary and waiting.

Sessions live under `~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-...-<session-id>.jsonl`.

## 1. Resolve and extract the brief

Run the bundled extractor by absolute path — `scripts/extract-session.py` next to this `SKILL.md`:

```bash
python3 /absolute/path/to/resume-from-codex/scripts/extract-session.py --cwd "$PWD" [session-id]
```

- If the user passed a **session** id (full/partial UUID) or thread name, pass it through.
- If they passed none, omit it — the script selects the latest-mtime rollout whose `session_meta.cwd` matches `$PWD`.
- On failure (no sessions, unknown id), stop and report the error. Do not invent a session.

**Done when:** the extractor has printed a **brief** and you know `session_id`, `path`, and `cwd`.

## 2. Ground against the live workspace

Treat the brief as a claim about the world, not ground truth. Before acting:

- If the brief's `cwd` differs from `$PWD`, say so once; continue only if the work still belongs here or the user pointed at that session on purpose.
- Read the **files in play** that still matter. Prefer the ones edited most, then the ones named in recent turns.
- Check live git state (`status`, branch, relevant diff) against claimed progress.
- Open any artifact the recent turns depend on (spec, issue, log, failing test output) rather than trusting the transcript's memory of it.

**Done when:** you can state in one short block — goal, what's already done, what's wrong or unfinished, and the immediate next action — and each claim is checked against the live tree or a file you just read.

## 3. Continue the interrupted work

Act on the next action immediately. You are resuming, not reporting.

- Carry forward the session's goal, constraints, and in-flight approach unless the live tree shows they are stale.
- Re-invoke any skill the session was mid-way through, when that skill still applies.
- Skip re-asking for the original goal. Ask only when the brief and the tree genuinely conflict and a wrong guess would be destructive.

**Done when:** the interrupted work has moved forward in this turn (edit, command, diagnosis step, or a blocked question that only the user can answer) — not when you have merely restated the brief.
