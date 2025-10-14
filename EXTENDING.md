# Extending bd with Custom Tables

bd is designed to be extended by applications that need more than basic issue tracking. The recommended pattern is to add your own tables to the same SQLite database that bd uses.

## Philosophy

**bd is focused** - It tracks issues, dependencies, and ready work. That's it.

**Your application adds orchestration** - Execution state, agent assignments, retry logic, etc.

**Shared database = simple queries** - Join `issues` with your tables for powerful queries.

This is the same pattern used by tools like Temporal (workflow + activity tables) and Metabase (core + plugin tables).

## Quick Example

```sql
-- Create your application's tables in the same database
CREATE TABLE myapp_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    status TEXT NOT NULL,  -- pending, running, failed, completed
    agent_id TEXT,
    started_at DATETIME,
    completed_at DATETIME,
    error TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE INDEX idx_executions_issue ON myapp_executions(issue_id);
CREATE INDEX idx_executions_status ON myapp_executions(status);

-- Query across layers
SELECT
    i.id,
    i.title,
    i.priority,
    e.status as execution_status,
    e.agent_id,
    e.started_at
FROM issues i
LEFT JOIN myapp_executions e ON i.id = e.issue_id
WHERE i.status = 'in_progress'
ORDER BY i.priority ASC;
```

## Integration Pattern

### 1. Initialize Your Database Schema

```go
package main

import (
    "database/sql"
    _ "modernc.org/sqlite"
)

const myAppSchema = `
-- Your application's tables
CREATE TABLE IF NOT EXISTS myapp_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    status TEXT NOT NULL,
    agent_id TEXT,
    started_at DATETIME,
    completed_at DATETIME,
    error TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS myapp_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id INTEGER NOT NULL,
    step_name TEXT NOT NULL,
    step_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (execution_id) REFERENCES myapp_executions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_executions_issue ON myapp_executions(issue_id);
CREATE INDEX IF NOT EXISTS idx_executions_status ON myapp_executions(status);
CREATE INDEX IF NOT EXISTS idx_checkpoints_execution ON myapp_checkpoints(execution_id);
`

func InitializeMyAppSchema(dbPath string) error {
    db, err := sql.Open("sqlite", dbPath)
    if err != nil {
        return err
    }
    defer db.Close()

    _, err = db.Exec(myAppSchema)
    return err
}
```

### 2. Use bd for Issue Management

```go
import (
    "github.com/steveyegge/beads"
)

// Open bd's storage
store, err := beads.NewSQLiteStorage(dbPath)
if err != nil {
    log.Fatal(err)
}

// Initialize your schema
if err := InitializeMyAppSchema(dbPath); err != nil {
    log.Fatal(err)
}

// Use bd to find ready work
readyIssues, err := store.GetReadyWork(ctx, beads.WorkFilter{Limit: 10})
if err != nil {
    log.Fatal(err)
}

// Use your tables for orchestration
for _, issue := range readyIssues {
    execution := &Execution{
        IssueID:   issue.ID,
        Status:    "pending",
        AgentID:   selectAgent(),
        StartedAt: time.Now(),
    }
    if err := createExecution(db, execution); err != nil {
        log.Printf("Failed to create execution: %v", err)
    }
}
```

### 3. Query Across Layers

```go
// Complex query joining bd's issues with your execution data
query := `
SELECT
    i.id,
    i.title,
    i.priority,
    i.status as issue_status,
    e.id as execution_id,
    e.status as execution_status,
    e.agent_id,
    e.error,
    COUNT(c.id) as checkpoint_count
FROM issues i
INNER JOIN myapp_executions e ON i.id = e.issue_id
LEFT JOIN myapp_checkpoints c ON e.id = c.execution_id
WHERE e.status = 'running'
GROUP BY i.id, e.id
ORDER BY i.priority ASC, e.started_at ASC
`

rows, err := db.Query(query)
// Process results...
```

