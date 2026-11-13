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
   - The recipe just writes the native plugin file and manages a marked section in the instruction file

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
  - A marked `<!-- BEGIN BEADS INTEGRATION -->` … `<!-- END BEADS INTEGRATION -->` section holding the repository workflow guidance for Copilot CLI

## Who owns which file

`bd setup copilot` owns `.copilot-plugin/plugin.json` outright, but
`.github/copilot-instructions.md` is the standard place for a repository's
own Copilot guidance, so bd manages only the marked section inside it:

- **Install** keeps your existing content and appends (or replaces, in
  place) exactly one marked section with the current guidance. A file an
  older bd release wrote whole is converted to a marked section instead of
  gaining a second copy.
- **`--check`** requires the marked section: a file that exists but holds
  only your own content is reported as not installed, non-zero, with the
  path named.
- **`--remove`** strips the section and deletes the manifest. The
  instructions file itself is kept — with your content — unless nothing
  but the section (or a recognized old whole-file install) was in it, in
  which case the file and its now-empty directory are removed.

If the file's BEADS INTEGRATION markers cannot be owned safely (a
dangling, reversed, nested or duplicated pair, or an unclosed marker), or
the file is a symbolic link, install and `--remove` refuse before touching
either file; fix the markers (or the link) by hand and re-run. Guidance a
pre-section release left in the file unmarked can be deleted manually once
the marked section is in place.

## Related Files

- `plugins/beads/.copilot-plugin/plugin.json` - Source plugin manifest for the shared plugin package
- `plugins/beads/copilot_manifest.go` - Embedded manifest source used by `bd setup copilot`
- `internal/recipes/recipes.go` - Lightweight `copilot` recipe definition
- `internal/recipes/template.go` - Static Copilot instructions template used by `bd setup`
- [GitHub Copilot integration](/integrations/github-copilot) - VS Code MCP integration

## References

- [GitHub Copilot CLI docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/use-copilot-cli)
- [Adding repository custom instructions for GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/add-custom-instructions)
