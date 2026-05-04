# Design: `bd unclaim` Command

**Issue:** gastownhall/beads#3693
**Date:** 2026-05-03
**Status:** Draft

## Problem

When an agent crashes mid-work, the issue gets stuck with:
- `assignee` set to the crashed agent
- `status` = `in_progress`

To re-claim the issue, users must manually:
1. Remove the assignee (`bd assign <id> ""`)
2. Change status to open (`bd update <id> --status open`)

This is error-prone and undocumented. Additionally, `bd claim` silently fails when an issue has an assignee but status is `open` (a different stuck state), with no guidance on how to fix it.

## Solution

Add a `bd unclaim` command that atomically:
1. Clears the assignee (sets to empty string)
2. Resets status to `open`
3. Records an `"unclaimed"` event

Also improve `bd claim` error messages to guide users toward `bd unclaim` when they encounter stuck issues.

## Design Decisions

### Command Placement
**Decision:** Standalone top-level command (`bd unclaim <id>`)

**Rationale:** Cleaner UX than a flag on `bd update`. Mirrors `bd reopen` pattern. Easier to discover.

### Scope
**Decision:** Regular issues only (no wisp support)

**Rationale:** Wisps are ephemeral and less likely to get stuck. Wisp support can be added later if needed.

### Atomicity
**Decision:** Storage-layer `UnclaimIssue` method with transaction semantics

**Rationale:** Consistent with `ClaimIssue` pattern. Ensures both fields are updated atomically with event recording.

## Implementation

### 1. Storage Layer

#### New file: `internal/storage/issueops/unclaim.go`

```go
package issueops

// UnclaimIssueInTx atomically unclaims an issue.
// Sets assignee to "" and status to "open".
// Records an "unclaimed" event.
// Only works on issues that have an assignee and status is "open" or "in_progress".
// Returns error if:
//   - Issue is closed (cannot unclaim closed issues)
//   - Issue has no assignee (nothing to unclaim)
func UnclaimIssueInTx(ctx context.Context, tx *sql.Tx, id string, actor string) error {
    // 1. Read current issue for event recording
    // 2. If status is "closed", return error: "cannot unclaim closed issue %s"
    // 3. If assignee is empty/null, return error: "issue %s is not assigned"
    // 4. UPDATE issues SET assignee = '', status = 'open', updated_at = ? WHERE id = ?
    // 5. Record "unclaimed" event
}
```

#### Storage interface addition: `internal/storage/storage.go`

```go
type Storage interface {
    // ... existing methods ...
    UnclaimIssue(ctx context.Context, id string, actor string) error
}
```

#### Dolt implementation: `internal/storage/dolt/issues.go`

```go
func (s *DoltStore) UnclaimIssue(ctx context.Context, id string, actor string) error {
    // 1. Begin transaction
    // 2. Call issueops.UnclaimIssueInTx
    // 3. DOLT_ADD/COMMIT
    // 4. Invalidate blocked IDs cache
}
```

#### Embedded Dolt implementation: `internal/storage/embeddeddolt/issues.go`

```go
func (s *EmbeddedDoltStore) UnclaimIssue(ctx context.Context, id string, actor string) error {
    // Same pattern as DoltStore
}
```

### 2. CLI Command

#### New file: `cmd/bd/unclaim.go`

```go
var unclaimCmd = &cobra.Command{
    Use:     "unclaim [id...]",
    GroupID: "issues",
    Short:   "Release a claimed issue",
    Long: `Release a claimed issue by clearing the assignee and resetting status to 'open'.

Use this when an agent crashes mid-work or you need to abandon a claimed task.
The issue becomes available for re-claiming by other agents.

Examples:
  bd unclaim bd-123
  bd unclaim bd-123 --reason "Agent crashed"
  bd unclaim bd-123 bd-456`,
    Args: cobra.MinimumNArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        // For each ID:
        //   1. Resolve issue
        //   2. Call issueStore.UnclaimIssue
        //   3. Output: "✓ Unclaimed bd-123" (or with reason)
        //   4. Support --json flag
    },
}
```

### 3. Claim Error Enhancement

#### Modified: `internal/storage/issueops/claim.go`

In `ClaimIssueInTx`, when `rowsAffected == 0`:

```go
if assignee != "" && currentStatus == types.StatusOpen {
    return nil, fmt.Errorf(
        "issue already assigned to %q. Use `bd unclaim %s` to release it before re-claiming",
        assignee, id,
    )
}
```

### 4. Documentation Updates

| File | Change |
|------|--------|
| `docs/CLI_REFERENCE.md` | Add `bd unclaim` command reference |
| `docs/QUICKSTART.md` | Add unclaim to workflow examples |
| `website/docs/cli-reference/issues.md` | Add unclaim to website docs |
| `cmd/bd/prime.go` | Add unclaim to agent guidance output |
| `cmd/bd/setup/junie.go` | Add unclaim to setup template |
| `cmd/bd/setup/aider.go` | Add unclaim to setup template |
| `cmd/bd/setup/cursor.go` | Add unclaim to setup template |

### 5. Testing

#### New file: `internal/storage/issueops/unclaim_test.go`

- Test unclaim clears assignee and sets status to open (from in_progress)
- Test unclaim clears assignee and sets status to open (from open — stuck claim)
- Test unclaim on closed issue returns error
- Test unclaim on unassigned issue returns error
- Test event recording

#### New file: `cmd/bd/unclaim_test.go`

- Test `bd unclaim bd-123` succeeds (from in_progress)
- Test `bd unclaim bd-123` succeeds (from open — stuck claim)
- Test `bd unclaim bd-123 --reason "crashed"` records reason
- Test `bd unclaim bd-123 --json` outputs JSON
- Test `bd unclaim` with multiple IDs
- Test error on non-existent issue
- Test error on closed issue
- Test error on unassigned issue

#### New file: `cmd/bd/unclaim_embedded_test.go`

- Same scenarios with embedded Dolt backend

#### Update: `cmd/bd/update_embedded_test.go`

- Test that claiming an assigned-but-open issue returns guidance message

## Acceptance Criteria

- [ ] `bd unclaim <id>` clears assignee and sets status to open (from in_progress or open)
- [ ] `bd unclaim <id>` returns error if issue is closed
- [ ] `bd unclaim <id>` returns error if issue has no assignee
- [ ] `bd unclaim <id> --reason "reason"` records the reason
- [ ] `bd unclaim <id> --json` outputs JSON
- [ ] `bd unclaim` with multiple IDs works
- [ ] `bd claim` on assigned-but-open issue returns guidance message
- [ ] Documentation updated for unclaim command
- [ ] Documentation updated for claim command with alert about unclaim
- [ ] All tests pass
