# Agent Mail Quick Start Guide

Get started with Agent Mail for multi-agent bd coordination in 5 minutes.

## What is Agent Mail?

Agent Mail is an **optional** coordination layer for bd that reduces latency from 2-5 seconds (git sync) to <100ms (HTTP API) and prevents work collisions through file reservations.

**When to use it:**
- ✅ Multiple AI agents working concurrently
- ✅ Frequent status updates (high collision risk)
- ✅ Real-time coordination needed

**When to skip it:**
- ❌ Single agent workflows
- ❌ Infrequent updates (low collision risk)
- ❌ Simplicity preferred over latency

## 5-Minute Setup

### Step 1: Install Agent Mail Server (30 seconds)

```bash
git clone https://github.com/Dicklesworthstone/mcp_agent_mail.git ~/mcp_agent_mail
cd ~/mcp_agent_mail
python3 -m venv .venv
source .venv/bin/activate  # Windows: .venv\Scripts\activate
pip install -e .
```

### Step 2: Start the Server (5 seconds)

```bash
python -m mcp_agent_mail.cli serve-http
# ✅ Server running on http://127.0.0.1:8765
```

Leave this terminal open. Open a new terminal for Step 3.

### Step 3: Configure Your Agent (10 seconds)

```bash
# Set environment variables
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=my-agent
export BEADS_PROJECT_ID=my-project
```

### Step 4: Use bd Normally (30 seconds)

```bash
# Find ready work
bd ready

# Claim an issue
bd update bd-42 --status in_progress
# ✅ Reserved bd-42 for my-agent in <100ms

# Complete work
bd close bd-42 "Done"
# ✅ Reservation released automatically
```

**That's it!** bd now uses Agent Mail for coordination.

### Step 5: Test Multi-Agent (1 minute)

Open a second terminal:

```bash
# Terminal 2 - Different agent
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=other-agent
export BEADS_PROJECT_ID=my-project

# Try claiming same issue
bd update bd-42 --status in_progress
# ❌ Error: bd-42 already reserved by my-agent
```

**Success!** Agent Mail prevented collision.

## Common Use Cases

### Use Case 1: Claude Desktop + Command Line Agent

**Terminal 1 - Agent Mail Server:**
```bash
cd ~/mcp_agent_mail
source .venv/bin/activate
python -m mcp_agent_mail.cli serve-http
```

**Terminal 2 - Command Line:**
```bash
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=cli-user
export BEADS_PROJECT_ID=my-project

bd ready
bd update bd-100 --status in_progress
```

**Claude Desktop:**
```
# In Claude's MCP settings, add env vars:
{
  "beads": {
    "command": "beads-mcp",
    "env": {
      "BEADS_AGENT_MAIL_URL": "http://127.0.0.1:8765",
      "BEADS_AGENT_NAME": "claude",
      "BEADS_PROJECT_ID": "my-project"
    }
  }
}
```

Now Claude and your command line won't step on each other!

### Use Case 2: Multiple Python Agents

**Terminal 1 - Server:**
```bash
cd ~/mcp_agent_mail
source .venv/bin/activate
python -m mcp_agent_mail.cli serve-http
```

**Terminal 2 - Agent A:**
```bash
cd ~/myproject/examples/python-agent
./agent_with_mail.py \
  --agent-name alice \
  --project-id myproject \
  --agent-mail-url http://127.0.0.1:8765
```

**Terminal 3 - Agent B:**
```bash
cd ~/myproject/examples/python-agent
./agent_with_mail.py \
  --agent-name bob \
  --project-id myproject \
  --agent-mail-url http://127.0.0.1:8765
```

Watch them coordinate in real-time!

### Use Case 3: Team Workflow

**Shared Server (runs on dev machine):**
```bash
# Machine 192.168.1.100
python -m mcp_agent_mail.cli serve-http --host 0.0.0.0
```

**Team Member 1:**
```bash
export BEADS_AGENT_MAIL_URL=http://192.168.1.100:8765
export BEADS_AGENT_NAME=alice
export BEADS_PROJECT_ID=team-project

bd ready
```

