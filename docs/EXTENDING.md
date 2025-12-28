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
    db, err := sql.Open("sqlite3", dbPath)
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

Here's how the VC (VibeCoder) orchestrator extends bd using `UnderlyingDB()`:

```go
package vc

import (
    "database/sql"
    "github.com/steveyegge/beads"
    _ "modernc.org/sqlite"
)

type VCStorage struct {
    beads.Storage  // Embed bd's storage
    db *sql.DB     // Cache the underlying DB
}

func NewVCStorage(dbPath string) (*VCStorage, error) {
    // Open bd's storage
    store, err := beads.NewSQLiteStorage(dbPath)
    if err != nil {
        return nil, err
    }
    
    vc := &VCStorage{
        Storage: store,
        db:      store.UnderlyingDB(),
    }
    
    // Create VC-specific tables
    if err := vc.initSchema(); err != nil {
        return nil, err
    }
    
    return vc, nil
}

func (vc *VCStorage) initSchema() error {
    schema := `
    -- VC's orchestration layer
    CREATE TABLE IF NOT EXISTS vc_executor_instances (
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
    
    CREATE TABLE IF NOT EXISTS vc_execution_state (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        executor_id TEXT NOT NULL,
        phase TEXT NOT NULL,  -- assessment, execution, analysis
        state_data TEXT NOT NULL,  -- JSON checkpoint data
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (executor_id) REFERENCES vc_executor_instances(id) ON DELETE CASCADE
    );
    
    CREATE INDEX IF NOT EXISTS idx_vc_executor_issue ON vc_executor_instances(issue_id);
    CREATE INDEX IF NOT EXISTS idx_vc_executor_status ON vc_executor_instances(status);
    CREATE INDEX IF NOT EXISTS idx_vc_execution_executor ON vc_execution_state(executor_id);
    `
    
    _, err := vc.db.Exec(schema)
    return err
}

// ClaimReadyWork atomically claims the highest priority ready work
func (vc *VCStorage) ClaimReadyWork(agentName string) (*ExecutorInstance, error) {
    query := `
    UPDATE vc_executor_instances
    SET status = 'executing', claimed_at = CURRENT_TIMESTAMP, agent_name = ?
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
    RETURNING id, issue_id, executor_type, status, agent_name, claimed_at
    `
    
    var ei ExecutorInstance
    err := vc.db.QueryRow(query, agentName).Scan(
        &ei.ID, &ei.IssueID, &ei.ExecutorType,
        &ei.Status, &ei.AgentName, &ei.ClaimedAt,
    )
    return &ei, err
}
```

**Key benefits of this approach:**
- ✅ VC extends bd without forking or modifying it
- ✅ Single database = simple JOINs across layers
- ✅ Foreign keys ensure referential integrity
- ✅ bd handles issue tracking, VC handles orchestration
- ✅ Can use bd's CLI alongside VC's custom operations

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

### Using UnderlyingDB() (Recommended)

The recommended way to extend bd is using the `UnderlyingDB()` method on the storage instance. This gives you access to the same database connection that bd uses, ensuring consistency and avoiding connection overhead:

```go
import (
    "database/sql"
    "github.com/steveyegge/beads"
    _ "modernc.org/sqlite"
)

// Open bd's storage
store, err := beads.NewSQLiteStorage(".beads/issues.db")
if err != nil {
    log.Fatal(err)
}
defer store.Close()

// Get the underlying database connection
db := store.UnderlyingDB()

// Create your extension tables using the same connection
schema := `
CREATE TABLE IF NOT EXISTS myapp_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    status TEXT NOT NULL,
    agent_id TEXT,
    started_at DATETIME,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);
`

if _, err := db.Exec(schema); err != nil {
    log.Fatal(err)
}

// Query bd's tables
var title string
var priority int
err = db.QueryRow(`
    SELECT title, priority FROM issues WHERE id = ?
`, issueID).Scan(&title, &priority)

// Update your tables
_, err = db.Exec(`
    INSERT INTO myapp_executions (issue_id, status, agent_id, started_at)
    VALUES (?, ?, ?, CURRENT_TIMESTAMP)
`, issueID, "running", "agent-1")

// Join across layers
rows, err := db.Query(`
    SELECT i.id, i.title, e.status, e.agent_id
    FROM issues i
    JOIN myapp_executions e ON i.id = e.issue_id
    WHERE e.status = 'running'
`)
```

**Safety warnings when using UnderlyingDB():**

⚠️ **NEVER** close the database connection returned by `UnderlyingDB()`. The storage instance owns this connection.

⚠️ **DO NOT** modify database pool settings (SetMaxOpenConns, SetConnMaxIdleTime) or SQLite PRAGMAs (WAL mode, journal settings) as this affects bd's core operations.

