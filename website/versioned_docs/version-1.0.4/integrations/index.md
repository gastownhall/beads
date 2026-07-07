---
id: index
title: Integrations
slug: /integrations
---

# Integrations

Beads integration pages are based on two sources of support in the repository:

- Built-in `bd setup` recipes from `internal/recipes/recipes.go`
- First-party MCP integrations and editor guides already shipped in the repo

Run this command to see the built-in setup recipes supported by your installed `bd` binary:

```bash
bd setup --list
```

## Built-in Setup Recipes

| Recipe | Integration | Primary setup surface |
|--------|-------------|-----------------------|
| `aider` | [Aider](/integrations/aider) | `.aider.conf.yml` and `.aider/` instructions |
| `claude` | [Claude Code](/integrations/claude-code) | Claude hooks and `CLAUDE.md` |
| `codex` | [Codex](/integrations/codex) | Beads skill, `AGENTS.md`, and Codex hooks |
| `cody` | [Sourcegraph Cody](/integrations/cody) | `.cody/rules/beads.md` |
| `cursor` | [Cursor](/integrations/cursor) | `.cursor/rules/beads.mdc` |
| `factory` | [Factory.ai Droid](/integrations/factory) | `AGENTS.md` |
| `gemini` | [Gemini CLI](/integrations/gemini) | Gemini hooks and `GEMINI.md` |
| `junie` | [Junie](/integrations/junie) | `.junie/guidelines.md` and MCP config |
| `kilocode` | [Kilo Code](/integrations/kilocode) | `.kilocode/rules/beads.md` |
| `mux` | [Mux](/integrations/mux) | `AGENTS.md`, optional `.mux/AGENTS.md`, and Mux hooks |
| `opencode` | [OpenCode](/integrations/opencode) | `AGENTS.md` |
| `windsurf` | [Windsurf](/integrations/windsurf) | `.windsurf/rules/beads.md` |

## MCP-Based Integrations

These integrations use the Beads MCP server rather than a dedicated `bd setup` recipe:

- [MCP Server](/integrations/mcp-server)
- [GitHub Copilot](/integrations/github-copilot)
