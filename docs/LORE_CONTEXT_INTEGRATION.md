# Lore Context Integration

This guide shows how to use [Lore Context](https://github.com/Lore-Context/lore-context) alongside beads for AI agent workflows that need both structured task tracking and semantic memory.

## Why Use Both?

Beads and Lore Context solve different but complementary problems:

| Capability | Beads | Lore Context |
|---|---|---|
| Task tracking | Structured issue graph with dependencies | — |
| Work queue | Priority-based ready/blocked detection | — |
| Version history | Dolt-powered audit trail | — |
| Semantic memory | — | Episode-based context retrieval |
| Cross-session recall | — | Automatic context windows |
| Codebase context | — | Repository-aware search |

**Beads** manages *what needs to be done* (structured task graph).
**Lore Context** manages *what the agent knows* (semantic memory layer).

Together they give agents both a work queue and institutional memory.

## Setup

### MCP Configuration

Run both MCP servers side by side in the same agent session:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    },
    "lore": {
      "command": "lore-context",
      "args": ["mcp"]
    }
  }
}
```

Both servers operate on the same project directory. Beads manages `.beads/` for task data; Lore Context manages its own storage for semantic memories.

### CLI Configuration

For CLI-based agents (Claude Code, Cursor, etc.), install both tools:

```bash
# Install beads
pip install beads-mcp   # or: brew install gastownhall/tap/bd

# Install lore-context
pip install lore-context
```

Then add beads workflow context via `bd prime` and Lore Context via its own context injection mechanism.

## Agent Workflow

The recommended workflow interleaves task management (beads) with contextual recall (Lore Context):

### 1. Check Ready Work

```
Agent calls: beads.ready()          -> Returns unblocked tasks
Agent calls: lore.context(query)    -> Returns relevant memories for context
```

### 2. Claim and Research

```
Agent calls: beads.claim(bd-42)     -> Atomically claim the task
Agent calls: lore.search(query)     -> Find related past decisions, patterns
```

### 3. Implement with Context

The agent now has:
- The task definition and acceptance criteria (from beads)
- Relevant past decisions and codebase knowledge (from Lore Context)
- Dependency context — what blocks this, what this blocks (from beads)

### 4. Record and Close

```
Agent calls: lore.save(content)     -> Store new insight or decision
Agent calls: beads.close(bd-42)     -> Mark task complete
```

## Cross-Referencing

Beads issue IDs and Lore Context memories can reference each other:

### In Beads Issues

Reference Lore memories in issue descriptions or notes:

```bash
bd update bd-42 --notes "See lore-memory-abc123 for authentication pattern decision"
bd update bd-42 --description "Based on lore-memory-xyz789: we decided to use JWT over sessions"
```

### In Lore Memories

Reference beads issues in memory content:

```bash
lore save --content "Chose JWT auth over session cookies (beads bd-42). Rationale: stateless scaling."
lore save --content "Fixed race condition in worker pool (beads bd-156). Key insight: use sync.Mutex not channels."
```

This creates a bidirectional knowledge graph:
- **Beads** tracks the work (what was done, when, in what order)
- **Lore Context** tracks the knowledge (why it was done, what was learned)

## Multi-Agent Patterns

### Orchestrator + Workers

```
Orchestrator:
  1. beads.ready()              -> Find ready tasks
  2. lore.search(task_context)  -> Gather relevant memories
  3. Delegate to worker with context bundle

Worker:
  1. Claim task: beads.claim(id)
  2. Implement with context from orchestrator
  3. Store learnings: lore.save(insight)
  4. Complete task: beads.close(id)
```

### Knowledge Accumulation

As agents work through a project, Lore Context accumulates knowledge:
- Architectural decisions and rationale
- Debugging patterns that worked
- Performance optimization insights
- API quirks and workarounds

New agents joining the project get this context automatically through Lore Context recall, while beads ensures they pick up the right tasks in the right order.

## Environment Variables

Both tools use standard environment configuration:

**Beads:**
- `BEADS_WORKING_DIR` — Project directory (auto-detected from `.beads/`)
- `BEADS_ACTOR` — Agent identity for audit trail

**Lore Context:**
- `LORE_PROJECT_ID` — Project identifier for scoped memories
- `LORE_STORAGE_PATH` — Custom storage location

Both auto-detect the project directory from the current working directory when run from a git repository.

## Troubleshooting

### Both servers pointing at wrong directory

Ensure both MCP servers start from the same working directory, or set explicit paths:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp",
      "env": { "BEADS_WORKING_DIR": "/path/to/project" }
    },
    "lore": {
      "command": "lore-context",
      "args": ["mcp"],
      "env": { "LORE_PROJECT_ID": "my-project" }
    }
  }
}
```

### Context too large

Both tools offer context optimization:
- **Beads**: Use `brief=True` and `discover_tools()` for lazy schema loading
- **Lore Context**: Use `token_budget` parameter to limit returned context size

### Conflicting memories

Lore Context supports memory versioning. If beads task outcomes contradict stored memories, use Lore's supersede mechanism to update rather than creating duplicates.

## See Also

- [Beads Quickstart](QUICKSTART.md) — Getting started with beads
- [Claude Integration](CLAUDE_INTEGRATION.md) — Claude Code setup
- [Copilot Integration](COPILOT_INTEGRATION.md) — GitHub Copilot setup
- [Community Tools](COMMUNITY_TOOLS.md) — Third-party integrations
- [Lore Context Documentation](https://github.com/Lore-Context/lore-context)
