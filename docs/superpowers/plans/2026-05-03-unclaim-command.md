# `bd unclaim` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `bd unclaim` command to release stuck claimed issues and improve `bd claim` error messages.

**Architecture:** Storage-layer `UnclaimIssue` method (mirrors `ClaimIssue`) with CLI command following `reopen.go` pattern. Atomic operation clears assignee and resets status to `open`.

**Tech Stack:** Go, Cobra CLI, Dolt SQL database, issueops transaction pattern

---

## File Structure

| File | Action | Purpose |
|------|--------|---------|
| `internal/storage/issueops/unclaim.go` | Create | Core unclaim logic in transaction |
| `internal/storage/issueops/unclaim_test.go` | Create | Unit tests for UnclaimIssueInTx |
| `internal/storage/storage.go` | Modify | Add UnclaimIssue to Storage interface |
| `internal/storage/dolt/issues.go` | Modify | DoltStore.UnclaimIssue implementation |
| `internal/storage/embeddeddolt/issues.go` | Modify | EmbeddedDoltStore.UnclaimIssue implementation |
| `internal/storage/issueops/claim.go` | Modify | Improve error message for assigned-but-open |
| `cmd/bd/unclaim.go` | Create | CLI command |
| `cmd/bd/unclaim_test.go` | Create | CLI integration tests |
| `cmd/bd/unclaim_embedded_test.go` | Create | Embedded Dolt CLI tests |
| `cmd/bd/update_embedded_test.go` | Modify | Add claim error guidance test |
| `docs/CLI_REFERENCE.md` | Modify | Add unclaim documentation |
| `docs/QUICKSTART.md` | Modify | Add unclaim to workflow |
| `website/docs/cli-reference/issues.md` | Modify | Add unclaim to website docs |
| `cmd/bd/prime.go` | Modify | Add unclaim to agent guidance |
| `cmd/bd/setup/junie.go` | Modify | Add unclaim to setup template |
| `cmd/bd/setup/aider.go` | Modify | Add unclaim to setup template |
| `cmd/bd/setup/cursor.go` | Modify | Add unclaim to setup template |

---

## Task 1: Storage Layer — `UnclaimIssueInTx`

**Files:**
- Create: `internal/storage/issueops/unclaim.go`
- Create: `internal/storage/issueops/unclaim_test.go`

### Step 1: Write failing tests for UnclaimIssueInTx

Create `internal/storage/issueops/unclaim_test.go`:

```go
package issueops

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestUnclaimIssueInTx_ClearsAssigneeAndStatus(t *testing.T) {
	// Setup: Create issue with assignee and status=in_progress
	// Act: Call UnclaimIssueInTx
	// Assert: assignee="" and status="open"
}

func TestUnclaimIssueInTx_FromOpenStuckClaim(t *testing.T) {
	// Setup: Create issue with assignee and status=open (stuck claim)
	// Act: Call UnclaimIssueInTx
	// Assert: assignee="" and status="open"
}

func TestUnclaimIssueInTx_ClosedIssueReturnsError(t *testing.T) {
	// Setup: Create issue with status=closed
	// Act: Call UnclaimIssueInTx
	// Assert: Error contains "cannot unclaim closed issue"
}

func TestUnclaimIssueInTx_UnassignedIssueReturnsError(t *testing.T) {
	// Setup: Create issue with no assignee and status=open
	// Act: Call UnclaimIssueInTx
	// Assert: Error contains "is not assigned"
}

func TestUnclaimIssueInTx_RecordsEvent(t *testing.T) {
	// Setup: Create issue with assignee and status=in_progress
	// Act: Call UnclaimIssueInTx
	// Assert: Event recorded with type "unclaimed"
}
```

### Step 2: Run tests to verify they fail

```bash
go test -tags gms_pure_go -run 'TestUnclaimIssueInTx' ./internal/storage/issueops/...
```

Expected: FAIL — `UnclaimIssueInTx` not defined

### Step 3: Implement UnclaimIssueInTx

Create `internal/storage/issueops/unclaim.go`:

```go
package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// UnclaimIssueInTx atomically unclaims an issue.
// Sets assignee to "" and status to "open".
// Records an "unclaimed" event.
// Only works on issues that have an assignee and status is "open" or "in_progress".
// Returns error if:
//   - Issue is closed (cannot unclaim closed issues)
//   - Issue has no assignee (nothing to unclaim)
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueInTx(ctx context.Context, tx *sql.Tx, id string, actor string) error {
	// Read current issue
	issueTable := "issues"
	eventTable := "events"

	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for unclaim: %w", err)
	}

	// Validate: cannot unclaim closed issues
	if oldIssue.Status == types.StatusClosed {
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}

	// Validate: must have an assignee to unclaim
	if oldIssue.Assignee == "" {
		return fmt.Errorf("issue %s is not assigned", id)
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee and reset status to open
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = 'open', updated_at = ?
		WHERE id = ? AND assignee != '' AND status IN ('open', 'in_progress')
	`, issueTable), now, id)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
	}

	// Record the unclaim event
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": "",
		"status":   "open",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "unclaimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record unclaim event: %w", err)
	}

	return nil
}
```

### Step 4: Run tests to verify they pass

```bash
go test -tags gms_pure_go -run 'TestUnclaimIssueInTx' ./internal/storage/issueops/...
```

Expected: PASS

### Step 5: Commit

```bash
git add internal/storage/issueops/unclaim.go internal/storage/issueops/unclaim_test.go
git commit -m "feat: add UnclaimIssueInTx storage operation"
```

---

## Task 2: Storage Interface + Dolt Implementation

**Files:**
- Modify: `internal/storage/storage.go`
- Modify: `internal/storage/dolt/issues.go`

### Step 1: Add UnclaimIssue to Storage interface

In `internal/storage/storage.go`, add to the `Storage` interface:

```go
type Storage interface {
	// ... existing methods ...
	UnclaimIssue(ctx context.Context, id string, actor string) error
}
```

### Step 2: Implement DoltStore.UnclaimIssue

In `internal/storage/dolt/issues.go`, add after `ClaimIssue`:

```go
// UnclaimIssue atomically unclaims an issue by clearing the assignee
// and resetting status to "open". Records an "unclaimed" event.
// Delegates SQL work to issueops.UnclaimIssueInTx; handles Dolt-specific concerns
// (DOLT_ADD/COMMIT, cache invalidation).
func (s *DoltStore) UnclaimIssue(ctx context.Context, id string, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.UnclaimIssueInTx(ctx, tx, id, actor); err != nil {
		return err
	}

	// Dolt versioning for permanent issues.
	for _, table := range []string{"issues", "events"} {
		_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
	}
	commitMsg := fmt.Sprintf("bd: unclaim %s", id)
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
		commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return wrapTransactionError("commit unclaim issue", err)
	}
	// Unclaiming changes status to open, affecting blocked ID computation
	s.invalidateBlockedIDsCache()
	return nil
}
```

### Step 3: Build to verify compilation

```bash
go build ./internal/storage/...
```

Expected: SUCCESS

### Step 4: Commit

```bash
git add internal/storage/storage.go internal/storage/dolt/issues.go
git commit -m "feat: add UnclaimIssue to Storage interface and DoltStore"
```

---

## Task 3: Embedded Dolt Implementation

**Files:**
- Modify: `internal/storage/embeddeddolt/issues.go`

### Step 1: Implement EmbeddedDoltStore.UnclaimIssue

In `internal/storage/embeddeddolt/issues.go`, add after `ClaimIssue`:

```go
// UnclaimIssue atomically unclaims an issue by clearing the assignee
// and resetting status to "open". Records an "unclaimed" event.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) UnclaimIssue(ctx context.Context, id string, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.UnclaimIssueInTx(ctx, tx, id, actor)
	})
}
```

### Step 2: Build to verify compilation

```bash
go build ./internal/storage/...
```

Expected: SUCCESS

### Step 3: Commit

```bash
git add internal/storage/embeddeddolt/issues.go
git commit -m "feat: add UnclaimIssue to EmbeddedDoltStore"
```

---

## Task 4: Improve Claim Error Message

**Files:**
- Modify: `internal/storage/issueops/claim.go`

### Step 1: Update error message in ClaimIssueInTx

In `internal/storage/issueops/claim.go`, find the error handling block (around line 90-93):

```go
// Current code:
if assignee != "" && assignee != actor {
    return nil, fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, assignee)
}
```

Change to:

```go
if assignee != "" && assignee != actor {
    if currentStatus == types.StatusOpen {
        return nil, fmt.Errorf("issue already assigned to %q. Use `bd unclaim %s` to release it before re-claiming", assignee, id)
    }
    return nil, fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, assignee)
}
```

### Step 2: Run existing claim tests

```bash
go test -tags gms_pure_go -run 'TestClaim' ./internal/storage/issueops/...
```

Expected: PASS (existing tests still pass)

### Step 3: Commit

```bash
git add internal/storage/issueops/claim.go
git commit -m "feat: improve claim error message for assigned-but-open issues"
```

---

## Task 5: CLI Command — `bd unclaim`

**Files:**
- Create: `cmd/bd/unclaim.go`

### Step 1: Create unclaim.go command

Create `cmd/bd/unclaim.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

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
		CheckReadonly("unclaim")
		reason, _ := cmd.Flags().GetString("reason")
		ctx := rootCtx

		unclaimedIssues := []*types.Issue{}
		hasError := false
		if store == nil {
			FatalErrorWithHint("database not initialized",
				diagHint())
		}
		for _, id := range args {
			// Resolve with prefix routing
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hasError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store

			if err := issueStore.UnclaimIssue(ctx, fullID, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error unclaiming %s: %v\n", fullID, err)
				hasError = true
				result.Close()
				continue
			}

			if reason != "" {
				if err := issueStore.AddIssueComment(ctx, fullID, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add reason comment: %v\n", err)
				}
			}

			if jsonOutput {
				updated, _ := issueStore.GetIssue(ctx, fullID)
				if updated != nil {
					unclaimedIssues = append(unclaimedIssues, updated)
				}
			} else {
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Unclaimed %s%s\n", ui.RenderPass("✓"), fullID, reasonMsg)
			}
			result.Close()
		}

		commandDidWrite.Store(true)

		if jsonOutput && len(unclaimedIssues) > 0 {
			outputJSON(unclaimedIssues)
		}

		if hasError {
			os.Exit(1)
		}
	},
}