## Real-World Example: VC Orchestrator

Here's how the VC (VibeCoder) orchestrator extends bd:

```sql
-- VC's orchestration layer
CREATE TABLE vc_executor_instances (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    executor_type TEXT NOT NULL,
    status TEXT NOT NULL,  -- pending, assessing, executing, analyzing, completed, failed
    agent_name TEXT,
    created_at DATETIME NOT NULL,
    claimed_at DATETIME,
    completed_at DATETIME,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE TABLE vc_execution_state (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    executor_id TEXT NOT NULL,
    phase TEXT NOT NULL,  -- assessment, execution, analysis
    state_data TEXT NOT NULL,  -- JSON checkpoint data
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (executor_id) REFERENCES vc_executor_instances(id) ON DELETE CASCADE
);

-- VC can now claim ready work atomically
UPDATE vc_executor_instances
SET status = 'executing', claimed_at = CURRENT_TIMESTAMP, agent_name = 'agent-1'
WHERE id = (
    SELECT ei.id
    FROM vc_executor_instances ei
    JOIN issues i ON ei.issue_id = i.id
    WHERE ei.status = 'pending'
    AND NOT EXISTS (
        SELECT 1 FROM dependencies d
        JOIN issues blocked ON d.depends_on_id = blocked.id
        WHERE d.issue_id = i.id
        AND d.type = 'blocks'
        AND blocked.status IN ('open', 'in_progress', 'blocked')
    )
    ORDER BY i.priority ASC
    LIMIT 1
)
RETURNING *;
```

## Best Practices

### 1. Namespace Your Tables

Prefix your tables with your application name to avoid conflicts:

```sql
-- Good
CREATE TABLE vc_executions (...);
CREATE TABLE myapp_checkpoints (...);

-- Bad
CREATE TABLE executions (...);  -- Could conflict with other apps
CREATE TABLE state (...);       -- Too generic
```

### 2. Use Foreign Keys

Always link your tables to `issues` with foreign keys:

```sql
CREATE TABLE myapp_executions (
    issue_id TEXT NOT NULL,
    -- ...
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
```

This ensures:
- Referential integrity
- Automatic cleanup when issues are deleted
- Ability to join with `issues` table

### 3. Index Your Query Patterns

Add indexes for common queries:

```sql
-- If you query by status frequently
CREATE INDEX idx_executions_status ON myapp_executions(status);

-- If you join on issue_id
CREATE INDEX idx_executions_issue ON myapp_executions(issue_id);

-- Composite index for complex queries
CREATE INDEX idx_executions_status_priority
ON myapp_executions(status, issue_id);
```

### 4. Don't Duplicate bd's Data

Don't copy fields from `issues` into your tables. Instead, join:

```sql
-- Bad: Duplicating data
CREATE TABLE myapp_executions (
    issue_id TEXT NOT NULL,
    issue_title TEXT,  -- Don't do this!
    issue_priority INTEGER,  -- Don't do this!
    -- ...
);

-- Good: Join when querying
SELECT i.title, i.priority, e.status
FROM myapp_executions e
JOIN issues i ON e.issue_id = i.id;
```

### 5. Use JSON for Flexible State

SQLite supports JSON functions, great for checkpoint data:

```sql
CREATE TABLE myapp_checkpoints (
    id INTEGER PRIMARY KEY,
    execution_id INTEGER NOT NULL,
    step_name TEXT NOT NULL,
    step_data TEXT,  -- Store as JSON
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Query JSON fields
SELECT
    id,
    json_extract(step_data, '$.completed') as completed,
    json_extract(step_data, '$.error') as error
FROM myapp_checkpoints
WHERE step_name = 'assessment';
```

## Common Patterns

### Pattern 1: Execution Tracking

Track which agent is working on which issue:

