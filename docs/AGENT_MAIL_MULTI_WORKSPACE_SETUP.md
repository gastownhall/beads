# Multi-Workspace Agent Mail Setup

Guide for running Agent Mail across multiple beads repositories.

## Service Management

### Start Agent Mail Server (One Instance for All Projects)

```bash
# Start in background with nohup
cd ~/src/mcp_agent_mail
source .venv/bin/activate
nohup python -m mcp_agent_mail.cli serve-http --host 127.0.0.1 --port 8765 > ~/agent-mail.log 2>&1 &
echo $! > ~/agent-mail.pid
```

### Check Server Status

```bash
# Health check
curl http://127.0.0.1:8765/health

# View logs
tail -f ~/agent-mail.log

# Check process
ps aux | grep mcp_agent_mail
# Or use saved PID
ps -p $(cat ~/agent-mail.pid) || echo "Server not running"
```

### Restart Server

```bash
# Kill old server
kill $(cat ~/agent-mail.pid) 2>/dev/null || pkill -f mcp_agent_mail

# Start fresh
cd ~/src/mcp_agent_mail
source .venv/bin/activate
nohup python -m mcp_agent_mail.cli serve-http --host 127.0.0.1 --port 8765 > ~/agent-mail.log 2>&1 &
echo $! > ~/agent-mail.pid
```

### Auto-Start on Reboot (macOS launchd)

```bash
# Create ~/Library/LaunchAgents/com.user.mcp-agent-mail.plist
cat > ~/Library/LaunchAgents/com.user.mcp-agent-mail.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.user.mcp-agent-mail</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/stevey/src/mcp_agent_mail/.venv/bin/python</string>
        <string>-m</string>
        <string>mcp_agent_mail.cli</string>
        <string>serve-http</string>
        <string>--host</string>
        <string>127.0.0.1</string>
        <string>--port</string>
        <string>8765</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/Users/stevey/src/mcp_agent_mail</string>
    <key>StandardOutPath</key>
    <string>/Users/stevey/agent-mail.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/stevey/agent-mail-error.log</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
EOF

# Load service
launchctl load ~/Library/LaunchAgents/com.user.mcp-agent-mail.plist

# Check status
launchctl list | grep mcp-agent-mail
```

## Project Configuration Strategy

**Three team channels** for tightly-coupled workers:

```bash
beads.dev    → All beads workers (5 repos)
vc.dev       → All vc workers (5 repos)
wyvern.dev   → All wyvern workers (3 repos)
```

**Why this design:**
- Each team's workers are tightly coupled and need frequent coordination
- Simple namespace strings (no filesystem paths required)
- Usenet-style naming for clarity
- Cross-project filing handled via git/PRs for now (until Agent Mail adds cross-project messaging)

### Configuration by Workspace

```bash
# All beads repos use same project
~/src/beads           → BEADS_PROJECT_ID=beads.dev
~/src/cino/beads      → BEADS_PROJECT_ID=beads.dev
~/src/dave/beads      → BEADS_PROJECT_ID=beads.dev
~/src/emma/beads      → BEADS_PROJECT_ID=beads.dev
~/src/fred/beads      → BEADS_PROJECT_ID=beads.dev

# All vc repos use same project
~/src/cino/vc         → BEADS_PROJECT_ID=vc.dev
~/src/dave/vc         → BEADS_PROJECT_ID=vc.dev
~/src/fred/vc         → BEADS_PROJECT_ID=vc.dev
~/src/vc              → BEADS_PROJECT_ID=vc.dev
(standalone at ~/src/vc if exists)

# All wyvern repos use same project
~/src/cino/wyvern     → BEADS_PROJECT_ID=wyvern.dev
~/src/fred/wyvern     → BEADS_PROJECT_ID=wyvern.dev
~/src/wyvern          → BEADS_PROJECT_ID=wyvern.dev
```

## Per-Directory Configuration

Create `.envrc` or `.env` file in each workspace:

### Example: ~/src/fred/vc/.envrc

```bash
# Agent Mail Configuration for fred's vc
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=fred-vc-$(hostname)
export BEADS_PROJECT_ID=vc.dev

# Load with direnv
# Install direnv: brew install direnv
# Then: direnv allow
```

### Example: ~/src/cino/beads/.envrc

```bash
# Agent Mail Configuration for cino's beads fork
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=cino-beads-$(hostname)
export BEADS_PROJECT_ID=beads.dev
```

### Quick Setup Script

```bash
#!/bin/bash
# setup-agent-mail.sh - Run in each workspace

WORKSPACE=$(pwd)
WORKSPACE_NAME=$(basename $WORKSPACE)
PARENT=$(basename $(dirname $WORKSPACE))

# Determine project ID based on coupling
case "$WORKSPACE_NAME" in
    vc|wyvern)
        # Tightly coupled - use parent coordination project
        PROJECT_ID="/Users/stevey/src/${PARENT}/coordination"
        ;;
    beads)
        # Loosely coupled - use workspace as project
        PROJECT_ID="$WORKSPACE"
        ;;
    *)
        # Default - use workspace
        PROJECT_ID="$WORKSPACE"
        ;;
esac

cat > .envrc <<EOF
# Agent Mail Configuration
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=${PARENT}-${WORKSPACE_NAME}-\$(hostname)
export BEADS_PROJECT_ID=$PROJECT_ID
EOF

echo "Created .envrc with PROJECT_ID=$PROJECT_ID"
echo "Run: direnv allow"
```

