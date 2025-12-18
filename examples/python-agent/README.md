# Python Agent Example

A simple Python script demonstrating how an AI agent can use bd to manage tasks.

## Features

- Finds ready work using `bd ready --json`
- Claims tasks by updating status
- Simulates discovering new issues during work
- Links discovered issues with `discovered-from` dependency
- Completes tasks and moves to the next one

## Prerequisites

- Python 3.7+
- bd installed: `go install github.com/steveyegge/beads/cmd/bd@latest`
- A beads database initialized: `bd init`

## Usage

```bash
# Make the script executable
chmod +x agent.py

# Run the agent
./agent.py
```

## What It Does

1. Queries for ready work (no blocking dependencies)
2. Claims the highest priority task
3. "Works" on the task (simulated)
4. If the task involves implementation, discovers a testing task
5. Creates the new testing task and links it with `discovered-from`
6. Completes the original task
7. Repeats until no ready work remains

## Example Output

```
🚀 Beads Agent starting...

============================================================
Iteration 1/10
============================================================

📋 Claiming task: bd-1
🤖 Working on: Implement user authentication (bd-1)
   Priority: 1, Type: feature

💡 Discovered: Missing test coverage for this feature
✨ Creating issue: Add tests for Implement user authentication
🔗 Linking bd-2 ← discovered-from ← bd-1
✅ Completing task: bd-1 - Implemented successfully

🔄 New work discovered and linked. Running another cycle...
```

## Integration with Real Agents

To integrate with a real LLM-based agent:

1. Replace `simulate_work()` with actual LLM calls
2. Parse the LLM's response for discovered issues/bugs
3. Use the issue ID to track context across conversations
4. Export/import JSONL to share state across agent sessions

## Advanced Usage

```python
# Create an agent with custom behavior
agent = BeadsAgent()

# Find specific types of work
ready = agent.run_bd("ready", "--priority", "1", "--assignee", "bot")

# Create issues with labels
agent.run_bd("create", "New task", "-l", "urgent,backend")

# Query dependency tree
tree = agent.run_bd("dep", "tree", "bd-1")
```

## See Also

- [../bash-agent/](../bash-agent/) - Bash version of this example
- [../claude-desktop-mcp/](../claude-desktop-mcp/) - MCP server for Claude Desktop