**Team Member 2:**
```bash
export BEADS_AGENT_MAIL_URL=http://192.168.1.100:8765
export BEADS_AGENT_NAME=bob
export BEADS_PROJECT_ID=team-project

bd ready
```

Entire team shares one coordination server!

### Use Case 4: CI/CD Pipeline

**GitHub Actions Example:**

```yaml
name: AI Agent Workflow
on: [push]

jobs:
  agent-work:
    runs-on: ubuntu-latest
    
    services:
      agent-mail:
        image: ghcr.io/dicklesworthstone/mcp_agent_mail:latest
        ports:
          - 8765:8765
    
    strategy:
      matrix:
        agent: [agent-1, agent-2, agent-3]
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Run agent
        env:
          BEADS_AGENT_MAIL_URL: http://localhost:8765
          BEADS_AGENT_NAME: ${{ matrix.agent }}
          BEADS_PROJECT_ID: ${{ github.repository }}
        run: |
          bd ready | head -1 | xargs -I {} bd update {} --status in_progress
          # ... do work ...
          bd close {} "Completed by CI"
```

Three agents run in parallel without collisions!

## Verification Checklist

After setup, verify everything works:

**✅ Server is running:**
```bash
curl http://127.0.0.1:8765/health
# Expected: {"status": "healthy"}
```

**✅ Environment variables are set:**
```bash
echo $BEADS_AGENT_MAIL_URL
# Expected: http://127.0.0.1:8765

echo $BEADS_AGENT_NAME
# Expected: my-agent

echo $BEADS_PROJECT_ID
# Expected: my-project
```

**✅ bd sees Agent Mail:**
```bash
bd info --json | grep agent_mail
# Expected: JSON with agent_mail config
```

**✅ Reservations work:**
```bash
# Terminal 1
bd update bd-test --status in_progress
# Expected: Success

# Terminal 2 (different agent)
export BEADS_AGENT_NAME=other-agent
bd update bd-test --status in_progress
# Expected: Error - reservation conflict
```

**✅ Graceful degradation works:**
```bash
# Stop server (Ctrl+C in server terminal)

# Try bd command
bd ready
# Expected: Warning about Agent Mail unavailable, but command succeeds
```

## Troubleshooting

### Problem: "Agent Mail unavailable" warnings

**Symptoms:**
```
WARN Agent Mail unavailable, falling back to git-only mode
```

**Quick Fix:**
1. Check server is running: `curl http://127.0.0.1:8765/health`
2. Verify URL is correct: `echo $BEADS_AGENT_MAIL_URL`
3. Restart server if needed

### Problem: Agents don't see each other's reservations

**Cause:** Different `BEADS_PROJECT_ID` values

**Quick Fix:**
```bash
# All agents MUST use same project ID!
export BEADS_PROJECT_ID=same-project-name
```

### Problem: Reservation stuck after agent crashes

**Quick Fix:**
```bash
# Option 1: Release via API
curl -X DELETE http://127.0.0.1:8765/api/reservations/bd-stuck

# Option 2: Restart server (clears all reservations)
pkill -f mcp_agent_mail
python -m mcp_agent_mail.cli serve-http
```

### Problem: Port 8765 already in use

**Quick Fix:**
```bash
# Find what's using port
lsof -i :8765  # macOS/Linux
netstat -ano | findstr :8765  # Windows

# Kill old server
pkill -f mcp_agent_mail

# Or use different port
python -m mcp_agent_mail.cli serve-http --port 8766
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8766
```

## Monitoring

### Web UI

View all reservations in real-time:
```bash
open http://127.0.0.1:8765/mail
```

### API

Check reservations programmatically:
```bash
# List all reservations
curl http://127.0.0.1:8765/api/reservations | jq

# Check specific reservation
curl http://127.0.0.1:8765/api/reservations/bd-42 | jq

# Release reservation manually
curl -X DELETE http://127.0.0.1:8765/api/reservations/bd-42
```

