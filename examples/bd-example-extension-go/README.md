# BD Extension Example (Go)

This example demonstrates how to extend bd with custom tables for application-specific orchestration, following the patterns described in [EXTENDING.md](../../docs/EXTENDING.md).

## What This Example Shows

1. **Schema Extension**: Adding custom tables (`example_executions`, `example_checkpoints`) to bd's SQLite database
2. **Foreign Key Integration**: Linking extension tables to bd's `issues` table with proper cascading
3. **Dual-Layer Access**: Using bd's Go API for issue management while directly querying extension tables
4. **Complex Queries**: Joining bd's issues with extension tables for powerful insights
5. **Execution Tracking**: Implementing agent assignment, checkpointing, and crash recovery patterns

## Key Patterns Illustrated

### Pattern 1: Namespace Your Tables

All tables are prefixed with `example_` to avoid conflicts:

```sql
CREATE TABLE example_executions (...)
CREATE TABLE example_checkpoints (...)
```

### Pattern 2: Foreign Key Relationships

Extension tables link to bd's issues with cascading deletes:

```sql
FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
```

### Pattern 3: Index Common Queries

Indexes are created for frequent query patterns:

```sql
CREATE INDEX idx_executions_status ON example_executions(status);
CREATE INDEX idx_executions_issue ON example_executions(issue_id);
```

### Pattern 4: Layer Separation

- **bd layer**: Issue tracking, dependencies, ready work
- **Extension layer**: Execution state, agent assignments, checkpoints

### Pattern 5: Join Queries

Powerful queries join both layers:

```sql
SELECT i.id, i.title, i.priority, e.status, e.agent_id, COUNT(c.id)
FROM issues i
LEFT JOIN example_executions e ON i.id = e.issue_id
LEFT JOIN example_checkpoints c ON e.id = c.execution_id
GROUP BY i.id, e.id
```

## Building and Running

### Prerequisites

- Go 1.24 or later
- bd initialized in a directory (run `bd init --prefix demo`)

### Install

```bash
# Install from the repository
go install github.com/steveyegge/beads/examples/bd-example-extension-go@latest

# Or install from local source
cd examples/bd-example-extension-go
go install .
```

The binary will be installed as `bd-example-extension-go` in your `$GOPATH/bin` (or `$GOBIN` if set).

### Running

```bash
# Auto-discover database and run
bd-example-extension-go

# Or specify database path
bd-example-extension-go -db .beads/demo.db
```

**Output:**
```
Claiming: demo-5
  ✓ assess
  ✓ implement
  ✓ test

Status:
  demo-4: Fix memory leak [closed] agent=agent-demo checkpoints=3
  demo-1: Implement auth [in_progress] agent=agent-alice checkpoints=0
  demo-5: Test minimized [closed] agent=demo-agent checkpoints=3
```

## Code Structure

**Just 116 lines total** - minimal, focused extension example.

- **main.go** (93 lines): Complete workflow with embedded schema
- **schema.sql** (23 lines): Extension tables (`example_executions`, `example_checkpoints`) with foreign keys and indexes

Demonstrates:
1. Auto-discover database (`beads.FindDatabasePath`)
2. Dual-layer access (bd API + direct SQL)
3. Execution tracking with checkpoints
4. Complex joined queries across layers

## Example Queries

### Find Running Executions with Checkpoint Count

```go
query := `
    SELECT i.id, i.title, e.status, e.agent_id, COUNT(c.id) as checkpoints
    FROM issues i
    INNER JOIN example_executions e ON i.id = e.issue_id
    LEFT JOIN example_checkpoints c ON e.id = c.execution_id
    WHERE e.status = 'running'
    GROUP BY i.id, e.id
`
```

### Find Failed Executions

```go
query := `
    SELECT i.id, i.title, e.error, e.completed_at
    FROM issues i
    INNER JOIN example_executions e ON i.id = e.issue_id
    WHERE e.status = 'failed'
    ORDER BY e.completed_at DESC
`
```

### Get Latest Checkpoint for Recovery

```go
query := `
    SELECT checkpoint_data
    FROM example_checkpoints
    WHERE execution_id = ?
    ORDER BY created_at DESC
    LIMIT 1
`
```

## Integration with bd

### Using bd's Go API

```go
// Auto-discover database path
dbPath := beads.FindDatabasePath()
if dbPath == "" {
    log.Fatal("No bd database found")
}

// Open bd storage
store, err := beads.NewSQLiteStorage(dbPath)

// Find ready work
readyIssues, err := store.GetReadyWork(ctx, beads.WorkFilter{Limit: 10})

// Update issue status
updates := map[string]interface{}{"status": beads.StatusInProgress}
err = store.UpdateIssue(ctx, issueID, updates, "agent-name")

// Close issue
err = store.CloseIssue(ctx, issueID, "Completed", "agent-name")

// Find corresponding JSONL path (for git hooks, monitoring, etc.)
jsonlPath := beads.FindJSONLPath(dbPath)
```

### Direct Database Access

```go
// Open same database for extension tables
db, err := sql.Open("sqlite3", dbPath)

// Initialize extension schema
_, err = db.Exec(Schema)

// Query extension tables
rows, err := db.Query("SELECT * FROM example_executions WHERE status = ?", "running")
```

## Testing the Example

1. **Initialize bd:**
   ```bash
   bd init --prefix demo
   ```

2. **Create some test issues:**
   ```bash
   bd create "Implement authentication" -p 1 -t feature
   bd create "Add API documentation" -p 1 -t task
   bd create "Refactor database layer" -p 2 -t task
   ```

3. **Run the demo:**
   ```bash
   bd-example-extension-go -cmd demo
   ```

4. **Check the results:**
   ```bash
   bd list
   sqlite3 .beads/demo.db "SELECT * FROM example_executions"
   ```

## Real-World Usage

This pattern is used in production by:

- **VC (VibeCoder)**: Multi-agent orchestration with state machines
- **CI/CD Systems**: Build tracking and artifact management
- **Task Runners**: Parallel execution with dependency resolution

See [EXTENDING.md](../../EXTENDING.md) for more patterns and the VC implementation example.

## Next Steps

1. **Add Your Own Tables**: Extend the schema with application-specific tables
2. **Implement State Machines**: Use checkpoints for resumable workflows
3. **Add Metrics**: Track execution times, retry counts, success rates
4. **Build Dashboards**: Query joined data for visibility
5. **Integrate with Agents**: Use bd's ready work queue for agent orchestration

## See Also

- [EXTENDING.md](../../EXTENDING.md) - Complete extension guide
- [../../README.md](../../README.md) - bd documentation
- [QUICKSTART.md](../../docs/QUICKSTART.md) - Quick start tutorial