⚠️ **Keep transactions short** - Long write transactions will block bd's core operations. Use read transactions when possible.

⚠️ **Expect errors after Close()** - Once you call `store.Close()`, operations on the underlying DB will fail. Use context cancellation to coordinate shutdown.

✅ **DO** use foreign keys to reference bd's tables for referential integrity.

✅ **DO** namespace your tables with your app name (e.g., `myapp_executions`).

✅ **DO** create indexes for your query patterns.

### When to use UnderlyingDB() vs sql.Open()

**Use `UnderlyingDB()`:**
- ✅ When you want to share the storage connection
- ✅ When you need tables in the same database as bd
- ✅ When you want automatic lifecycle management
- ✅ For most extension use cases (like VC)

**Use `sql.Open()` separately:**
- When you need independent connection pool settings
- When you need different timeout/retry behavior
- When you're managing multiple databases
- When you need fine-grained connection control

### Alternative: Independent Connection

If you need independent connection management, you can still open the database directly:

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

// Open your own connection to the same database
db, err := sql.Open("sqlite3", dbPath)
if err != nil {
    log.Fatal(err)
}
defer db.Close()

// Configure your connection independently
db.SetMaxOpenConns(10)
db.SetConnMaxIdleTime(time.Minute)

// Query bd's tables
var title string
var priority int
err = db.QueryRow(`
    SELECT title, priority FROM issues WHERE id = ?
`, issueID).Scan(&title, &priority)

// Find corresponding JSONL path (for git hooks, monitoring, etc.)
jsonlPath := beads.FindJSONLPath(dbPath)
fmt.Printf("BD exports to: %s\n", jsonlPath)
```

## Batch Operations for Performance

When creating many issues at once (e.g., bulk imports, batch processing), use `CreateIssues` for significantly better performance:

```go
import (
    "context"
    "github.com/steveyegge/beads/internal/storage/sqlite"
    "github.com/steveyegge/beads/internal/types"
)

// Open bd's storage
store, err := sqlite.New(".beads/issues.db")
if err != nil {
    log.Fatal(err)
}

ctx := context.Background()

// Prepare batch of issues to create
issues := make([]*types.Issue, 0, 1000)
for _, item := range externalData {
    issue := &types.Issue{
        Title:      item.Title,
        Description: item.Description,
        Status:     types.StatusOpen,
        Priority:   item.Priority,
        IssueType:  types.TypeTask,
    }
    issues = append(issues, issue)
}

// Create all issues in a single atomic transaction (5-15x faster!)
if err := store.CreateIssues(ctx, issues, "import"); err != nil {
    log.Fatal(err)
}

// REMOVED (bd-c7af): SyncAllCounters - no longer needed with hash IDs
```

### Performance Comparison

| Operation | CreateIssue Loop | CreateIssues Batch | Speedup |
|-----------|------------------|---------------------|---------|
| 100 issues | ~900ms | ~30ms | 30x |
| 500 issues | ~4.5s | ~800ms | 5.6x |
| 1000 issues | ~9s | ~950ms | 9.5x |

### When to Use Each Method

**Use `CreateIssue` (single issue):**
- Interactive CLI commands (`bd create`)
- Single issue creation in your app
- User-facing operations

**Use `CreateIssues` (batch):**
- Bulk imports from external systems
- Batch processing workflows
- Creating multiple related issues at once
- Agent workflows that generate many issues

### Batch Import Pattern

```go
// Example: Import from external issue tracker
func ImportFromExternal(externalIssues []ExternalIssue) error {
    store, err := sqlite.New(".beads/issues.db")
    if err != nil {
        return err
    }
    ctx := context.Background()
    
    // Convert external format to bd format
    issues := make([]*types.Issue, 0, len(externalIssues))
    for _, ext := range externalIssues {
        issue := &types.Issue{
            ID:          fmt.Sprintf("import-%d", ext.ID),  // Explicit IDs
            Title:       ext.Title,
            Description: ext.Description,
            Status:      convertStatus(ext.Status),
            Priority:    ext.Priority,
            IssueType:   convertType(ext.Type),
        }
        
        // Normalize closed_at for closed issues
        if issue.Status == types.StatusClosed {
            closedAt := ext.ClosedAt
            issue.ClosedAt = &closedAt
        }
        
        issues = append(issues, issue)
    }
    
    // Atomic batch create
    if err := store.CreateIssues(ctx, issues, "external-import"); err != nil {
        return fmt.Errorf("batch create failed: %w", err)
    }
    
    // REMOVED (bd-c7af): SyncAllCounters - no longer needed with hash IDs
    
    return nil
}
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

- [README.md](../README.md) - Complete bd documentation
- [QUICKSTART.md](QUICKSTART.md) - Quick start tutorial
- Check out VC's implementation at `github.com/steveyegge/vc` for a real-world example
