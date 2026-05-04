# Beads Library Usage Example

This example demonstrates using Beads as a Go library in external projects (like VC).

## Why Use Beads as a Library?

Instead of spawning `bd` CLI processes:
- ✅ **Direct API access** - Call functions directly instead of parsing JSON output
- ✅ **Type safety** - Compile-time checking of types and interfaces
- ✅ **Performance** - No process spawn overhead
- ✅ **Transactions** - Access to database transactions for complex operations
- ✅ **Shared database** - Multiple components can use same database connection
- ✅ **Error handling** - Proper Go error types instead of parsing stderr

## Installation

In your Go project:

```bash
go get github.com/steveyegge/beads@latest
```

## Basic Usage

```go
package main

import (
    "context"
    "log"
    
    "github.com/steveyegge/beads"
)

func main() {
    ctx := context.Background()
    
    // Find and open database
    dbPath := beads.FindDatabasePath()
    store, err := beads.Open(ctx, dbPath)
    if err != nil {
        log.Fatal(err)
    }
    defer store.Close()
    
    // Get ready work
    ready, err := store.GetReadyWork(ctx, beads.WorkFilter{
        Status: beads.StatusOpen,
        Limit: 10,
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // Process ready issues...
}
```

## Running This Example

```bash
# From this directory
cd examples/library-usage

# Make sure there's a Beads database
bd init --prefix demo

# Run the example
go run main.go
```

## Available Operations

The `beads.Storage` interface provides:

### Issues
- `CreateIssue(ctx, issue, actor)` - Create a new issue
- `CreateIssues(ctx, issues, actor)` - Batch create issues
- `GetIssue(ctx, id)` - Get issue by ID
- `UpdateIssue(ctx, id, updates, actor)` - Update issue fields
- `CloseIssue(ctx, id, reason, actor)` - Close an issue
- `SearchIssues(ctx, query, filter)` - Search with filters

### Dependencies
- `AddDependency(ctx, dep, actor)` - Add dependency between issues
- `RemoveDependency(ctx, issueID, dependsOnID, actor)` - Remove dependency
- `GetDependencies(ctx, issueID)` - Get what this issue depends on
- `GetDependents(ctx, issueID)` - Get what depends on this issue
- `GetDependencyTree(ctx, issueID, maxDepth, showAllPaths)` - Visualize tree

### Labels
- `AddLabel(ctx, issueID, label, actor)` - Add label to issue
- `RemoveLabel(ctx, issueID, label, actor)` - Remove label
- `GetLabels(ctx, issueID)` - Get all labels for an issue
- `GetIssuesByLabel(ctx, label)` - Find issues with label

### Ready Work & Blocking
- `GetReadyWork(ctx, filter)` - Find issues with no blockers
- `GetBlockedIssues(ctx)` - Find blocked issues with blocker info
- `GetEpicsEligibleForClosure(ctx)` - Find completable epics

### Comments & Events
- `AddIssueComment(ctx, issueID, author, text)` - Add comment
- `GetIssueComments(ctx, issueID)` - Get all comments
- `GetEvents(ctx, issueID, limit)` - Get audit trail

### Statistics
- `GetStatistics(ctx)` - Get aggregate metrics

## Types

All types are exported via the `beads` package:

```go
// Core types
beads.Issue
beads.Status (Open, InProgress, Closed, Blocked)
beads.IssueType (Bug, Feature, Task, Epic, Chore)
beads.Priority (0-4)

// Relationships
beads.Dependency
beads.DependencyType (Blocks, Related, ParentChild, DiscoveredFrom)

// Metadata
beads.Label
beads.Comment
beads.Event

// Queries
beads.IssueFilter
beads.WorkFilter
beads.BlockedIssue
beads.EpicStatus
beads.Statistics
```

## VC Integration Example

For VC (VibeCoder), the integration would look like:

```go
// In VC's storage layer
type VCStorage struct {
    beads beads.Storage
}

func NewVCStorage(ctx context.Context, dbPath string) (*VCStorage, error) {
    store, err := beads.Open(ctx, dbPath)
    if err != nil {
        return nil, err
    }
    
    return &VCStorage{beads: store}, nil
}

// Claim ready work for executor
func (s *VCStorage) ClaimWork(ctx context.Context, executorID string) (*beads.Issue, error) {
    ready, err := s.beads.GetReadyWork(ctx, beads.WorkFilter{
        Status: beads.StatusOpen,
        Limit: 1,
    })
    if err != nil {
        return nil, err
    }
    
    if len(ready) == 0 {
        return nil, nil // No work available
    }
    
    issue := ready[0]
    
    // Claim it
    updates := map[string]interface{}{
        "status": beads.StatusInProgress,
        "assignee": executorID,
    }
    
    if err := s.beads.UpdateIssue(ctx, issue.ID, updates, executorID); err != nil {
        return nil, err
    }
    
    return issue, nil
}
```

## Best Practices

1. **Context** - Always pass `context.Context` for cancellation support
2. **Actor** - Provide meaningful actor strings for audit trail
3. **Error handling** - Check all errors; database operations can fail
4. **Close** - Always `defer store.Close()` after opening
5. **Transactions** - For complex multi-step operations, consider using the underlying database connection directly

## See Also

- [bd-example-extension-go](../bd-example-extension-go/README.md) - Working extension example
- [beads.go](../../beads.go) - Public API source
- [internal/storage/storage.go](../../internal/storage/storage.go) - Storage interface
