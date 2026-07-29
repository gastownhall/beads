# ADR-0001: Use bd prime as CLI Reference Source of Truth

## Status

Accepted

## Context

The beads skill maintained CLI reference documentation in multiple locations:

- `SKILL.md` inline (~2,000+ words of CLI reference)
- `references/CLI_REFERENCE.md` (~2,363 words)
- Scattered examples throughout resource files

This created:
- **Duplication**: Same commands documented 2-3 times
- **Drift risk**: Documentation can fall behind bd versions
- **Token overhead**: ~3,000+ tokens loaded even for simple operations

Meanwhile, bd provides `bd prime` which generates AI-optimized workflow context automatically.

## Decision

Use `bd prime` as the single source of truth for CLI commands:

1. **SKILL.md** contains only value-add content (decision frameworks, cognitive patterns)
2. **CLI reference** points to `bd prime` (auto-loaded by hooks) and `bd --help`
3. **Resources** provide depth for advanced features (molecules, agents, gates)

## Consequences

### Positive

- **Zero maintenance**: CLI docs auto-update with bd versions
- **DRY**: Single source of truth
- **Accurate**: No version drift possible
- **Lighter SKILL.md**: ~500 words vs ~3,300

### Negative

- **Dependency on bd prime format**: If output changes significantly, may need adaptation
- **External tool requirement**: Skill assumes bd is installed

## Implementation

Files restructured:
- `SKILL.md` — Reduced from 3,306 to ~500 words
- `references/` → `resources/` — Directory rename for consistency
- New resources added: `agents.md`, `async-gates.md`, `chemistry-patterns.md`, `worktrees.md`
- Existing resources preserved with path updates

## Related

- Claude Code skill progressive disclosure guidelines
- Similar pattern implemented in other Claude Code skill ecosystems

## Date

2025-01-02
