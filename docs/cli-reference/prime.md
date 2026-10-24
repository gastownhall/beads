---
title: "bd prime"
description: "Output AI-optimized workflow context"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc prime`.

Output essential Beads workflow context in AI-optimized markdown format.

Automatically detects if MCP server is active and adapts output:
- MCP mode: Brief workflow reminders (~50 tokens)
- CLI mode: Full command reference (~1-2k tokens)

Designed for Claude Code, Gemini CLI, and Codex SessionStart hooks to prevent
agents from forgetting bd workflow after context compaction.

Config options:
- no-git-ops: When true, outputs stealth mode (no git commands in session close protocol).
  Set via: bd config set no-git-ops true
  Useful when you want to control when commits happen manually.
- agent.profile: Explicit policy profile for git/commit authority wording
  (conservative | minimal | team-maintainer; default conservative).
  Set via: bd config set agent.profile team-maintainer
  Or per-session: BD_AGENT_PROFILE=team-maintainer (env var takes precedence).
  See docs/getting-started/ide-setup.md#policy-profiles for what each profile means.

	Workflow customization:
	- Place a .beads/PRIME.md file in the local clone or resolved workspace to override the default workflow text. Persistent memories (from bd remember) are still appended so memory injection keeps working under a custom template.
	- Use --export to dump the default content for customization.
	- Use --memories-only for hook contexts that should inject only persistent memories; this returns only the memories section even when a custom PRIME.md is present.
	- Use --no-memories to omit the persistent memories section (useful when the memories section is large and would dominate a context budget). --memories-only takes precedence if both are set.

Memory injection caps:
	Large memory sets can exceed what a session-start hook host will ingest,
	and hosts truncate silently. Cap what prime injects with --max-memories N
	and/or --max-memory-chars N (or the prime.max-memories /
	prime.max-memory-chars config keys; an explicit flag wins, and an explicit
	0 forces unlimited). Caps apply at whole-memory boundaries, at least one
	memory is always emitted, and a banner ahead of the entries reports how
	many were elided and how to browse the rest with bd memories.
	--max-memory-chars caps the total bytes of the injected memory entries;
	the section header and elision banner are excluded from the budget.

```
bd prime [flags]
```

**Flags:**

```
      --export                 Output default content (ignores PRIME.md override)
      --full                   Force full CLI output (ignore MCP detection)
      --hook-json              Wrap output in the SessionStart hook JSON envelope (Claude Code, Gemini CLI, Codex)
      --max-memories int       Cap injected persistent memories to N entries (0 = unlimited; falls back to the prime.max-memories config key)
      --max-memory-chars int   Cap the total bytes of injected memory entries, at whole-memory boundaries; section header and banner are not counted (0 = unlimited; falls back to the prime.max-memory-chars config key)
      --mcp                    Force MCP mode (minimal output)
      --memories-only          Output only persistent memories for compact hook contexts
      --no-memories            Omit the persistent memories section (ignored when --memories-only is set, which wins)
      --stealth                Stealth mode (no git operations, flush only)
```
