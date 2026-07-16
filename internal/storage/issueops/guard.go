package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Guard is an optional ownership precondition for a mutating verb, folded
// into the statement's WHERE clause so the check-and-write is one atomic
// compare-and-swap. Assignee compares blank-insensitively (” and NULL are
// the same unassigned state, matching the claim predicates); Fence compares
// the monotonic claim_fence (see fence.go). Either or both axes may be set.
//
// Authorization semantics (the class-T rule from the ownership-fencing
// design): an ownership-TRANSITION verb (unclaim, transfer, reclaim) carrying
// a satisfied guard is authorized regardless of the caller's actor — the
// guard is the credential, proving the caller acted on a current read of the
// ownership state. force never skips a supplied guard; it bypasses only the
// unguarded owner check.
type Guard struct {
	Assignee *string
	Fence    *int64
}

// IsZero reports whether no guard axis is set.
func (g Guard) IsZero() bool { return g.Assignee == nil && g.Fence == nil }

// whereClause renders the guard as WHERE-clause conjuncts plus their args.
// Returns "" when the guard is empty.
func (g Guard) whereClause() (string, []interface{}) {
	clause := ""
	var args []interface{}
	if g.Assignee != nil {
		clause += " AND COALESCE(assignee, '') = ?"
		args = append(args, *g.Assignee)
	}
	if g.Fence != nil {
		clause += " AND claim_fence = ?"
		args = append(args, *g.Fence)
	}
	return clause, args
}

// GuardWhereClause exposes the guard's WHERE rendering to sibling storage
// layers that hand-roll their ownership SQL (the proxied domain/db Update),
// keeping the comparison semantics in one place.
func GuardWhereClause(g Guard) (string, []interface{}) { return g.whereClause() }

type guardCtxKey struct{}

// WithGuard attaches an ownership guard to the context, the same transport
// WithLeaseTTL uses: it flows through the existing store method signatures
// into the *InTx implementations without widening every storage interface.
// Stores whose mutation paths bypass issueops must either honor the guard
// explicitly (domain/db Update does) or refuse it loudly — silently dropping
// a guard is the one forbidden outcome.
func WithGuard(ctx context.Context, g Guard) context.Context {
	if g.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, guardCtxKey{}, g)
}

// GuardFrom extracts the ownership guard from the context, if any.
func GuardFrom(ctx context.Context) (Guard, bool) {
	g, ok := ctx.Value(guardCtxKey{}).(Guard)
	return g, ok
}

// GuardMatchesCurrentRow re-reads the row's ownership cells and evaluates
// the guard in Go — for paths (like an idempotent already-closed close) that
// must distinguish "guard still holds" from "guard superseded" without
// executing a write. Returns storage.ErrNotFound when the row is gone.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GuardMatchesCurrentRow(ctx context.Context, tx DBTX, issueTable, id string, g Guard) (bool, error) {
	var assignee sql.NullString
	var fence int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COALESCE(assignee, ''), claim_fence FROM %s WHERE id = ?`, issueTable), id).
		Scan(&assignee, &fence)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("issue %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("re-read for guard evaluation on %s: %w", id, err)
	}
	if g.Assignee != nil && assignee.String != *g.Assignee {
		return false, nil
	}
	if g.Fence != nil && fence != *g.Fence {
		return false, nil
	}
	return true, nil
}

// guardConflictFromIssue builds the typed conflict from an already-read
// issue, for guarded pre-checks that fail before the write executes.
func guardConflictFromIssue(issue *types.Issue, g Guard) error {
	return &storage.PreconditionFailedError{
		ID:               issue.ID,
		ExpectedAssignee: g.Assignee,
		ExpectedFence:    g.Fence,
		CurrentAssignee:  issue.Assignee,
		CurrentFence:     issue.ClaimFence,
	}
}

// GuardPreconditionError re-reads the row's ownership cells inside the same
// transaction and builds the typed conflict for a guarded write that matched
// zero rows. Returns ErrNotFound when the row does not exist at all.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GuardPreconditionError(ctx context.Context, tx DBTX, issueTable, id string, g Guard) error {
	var assignee sql.NullString
	var fence int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COALESCE(assignee, ''), claim_fence FROM %s WHERE id = ?`, issueTable), id).
		Scan(&assignee, &fence)
	if err == sql.ErrNoRows {
		return fmt.Errorf("issue %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("re-read after guarded write conflict on %s: %w", id, err)
	}
	return &storage.PreconditionFailedError{
		ID:               id,
		ExpectedAssignee: g.Assignee,
		ExpectedFence:    g.Fence,
		CurrentAssignee:  assignee.String,
		CurrentFence:     fence,
	}
}
