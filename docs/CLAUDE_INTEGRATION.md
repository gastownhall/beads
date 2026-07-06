# Claude Code Integration Design

This document explains design decisions for Claude Code integration in beads.

## Integration Approach

**Recommended: CLI + Hooks** - Beads uses a simple, universal approach to Claude Code integration:
- `bd prime` command for context injection (~1-2k tokens)
- Hooks (SessionStart) for automatic context refresh
- Direct CLI commands with `--json` flags
- Optional: Plugin for slash commands and enhanced UX

**Alternative: MCP Server** - For MCP-only environments (Claude Desktop, no shell):
- Higher context overhead (MCP tool schemas)
- Use only when CLI is unavailable

## Why CLI + Hooks Over MCP?

**Context efficiency matters**, even with large context windows:

1. **Compute cost scales with tokens** - Every token in your context consumes compute on every inference, regardless of whether it's used
2. **Latency increases with context** - Larger prompts take longer to process
3. **Energy consumption** - Each token has environmental impact; lean prompts are more sustainable
4. **Attention quality** - Models attend better to smaller, focused contexts

**The math:**
- MCP tool schemas can add 10-50k tokens to context (depending on number of tools)
- `bd prime` adds ~1-2k tokens of workflow context
- That's 10-50x less context overhead

**When context size doesn't matter:**
- MCP-only environments where CLI isn't available (Claude Desktop)
- Very short conversations where context overhead is negligible

**When to prefer CLI + hooks:**
- Any environment with shell access (Claude Code, Cursor, Windsurf, etc.)
- Long conversations or coding sessions
- Multi-editor workflows (CLI is universal)

## Why Not Claude Skills?

**Decision: Beads does NOT use or require Claude Skills (.claude/skills/)**

### Reasons

1. **Redundant with bd prime**
   - `bd prime` already provides workflow context (~1-2k tokens)
   - Skills would duplicate this information
   - More systems = more complexity

2. **Simplicity is core to beads**
   - Workflow fits in simple command set: ready → create → update → close → sync
   - Already well-documented in ~1-2k tokens
   - Complex workflow orchestration not needed

3. **Editor agnostic**
   - Skills are Claude-specific
   - Breaks beads' editor-agnostic philosophy
   - Cursor, Windsurf, Zed, etc. wouldn't benefit

4. **Maintenance burden**
   - Another system to document and test
   - Another thing that can drift out of sync
   - Another migration path when things change

### If Skills were needed...

They should be:
- Provided by the beads plugin (not bd core tool)
- Complementary (not replacing) bd prime
- Optional power-user workflows only
- Opt-in, never required

### Current approach is better

- ✅ `bd prime` - Universal context injection
- ✅ Hooks - Automatic context refresh
- ✅ Plugin - Optional Claude-specific enhancements
- ✅ MCP - Optional native tool access (legacy)
- ❌ Skills - Unnecessary complexity

Users who want custom Skills can create their own, but beads doesn't ship with or require them.

## Installation

```bash
# Install Claude Code hooks globally
bd setup claude

# Install for this project only
bd setup claude --project

# Use stealth mode (flush only, no git operations)
bd setup claude --stealth

# Check installation status
bd setup claude --check

# Remove hooks
bd setup claude --remove
```

**What it installs:**
- SessionStart hook: Runs `bd prime` when Claude Code starts a session
- SessionStart hook: Runs `bd prime` when Claude Code starts a session and after compaction

## Related Files

- `cmd/bd/prime.go` - Context generation
- `cmd/bd/setup/claude.go` - Hook installation
- `cmd/bd/doctor/claude.go` - Integration verification
- `docs/CLAUDE.md` - General project guidance for Claude

## References

- [Claude Skills Documentation](https://support.claude.com/en/articles/12580051-teach-claude-your-way-of-working-using-skills)
- [Claude Skills Best Practices](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/best-practices)