func init() {
	unclaimCmd.Flags().StringP("reason", "r", "", "Reason for unclaiming")
	unclaimCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(unclaimCmd)
}
```

### Step 2: Build to verify compilation

```bash
go build ./cmd/bd/...
```

Expected: SUCCESS

### Step 3: Commit

```bash
git add cmd/bd/unclaim.go
git commit -m "feat: add bd unclaim CLI command"
```

---

## Task 6: CLI Tests

**Files:**
- Create: `cmd/bd/unclaim_test.go`
- Create: `cmd/bd/unclaim_embedded_test.go`

### Step 1: Create unclaim_test.go

Create `cmd/bd/unclaim_test.go`:

```go
package main

import (
	"testing"
)

func TestUnclaim_BasicSuccess(t *testing.T) {
	// Setup: Create issue, claim it
	// Act: bd unclaim <id>
	// Assert: Success output, assignee cleared, status=open
}

func TestUnclaim_FromOpenStuckClaim(t *testing.T) {
	// Setup: Create issue, set assignee manually, status=open
	// Act: bd unclaim <id>
	// Assert: Success output, assignee cleared, status=open
}

func TestUnclaim_WithReason(t *testing.T) {
	// Setup: Create issue, claim it
	// Act: bd unclaim <id> --reason "Agent crashed"
	// Assert: Success output includes reason, comment recorded
}

func TestUnclaim_JSONOutput(t *testing.T) {
	// Setup: Create issue, claim it
	// Act: bd unclaim <id> --json
	// Assert: Valid JSON output with updated issue
}

func TestUnclaim_MultipleIDs(t *testing.T) {
	// Setup: Create two issues, claim both
	// Act: bd unclaim <id1> <id2>
	// Assert: Both unclaimed successfully
}

func TestUnclaim_NonExistentIssue(t *testing.T) {
	// Act: bd unclaim nonexistent
	// Assert: Error output
}

func TestUnclaim_ClosedIssue(t *testing.T) {
	// Setup: Create issue, close it
	// Act: bd unclaim <id>
	// Assert: Error about closed issue
}

