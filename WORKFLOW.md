# Beads Workflow Guide

Complete guide to using Beads for solo development and with AI coding assistants like Claude Code.

## Table of Contents

- [Vibe Coding with Claude Code](#vibe-coding-with-claude-code)
- [Database Structure](#database-structure)
- [Git Workflow](#git-workflow)
- [Advanced Usage](#advanced-usage)

---

## Vibe Coding with Claude Code

### The "Let's Continue" Protocol

**Start of every session:**

```bash
# 1. Check for abandoned work
beads list --status in_progress

# 2. If none, get ready work
beads ready --limit 5

# 3. Show top priority
beads show bd-X
```

Tell Claude: **"Let's continue"** and it runs these commands.

### Full Project Workflow

#### Session 1: Project Kickoff

**You:** "Starting a new e-commerce project. Help me plan it."

**Claude creates issues:**
```bash
cd ~/my-project
alias beads="~/src/beads/beads --db ./project.db"

beads create "Set up Next.js project" -p 0 -t task
beads create "Design database schema" -p 0 -t task
beads create "Build authentication system" -p 1 -t feature
beads create "Create API routes" -p 1 -t feature
beads create "Build UI components" -p 2 -t feature
beads create "Add tests" -p 2 -t task
beads create "Deploy to production" -p 3 -t task
```

**Map dependencies:**
```bash
beads dep add bd-4 bd-2  # API depends on schema
beads dep add bd-3 bd-2  # Auth depends on schema
beads dep add bd-5 bd-4  # UI depends on API
beads dep add bd-6 bd-3  # Tests depend on auth
beads dep add bd-6 bd-5  # Tests depend on UI
beads dep add bd-7 bd-6  # Deploy depends on tests
```

**Visualize:**
```bash
beads dep tree bd-7
```

Output:
```
🌲 Dependency tree for bd-7:

→ bd-7: Deploy to production [P3] (open)
  → bd-6: Add tests [P2] (open)
    → bd-3: Build authentication system [P1] (open)
      → bd-2: Design database schema [P0] (open)
    → bd-5: Build UI components [P2] (open)
      → bd-4: Create API routes [P1] (open)
        → bd-2: Design database schema [P0] (open)
```

**Check ready work:**
```bash
beads ready
```

```
📋 Ready work (2 issues with no blockers):

1. [P0] bd-1: Set up Next.js project
2. [P0] bd-2: Design database schema
```

#### Session 2: Foundation

**You:** "Let's continue"

**Claude:**
```bash
beads ready
# Shows: bd-1, bd-2
```

**You:** "Work on bd-2"

**Claude:**
```bash
beads update bd-2 --status in_progress
beads show bd-2

# ... designs schema, creates migrations ...

beads close bd-2 --reason "Schema designed with Prisma, migrations created"
beads ready
```

Now shows:
```
📋 Ready work (3 issues):

1. [P0] bd-1: Set up Next.js project
2. [P1] bd-3: Build authentication system  ← Unblocked!
3. [P1] bd-4: Create API routes            ← Unblocked!
```

#### Session 3: Building Features

**You:** "Let's continue, work on bd-3"

**Claude:**
```bash
beads ready  # Confirms bd-3 is ready
beads update bd-3 --status in_progress

# ... implements JWT auth, middleware ...

beads close bd-3 --reason "Auth complete with JWT tokens and protected routes"
```

#### Session 4: Discovering Blockers

**You:** "Let's continue, work on bd-4"

**Claude starts working, then:**

**You:** "We need to add OAuth before we can finish the API properly"

**Claude:**
```bash
beads create "Set up OAuth providers (Google, GitHub)" -p 1 -t task
beads dep add bd-4 bd-8  # API now depends on OAuth
beads update bd-4 --status blocked
beads ready
```

Shows:
```
📋 Ready work (2 issues):

1. [P0] bd-1: Set up Next.js project
2. [P1] bd-8: Set up OAuth providers  ← New blocker must be done first
```

**Claude:** "I've blocked bd-4 and created bd-8 as a prerequisite. Should I work on OAuth setup now?"

#### Session 5: Unblocking

**You:** "Yes, do bd-8"

**Claude completes OAuth setup:**
```bash
beads close bd-8 --reason "OAuth configured for Google and GitHub"
beads update bd-4 --status open  # Manually unblock
beads ready
```

Now bd-4 is ready again!

### Pro Tips for AI Pairing

**1. Add context with comments:**
```bash
beads update bd-5 --status in_progress
# Work session ends mid-task
beads comment bd-5 "Implemented navbar and footer, still need shopping cart icon"
```

Next session, Claude reads the comment and continues.

**2. Break down epics when too big:**
```bash
beads create "Epic: User Management" -p 1 -t epic
beads create "User registration flow" -p 1 -t task
beads create "User login/logout" -p 1 -t task
beads create "Password reset" -p 2 -t task

beads dep add bd-10 bd-9 --type parent-child
beads dep add bd-11 bd-9 --type parent-child
beads dep add bd-12 bd-9 --type parent-child
```

**3. Use labels for filtering:**
```bash
beads create "Fix login timeout" -p 0 -l "bug,auth,urgent"
beads create "Add loading spinner" -p 2 -l "ui,polish"

# Later
beads list --status open | grep urgent
```

**4. Track estimates:**
```bash
beads create "Refactor user service" -p 2 --estimated-minutes 120
beads ready  # Shows estimates for planning
```

---

## Database Structure

### What's Inside project.db?

A single **SQLite database file** (typically 72KB-1MB) containing:

#### Tables

**1. `issues` - Core issue data**
```sql
CREATE TABLE issues (
    id TEXT PRIMARY KEY,                    -- "bd-1", "bd-2", etc.
    title TEXT NOT NULL,
    description TEXT,
    design TEXT,                            -- Solution design
    acceptance_criteria TEXT,               -- Definition of done
    notes TEXT,                             -- Working notes
    status TEXT DEFAULT 'open',             -- open|in_progress|blocked|closed
    priority INTEGER DEFAULT 2,             -- 0-4 (0=highest)
    issue_type TEXT DEFAULT 'task',         -- bug|feature|task|epic|chore
    assignee TEXT,
    estimated_minutes INTEGER,
    created_at DATETIME,
    updated_at DATETIME,
    closed_at DATETIME
);
```

**2. `dependencies` - Relationship graph**
```sql
CREATE TABLE dependencies (
    issue_id TEXT NOT NULL,                 -- "bd-2"
    depends_on_id TEXT NOT NULL,            -- "bd-1" (bd-2 depends on bd-1)
    type TEXT DEFAULT 'blocks',             -- blocks|related|parent-child
    created_at DATETIME,
    created_by TEXT,
    PRIMARY KEY (issue_id, depends_on_id)
);
```

**3. `labels` - Tags for categorization**
```sql
CREATE TABLE labels (
    issue_id TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY (issue_id, label)
);
```

**4. `events` - Complete audit trail**
```sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    event_type TEXT NOT NULL,               -- created|updated|commented|closed|etc
    actor TEXT NOT NULL,                    -- who made the change
    old_value TEXT,                         -- before (JSON)
    new_value TEXT,                         -- after (JSON)
    comment TEXT,                           -- for comments and close reasons
    created_at DATETIME
);
```

**5. `ready_issues` - VIEW (auto-computed)**
```sql
-- Shows issues with NO open blockers
-- This is the magic that powers "beads ready"
CREATE VIEW ready_issues AS
SELECT i.*
FROM issues i
WHERE i.status = 'open'
  AND NOT EXISTS (
    SELECT 1 FROM dependencies d
    JOIN issues blocked ON d.depends_on_id = blocked.id
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND blocked.status IN ('open', 'in_progress', 'blocked')
  );
```

**6. `blocked_issues` - VIEW (auto-computed)**
```sql
-- Shows issues WITH open blockers
CREATE VIEW blocked_issues AS
SELECT
    i.*,
    COUNT(d.depends_on_id) as blocked_by_count
FROM issues i
JOIN dependencies d ON i.id = d.issue_id
JOIN issues blocker ON d.depends_on_id = blocker.id
WHERE i.status IN ('open', 'in_progress', 'blocked')
  AND d.type = 'blocks'
  AND blocker.status IN ('open', 'in_progress', 'blocked')
GROUP BY i.id;
```

### Example Data

**Issues table:**
```
bd-1|Critical bug|Fix login timeout|||open|0|bug|||2025-10-11 19:23:10|2025-10-11 19:23:10|
bd-2|High priority||Need auth first||open|1|feature|||2025-10-11 19:23:11|2025-10-11 19:23:11|
```

**Dependencies table:**
```
bd-2|bd-1|blocks|2025-10-11 19:23:16|stevey
```
Translation: "bd-2 depends on bd-1 (blocks type), created by stevey"

**Events table:**
```
1|bd-1|created|stevey||{"id":"bd-1","title":"Critical bug",...}||2025-10-11 19:23:10
2|bd-2|created|stevey||{"id":"bd-2","title":"High priority",...}||2025-10-11 19:23:11
3|bd-2|dependency_added|stevey|||Added dependency: bd-2 blocks bd-1|2025-10-11 19:23:16
```

### Inspecting the Database

**Show all tables:**
```bash
sqlite3 project.db ".tables"
```

**View schema:**
```bash
sqlite3 project.db ".schema issues"
```

**Query directly:**
```bash
# Find all P0 issues
sqlite3 project.db "SELECT id, title FROM issues WHERE priority = 0;"

# See dependency graph
sqlite3 project.db "SELECT issue_id, depends_on_id FROM dependencies;"

# View audit trail for an issue
sqlite3 project.db "SELECT * FROM events WHERE issue_id = 'bd-5' ORDER BY created_at;"

# Who's working on what?
sqlite3 project.db "SELECT assignee, COUNT(*) FROM issues WHERE status = 'in_progress' GROUP BY assignee;"

# See what's ready (same as beads ready)
sqlite3 project.db "SELECT id, title, priority FROM ready_issues ORDER BY priority;"
```

**Export to CSV:**
```bash
sqlite3 project.db -header -csv "SELECT * FROM issues;" > issues.csv
```

**Database size:**
```bash
ls -lh project.db
# Typically: 72KB (empty) to ~1MB (1000 issues)
```

---

## Git Workflow

### Auto-Sync Behavior

bd automatically keeps your database and git in sync:

**Making changes:**
```bash
bd create "New task" -p 1
bd update bd-5 --status in_progress
# bd automatically exports to .beads/issues.jsonl after 5 seconds

git add .beads/issues.jsonl
git commit -m "Started working on bd-5"
git push
```

**After git pull:**
```bash
git pull
# bd automatically detects JSONL is newer on next command

bd ready  # Auto-imports fresh data from git!
bd list --status in_progress  # See what you were working on
```

### Multi-Machine Workflow

**Machine 1:**
```bash
bd create "New task" -p 1
bd update bd-5 --status in_progress
# Wait 5 seconds for auto-export, or run: bd sync

git add .beads/issues.jsonl
git commit -m "Started working on bd-5"
git push
```

**Machine 2:**
```bash
git pull
bd ready  # Auto-imports, sees bd-5 is in progress
```

### Zero-Lag Sync (Optional)

Install git hooks for immediate sync:
```bash
cd examples/git-hooks && ./install.sh
```

This eliminates the 5-second debounce and guarantees import after `git pull`.

### Team Workflow

**Each developer has their own database:**
```bash
# Alice's machine
beads --db alice.db create "Fix bug"

# Bob's machine
beads --db bob.db create "Add feature"

# Merge by convention:
# - Alice handles backend issues (bd-1 to bd-50)
# - Bob handles frontend issues (bd-51 to bd-100)
```

Or use **PostgreSQL** for shared state (future feature).

### Branching Strategy

**Option 1: Database per branch**
```bash
git checkout -b feature/auth
cp main.db auth.db
beads --db auth.db create "Add OAuth" -p 1
# Work on branch...
git add auth.db
git commit -m "Auth implementation progress"
```

**Option 2: Single database, label by branch**
```bash
beads create "Add OAuth" -p 1 -l "branch:feature/auth"
beads list | grep "branch:feature/auth"
```

---

## Advanced Usage

### Alias Setup

Add to `~/.bashrc` or `~/.zshrc`:

```bash
# Project-specific
alias b="~/src/beads/beads --db ./project.db"

# Usage
b create "Task" -p 1
b ready
b show bd-5
```

### Scripting Beads

**Find all unassigned P0 issues:**
```bash
#!/bin/bash
beads list --priority 0 --status open | grep -v "Assignee:"
```

**Auto-close issues from git commits:**
```bash
#!/bin/bash
# In git hook: .git/hooks/commit-msg

COMMIT_MSG=$(cat $1)
if [[ $COMMIT_MSG =~ bd-([0-9]+) ]]; then
    ISSUE_ID="bd-${BASH_REMATCH[1]}"
    ~/src/beads/beads --db ./project.db close "$ISSUE_ID" \
        --reason "Auto-closed from commit: $(git rev-parse --short HEAD)"
fi
```

**Weekly report:**
```bash
#!/bin/bash
echo "Issues closed this week:"
sqlite3 project.db "
    SELECT id, title, closed_at
    FROM issues
    WHERE closed_at > date('now', '-7 days')
    ORDER BY closed_at DESC;
"
```

### Multi-Project Management

**Use different databases:**
```bash
# Personal projects
beads --db ~/personal.db create "Task"

# Work projects
beads --db ~/work.db create "Task"

# Client A
beads --db ~/clients/client-a.db create "Task"
```

**Or use labels:**
```bash
beads create "Task" -l "project:website"
beads create "Task" -l "project:mobile-app"

# Filter by project
sqlite3 ~/.beads/beads.db "
    SELECT i.id, i.title
    FROM issues i
    JOIN labels l ON i.id = l.issue_id
    WHERE l.label = 'project:website';
"
```

### Export/Import

**Export issues to JSON:**
```bash
sqlite3 project.db -json "SELECT * FROM issues;" > backup.json
```

**Export dependency graph:**
```bash
# DOT format for Graphviz
sqlite3 project.db "
    SELECT 'digraph G {'
    UNION ALL
    SELECT '  \"' || issue_id || '\" -> \"' || depends_on_id || '\";'
    FROM dependencies
    UNION ALL
    SELECT '}';
" > graph.dot

dot -Tpng graph.dot -o graph.png
```

### Performance Tips

**Vacuum regularly for large databases:**
```bash
sqlite3 project.db "VACUUM;"
```

**Add custom indexes:**
```bash
sqlite3 project.db "CREATE INDEX idx_labels_custom ON labels(label) WHERE label LIKE 'project:%';"
```

**Archive old issues:**
```bash
sqlite3 project.db "
    DELETE FROM issues
    WHERE status = 'closed'
    AND closed_at < date('now', '-6 months');
"
```

---

## Troubleshooting

**Database locked:**
```bash
# Another process is using it
lsof project.db
# Kill the process or wait for it to finish
```

**Corrupted database:**
```bash
# Check integrity
sqlite3 project.db "PRAGMA integrity_check;"

# Recover
sqlite3 project.db ".dump" | sqlite3 recovered.db
```

**Reset everything:**
```bash
rm ~/.beads/beads.db
beads create "Fresh start" -p 1
```

---

## Summary

**Beads is:**
- A single binary
- A single database file
- Simple commands
- Powerful dependency tracking
- Perfect for solo dev or AI pairing

**The workflow:**
1. Brain dump all tasks → `beads create`
2. Map dependencies → `beads dep add`
3. Find ready work → `beads ready`
4. Work on it → `beads update --status in_progress`
5. Complete it → `beads close`
6. Commit database → `git add project.db`
7. Repeat

**The magic:**
- Database knows what's ready
- Git tracks your progress
- AI can query and update
- You never lose track of "what's next"
