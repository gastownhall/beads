# Troubleshooting Guide

Common issues encountered when using bd and how to resolve them.

## Interface-Specific Troubleshooting

**MCP tools (local environment):**
- MCP tools require Dolt server running
- Check server status: `bd doctor` (CLI)
- If MCP tools fail, verify Dolt server is running and restart if needed

**CLI (web environment or local):**
- CLI can use server mode (default) or embedded mode (direct database access)
- Embedded mode has 3-5 second sync delay
- Web environment: Install via `npm install -g @beads/cli`
- Web environment: Initialize via `bd init <prefix>` before first use

**Most issues below apply to both interfaces** - the underlying database and server behavior is the same.

## Contents

- [Dependencies Not Persisting](#dependencies-not-persisting)
- [Status Updates Not Visible](#status-updates-not-visible)
- [Dolt Server Won't Start](#dolt-server-wont-start)
- [Database Errors in Cloud-Synced Folders](#database-errors-in-cloud-synced-folders)
- [Database Not Initialized](#database-not-initialized)
- [Version Requirements](#version-requirements)

---

## Dependencies Not Persisting

### Symptom
```bash
bd dep add issue-2 issue-1 --type blocks
# Reports: ✓ Added dependency
bd show issue-2
# Shows: No dependencies listed
```

### Root Cause (Fixed in v0.15.0+)
This was a **bug in bd** (GitHub issue #101) where dependencies were ignored during issue creation. **Fixed in bd v0.15.0** (Oct 21, 2025).

### Resolution

**1. Check your bd version:**
```bash
bd version
```

**2. If version < 0.15.0, update bd:**
```bash
# Via Homebrew (macOS/Linux)
brew upgrade beads

# Via install script
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash

# Via package manager
# See https://github.com/gastownhall/beads#installing
```

**3. Restart Dolt server after upgrade:**
```bash
bd dolt stop          # Stop old server
bd dolt start         # Start new server with fix
```

**4. Test dependency creation:**
```bash
bd create "Test A" -t task
bd create "Test B" -t task
bd dep add <B-id> <A-id> --type blocks
bd show <B-id>
# Should show: "Depends on (1): → <A-id>"
```

### Still Not Working?

If dependencies still don't persist after updating:

1. **Check Dolt server is running:**
   ```bash
   bd doctor
   ```

2. **Try in server mode:**
   ```bash
   # Use: bd dep add ...  (let the Dolt server handle it)
   ```

3. **Check database directly:**
   ```bash
   bd sql "SELECT * FROM dependencies WHERE issue_id = '<id>'"
   # Should show dependency rows
   ```

4. **Report to beads GitHub** with:
   - `bd version` output
   - Operating system
   - Reproducible test case

---

## Status Updates Not Visible

### Symptom
```bash
# In embedded mode, updates may not reflect immediately
bd update issue-1 --claim
bd show issue-1
# Shows: Status: open (not in_progress!)
```

### Root Cause
This is **expected behavior** when using embedded mode. Understanding requires knowing bd's architecture:

**BD Architecture:**
- **Dolt database** (`.beads/dolt/`): Source of truth for all data
- **Dolt server**: Handles concurrent access and replication

**In embedded mode (without Dolt server):**
- **Writes**: Go directly to the Dolt database
- **Reads**: Also from the Dolt database
- **Sync delay**: Embedded mode may have brief delays reflecting writes

### Resolution

**Option 1: Use server mode (recommended)**
```bash
# With the Dolt server running, operations reflect immediately
bd update issue-1 --claim
bd show issue-1
# Status reflects immediately
```

**Option 2: Wait for sync (if using embedded mode)**
```bash
bd update issue-1 --claim
# Wait for server to sync
sleep 5
bd show issue-1
# Status should reflect now
```

**Option 3: Manual sync trigger**
```bash
bd update issue-1 --claim
# Trigger sync by exporting/importing
bd export > /dev/null 2>&1  # Forces sync
bd show issue-1
```

### When to Use Embedded Mode

**Use embedded mode for:**
- Batch import scripts (performance)
- CI/CD environments (no persistent server)
- Testing/debugging

**Don't use embedded mode for:**
- Interactive development
- Real-time status checks
- When you need immediate query results

---

## Dolt Server Won't Start

### Symptom
```bash
bd dolt start
# Error: not in a git repository
# Hint: run 'git init' to initialize a repository
```

### Root Cause
The Dolt server requires a **git repository** because it uses git for:
- Syncing issues to git remote (optional)
- Commit history of issue changes

### Resolution

**Initialize git repository:**
```bash
# In your project directory
git init
bd dolt start
# Dolt server should start now
```

**Configuration:**
- `dolt.auto-commit: on`: Auto-commit changes
- See `bd config --help` for all Dolt server options

---

## Database Errors in Cloud-Synced Folders

### Symptom
```bash
# In directory: /Users/name/Google Drive/...
bd init myproject
# Error: disk I/O error (522)
# OR: Error: database is locked
```

### Current Storage Model
Current versions of bd do not store project data in SQLite. The default
embedded mode runs Dolt in-process, stores the live database under
`.beads/embeddeddolt/`, and permits one writer at a time using a file lock.
A locally managed, per-project SQL server normally stores data under
`.beads/dolt/` and routes concurrent access through the Dolt SQL server.
Shared-server data normally lives under `~/.beads/shared-server/dolt/` instead,
while an externally managed server owns data on its host. Proxied-server and
custom configurations can use another configured root.

In embedded or locally managed per-project mode, a cloud-synced project can
therefore sync a live Dolt directory tree. In shared, external, or custom-root
server modes, the project `.beads/` directory may contain only client
configuration, not the live database. Cloud-folder replication is different
from using a Dolt remote or a Dolt-native backup, and SQLite-era advice about
standalone database files or a shared local database workaround does not apply.

### Resolution

**1. Capture diagnostics before changing the database:**

```bash
bd version
bd doctor
bd dolt status
```

Do not delete or reinitialize `.beads/` while diagnosing the problem. Before
any repair, identify which mode owns the live database and stop its writers.
In embedded mode, exit every bd process before copying `.beads/`. For a locally
managed per-project server, stop that server before copying `.beads/`. For a
shared, external, proxied, or custom-root server, do not assume that copying
the project `.beads/` captures the data or stop a shared service unilaterally;
coordinate with its operator and use its supported backup procedure for the
actual configured data root.

**2. Isolate filesystem behavior:**

For embedded or locally managed per-project mode, stop the owning processes,
pause or quit the cloud sync client, copy the project to a local path that is
not being synchronized, and retry there. Moving only the project does not move
a shared or external server database; for those modes, use the server
operator's storage and backup procedure. If the error occurs only when the live
Dolt root is cloud-synchronized, keep that root on a local filesystem and use a
supported sync mechanism.

**3. Use a Dolt remote to share live issue history between working copies:**

```bash
# Existing, verified-authoritative working copy
bd dolt remote list

# If no Dolt remote is configured, push can adopt the project's Git origin.
bd dolt push
```

In a fresh Git clone whose origin contains `refs/dolt/data`:

```bash
bd bootstrap

# Normal synchronization after bootstrap
bd dolt pull
bd dolt push
```

`bd dolt push` and `bd dolt pull` synchronize Dolt history; a normal Git clone
does not fetch `refs/dolt/data`, so new clones must run `bd bootstrap`.
Before the first push, verify that this local database is the authoritative,
healthy copy. If the remote already contains different Dolt history, stop and
reconcile it instead of forcing or replacing either side.

**4. Use `bd backup` for a restorable copy:**

```bash
# Embedded or same-host locally managed server:
# the Dolt process must be able to access this absolute filesystem path.
bd backup init ~/Dropbox/beads-backup
bd backup sync
bd backup status
```

Filesystem backup URLs are opened by the Dolt process. For an external server,
use a destination that the server can access and follow the server operator's
backup procedure. In shared-server mode, coordinate the destination because
clients register a server-side backup name. `bd backup` is not supported in
proxied-server mode; back up its configured data root using the owning
operator's procedure instead.

A successful Dolt-native backup preserves branches, commit history, and
working-set data. A JSONL export is not a full Dolt database backup.

---

## Database Not Initialized

### Symptom
```bash
bd create "Test" -t task
# Error: database not found
```

### Root Cause
`bd init` was not run in the project directory.

### Resolution

**Initialize bd in the project:**
```bash
bd init myproject
bd create "Task 1" -t task
bd show <id>
# Shows task data
```

**Pattern for batch scripts:**
```bash
#!/bin/bash
# Batch import script

bd init myproject
bd dolt start               # Start Dolt server
sleep 3                     # Wait for initialization

# Create issues
for item in "${items[@]}"; do
    bd create "$item" -t feature
done

# Query results
bd stats
```

---

## Version Requirements

### Minimum Version for Dependency Persistence

**Issue:** Dependencies created but don't appear in `bd show` or dependency tree.

**Fix:** Upgrade to **bd v0.15.0+** (released Oct 2025)

**Check version:**
```bash
bd version
# Should show: bd version 0.15.0 or higher
```

**If using MCP plugin:**
```bash
# Update Claude Code beads plugin
claude plugin update beads
```

### Breaking Changes

**v0.15.0:**
- MCP parameter names changed from `from_id/to_id` to `issue_id/depends_on_id`
- Dependency creation now persists correctly in server mode

**v0.14.0:**
- Architecture changes
- Dolt storage backend introduced

---

## MCP-Specific Issues

### Dependencies Created Backwards

**Symptom:**
Using MCP tools, dependencies end up reversed from intended.

**Example:**
```python
# Want: "task-2 depends on task-1" (task-1 blocks task-2)
beads_add_dependency(issue_id="task-1", depends_on_id="task-2")
# Wrong! This makes task-1 depend on task-2
```

**Root Cause:**
Parameter confusion between old (`from_id/to_id`) and new (`issue_id/depends_on_id`) names.

**Resolution:**

**Correct MCP usage (bd v0.15.0+):**
```python
# Correct: task-2 depends on task-1
beads_add_dependency(
    issue_id="task-2",        # Issue that has dependency
    depends_on_id="task-1",   # Issue that must complete first
    dep_type="blocks"
)
```

**Mnemonic:**
- `issue_id`: The issue that **waits**
- `depends_on_id`: The issue that **must finish first**

**Equivalent CLI:**
```bash
bd dep add task-2 task-1 --type blocks
# Meaning: task-2 depends on task-1
```

**Verify dependency direction:**
```bash
bd show task-2
# Should show: "Depends on: task-1"
# Not the other way around
```

---

## Getting Help

### Debug Checklist

Before reporting issues, collect this information:

```bash
# 1. Version
bd version

# 2. Dolt server status
bd doctor

# 3. Dolt mode, reachability, branch, and current commit
bd dolt status
bd vc status  # Branch and HEAD only; skip in proxied-server mode

# 4. Git status
git status
git log --oneline -1

# 5. Database contents (for dependency issues)
bd sql "SELECT * FROM dependencies" --json | head -50
```

### Report to beads GitHub

If problems persist:

1. **Check existing issues:** https://github.com/gastownhall/beads/issues
2. **Create new issue** with:
   - bd version (`bd version`)
   - Operating system
   - Debug checklist output (above)
   - Minimal reproducible example
   - Expected vs actual behavior

### Claude Code Skill Issues

If the **bd-issue-tracking skill** provides incorrect guidance:

1. **Check skill version:**
   ```bash
   ls -la ~/.claude/skills/bd-issue-tracking/
   head -20 ~/.claude/skills/bd-issue-tracking/SKILL.md
   ```

2. **Report via Claude Code feedback** or user's GitHub

---

## Quick Reference: Common Fixes

| Problem | Quick Fix |
|---------|-----------|
| Dependencies not saving | Upgrade to bd v0.15.0+ |
| Status updates lag | Use server mode (ensure Dolt server is running) |
| Dolt server won't start | Run `git init` first |
| Database errors in a cloud-synced folder | Diagnose from a local copy; sync with a Dolt remote or `bd backup` |
| Database not initialized | Run `bd init` in the project directory |
| Dependencies backwards (MCP) | Update to v0.15.0+, use `issue_id/depends_on_id` correctly |

---

## Related Documentation

- [CLI Reference](CLI_REFERENCE.md) - Live command reference pointers
- [Dependencies Guide](DEPENDENCIES.md) - Understanding dependency types
- [Workflows](WORKFLOWS.md) - Step-by-step workflow guides
- [beads GitHub](https://github.com/gastownhall/beads) - Official documentation
