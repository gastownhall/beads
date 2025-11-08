# Agent Mail Integration Example

This example demonstrates using bd with **Agent Mail** for multi-agent coordination. It shows how to handle reservation conflicts, graceful degradation, and best practices for real-time collaboration.

## Quick Start

### Prerequisites

1. **Install bd** (0.21.0+):
   ```bash
   go install github.com/steveyegge/beads/cmd/bd@latest
   ```

2. **Install Agent Mail server**:
   ```bash
   git clone https://github.com/Dicklesworthstone/mcp_agent_mail.git
   cd mcp_agent_mail
   python3 -m venv .venv
   source .venv/bin/activate
   pip install -e .
   ```

3. **Initialize beads database**:
   ```bash
   bd init --prefix bd
   ```

4. **Create some test issues**:
   ```bash
   bd create "Implement login feature" -t feature -p 1
   bd create "Add database migrations" -t task -p 1
   bd create "Fix bug in auth flow" -t bug -p 0
   bd create "Write integration tests" -t task -p 2
   ```

## Usage Scenarios

### Scenario 1: Single Agent (Git-Only Mode)

No Agent Mail server required. The agent works in traditional git-sync mode:

```bash
# Run agent without Agent Mail
./agent_with_mail.py --agent-name alice --project-id myproject
```

**What happens:**
- Agent finds ready work using `bd ready`
- Claims issues by updating status to `in_progress`
- Completes work and closes issues
- All coordination happens via git (2-5 second latency)

### Scenario 2: Multi-Agent with Agent Mail

Start the Agent Mail server and run multiple agents:

**Terminal 1 - Start Agent Mail server:**
```bash
cd ~/mcp_agent_mail
source .venv/bin/activate
python -m mcp_agent_mail.cli serve-http
# Server runs on http://127.0.0.1:8765
```

**Terminal 2 - First agent:**
```bash
./agent_with_mail.py \
  --agent-name alice \
  --project-id myproject \
  --agent-mail-url http://127.0.0.1:8765 \
  --max-iterations 5
```

**Terminal 3 - Second agent:**
```bash
./agent_with_mail.py \
  --agent-name bob \
  --project-id myproject \
  --agent-mail-url http://127.0.0.1:8765 \
  --max-iterations 5
```

**Terminal 4 - Monitor (optional):**
```bash
# Watch reservations in real-time
open http://127.0.0.1:8765/mail
```

**What happens:**
- Both agents query for ready work
- First agent to claim an issue gets exclusive reservation
- Second agent gets reservation conflict and tries different work
- Coordination happens in <100ms via Agent Mail
- No duplicate work, no git collisions

### Scenario 3: Environment Variables

Set Agent Mail configuration globally:

```bash
# In your shell profile (~/.bashrc, ~/.zshrc)
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=my-agent
export BEADS_PROJECT_ID=my-project

# Now all bd commands use Agent Mail automatically
./agent_with_mail.py --max-iterations 3
```

### Scenario 4: Graceful Degradation

Start an agent with Agent Mail enabled, then stop the server mid-run:

**Terminal 1:**
```bash
# Start server
cd ~/mcp_agent_mail
source .venv/bin/activate
python -m mcp_agent_mail.cli serve-http
```

**Terminal 2:**
```bash
# Start agent
./agent_with_mail.py \
  --agent-name charlie \
  --agent-mail-url http://127.0.0.1:8765 \
  --max-iterations 10
```

**Terminal 1 (after a few iterations):**
```bash
# Stop server (Ctrl+C)
^C
```

**What happens:**
- Agent starts in Agent Mail mode (<100ms latency)
- After server stops, agent automatically falls back to git-only mode
- No errors, no crashes - work continues normally
- Only difference is increased latency (2-5 seconds)

## Example Output

### With Agent Mail (Successful Reservation)

```
✨ Agent Mail enabled: alice @ http://127.0.0.1:8765

🚀 Agent 'alice' starting...
   Project: myproject
   Agent Mail: Enabled

============================================================
Iteration 1/5
============================================================

📋 Claiming issue: bd-42
   ✅ Successfully claimed bd-42
🤖 Working on: Implement login feature (bd-42)
   Priority: 1, Type: feature
💡 Creating discovered issue: Follow-up work for Implement login feature
   ✅ Created bd-43
🔗 Linked bd-43 ← discovered-from ← bd-42
✅ Completing issue: bd-42
   ✅ Issue bd-42 completed
```

### With Agent Mail (Reservation Conflict)

```
✨ Agent Mail enabled: bob @ http://127.0.0.1:8765

🚀 Agent 'bob' starting...
   Project: myproject
   Agent Mail: Enabled

============================================================
Iteration 1/5
============================================================

📋 Claiming issue: bd-42
⚠️  Reservation conflict: Error: bd-42 already reserved by alice
   ⚠️  Issue bd-42 already claimed by another agent
📋 Claiming issue: bd-44
   ✅ Successfully claimed bd-44
🤖 Working on: Write integration tests (bd-44)
   Priority: 2, Type: task
```

### Git-Only Mode (No Agent Mail)

```
📝 Git-only mode: charlie

🚀 Agent 'charlie' starting...
   Project: myproject
   Agent Mail: Disabled (git-only mode)

============================================================
Iteration 1/5
============================================================

📋 Claiming issue: bd-42
   ✅ Successfully claimed bd-42
🤖 Working on: Implement login feature (bd-42)
   Priority: 1, Type: feature
```

