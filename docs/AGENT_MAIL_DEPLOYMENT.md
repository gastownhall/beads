# Agent Mail Deployment - Last Mile Steps

Complete step-by-step guide to deploy Agent Mail across all 12 beads-enabled workspaces.

## Prerequisites

- ✅ MCP Agent Mail package installed at `~/src/mcp_agent_mail/`
- ✅ beads integration code completed (just finished)
- ✅ Helper scripts created in `scripts/`
- ✅ direnv installed (`brew install direnv`)
- ✅ direnv hook added to shell config

## Architecture Overview

**One Server, Three Channels:**
- **Agent Mail Server**: Single instance on `http://127.0.0.1:8765`
- **beads.dev**: 5 beads workers communicate here
- **vc.dev**: 5 vc workers communicate here  
- **wyvern.dev**: 3 wyvern workers communicate here

**Total: 13 workspaces** (12 repos + main beads for version bump)

## Step 1: Version Bump (5 minutes)

Since Agent Mail integration is brand new, bump beads version first.

```bash
cd ~/src/fred/beads

# Determine new version (check current first)
./bd --version
# Example output: bd version 0.16.0

# Bump to next version
./scripts/bump-version.sh 0.23.0 --commit

# Push to trigger release
git push origin main
git push origin v0.23.0
```

**Rationale:** All workspaces should use the new version with Agent Mail support.

## Step 2: Start Agent Mail Server (2 minutes)

```bash
cd ~/src/fred/beads

# Start server in background
./scripts/start-agent-mail-server.sh

# Expected output:
# ✅ Agent Mail server started successfully!
#    PID: 12345
#    Health: http://127.0.0.1:8765/health
#    Web UI: http://127.0.0.1:8765/mail

# Verify server health
curl http://127.0.0.1:8765/health
# Expected: {"status": "healthy"}
```

**Troubleshooting:**
- If port 8765 in use: `lsof -i :8765` then `kill <PID>`
- If server fails: Check `~/agent-mail.log` for errors
- If venv missing: See installation steps in `docs/AGENT_MAIL_QUICKSTART.md`

## Step 3: Configure All Workspaces (10 minutes)

Run setup script in each workspace to create `.envrc` files.

### 3a. Configure 5 beads repos

```bash
# Main beads repo
cd ~/src/beads
../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# cino/beads fork
cd ~/src/cino/beads
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# dave/beads fork
cd ~/src/dave/beads
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# emma/beads fork
cd ~/src/emma/beads
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# fred/beads fork (current repo)
cd ~/src/fred/beads
./scripts/setup-agent-mail-workspace.sh .
direnv allow
```

**Expected .envrc content:**
```bash
# Agent Mail Configuration
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=fred-beads-macbook  # (varies by workspace/hostname)
export BEADS_PROJECT_ID=beads.dev
```

### 3b. Configure 5 vc repos

```bash
# Main vc repo (if standalone exists)
cd ~/src/vc
../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# cino/vc
cd ~/src/cino/vc
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# dave/vc
cd ~/src/dave/vc
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# fred/vc
cd ~/src/fred/vc
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# (One more standalone vc if it exists - adjust path as needed)
```

**Expected PROJECT_ID:** `vc.dev`

### 3c. Configure 3 wyvern repos

```bash
# Main wyvern repo
cd ~/src/wyvern
../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# cino/wyvern
cd ~/src/cino/wyvern
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow

# fred/wyvern
cd ~/src/fred/wyvern
../../fred/beads/scripts/setup-agent-mail-workspace.sh .
direnv allow
```

**Expected PROJECT_ID:** `wyvern.dev`

## Step 4: Upgrade beads Binary Everywhere (5 minutes)

After version bump in Step 1, upgrade all workspaces to new version.