```sql
CREATE TABLE myapp_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL UNIQUE,  -- One execution per issue
    agent_id TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at DATETIME NOT NULL,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Claim an issue for execution
INSERT INTO myapp_executions (issue_id, agent_id, status, started_at)
VALUES (?, ?, 'running', CURRENT_TIMESTAMP)
ON CONFLICT (issue_id) DO UPDATE
SET agent_id = excluded.agent_id, started_at = CURRENT_TIMESTAMP;
```

### Pattern 2: Checkpoint/Resume

Store execution checkpoints for crash recovery:

```sql
CREATE TABLE myapp_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id INTEGER NOT NULL,
    phase TEXT NOT NULL,
    checkpoint_data TEXT NOT NULL,  -- JSON
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (execution_id) REFERENCES myapp_executions(id) ON DELETE CASCADE
);

-- Latest checkpoint for an execution
SELECT checkpoint_data
FROM myapp_checkpoints
WHERE execution_id = ?
ORDER BY created_at DESC
LIMIT 1;
```

### Pattern 3: Result Storage

Store execution results linked to issues:

```sql
CREATE TABLE myapp_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    result_type TEXT NOT NULL,  -- success, partial, failed
    output_data TEXT,  -- JSON: files changed, tests run, etc.
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

-- Get all results for an issue
SELECT result_type, output_data, created_at
FROM myapp_results
WHERE issue_id = ?
ORDER BY created_at DESC;
```

## Programmatic Access

Use bd's `--json` flags for scripting:

```bash
#!/bin/bash

# Find ready work
READY=$(bd ready --limit 1 --json)
ISSUE_ID=$(echo $READY | jq -r '.[0].id')

if [ "$ISSUE_ID" = "null" ]; then
    echo "No ready work"
    exit 0
fi

# Create execution record in your table
sqlite3 .beads/myapp.db <<SQL
INSERT INTO myapp_executions (issue_id, agent_id, status, started_at)
VALUES ('$ISSUE_ID', 'agent-1', 'running', datetime('now'));
SQL

# Claim issue in bd
bd update $ISSUE_ID --status in_progress

# Execute work...
echo "Working on $ISSUE_ID"

# Mark complete
bd close $ISSUE_ID --reason "Completed by agent-1"
sqlite3 .beads/myapp.db <<SQL
UPDATE myapp_executions
SET status = 'completed', completed_at = datetime('now')
WHERE issue_id = '$ISSUE_ID';
SQL
```

## Direct Database Access

You can always access bd's database directly:

```go
import (
    "database/sql"
    _ "modernc.org/sqlite"
    "github.com/steveyegge/beads"
)

// Auto-discover bd's database path
dbPath := beads.FindDatabasePath()
if dbPath == "" {
    log.Fatal("No bd database found. Run 'bd init' first.")
}

// Open the same database bd uses
db, err := sql.Open("sqlite", dbPath)
if err != nil {
    log.Fatal(err)
}

// Query bd's tables directly
var title string
var priority int
err = db.QueryRow(`
    SELECT title, priority FROM issues WHERE id = ?
`, issueID).Scan(&title, &priority)

// Update your tables
_, err = db.Exec(`
    INSERT INTO myapp_executions (issue_id, status) VALUES (?, ?)
`, issueID, "running")

// Find corresponding JSONL path (for git hooks, monitoring, etc.)
jsonlPath := beads.FindJSONLPath(dbPath)
fmt.Printf("BD exports to: %s\n", jsonlPath)
```

## Summary

The key insight: **bd is a focused issue tracker, not a framework**.

By extending the database:
- You get powerful issue tracking for free
- Your app adds orchestration logic
- Simple SQL joins give you full visibility
- No tight coupling or version conflicts

This pattern scales from simple scripts to complex orchestrators like VC.

## See Also

- [README.md](README.md) - Complete bd documentation
- Run `bd quickstart` - Interactive tutorial
- Check out VC's implementation at `github.com/steveyegge/vc` for a real-world example