### Logs

Agent Mail logs to stdout. Redirect to file if needed:
```bash
python -m mcp_agent_mail.cli serve-http > agent-mail.log 2>&1 &
tail -f agent-mail.log
```

## Best Practices

### 1. Use Descriptive Agent Names

**Bad:**
```bash
export BEADS_AGENT_NAME=agent1
export BEADS_AGENT_NAME=agent2
```

**Good:**
```bash
export BEADS_AGENT_NAME=claude-frontend
export BEADS_AGENT_NAME=gpt4-backend
export BEADS_AGENT_NAME=alice-laptop
```

Makes debugging much easier!

### 2. Set Environment Variables Globally

**Option 1: Shell Profile**
```bash
# Add to ~/.bashrc or ~/.zshrc
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=$(whoami)-$(hostname)
export BEADS_PROJECT_ID=$(basename $(pwd))
```

**Option 2: Project Config**
```bash
# .env file in project root
BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
BEADS_AGENT_NAME=my-agent
BEADS_PROJECT_ID=my-project

# Load in scripts
source .env
```

### 3. Use Same Project ID Across Team

Create a shared config:
```bash
# team-config.sh
export BEADS_PROJECT_ID=our-team-project
export BEADS_AGENT_MAIL_URL=http://agent-mail.internal:8765

# Each team member sources it
source team-config.sh
export BEADS_AGENT_NAME=alice  # Only this differs per person
```

### 4. Monitor Reservations in Long-Running Agents

```python
# Check reservation health periodically
import requests

def check_reservations():
    resp = requests.get(f"{agent_mail_url}/api/reservations")
    my_reservations = [r for r in resp.json() if r["agent_id"] == agent_name]
    
    for res in my_reservations:
        # Release if work completed
        if is_done(res["resource_id"]):
            requests.delete(f"{agent_mail_url}/api/reservations/{res['resource_id']}")
```

### 5. Handle Graceful Degradation

Always assume Agent Mail might be unavailable:
```python
try:
    bd_update(issue_id, status="in_progress")
except ReservationConflict:
    # Expected - try different issue
    pass
except Exception:
    # Agent Mail down - falls back to git
    # Continue normally
    pass
```

## Next Steps

1. **Read the full guide**: [AGENT_MAIL.md](AGENT_MAIL.md)
2. **Try the Python example**: [examples/python-agent/AGENT_MAIL_EXAMPLE.md](../examples/python-agent/AGENT_MAIL_EXAMPLE.md)
3. **Review the ADR**: [adr/002-agent-mail-integration.md](adr/002-agent-mail-integration.md)
4. **Check out benchmarks**: [../latency_results.md](../latency_results.md)

## Getting Help

**Documentation:**
- [AGENT_MAIL.md](AGENT_MAIL.md) - Complete integration guide
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) - General bd troubleshooting
- [FAQ.md](FAQ.md) - Frequently asked questions

**Issues:**
- [bd issues](https://github.com/steveyegge/beads/issues) - Integration bugs
- [Agent Mail issues](https://github.com/Dicklesworthstone/mcp_agent_mail/issues) - Server bugs

**Community:**
- [Discussions](https://github.com/steveyegge/beads/discussions) - Ask questions
- [Examples](../examples/) - Learn from working code

## TL;DR - Copy-Paste Setup

```bash
# 1. Install Agent Mail
git clone https://github.com/Dicklesworthstone/mcp_agent_mail.git ~/mcp_agent_mail
cd ~/mcp_agent_mail
python3 -m venv .venv
source .venv/bin/activate
pip install -e .

# 2. Start server (leave running)
python -m mcp_agent_mail.cli serve-http &

# 3. Configure agent (in new terminal)
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=my-agent
export BEADS_PROJECT_ID=my-project

# 4. Use bd normally - coordination happens automatically!
bd ready
bd update bd-42 --status in_progress
bd close bd-42 "Done"
```

**Done!** You're now using Agent Mail for sub-100ms coordination.
