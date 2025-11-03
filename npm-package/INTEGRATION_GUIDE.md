# Complete Integration Guide: @beads/bd + Claude Code for Web

This guide shows the complete end-to-end setup for using bd (beads) in Claude Code for Web via the npm package.

## 🎯 Goal

Enable automatic issue tracking with bd in every Claude Code for Web session with zero manual setup.

## 📋 Prerequisites

- A git repository with your project
- Claude Code for Web access

## 🚀 Quick Setup (5 Minutes)

### Step 1: Create SessionStart Hook

Create the file `.claude/hooks/session-start.sh` in your repository:

```bash
#!/bin/bash
# Auto-install bd in every Claude Code for Web session

# Install bd globally from npm
npm install -g @beads/bd

# Initialize bd if not already done
if [ ! -d .beads ]; then
  bd init --quiet
fi

# Show current work
echo ""
echo "📋 Ready work:"
bd ready --limit 5 || echo "No ready work found"
```

### Step 2: Make Hook Executable

```bash
chmod +x .claude/hooks/session-start.sh
```

### Step 3: Update AGENTS.md

Add bd usage instructions to your AGENTS.md file:

```markdown
## Issue Tracking with bd

This project uses bd (beads) for issue tracking. It's automatically installed in each session via SessionStart hook.

### Finding Work

```bash
# See what's ready to work on
bd ready --json | jq '.[0]'

# Get issue details
bd show <issue-id> --json
```

### Creating Issues

```bash
# Create a new issue
bd create "Task description" -t task -p 1 --json

# Create a bug
bd create "Bug description" -t bug -p 0 --json
```

### Working on Issues

```bash
# Update status to in_progress
bd update <issue-id> --status in_progress

# Add a comment
bd comments add <issue-id> "Progress update"

# Close when done
bd close <issue-id> --reason "Description of what was done"
```

### Managing Dependencies

```bash
# Issue A blocks issue B
bd dep add <issue-b> <issue-a>

# Show dependency tree
bd dep tree <issue-id>
```

### Best Practices

1. **Always use --json**: Makes output easy to parse programmatically
2. **Create issues proactively**: When you notice work, file it immediately
3. **Link discovered work**: Use `bd dep add --type discovered-from`
4. **Close with context**: Always provide --reason when closing
5. **Commit .beads/**: The .beads/issues.jsonl file should be committed to git
```

### Step 4: Commit and Push

```bash
git add .claude/hooks/session-start.sh AGENTS.md
git commit -m "Add bd auto-install for Claude Code for Web"
git push
```

## 🎬 How It Works

### First Session in Claude Code for Web

1. **Session starts** → Claude Code for Web creates fresh Linux VM
2. **Hook runs** → `.claude/hooks/session-start.sh` executes automatically
3. **npm install** → Downloads @beads/bd package from npm
4. **Postinstall** → Downloads native bd binary for platform (~17MB)
5. **bd init** → Imports existing issues from `.beads/issues.jsonl` in git
6. **Ready** → `bd` command is available, shows ready work

**Time: ~5-10 seconds**

### Subsequent Sessions

Same process, but:
- Git clone pulls existing `.beads/issues.jsonl`
- `bd init --quiet` imports all existing issues
- Agent picks up right where it left off

**Time: ~5-10 seconds**

## 💡 Usage Patterns

### Pattern 1: Agent Starts Session

```
Agent: Starting new session...
System: Running SessionStart hook...
System: Installing bd v0.21.5...
System: ✓ bd installed successfully
System:
System: 📋 Ready work:
System: bd-a1b2 [P1] Fix authentication bug
System: bd-f14c [P1] Add user profile page
System:
Agent: I can see there are 2 ready tasks. Let me work on bd-a1b2 (Fix authentication bug).
Agent: [runs] bd update bd-a1b2 --status in_progress
```

### Pattern 2: Discovering New Work

```
Agent: While fixing bd-a1b2, I noticed the password reset flow is also broken.
Agent: [runs] bd create "Fix password reset flow" -t bug -p 1 --json
System: {"id": "bd-3e7a", ...}
Agent: [runs] bd dep add bd-3e7a bd-a1b2 --type discovered-from
Agent: I've filed bd-3e7a for the password reset issue and linked it to the current work.
```

### Pattern 3: Completing Work

```
Agent: I've fixed the authentication bug. Tests are passing.
Agent: [runs] bd close bd-a1b2 --reason "Fixed session validation logic. Added tests."
Agent: [runs] git add . && git commit -m "Fix authentication bug (bd-a1b2)"
Agent: Work completed. Ready for next task.
```

## 🔧 Advanced Configuration

### Custom Installation Path

Install to project instead of global:

```bash
#!/bin/bash
# .claude/hooks/session-start.sh

# Install as project dependency
npm install --save-dev @beads/bd

# Use via npx
npx bd init --quiet

echo "Use 'npx bd' for all bd commands"
```

