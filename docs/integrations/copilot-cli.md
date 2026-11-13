---
title: GitHub Copilot CLI Integration Design
description: Design rationale and setup for the Copilot CLI integration, which uses a plugin manifest plus repository instructions
---

This document explains design decisions for GitHub Copilot CLI integration in beads.

For **VS Code + MCP**, see [GitHub Copilot](/integrations/github-copilot).

## Integration Approach

**Recommended: Copilot CLI plugin + repository instructions** - Beads uses Copilot CLI's native plugin manifest plus repository instructions:
- `.copilot-plugin/plugin.json` registers `bd prime` hooks natively
- `.github/copilot-instructions.md` provides repository-specific workflow guidance
- Direct CLI commands with `--json` flags remain the primary operational interface

**Alternative: VS Code MCP** - For Copilot Chat in the editor:
- Native tool calling through MCP
- Higher context overhead from tool schemas
- Use when you want editor-native tool access instead of terminal-first workflow

## Why Plugin + Instructions Over Custom Setup Code?

**The plugin manifest already models the behavior we want:**

1. **Hooks belong in the tool's native format**
   - Copilot CLI understands plugin manifests directly
   - `SessionStart` and `PreCompact` can be declared as data instead of custom Go logic
   - This keeps beads core smaller and easier to maintain

2. **Instructions stay explicit and reviewable**
   - Repository guidance still lives in `.github/copilot-instructions.md`
   - Teams can review the instructions like any other project documentation
   - The hook behavior and the human-readable guidance stay separate

3. **Lower maintenance burden**
   - No Copilot-specific install/check/remove implementation in core
   - No Copilot-specific doctor checks
   - The recipe writes the native plugin file and keeps a marked section in
     the instruction file

## Why Copilot CLI Over MCP for Terminal Work?

**Context efficiency still matters**, even with large context windows:

1. **Compute cost scales with tokens** - Every token in context is processed on every inference
2. **Latency increases with context** - Smaller prompts keep the CLI more responsive
3. **Energy consumption** - Lean prompts are more sustainable over long sessions
4. **Attention quality** - Models generally perform better with tighter, more relevant context

**The math:**
- MCP tool schemas can add 10-50k tokens to context
- `bd prime` adds ~1-2k tokens of workflow context
- That is an order-of-magnitude reduction in overhead

## Installation

```bash
# Install the Copilot CLI plugin manifest + repository instructions
bd setup copilot

# Check installation status
bd setup copilot --check

# Remove the integration
bd setup copilot --remove
```

**What it installs:**
- `.copilot-plugin/plugin.json`
  - `SessionStart` hook: Runs `bd prime` when Copilot CLI starts a session
  - `PreCompact` hook: Runs `bd prime` before context compaction
- `.github/copilot-instructions.md`
  - Repository workflow guidance for Copilot CLI, kept as a marked section

## Sharing `.github/copilot-instructions.md`

`.github/copilot-instructions.md` is where a repository keeps its own Copilot
instructions, so `bd setup copilot` does not own the whole file. It keeps
the beads guidance between `<!-- BEGIN BEADS INTEGRATION -->` and
`<!-- END BEADS INTEGRATION -->` markers and leaves everything else alone:

- **Setup** creates the file holding just the section when it is missing or
  empty, appends the section after your content when the file has none, and
  replaces an existing section in place. Running it again changes nothing.
- **`--check`** fails, naming the file, when it is missing, holds no beads
  section, or has malformed markers. It also needs the plugin manifest.
- **`--remove`** takes out only the section, rejoining the text around it
  with the blank line that set it off, and deletes the file only when nothing
  but whitespace is left.
- **Earlier releases** wrote the template as the whole file, unmarked. A
  file that is exactly that template passes `--check`, and setup converts it
  to one marked section. If you edited that file, setup appends a new
  section; delete the old unmarked guidance by hand.
- **Refusals:** setup and remove change nothing, and exit non-zero, when the
  file's markers are malformed (a dangling, reversed, nested or repeated
  marker) or when `.github/copilot-instructions.md` is itself a symbolic
  link. Fix the markers, or replace the link with a regular file, and run
  the command again. A symlinked `.github` directory works as before.

`.copilot-plugin/plugin.json` is bd's own: setup writes it whole, and
`--remove` deletes it.

## Related Files

- `plugins/beads/.copilot-plugin/plugin.json` - Source plugin manifest for the shared plugin package
- `plugins/beads/copilot_manifest.go` - Embedded manifest source used by `bd setup copilot`
- `internal/recipes/recipes.go` - Lightweight `copilot` recipe definition
- `internal/recipes/template.go` - Static Copilot instructions template used by `bd setup`
- [GitHub Copilot integration](/integrations/github-copilot) - VS Code MCP integration

## References

- [GitHub Copilot CLI docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli)
- [Adding repository custom instructions for GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/add-custom-instructions)