func TestUnclaim_UnassignedIssue(t *testing.T) {
	// Setup: Create issue (no assignee)
	// Act: bd unclaim <id>
	// Assert: Error about not assigned
}
```

### Step 2: Create unclaim_embedded_test.go

Create `cmd/bd/unclaim_embedded_test.go` with same test scenarios using embedded Dolt backend.

### Step 3: Run tests

```bash
go test -tags gms_pure_go -run 'TestUnclaim' ./cmd/bd/...
```

Expected: PASS

### Step 4: Commit

```bash
git add cmd/bd/unclaim_test.go cmd/bd/unclaim_embedded_test.go
git commit -m "test: add tests for bd unclaim command"
```

---

## Task 7: Claim Error Guidance Test

**Files:**
- Modify: `cmd/bd/update_embedded_test.go`

### Step 1: Add test for claim error guidance

In `cmd/bd/update_embedded_test.go`, add:

```go
func TestClaim_AssignedButOpen_ReturnsGuidance(t *testing.T) {
	// Setup: Create issue, manually set assignee, status=open
	// Act: bd update <id> --claim (with different actor)
	// Assert: Error contains "Use `bd unclaim"
}
```

### Step 2: Run test

```bash
go test -tags gms_pure_go -run 'TestClaim_AssignedButOpen' ./cmd/bd/...
```

Expected: PASS

### Step 3: Commit

```bash
git add cmd/bd/update_embedded_test.go
git commit -m "test: add test for claim error guidance on assigned-but-open issues"
```

---

## Task 8: Documentation Updates

**Files:**
- Modify: `docs/CLI_REFERENCE.md`
- Modify: `docs/QUICKSTART.md`
- Modify: `website/docs/cli-reference/issues.md`
- Modify: `cmd/bd/prime.go`
- Modify: `cmd/bd/setup/junie.go`
- Modify: `cmd/bd/setup/aider.go`
- Modify: `cmd/bd/setup/cursor.go`

### Step 1: Update CLI_REFERENCE.md

Add `bd unclaim` section after `bd reopen`:

```markdown
### bd unclaim

Release a claimed issue by clearing the assignee and resetting status to 'open'.

**Usage:** `bd unclaim <id> [id...] [--reason "reason"]`

**Flags:**
- `--reason, -r` — Reason for unclaiming

**Examples:**
```bash
bd unclaim bd-123
bd unclaim bd-123 --reason "Agent crashed"
bd unclaim bd-123 bd-456
```

**When to use:**
- Agent crashed mid-work
- Need to abandon a claimed task
- Issue is stuck with assignee but status is open
```

### Step 2: Update QUICKSTART.md

Add unclaim to workflow section:

```markdown
**Stuck issue?** If an agent crashes mid-work:
```bash
bd unclaim <id>  # Release the stuck issue
bd ready          # Find available work
```
```

### Step 3: Update website docs

In `website/docs/cli-reference/issues.md`, add unclaim command documentation.

### Step 4: Update prime.go

In `cmd/bd/prime.go`, add to agent guidance:

```markdown
- `bd unclaim <id>` — Release a stuck claimed issue
```

### Step 5: Update setup templates

In `cmd/bd/setup/junie.go`, `aider.go`, `cursor.go`, add:

```markdown
bd unclaim <id>               # Release stuck issue
```

### Step 6: Commit

```bash
git add docs/CLI_REFERENCE.md docs/QUICKSTART.md website/docs/cli-reference/issues.md cmd/bd/prime.go cmd/bd/setup/junie.go cmd/bd/setup/aider.go cmd/bd/setup/cursor.go
git commit -m "docs: add bd unclaim command documentation"
```

---

## Task 9: Final Verification

### Step 1: Run full test suite

```bash
make test
```

Expected: ALL PASS

### Step 2: Run linter

```bash
golangci-lint run ./...
```

Expected: No new warnings

### Step 3: Build binary

```bash
make install
```

Expected: SUCCESS

### Step 4: Manual smoke test

```bash
bd init --prefix test-unclaim
bd create "Test unclaim issue" -p 1
bd update <id> --claim
bd unclaim <id>
bd show <id>  # Verify assignee="" and status=open
```

### Step 5: Final commit (if any fixes needed)

```bash
git add -A
git commit -m "fix: address final verification issues"
```

---

## Acceptance Criteria Checklist

- [ ] `bd unclaim <id>` clears assignee and sets status to open (from in_progress)
- [ ] `bd unclaim <id>` clears assignee and sets status to open (from open — stuck claim)
- [ ] `bd unclaim <id>` returns error if issue is closed
- [ ] `bd unclaim <id>` returns error if issue has no assignee
- [ ] `bd unclaim <id> --reason "reason"` records the reason
- [ ] `bd unclaim <id> --json` outputs JSON
- [ ] `bd unclaim` with multiple IDs works
- [ ] `bd claim` on assigned-but-open issue returns guidance message
- [ ] Documentation updated for unclaim command
- [ ] Documentation updated for claim command with alert about unclaim
- [ ] All tests pass
- [ ] Linter passes
- [ ] Binary builds successfully