### Conditional Installation

Only install if not already present:

```bash
#!/bin/bash
# .claude/hooks/session-start.sh

if ! command -v bd &> /dev/null; then
    echo "Installing bd..."
    npm install -g @beads/bd
else
    echo "bd already available: $(bd version)"
fi

# Rest of setup...
```

### Silent Installation

Minimal output for cleaner logs:

```bash
#!/bin/bash
# .claude/hooks/session-start.sh

npm install -g @beads/bd --silent 2>&1 | grep -v "npm WARN"
bd init --quiet 2>&1 | grep -v "already initialized"
```

## 📊 Benefits

### For Agents

- ✅ **Persistent memory**: Issue context survives session resets
- ✅ **Structured planning**: Dependencies create clear work order
- ✅ **Automatic setup**: No manual intervention needed
- ✅ **Git-backed**: All issues are version controlled
- ✅ **Fast queries**: `bd ready` finds work instantly

### For Humans

- ✅ **Visibility**: See what agents are working on
- ✅ **Auditability**: Full history of issue changes
- ✅ **Collaboration**: Multiple agents share same issue database
- ✅ **Portability**: Works locally and in cloud sessions
- ✅ **No servers**: Everything is git and SQLite

### vs Markdown TODOs

| Feature | bd Issues | Markdown TODOs |
|---------|-----------|----------------|
| Dependencies | ✅ 4 types | ❌ None |
| Ready work detection | ✅ Automatic | ❌ Manual |
| Status tracking | ✅ Built-in | ❌ Manual |
| History/audit | ✅ Full trail | ❌ Git only |
| Queries | ✅ SQL-backed | ❌ Text search |
| Cross-session | ✅ Persistent | ⚠️ Markdown only |
| Agent-friendly | ✅ JSON output | ⚠️ Parsing required |

## 🐛 Troubleshooting

### "bd: command not found"

**Cause**: SessionStart hook didn't run or installation failed

**Fix**:
```bash
# Manually install
npm install -g @beads/bd

# Verify
bd version
```

### "Database not found"

**Cause**: `bd init` wasn't run

**Fix**:
```bash
bd init
```

### "Issues.jsonl merge conflict"

**Cause**: Two sessions modified issues concurrently

**Fix**: See the main beads TROUBLESHOOTING.md for merge resolution

### Slow Installation

**Cause**: Network latency downloading binary

**Optimize**:
```bash
# Use npm cache
npm config set cache ~/.npm-cache

# Or install as dependency (cached by package-lock.json)
npm install --save-dev @beads/bd
```

## 📚 Next Steps

1. **Read the full docs**: https://github.com/steveyegge/beads
2. **Try the quickstart**: `bd quickstart` (interactive tutorial)
3. **Set up MCP**: For local Claude Desktop integration
4. **Explore examples**: https://github.com/steveyegge/beads/tree/main/examples

## 🔗 Resources

- [beads GitHub](https://github.com/steveyegge/beads)
- [npm package](https://www.npmjs.com/package/@beads/bd)
- [Claude Code docs](https://docs.claude.com/claude-code)
- [SessionStart hooks](https://docs.claude.com/claude-code/hooks)

## 💬 Example Agent Prompt

Add this to your project's system prompt or AGENTS.md:

```
You have access to bd (beads) for issue tracking. It's automatically installed in each session.

WORKFLOW:
1. Start each session: Check `bd ready --json` for available work
2. Choose a task: Pick highest priority with no blockers
3. Update status: `bd update <id> --status in_progress`
4. Work on it: Implement, test, document
5. File new issues: Create issues for any work discovered
6. Link issues: Use `bd dep add` to track relationships
7. Close when done: `bd close <id> --reason "what you did"`
8. Commit changes: Include .beads/issues.jsonl in commits

ALWAYS:
- Use --json flags for programmatic parsing
- Create issues proactively (don't let work be forgotten)
- Link related issues with dependencies
- Close issues with descriptive reasons
- Commit .beads/issues.jsonl with code changes

NEVER:
- Use markdown TODOs (use bd instead)
- Work on blocked issues (check `bd show <id>` for blockers)
- Close issues without --reason
- Forget to commit .beads/issues.jsonl
```

## 🎉 Success Criteria

After setup, you should see:

✅ New sessions automatically have `bd` available
✅ Agents use `bd` for all issue tracking
✅ Issues persist across sessions via git
✅ Multiple agents can collaborate on same issues
✅ No manual installation required

## 🆘 Support

- [File an issue](https://github.com/steveyegge/beads/issues)
- [Read the FAQ](https://github.com/steveyegge/beads/blob/main/FAQ.md)
- [Check troubleshooting](https://github.com/steveyegge/beads/blob/main/TROUBLESHOOTING.md)