## Project Relationship Matrix

| Workspace | Project Channel | Agent Name | Can Message |
|-----------|----------------|------------|-------------|
| ~/src/beads | beads.dev | main-beads-* | All beads workers ✅ |
| ~/src/cino/beads | beads.dev | cino-beads-* | All beads workers ✅ |
| ~/src/dave/beads | beads.dev | dave-beads-* | All beads workers ✅ |
| ~/src/emma/beads | beads.dev | emma-beads-* | All beads workers ✅ |
| ~/src/fred/beads | beads.dev | fred-beads-* | All beads workers ✅ |
| ~/src/cino/vc | vc.dev | cino-vc-* | All vc workers ✅ |
| ~/src/dave/vc | vc.dev | dave-vc-* | All vc workers ✅ |
| ~/src/fred/vc | vc.dev | fred-vc-* | All vc workers ✅ |
| ~/src/vc | vc.dev | main-vc-* | All vc workers ✅ |
| ~/src/cino/wyvern | wyvern.dev | cino-wyvern-* | All wyvern workers ✅ |
| ~/src/fred/wyvern | wyvern.dev | fred-wyvern-* | All wyvern workers ✅ |
| ~/src/wyvern | wyvern.dev | main-wyvern-* | All wyvern workers ✅ |

## Monitoring Multiple Projects

### Web UI

View all projects and agents:
```bash
open http://127.0.0.1:8765/mail
```

### API Queries

```bash
# List all projects
curl http://127.0.0.1:8765/api/projects | jq

# View agents in specific project
curl "http://127.0.0.1:8765/api/projects/$(urlencode /Users/stevey/src/fred/coordination)/agents" | jq

# Check file reservations across all projects
curl http://127.0.0.1:8765/api/file_reservations | jq
```

### Unified Dashboard Script

```bash
#!/bin/bash
# agent-mail-status.sh - View all active agents and reservations

echo "=== Agent Mail Status ==="
echo

echo "Server Health:"
curl -s http://127.0.0.1:8765/health | jq -r '.status // "UNREACHABLE"'
echo

echo "Active Projects:"
curl -s http://127.0.0.1:8765/api/projects | jq -r '.[] | "\(.slug) - \(.human_key)"'
echo

echo "Active Reservations:"
curl -s http://127.0.0.1:8765/api/file_reservations | jq -r '.[] | "\(.agent_name) → \(.resource_id) (\(.project_id))"'
```

## Future: Cross-Project Messaging

When Agent Mail adds cross-project support (planned), you'll be able to:

```bash
# Send message from fred/vc to cino/vc
bd agent-mail send \
  --from fred-vc \
  --to cino-vc \
  --to-project /Users/stevey/src/cino/coordination \
  --subject "API changes in shared library"
```

For now, use git commits/PRs for cross-project coordination.

## Troubleshooting

### Problem: Different agents step on each other

**Symptom:** Two agents in same project both claim same issue

**Cause:** Agents using different `BEADS_AGENT_NAME` but same project

**Fix:** Ensure unique agent names per workspace:
```bash
export BEADS_AGENT_NAME=$(whoami)-$(basename $PWD)-$(hostname)
```

### Problem: Agents can't see each other

**Symptom:** Agent sends message but recipient doesn't receive it

**Cause:** Agents in different `BEADS_PROJECT_ID`

**Fix:** Verify both agents use same project ID:
```bash
# In each workspace
echo $BEADS_PROJECT_ID
```

### Problem: Reservation persists after crash

**Symptom:** Issue stays reserved after agent died

**Fix:**
```bash
# Release via API
curl -X DELETE http://127.0.0.1:8765/api/reservations/bd-stuck-issue

# Or restart server (clears all)
kill $(cat ~/agent-mail.pid)
cd ~/src/mcp_agent_mail
source .venv/bin/activate
nohup python -m mcp_agent_mail.cli serve-http --host 127.0.0.1 --port 8765 > ~/agent-mail.log 2>&1 &
echo $! > ~/agent-mail.pid
```

## Best Practices

1. **Use direnv for automatic env loading**
   ```bash
   brew install direnv
   # Add to ~/.zshrc: eval "$(direnv hook zsh)"
   # Then create .envrc in each workspace
   ```

2. **Descriptive agent names**
   ```bash
   # Bad: export BEADS_AGENT_NAME=agent1
   # Good: export BEADS_AGENT_NAME=fred-vc-macbook
   ```

3. **Monitor server logs**
   ```bash
   tail -f ~/agent-mail.log
   ```

4. **Health check in scripts**
   ```bash
   if ! curl -sf http://127.0.0.1:8765/health > /dev/null; then
     echo "WARNING: Agent Mail unavailable (falling back to git-only)"
   fi
   ```

5. **Document project relationships**
   - Keep this file updated when adding workspaces
   - Add comments in .envrc explaining project coupling

## Summary

- **One server** handles all projects (lightweight HTTP API)
- **Project ID = namespace** for agent isolation
- **Tight coupling** → shared project (vc + wyvern)
- **Loose coupling** → separate projects (beads forks)
- **Auto-restart** via launchd recommended
- **Per-directory .envrc** for automatic config loading
