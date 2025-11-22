# Claude Code Integration Design

This document explains design decisions for Claude Code integration in beads.

## Integration Approach

Beads uses a simple, universal approach to Claude Code integration:
- `bd prime` command for context injection
- Hooks (SessionStart/PreCompact) for automatic context refresh
- Optional: Plugin for slash commands and enhanced UX
- Optional: MCP server for native tool access (legacy)

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

## Related Files

- `cmd/bd/prime.go` - Context generation
- `cmd/bd/setup/claude.go` - Hook installation
- `cmd/bd/doctor/claude.go` - Integration verification
- `docs/CLAUDE.md` - General project guidance for Claude

## References

- [Claude Skills Documentation](https://support.claude.com/en/articles/12580051-teach-claude-your-way-of-working-using-skills)
- [Claude Skills Best Practices](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/best-practices)
