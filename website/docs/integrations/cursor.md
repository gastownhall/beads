---
id: cursor
title: Cursor
---

# Cursor Integration

Use Beads with Cursor through a project rules file **and** agent lifecycle hooks.
The hooks are what keep Beads context alive across compaction — without them,
Cursor only sees the static rules file and the agent must remember to run
`bd prime` itself (which it often forgets after a context compaction).

```bash
bd setup cursor
bd setup cursor --check
```

`bd setup cursor` installs three things:

- **`.cursor/rules/beads.mdc`** — an always-applied rule with the canonical Beads
  workflow guidance (the same content every other editor integration uses), so it
  stays in sync as the workflow evolves. Because it is always applied, Cursor
  re-includes it every turn, including after a compaction.
- **`.agents/skills/beads/SKILL.md`** — the Beads agent skill, which Cursor loads
  natively from `.agents/skills/` (the same skill the Codex integration installs).
  It gives the agent an on-demand, progressive-disclosure reference for `bd`.
- **`.cursor/hooks.json`** — agent hooks that run the hidden `bd cursor-hook`
  command at key points in the session. Existing (non-Beads) hooks in this file
  are preserved.

The managed hooks are:

| Event | What it does |
|-------|--------------|
| `sessionStart` | Injects full `bd prime` output so the agent starts every session already knowing the workflow, ready work, and project memories. |
| `preCompact` | Arms a one-shot marker and shows a short reminder that context will be restored. |
| `postToolUse` | After a compaction, re-injects `bd prime` exactly once on the next tool call, then no-ops. |

:::note Requirements
Hooks run in the Cursor IDE and in recent `cursor-agent` CLI builds (verified on
the 2026.06 line; early-2026 CLI builds only fired shell hooks). After setup,
restart Cursor or start a **new** `cursor-agent` session so the hooks load.

The hook entries call `bd cursor-hook`, which Cursor resolves on `PATH` — exactly
like the Claude Code and Codex hooks. **But that `PATH` is not your shell's:**
Cursor rebuilds it for hook subprocesses and prepends its own directories, so a
stale `bd` earlier in *that* order silently wins and every hook fails with no
visible error. The classic case is an old Homebrew `bd` (without the
`cursor-hook` command) shadowing a newer build. Make sure the first `bd` Cursor
finds is current — `~/.local/bin` and `~/go/bin` sort ahead of Homebrew in
Cursor's PATH, so installing or symlinking `bd` there is a reliable way to win
resolution.

In the CLI, the workspace must also be **trusted** — hooks do not run in an
untrusted workspace.
:::

:::tip Auto-installed by `bd init`
You usually don't need to run `bd setup cursor` by hand. `bd init` installs the
Cursor integration automatically (the same way it sets up Claude Code and
Codex), and stages `.cursor/` so the rules + hooks are committed with your repo.
Run `bd setup cursor` only to (re)install into an existing project or to repair
drift.
:::

## Global install (all projects)

To wire the hooks once for every project instead of per-repo:

```bash
bd setup cursor --global          # writes ~/.cursor/hooks.json
bd setup cursor --check --global
bd setup cursor --remove --global
```

Global scope installs the **hooks and the agent skill** (`~/.agents/skills/beads`),
but not a rules file. Cursor has no reliable file-based global *rules* location —
global rules belong in **Cursor Settings → Rules** — so `.cursor/rules/beads.mdc`
is project-scoped. This is fine in practice: the global `sessionStart` hook
already injects the full `bd prime` context into every project, which supersedes
the static rule.

## Why these specific hooks

The Claude Code / Codex integrations recover from compaction with a dedicated
post-compaction event plus a prompt-submission hook. Cursor's
[hook API](https://cursor.com/docs/hooks) doesn't offer equivalents, so the
recovery is built from what Cursor *can* do:

- Cursor has **no post-compaction hook** — only `preCompact`, which is
  observational and "cannot block or modify the compaction behavior" (its only
  output is `user_message`). So `preCompact` just arms a one-shot marker and
  notifies you.
- Only **two** hooks can inject model-visible context (`additional_context`):
  `sessionStart` and `postToolUse`. `sessionStart` fires only when a new
  conversation is created — not after a compaction, and not when resuming with
  `cursor-agent --continue`. `preCompact` and `postToolUse` *do* fire in resumed
  sessions, so compaction recovery still works under `--continue`; only the
  one-time start-of-session injection is skipped (the resumed conversation
  already contains it).
- That makes `postToolUse` the only place to re-inject `bd prime` after a
  compaction — which the docs explicitly support ("Useful for ... injecting
  context").

:::caution Limitation
Post-compaction recovery fires on the **next tool call**. If you have a
purely conversational turn right after a compaction (no tool use), the refresh
waits until the next tool runs — no Cursor hook can inject context on a bare
prompt. Agent sessions call tools often, so in practice this is quick. You can
always run `bd prime` manually to force a refresh.
:::

## Verifying it works (CLI, no hooks pane)

The `cursor-agent` CLI has no hooks output pane, so verify the hooks directly:

```bash
# sessionStart should return {"continue": true, "additional_context": "...bd prime..."}
echo '{"hook_event_name":"sessionStart","workspace_roots":["'"$PWD"'"]}' \
  | bd cursor-hook sessionStart | jq .

# In a real session, the agent should know your bd workflow without being told,
# and should recover it after a compaction without you running `bd prime`.
```

If `additional_context` is empty, the directory is not a Beads workspace yet
(run `bd init`) or `bd` is not on Cursor's `PATH`. Note this command runs *your
shell's* `bd`; Cursor may resolve a different one for hooks (see Requirements
above), so a passing check here doesn't guarantee Cursor runs the same build.

`bd doctor` also reports three Cursor checks (under integration diagnostics),
paralleling the Claude checks:

- **Cursor Integration** — flags when Cursor is in use but the hooks aren't installed.
- **Cursor Settings Health** — errors if `.cursor/hooks.json` is malformed JSON
  (which would silently disable every hook).
- **Cursor Hook Completeness** — warns if only some of the three managed events
  are installed (e.g. after a hand-edit), since recovery needs all three.

## Remove

```bash
bd setup cursor --remove
```

This removes the rules file and the Beads-managed hook entries while leaving any
of your own hooks in `.cursor/hooks.json` intact. The shared agent skill
(`.agents/skills/beads`) is removed too — unless the Codex integration is still
installed, in which case it is kept because both integrations use it.
