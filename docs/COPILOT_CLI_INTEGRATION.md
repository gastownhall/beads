# GitHub Copilot CLI Integration Design

This document explains design decisions for GitHub Copilot CLI integration in beads.

For **VS Code + MCP**, see [COPILOT_INTEGRATION.md](COPILOT_INTEGRATION.md).

## Integration Approach

**Recommended: Copilot CLI + Hooks** - Beads uses Copilot CLI's native instruction files and hook system:
- `bd prime` command for context injection (~1-2k tokens)
- Personal hooks in `~/.copilot/hooks/` for global workflow defaults
- Repository hooks in `.github/hooks/` for project-specific workflow refresh
- Direct CLI commands with `--json` flags

**Alternative: VS Code MCP** - For Copilot Chat in the editor:
- Native tool calling through MCP
- Higher context overhead from tool schemas
- Use when you want editor-native tool access instead of terminal-first workflow

## Why Copilot CLI + Hooks Over MCP?

**Context efficiency still matters**, even with large context windows:

1. **Compute cost scales with tokens** - Every token in context is processed on every inference
2. **Latency increases with context** - Smaller prompts keep the CLI more responsive
3. **Energy consumption** - Lean prompts are more sustainable over long sessions
4. **Attention quality** - Models generally perform better with tighter, more relevant context

**The math:**
- MCP tool schemas can add 10-50k tokens to context
- `bd prime` adds ~1-2k tokens of workflow context
- That is an order-of-magnitude reduction in overhead

**When context size matters less:**
- You specifically want Copilot Chat tool calls inside VS Code
- You are in a workflow where terminal hooks are not the right integration point

**When to prefer Copilot CLI + hooks:**
- Terminal-first coding sessions
- Long-running implementation work
- Projects where you want automatic `bd prime` refresh without MCP setup
- Workflows that span multiple editors or shells

## Why Global + Project Scopes?

**Decision: Beads supports both personal and repository-local Copilot CLI integration**

### Reasons

1. **Copilot CLI supports both surfaces**
   - Global instructions live in `~/.copilot/copilot-instructions.md`
   - Personal hooks live in `~/.copilot/hooks/`
   - Repository-local instructions and hooks live under `.github/`

2. **Global defaults are convenient**
   - `bd setup copilot` gives you a personal baseline once
   - Useful when you work across many repositories
   - Keeps your common workflow available everywhere

3. **Project hooks remain important**
   - Repository-local hooks let a project opt into Beads explicitly
   - Team repositories can commit `.github/hooks/*.json` and `.github/copilot-instructions.md`
   - Local project setup stays self-contained and reviewable

4. **Beads stays explicit**
   - Global mode handles personal defaults
   - Project mode handles repository behavior
   - Users can choose one or both instead of getting hidden behavior

### Current behavior

- `bd setup copilot` or `bd setup copilot --global`
  - installs `~/.copilot/copilot-instructions.md`
  - installs `~/.copilot/hooks/beads-copilot.json`

- `bd setup copilot --project`
  - installs `.github/copilot-instructions.md`
  - installs `.github/hooks/beads-copilot.json`

### Version policy

Beads requires **Copilot CLI 1.0.5 or newer** for the `preCompact` hook. The integration intentionally targets full parity with the Claude-style hook model instead of shipping a weaker partial setup.

## Installation

```bash
# Install Copilot CLI defaults globally
bd setup copilot

# Same as above, but explicit
bd setup copilot --global

# Install for this project only
bd setup copilot --project

# Use stealth mode for project hooks
bd setup copilot --project --stealth

# Check installation status
bd setup copilot --check
bd setup copilot --project --check

# Remove integration
bd setup copilot --remove
bd setup copilot --project --remove
```

**What it installs:**
- `sessionStart` hook: Runs `bd prime` when Copilot CLI starts a session
- `preCompact` hook: Runs `bd prime` before context compaction to preserve workflow instructions
- Minimal-profile Beads instructions in Copilot's instruction files

## Stealth Mode

Stealth mode is only relevant for **project hooks**:

```bash
bd setup copilot --project --stealth
```

This changes the project hook command from `bd prime` to `bd prime --stealth`.

## Related Files

- `cmd/bd/setup/copilot.go` - Copilot setup/check/remove logic
- `cmd/bd/setup/copilot_test.go` - Copilot setup tests
- `cmd/bd/doctor/copilot.go` - Repository-level Copilot verification
- `docs/COPILOT_INTEGRATION.md` - VS Code MCP integration

## References

- [GitHub Copilot CLI docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli)
- [Copilot hooks documentation](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks)