## Code Walkthrough

### Key Methods

**`__init__`**: Configure Agent Mail environment variables
```python
if self.agent_mail_url:
    os.environ["BEADS_AGENT_MAIL_URL"] = self.agent_mail_url
    os.environ["BEADS_AGENT_NAME"] = self.agent_name
    os.environ["BEADS_PROJECT_ID"] = self.project_id
```

**`run_bd`**: Execute bd commands with error handling
```python
result = subprocess.run(["bd"] + list(args) + ["--json"], ...)
if "already reserved" in result.stderr:
    return {"error": "reservation_conflict"}
```

**`claim_issue`**: Try to claim an issue, handle conflicts
```python
result = self.run_bd("update", issue_id, "--status", "in_progress")
if result["error"] == "reservation_conflict":
    return False  # Try different issue
```

**`complete_issue`**: Close issue and release reservation
```python
self.run_bd("close", issue_id, "--reason", reason)
# Agent Mail automatically releases reservation
```

### Error Handling

The agent handles three types of failures:

1. **Reservation conflicts** - Expected in multi-agent workflows:
   ```python
   if "reservation_conflict" in result:
       print("⚠️  Issue already claimed by another agent")
       return False  # Try different work
   ```

2. **Agent Mail unavailable** - Graceful degradation:
   ```python
   # bd automatically falls back to git-only mode
   # No special handling needed!
   ```

3. **Command failures** - General errors:
   ```python
   if returncode != 0:
       print(f"❌ Command failed: {stderr}")
       return {"error": "command_failed"}
   ```

## Integration Tips

### Real LLM Agents

To integrate with Claude, GPT-4, or other LLMs:

1. **Replace `simulate_work()` with LLM calls**:
   ```python
   def simulate_work(self, issue: Dict[str, Any]) -> None:
       # Call LLM with issue context
       prompt = f"Implement: {issue['title']}\nDescription: {issue['description']}"
       response = llm_client.generate(prompt)
       
       # Parse response for new issues/bugs
       if "TODO" in response or "BUG" in response:
           self.create_discovered_issue(
               "Found during work",
               issue["id"]
           )
   ```

2. **Use issue IDs for conversation context**:
   ```python
   # Track conversation history per issue
   conversation_history[issue["id"]].append({
       "role": "user",
       "content": issue["description"]
   })
   ```

3. **Export state after each iteration**:
   ```python
   # Ensure git state is synced
   subprocess.run(["bd", "sync"])
   ```

### CI/CD Integration

Run agents in GitHub Actions with Agent Mail:

```yaml
jobs:
  agent-workflow:
    runs-on: ubuntu-latest
    services:
      agent-mail:
        image: ghcr.io/dicklesworthstone/mcp_agent_mail:latest
        ports:
          - 8765:8765
    
    strategy:
      matrix:
        agent: [alice, bob, charlie]
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Run agent
        env:
          BEADS_AGENT_MAIL_URL: http://localhost:8765
          BEADS_AGENT_NAME: ${{ matrix.agent }}
          BEADS_PROJECT_ID: ${{ github.repository }}
        run: |
          ./examples/python-agent/agent_with_mail.py --max-iterations 3
```

### Monitoring & Debugging

**View reservations in real-time:**
```bash
# Web UI
open http://127.0.0.1:8765/mail

# API
curl http://127.0.0.1:8765/api/reservations | jq
```

**Check Agent Mail connectivity:**
```bash
# Health check
curl http://127.0.0.1:8765/health

# Test reservation
curl -X POST http://127.0.0.1:8765/api/reservations \
  -H "Content-Type: application/json" \
  -d '{"resource_id": "bd-test", "agent_id": "test-agent", "project_id": "test"}'
```

**Debug agent behavior:**
```bash
# Increase verbosity
./agent_with_mail.py --agent-name debug-agent --max-iterations 1

# Check bd Agent Mail status
bd info --json | grep -A5 agent_mail
```

## Common Issues

### "Agent Mail unavailable" warnings

**Cause:** Server not running or wrong URL

**Solution:**
```bash
# Verify server is running
curl http://127.0.0.1:8765/health

# Check environment variables
echo $BEADS_AGENT_MAIL_URL
echo $BEADS_AGENT_NAME
echo $BEADS_PROJECT_ID
```

### Reservations not released after crash

**Cause:** Agent crashed before calling `bd close`

**Solution:**
```bash
# Manual release via API
curl -X DELETE http://127.0.0.1:8765/api/reservations/bd-42

# Or restart server (clears all ephemeral state)
pkill -f mcp_agent_mail
python -m mcp_agent_mail.cli serve-http
```

### Agents don't see each other's reservations

**Cause:** Different `BEADS_PROJECT_ID` values

**Solution:**
```bash
# Ensure all agents use SAME project ID
export BEADS_PROJECT_ID=my-project  # All agents must use this!

# Verify
./agent_with_mail.py --agent-name alice &
./agent_with_mail.py --agent-name bob &
# Both should coordinate on same namespace
```

## See Also

- [../../docs/AGENT_MAIL.md](../../docs/AGENT_MAIL.md) - Complete Agent Mail integration guide
- [../../docs/adr/002-agent-mail-integration.md](../../docs/adr/002-agent-mail-integration.md) - Architecture decision record
- [agent.py](agent.py) - Original agent example (git-only mode)
- [Agent Mail Repository](https://github.com/Dicklesworthstone/mcp_agent_mail)

## License

Apache 2.0 (same as beads)
