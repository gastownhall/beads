# Aider Integration Guide

This guide explains how to integrate [Aider](https://aider.chat/) with Beads for AI-assisted coding with issue tracking.

## Overview

Aider is an AI pair programming tool that works in your terminal. Unlike autonomous AI agents like Claude Code, **Aider requires explicit user confirmation** to run commands via the `/run` command.

The beads integration for Aider:
- Creates `.aider.conf.yml` with bd workflow instructions
- Provides `.aider/README.md` with quick reference
- Instructs the AI to **suggest** bd commands (not run them automatically)
- Works with aider's human-in-the-loop design philosophy

## Installation

### 1. Install Beads

```bash
# Install beads CLI
go install github.com/steveyegge/beads/cmd/bd@latest

# Initialize in your project
cd your-project
bd init --quiet
```

### 2. Setup Aider Integration

```bash
# Install aider integration files
bd setup aider

# Verify installation
bd setup aider --check
```

This creates:
- `.aider.conf.yml` - Instructions for the AI about bd workflow
- `.aider/README.md` - Quick reference guide

### 3. Install Aider

```bash
# Install aider via pip
pip install aider-chat

# Or via pipx (recommended)
pipx install aider-chat
```

## Usage Workflow

### Starting a Session

1. **Start aider in your project:**
   ```bash
   cd your-project
   aider
   ```

2. **Ask the AI about available work:**
   ```
   You: What issues are ready to work on?

   Aider: Let me check the available work. Run:
   /run bd ready
   ```

3. **Run the suggested command:**
   ```
   /run bd ready
   ```

4. **Choose an issue and ask AI to claim it:**
   ```
   You: Let's work on bd-42

   Aider: Great choice! To claim it, run:
   /run bd update bd-42 --status in_progress
   ```

### During Development

The AI will suggest appropriate bd commands as you work:

- **Create issues for bugs you find:**
  ```
  You: I found a bug in the auth code

  Aider: Let's track that. Run:
  /run bd create "Fix auth bug in login handler" --description="Login fails with 500 when password contains quotes" -t bug -p 1
  ```

- **Link discovered work:**
  ```
  Aider: Since we found this while working on bd-42, run:
  /run bd create "Fix auth bug" --description="..." -p 1 --deps discovered-from:bd-42
  ```

- **Check issue details:**
  ```
  /run bd show bd-42
  ```

### Completing Work

When you're done:

```
You: This issue is complete

Aider: Great! Mark it complete by running:
/run bd close bd-42 --reason "Implemented login fix and added tests"

Then sync to git:
/run bd sync
```

## Configuration

### .aider.conf.yml

The config file contains instructions for the AI:

```yaml
# Core workflow rules:
# 1. Track ALL work in bd (never use markdown TODOs)
# 2. Suggest 'bd ready' to find available work
# 3. Suggest 'bd create' for new issues/tasks/bugs
# 4. Suggest 'bd sync' at end of session
# 5. ALWAYS suggest commands - user will run them via /run
```

You can customize this file to add project-specific instructions.

### Aider Commands vs bd Commands

**Aider commands** start with `/` and control aider itself:
- `/run <command>` - Run a shell command
- `/add <file>` - Add file to context
- `/help` - Show aider help

**bd commands** are run via `/run`:
- `/run bd ready` - Check available work
- `/run bd create "..."` - Create an issue
- `/run bd show bd-42` - View issue details

## Common Patterns

### Starting Work

```bash
# Check what's available
/run bd ready

# Claim an issue
/run bd update bd-abc --status in_progress
```

### Discovering Work

```bash
# Create a new issue
/run bd create "Refactor auth module" --description="Current auth code has tight coupling" -t task -p 2

# Link it to current work
/run bd dep add bd-new --type discovered-from --target bd-abc
```

### Completing Work

```bash
# Close the issue
/run bd close bd-abc --reason "Implemented and tested"

# Sync to git
/run bd sync
```

### Checking Status

```bash
# View issue details
/run bd show bd-abc

# List all open issues
/run bd list --status=open

# Check dependencies
/run bd dep tree bd-abc
```

## Comparison: Aider vs Claude Code

### Aider (Human-in-the-Loop)

- ✅ User must confirm all commands via `/run`
- ✅ Full control over what gets executed
- ✅ AI **suggests** bd commands
- ⚠️ More manual interaction required

### Claude Code (Autonomous)

- ✅ AI directly executes bd commands
- ✅ Faster workflow (no confirmation needed)
- ✅ Hooks auto-inject bd context
- ⚠️ Less user control over command execution

**Both approaches work well with beads!** Choose based on your preference for automation vs. control.

## Tips for Aider Users

### 1. Ask for Suggestions

Instead of running commands yourself, ask the AI:
```
You: How do I check what work is available?
Aider: Run `/run bd ready` to see all unblocked issues
```

### 2. Let the AI Track Work

The AI knows the bd workflow and will suggest appropriate commands:
```
You: I'm starting work on the login feature
Aider: First, let's claim it. Run:
/run bd update bd-xyz --status in_progress
```

### 3. Use bd prime for Context

Get the full workflow guide:
```bash
/run bd prime
```

The AI will read this and have complete context about bd commands.

### 4. Create Aliases

Add to your shell config for faster commands:
```bash
alias bdr='/run bd ready'
alias bdc='/run bd create'
alias bds='/run bd sync'
```

Then in aider:
```
bdr                    # Instead of /run bd ready
bdc "Fix bug" -t bug  # Instead of /run bd create "Fix bug" -t bug
```

## Troubleshooting

### "The AI isn't suggesting bd commands"

1. Check that `.aider.conf.yml` exists:
   ```bash
   bd setup aider --check
   ```

2. Reload aider to pick up the config:
   ```bash
   # Exit aider (Ctrl-D or /exit)
   # Restart aider
   aider
   ```

3. Explicitly ask about bd:
   ```
   You: What should I use for issue tracking?
   Aider: This project uses Beads (bd) for issue tracking...
   ```

### "Commands are failing"

Make sure you're in a beads-initialized directory:
```bash
/run bd doctor
```

If not initialized:
```bash
/run bd init --quiet
```

### "I want to remove the integration"

```bash
bd setup aider --remove
```

This removes `.aider.conf.yml` and `.aider/README.md`.

## Advanced Usage

### Multi-File Context

Aider can track multiple files. Combine with bd:

```
You: Add the auth files to context and check for related issues

/run bd list --json | grep -i auth
/add src/auth/*.py
```

### Batch Operations

Run multiple bd commands in sequence:
```bash
# In your shell (not aider)
bd ready && bd show bd-42 && bd update bd-42 --status in_progress
```

### Integration with Git

Aider handles git commits. bd tracks issues. They work together:

1. Claim issue: `/run bd update bd-42 --status in_progress`
2. Make changes with aider
3. Aider commits changes
4. Complete issue: `/run bd close bd-42`
5. Sync issues: `/run bd sync`

## Example Session

```
$ aider

You: What issues are ready to work on?

Aider: Let me check. Run:
/run bd ready

📋 Ready work (3 issues with no blockers):
1. [P1] bd-42: Fix login bug
2. [P2] bd-99: Add password reset
3. [P3] bd-17: Refactor auth tests

You: Let's work on bd-42

Aider: Great! First claim it:
/run bd update bd-42 --status in_progress

You: Can you show me the details?

Aider: Run:
/run bd show bd-42

[Shows issue details]

You: Ok, add the auth files to context

Aider: Added src/auth/login.py and src/auth/utils.py

You: Fix the bug described in the issue

[Aider makes the changes]

You: Looks good! Mark it complete

Aider: Run:
/run bd close bd-42 --reason "Fixed login bug - added input validation"

Then sync:
/run bd sync
```

## References

- [Aider Documentation](https://aider.chat/docs/)
- [Beads Documentation](https://github.com/steveyegge/beads)
- [AGENTS.md](../AGENTS.md) - Complete bd workflow guide
- [QUICKSTART.md](QUICKSTART.md) - Quick start guide