```bash
# Wait for GitHub release to build (check https://github.com/steveyegge/beads/releases)
# Or build locally if impatient

# Option 1: Install from release (when ready)
curl -sSL https://raw.githubusercontent.com/steveyegge/beads/main/install.sh | bash

# Option 2: Build locally and copy to all repos
cd ~/src/fred/beads
go build -o bd ./cmd/bd

# Copy to other repos (example for beads repos)
cp bd ~/src/beads/bd
cp bd ~/src/cino/beads/bd
cp bd ~/src/dave/beads/bd
cp bd ~/src/emma/beads/bd

# Verify new version includes Agent Mail support
cd ~/src/fred/beads
./bd --version
# Expected: bd version 0.23.0 (or whatever you bumped to)

./bd info --json | grep -i agent
# Expected: JSON output showing agent_mail config
```

## Step 5: Document Configuration (2 minutes)

Add Agent Mail config to each workspace's AGENTS.md (if applicable). Agents need these instructions before testing.

Example for vc repos:

```bash
cd ~/src/fred/vc

# Add to AGENTS.md (or create if missing)
cat >> AGENTS.md <<'EOF'

## Agent Mail Configuration

This workspace participates in multi-agent coordination via MCP Agent Mail.

**Channel**: vc.dev (shared with all vc workers)
**Server**: http://127.0.0.1:8765
**Agent Name**: fred-vc-<hostname>

**Configuration**: Loaded automatically via `.envrc` (direnv)

**Tightly coupled workers**:
- ~/src/vc
- ~/src/cino/vc
- ~/src/dave/vc
- ~/src/fred/vc
- (one more standalone)

All vc workers coordinate issue reservations in real-time (<100ms latency).

**Cross-project coordination**: vc → beads bugs filed via git/PRs (not Agent Mail messaging)

See `docs/AGENT_MAIL_MULTI_WORKSPACE_SETUP.md` for details.
EOF
```

Repeat for beads and wyvern workspaces with appropriate channel names.

## Step 6: Test Same-Channel Coordination (5 minutes)

Verify agents in same channel can see each other's reservations.

### 6a. Test vc.dev channel (2 vc repos)

**Terminal 1 - fred/vc:**
```bash
cd ~/src/fred/vc

# Verify env vars
echo $BEADS_PROJECT_ID  # Expected: vc.dev
echo $BEADS_AGENT_NAME  # Expected: fred-vc-<hostname>

# Create test issue and reserve it
bd create "Test Agent Mail coordination" -p 2 -t task
# Example output: bd-test42

bd update bd-test42 --status in_progress
# Expected: ✅ Reserved bd-test42 for fred-vc-macbook
```

**Terminal 2 - cino/vc:**
```bash
cd ~/src/cino/vc

# Verify env vars
echo $BEADS_PROJECT_ID  # Expected: vc.dev
echo $BEADS_AGENT_NAME  # Expected: cino-vc-<hostname>

# Try to claim same issue
bd update bd-test42 --status in_progress
# Expected: ❌ Error - bd-test42 already reserved by fred-vc-macbook
```

**Success!** Collision prevented across different repos in same channel.

**Terminal 1 - Cleanup:**
```bash
cd ~/src/fred/vc
bd close bd-test42 "Test complete"
# Expected: ✅ Reservation released
```

### 6b. Test channel isolation

Verify agents in different channels DON'T interfere.

**Terminal 1 - fred/beads (beads.dev):**
```bash
cd ~/src/fred/beads
bd create "Test channel isolation" -p 2
# Example: Created bd-test1

bd update bd-test1 --status in_progress
# Expected: Success
```

**Terminal 2 - fred/vc (vc.dev):**
```bash
cd ~/src/fred/vc
bd create "Test channel isolation" -p 2
# Example: Created bd-test2

bd update bd-test2 --status in_progress
# Expected: Success (no conflict - different channel!)
```

**Both reservations succeed** because they're in different channels.

## Step 7: Monitor All Channels (2 minutes)

```bash
cd ~/src/fred/beads

# Check overall status
./scripts/agent-mail-status.sh

# Expected output:
# === Agent Mail Status ===
# Server Process: ✅ Running (PID: 12345)
# Server Health: ✅ OK
# Active Projects:
#   • beads.dev
#   • vc.dev
#   • wyvern.dev
# Active File Reservations:
#   (list of current reservations across all channels)

# Open Web UI for visual monitoring
open http://127.0.0.1:8765/mail
```

