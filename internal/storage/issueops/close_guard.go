package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// closeGuardRow is the slice of the close target the close guards read.
type closeGuardRow struct {
	isTemplate bool
	pinned     bool
	status     types.Status
	assignee   string
}

// enforceCloseGuardsInTx applies the close guards — template read-only, the
// pin, and the assignee authority fence (be-035) — to an open target whose
// dependency routing the caller already read from this transaction.
//
// They are the guards `bd close` used to run as a pre-read in cmd/bd, which
// left every other caller of the role (bd serve, BatchCloser, BatchApplier
// close items, a library embedder) closing what bd refused. Living here, they
// read the row the close is about to write, inside the same transaction, so
// there is no read-then-write window either.
//
// Order is the historical one: template first (no bypass), then the pin, then
// the assignee — so a request that trips more than one reads the refusal bd
// always printed for it. force bypasses the pin and the assignee and nothing
// else.
//
// The caller runs this only for a target that is not already literally closed:
// a re-close has no state change to guard, and it stays the idempotent no-op it
// has always been (ga-ktn9pe.4.8) — a forced close of a pinned bead leaves the
// pin set, and the plain retry must not then refuse.
func enforceCloseGuardsInTx(ctx context.Context, tx DBTX, id, targetColumn, actor string, force bool) error {
	row, err := readCloseGuardRowInTx(ctx, tx, id, targetColumn)
	if err != nil {
		return err
	}
	return CheckClosable(id, &types.Issue{
		IsTemplate: row.isTemplate,
		Pinned:     row.pinned,
		Status:     row.status,
		Assignee:   row.assignee,
	}, actor, force)
}

// CheckClosable is the close guards' one decision over an issue's pre-image:
// template read-only (*TemplateReadOnlyError, no bypass), then — unless force —
// the pin (*PinnedError, either spelling) and the assignee authority fence
// (*CloseNotAssigneeError; an unassigned issue is closable by anyone). A nil
// issue passes. It is pure: enforceCloseGuardsInTx applies it to the row the
// close is about to write, inside the close transaction, which is the
// authority; `bd close` also applies it to its resolved pre-image ahead of its
// own gate-satisfaction check, so a refusal that trips both reads the guard's
// sentence, as it always has.
//
// The caller decides whether to guard at all: an already-closed target is an
// idempotent re-close with no state change to guard (ga-ktn9pe.4.8).
func CheckClosable(id string, issue *types.Issue, actor string, force bool) error {
	if issue == nil {
		return nil
	}
	if issue.IsTemplate {
		return &publicops.TemplateReadOnlyError{IssueID: id}
	}
	if force {
		return nil
	}
	// Both pin spellings are load-bearing (ga-z3vht): Gas Town pins by
	// status, Gas City by the column. See validation.NotPinned.
	if issue.Pinned || issue.Status == types.StatusPinned {
		return &publicops.PinnedError{IssueID: id}
	}
	if issue.Assignee != "" && !actorMatches(issue.Assignee, actor) {
		return &publicops.CloseNotAssigneeError{IssueID: id, Assignee: issue.Assignee, Actor: actor}
	}
	return nil
}

//nolint:gosec // G201: table is one of two hardcoded identifiers chosen from targetColumn.
func readCloseGuardRowInTx(ctx context.Context, tx DBTX, id, targetColumn string) (closeGuardRow, error) {
	table := "issues"
	switch targetColumn {
	case "depends_on_issue_id":
	case "depends_on_wisp_id":
		table = "wisps"
	default:
		return closeGuardRow{}, fmt.Errorf("close guard: unsupported target column %q", targetColumn)
	}
	var (
		row      closeGuardRow
		template sql.NullBool
		pinned   sql.NullBool
		status   string
		assignee sql.NullString
	)
	if err := tx.QueryRowContext(ctx,
		"SELECT is_template, pinned, status, assignee FROM "+table+" WHERE id = ?", id,
	).Scan(&template, &pinned, &status, &assignee); err != nil {
		return closeGuardRow{}, fmt.Errorf("read close guard state for %s from %s: %w", id, table, err)
	}
	row.isTemplate = template.Valid && template.Bool
	row.pinned = pinned.Valid && pinned.Bool
	row.status = types.Status(status)
	row.assignee = assignee.String
	return row, nil
}
