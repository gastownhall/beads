---
title: Cursor
description: Set up beads for Cursor with an always-applied rules file, the beads agent skill, and agent hooks that survive compaction
---

Use Beads with Cursor through an always-applied project rules file, the shared
`beads` agent skill, and Cursor agent hooks that load `bd prime` on session
start and recover it after a context compaction.

```bash
bd setup cursor
bd setup cursor --check
```

Project setup writes three things:

- **`.cursor/rules/beads.mdc`** — an always-applied rule with the canonical
  Beads workflow guidance (the same content every other editor integration
  uses). Because it is always applied, Cursor re-includes it every turn.
- **`.agents/skills/beads/`** — the Beads agent skill, the same one
  `bd setup codex` installs. Cursor loads `.agents/skills/` natively.
- **`.cursor/hooks.json`** — `sessionStart`, `preCompact`, and `postToolUse`
  hooks that run `bd cursor-hook <event>`. Entries you added yourself are left
  alone; only the `bd cursor-hook` entries are managed.

The hooks invoke `bd`, so make sure `bd` is on `PATH` for Cursor. Restart
Cursor (or start a new `cursor-agent` session) after installing so the rule
and hooks load.

`bd init` runs this project setup by default, alongside the Claude Code and
Codex setup, unless `--skip-agents` or `--stealth` is used (or the repository
is bare).

## Global install

```bash
bd setup cursor --global
bd setup cursor --global --check
```

`--global` writes the hooks to `~/.cursor/hooks.json` and the skill to
`~/.agents/skills/beads/`. It does not write a rules file: Cursor has no
file-based global rules location (global rules live in Cursor Settings →
Rules), so either add the guidance there or run `bd setup cursor` per project.
`--check` in global scope verifies the hooks and the skill only.

## Hook lifecycle

- `sessionStart` injects the full `bd prime` output as additional context for
  the new session.
- `preCompact` arms a one-shot refresh marker and shows a short note that the
  workflow context will be re-injected on the next tool call.
- `postToolUse` is a no-op on every tool call unless that marker is present,
  in which case it injects `bd prime` once more and clears the marker.

`preCompact` alone cannot inject context, so the marker plus the first
`postToolUse` after compaction is the recovery path. If `bd prime` fails, the
marker stays in place and a later tool call retries.

Refresh markers live in a user cache directory keyed by the Cursor
`conversation_id` and the first workspace root. They are not written to
tracked files or to the Beads database.

These events fire in the Cursor IDE and in recent `cursor-agent` CLI builds
(early-2026 CLI builds only fired shell hooks). Where the agent hooks do not
fire, the rules file and skill still apply; run `bd prime` by hand after a
compaction.

## Verifying it works

```bash
bd setup cursor --check
```

This reports the rules file (project scope only), the three managed hooks, and
the agent skill, and exits non-zero with the setup command to run if any is
missing.

In a session, the agent should know your bd workflow without being told — ask
it what work is ready and it should run `bd ready`. If it doesn't, confirm
`.cursor/rules/beads.mdc` exists, that `bd` is on `PATH` for Cursor, and
restart Cursor.

## Remove

```bash
bd setup cursor --remove
bd setup cursor --global --remove
```

Project removal deletes the rules file and removes only the `bd cursor-hook`
entries from `.cursor/hooks.json`, keeping any hooks of your own (the file is
deleted once nothing but its `version` remains). The shared
`.agents/skills/beads/` skill is removed unless the Codex integration is still
installed, in which case it is kept and `bd` says so. `--global` removes the
same from `~/.cursor/hooks.json` and `~/.agents/skills/beads/`.

## Manual fallback

If you manage `.cursor/hooks.json` by hand instead of running
`bd setup cursor`, the equivalent shape is:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{ "command": "bd cursor-hook sessionStart" }],
    "preCompact": [{ "command": "bd cursor-hook preCompact" }],
    "postToolUse": [{ "command": "bd cursor-hook postToolUse" }]
  }
}
```

`bd setup cursor --check` recognises these entries by their
`bd cursor-hook ` command prefix.

## Related

- [IDE Setup](/getting-started/ide-setup) — all editor integrations
- [Claude Code](/integrations/claude-code)
- [Codex](/integrations/codex)