## Step 8: Make Server Auto-Start on Reboot (Optional, 5 minutes)

Use macOS launchd for automatic server startup.

```bash
# Create launchd plist
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

# Verify loaded
launchctl list | grep mcp-agent-mail
# Expected: Shows PID and status

# Test restart
sudo reboot  # (or just log out/in)
# After reboot, verify server auto-started:
curl http://127.0.0.1:8765/health
```

## Step 9: Auto-Start on Reboot (Optional, 5 minutes)

Use macOS launchd for automatic server startup.

```bash
# Create launchd plist
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

# Verify loaded
launchctl list | grep mcp-agent-mail
# Expected: Shows PID and status

# Test restart
sudo reboot  # (or just log out/in)
# After reboot, verify server auto-started:
curl http://127.0.0.1:8765/health
```

## Step 10: Verification Checklist

Run through this checklist to confirm deployment success.

### ✅ Server Health
- [ ] `curl http://127.0.0.1:8765/health` returns `{"status": "healthy"}`
- [ ] PID file exists: `cat ~/agent-mail.pid`
- [ ] Process running: `ps -p $(cat ~/agent-mail.pid)`
- [ ] Logs clean: `tail ~/agent-mail.log` (no errors)

### ✅ Workspace Configuration
- [ ] All 13 workspaces have `.envrc` files
- [ ] All `.envrc` files have correct `BEADS_PROJECT_ID`:
  - 5 beads repos → `beads.dev`
  - 5 vc repos → `vc.dev`
  - 3 wyvern repos → `wyvern.dev`
- [ ] All workspaces allowed: `direnv allow` in each
- [ ] Env vars load automatically when `cd`-ing into workspace

### ✅ beads Binary
- [ ] All workspaces using new version with Agent Mail support
- [ ] `bd --version` shows 0.23.0+ everywhere
- [ ] `bd info --json | grep agent_mail` shows config

### ✅ Multi-Agent Coordination
- [ ] Same channel: Reservation conflict works (tested in Step 6a)
- [ ] Different channels: No interference (tested in Step 6b)
- [ ] Web UI shows all 3 channels: http://127.0.0.1:8765/mail
- [ ] Status script works: `./scripts/agent-mail-status.sh`

### ✅ Persistence
- [ ] Server survives reboot (if launchd configured in Step 8)
- [ ] Reservations cleared on server restart (expected behavior)
- [ ] Agents re-register automatically after server restart

## Common Issues

### Issue: direnv not loading .envrc

**Symptoms:**
```bash
cd ~/src/fred/beads
echo $BEADS_PROJECT_ID
# (empty output)
```

**Fix:**
```bash
# Check direnv hook installed
grep direnv ~/.zshrc
# Should see: eval "$(direnv hook zsh)"

# If missing, add it
echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc
source ~/.zshrc

# Allow .envrc
cd ~/src/fred/beads
direnv allow
```

### Issue: Agent names collide

**Symptoms:** Two workspaces use same `BEADS_AGENT_NAME`

**Fix:** Edit `.envrc` to make agent names unique:
```bash
# Bad (collision!)
export BEADS_AGENT_NAME=fred-beads-macbook  # Same in fred/beads and fred/vc

# Good (unique)
export BEADS_AGENT_NAME=fred-beads-macbook   # In fred/beads
export BEADS_AGENT_NAME=fred-vc-macbook      # In fred/vc
```

The `setup-agent-mail-workspace.sh` script already handles this by including workspace name.

### Issue: Server not accessible

**Symptoms:**
```bash
bd update bd-42 --status in_progress
# WARN Agent Mail unavailable, falling back to git-only mode
```

**Fix:**
```bash
# Check server health
curl http://127.0.0.1:8765/health
# If unreachable, restart server:
./scripts/stop-agent-mail-server.sh
./scripts/start-agent-mail-server.sh
```

### Issue: Old reservations stuck

