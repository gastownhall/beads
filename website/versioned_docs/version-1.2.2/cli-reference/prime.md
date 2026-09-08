---
id: prime
title: bd prime
slug: /cli-reference/prime
sidebar_position: 500
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc prime`

## bd prime

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

	Workflow customization:
	- Place a .beads/PRIME.md file in the local clone or resolved workspace to override the default output entirely.
	- Use --export to dump the default content for customization.
	- Use --memories-only for hook contexts that should inject only persistent memories.

```
bd prime [flags]
```

**Flags:**

```
      --export          Output default content (ignores PRIME.md override)
      --full            Force full CLI output (ignore MCP detection)
      --hook-json       Wrap output in the SessionStart hook JSON envelope (Claude Code, Gemini CLI, Codex)
      --mcp             Force MCP mode (minimal output)
      --memories-only   Output only persistent memories for compact hook contexts
      --stealth         Stealth mode (no git operations, flush only)
```
