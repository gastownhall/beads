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
   - The plugin manifest is bd's own file, written as-is
   - The instructions file is shared, so bd manages only a marked section in it, with the same code as the `AGENTS.md`, `CLAUDE.md` and `GEMINI.md` integrations
   - No Copilot-specific doctor checks

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
  - A managed beads section (minimal profile) between `<!-- BEGIN BEADS INTEGRATION ... -->` and `<!-- END BEADS INTEGRATION -->` markers, pointing Copilot CLI to `bd prime`

**Your own instructions are kept** (unreleased; before this fix `bd setup copilot` replaced the whole file):
- If `.github/copilot-instructions.md` already exists, `bd setup copilot` leaves its contents as they are and appends the beads section after a blank line. Re-running it updates only that section, and does nothing when it is current.
- If the file does not exist, it is created holding only the section.
- Text without the markers, including the static instructions an older `bd setup copilot` wrote, is treated as yours: bd never edits or removes it. Delete it by hand if you no longer want it.
- If the file is a symlink, bd leaves it and its target alone and says so; edit the target directly or replace the link with a regular file.
- bd takes the section to be what lies between exactly one BEGIN and one END marker, in that order, each a comment alone on its line and outside code blocks and other HTML blocks, as Markdown (CommonMark) reads the file. If the markers are incomplete, out of order or repeated, share a line with other text or sit in a code block or another HTML block (as when your own text mentions one), install, `--check` and `--remove` stop with an error and change nothing; fix or remove the markers by hand.
- If a file without the markers ends inside an unclosed code block or HTML comment, where the section would be appended, install stops with an error naming the block and changes nothing; close the block first.

**`--check`** passes only when the plugin manifest exists and the instructions file holds a current beads section; for a symlink, the file it links to, which Copilot reads, must hold one. It reports missing files, an existing file with no beads section, a stale section, or malformed markers, and exits nonzero for each.

**`--remove`** deletes the plugin manifest and removes only the beads section, with the blank lines that set it off; the rest of the file stays. A file that held nothing but the section is deleted.

A `[recipes.copilot]` entry in `.beads/recipes.toml` replaces the built-in recipe and is installed as a plain file recipe.

## Related Files

- `plugins/beads/.copilot-plugin/plugin.json` - Source plugin manifest for the shared plugin package
- `plugins/beads/copilot_manifest.go` - Embedded manifest source used by `bd setup copilot`
- `internal/recipes/recipes.go` - Lightweight `copilot` recipe definition
- `cmd/bd/setup/copilot.go` - Install, check and remove for the plugin manifest and the managed instructions section
- [GitHub Copilot integration](/integrations/github-copilot) - VS Code MCP integration

## References

- [GitHub Copilot CLI docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli)
- [Adding repository custom instructions for GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/add-custom-instructions)