**Symptoms:** Agent crashed but reservation persists

**Fix:**
```bash
# Option 1: Release via API
curl -X DELETE http://127.0.0.1:8765/api/reservations/bd-stuck

# Option 2: Restart server (clears all)
./scripts/stop-agent-mail-server.sh
./scripts/start-agent-mail-server.sh
```

## Maintenance

### Daily Operations

**Start/stop server manually:**
```bash
cd ~/src/fred/beads
./scripts/start-agent-mail-server.sh
./scripts/stop-agent-mail-server.sh
```

**Check status:**
```bash
./scripts/agent-mail-status.sh
```

**View logs:**
```bash
tail -f ~/agent-mail.log
```

**Monitor Web UI:**
```bash
open http://127.0.0.1:8765/mail
```

### Upgrading beads

When you bump beads version in the future:

```bash
cd ~/src/fred/beads

# Bump version
./scripts/bump-version.sh 0.24.0 --commit
git push origin main
git push origin v0.24.0

# Wait for release, then upgrade all workspaces
curl -sSL https://raw.githubusercontent.com/steveyegge/beads/main/install.sh | bash

# Or rebuild locally and distribute
go build -o bd ./cmd/bd
# ... copy to other repos
```

### Adding New Workspaces

To add a new workspace to Agent Mail coordination:

```bash
# Example: Adding ~/src/gina/beads
cd ~/src/gina/beads

# Run setup script
~/src/fred/beads/scripts/setup-agent-mail-workspace.sh .

# Allow direnv
direnv allow

# Verify configuration
echo $BEADS_PROJECT_ID
# Expected: beads.dev (or vc.dev/wyvern.dev)

# Test reservation
bd ready | head -1 | xargs -I {} bd update {} --status in_progress
# Should work immediately
```

## Success Criteria

You've successfully deployed Agent Mail when:

1. ✅ **Server running**: `curl http://127.0.0.1:8765/health` returns healthy
2. ✅ **All workspaces configured**: 13 `.envrc` files created and allowed
3. ✅ **Three channels active**: beads.dev, vc.dev, wyvern.dev visible in Web UI
4. ✅ **Coordination works**: Reservation conflict test passes (Step 6a)
5. ✅ **Isolation works**: Different channels don't interfere (Step 6b)
6. ✅ **Monitoring works**: Status script shows all active projects
7. ✅ **Auto-start works**: Server survives reboot (if Step 8 completed)

## Next Steps

After successful deployment:

1. **Start using bd normally** - Agent Mail coordination happens automatically
2. **Monitor reservations** via Web UI during concurrent work
3. **File issues** for any coordination bugs (rare with hash-based IDs)
4. **Consider MCP integration** for Claude Desktop (see `docs/AGENT_MAIL_QUICKSTART.md`)
5. **Wait for cross-project messaging** (planned in Agent Mail roadmap)

## Reference

- **Main Guide**: [AGENT_MAIL_MULTI_WORKSPACE_SETUP.md](AGENT_MAIL_MULTI_WORKSPACE_SETUP.md)
- **Quick Start**: [AGENT_MAIL_QUICKSTART.md](AGENT_MAIL_QUICKSTART.md)
- **Architecture**: [AGENT_MAIL.md](AGENT_MAIL.md)
- **ADR**: [adr/002-agent-mail-integration.md](../adr/002-agent-mail-integration.md)

## Timeline Estimate

- **Step 1** (Version bump): 5 minutes
- **Step 2** (Start server): 2 minutes
- **Step 3** (Configure 13 workspaces): 10 minutes
- **Step 4** (Upgrade binaries): 5 minutes
- **Step 5** (Document): 2 minutes
- **Step 6** (Same-channel test): 5 minutes
- **Step 7** (Channel isolation test): 5 minutes
- **Step 8** (Monitor): 2 minutes
- **Step 9** (Auto-start, optional): 5 minutes
- **Step 10** (Verify checklist): 5 minutes

**Total: ~46 minutes** (or ~41 minutes if skipping auto-start)

You're ready to deploy! 🚀
