package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateCloseBatchRequest applies the request rules every BatchCloser
// implementation shares, so a rule is a contract rather than one backend's
// habit. It rejects the request outright; a per-item refusal is a result, not
// a validation failure, and never reaches here.
func ValidateCloseBatchRequest(request publicops.CloseBatchRequest) error {
	if request.Actor == "" {
		return fmt.Errorf("%w: close batch requires an actor", storage.ErrValidation)
	}
	if len(request.Items) == 0 {
		return fmt.Errorf("%w: close batch requires at least one item", storage.ErrValidation)
	}
	for i, item := range request.Items {
		if item.IssueID == "" {
			return fmt.Errorf("%w: close batch item %d requires an issue ID", storage.ErrValidation, i)
		}
	}
	if request.ClaimNext != nil {
		if err := ValidateClaimNextRequest(publicops.ClaimNextRequest{Actor: request.Actor, Filter: *request.ClaimNext}); err != nil {
			return err
		}
	}
	return nil
}

// CloseBatchCommitMessage is the history entry a batch records. It is the
// only spelling — the request carries no label to override it, and could not
// compose this one, because it names what LANDED rather than what was asked for,
// which is why it is composed from the result and not from the request: a
// batch that skipped a mistyped id must not claim it in the log.
//
// LANDED is Changed, not "no error": an idempotent re-close persisted nothing,
// so naming it would put an id in `bd dolt log` under a commit that did not
// touch it.
//
// IT NAMES DURABLE LANDINGS ONLY. The wisp tables are dolt-ignored precisely so
// ephemeral work never ships, so an entry naming a wisp is the sync artifact
// ignoring them exists to prevent (issueops/batchcloser.go:128-135). An
// ephemeral landing is reported as a COUNT instead of being dropped, because
// the message is also the callers' commit-or-not signal: the unit-of-work
// backend reads "" as "roll this attempt back", and a batch that closed a wisp
// and nothing else must still keep the close (measured — a durable-only message
// leaves the wisp open there).
//
// It returns "" when nothing landed, which is how the callers spell "commit
// nothing" — a batch that wrote nothing records no history entry.
func CloseBatchCommitMessage(result publicops.CloseBatchResult) string {
	ids := make([]string, 0, len(result.Outcomes))
	ephemeral := 0
	for _, outcome := range result.Outcomes {
		if outcome.Err != nil || !outcome.Changed {
			continue
		}
		if closeOutcomeIsEphemeral(outcome) {
			ephemeral++
			continue
		}
		ids = append(ids, outcome.IssueID)
	}

	var parts []string
	switch {
	case len(ids) > 0:
		parts = append(parts, "close "+strings.Join(ids, ", "))
	case ephemeral == 1:
		parts = append(parts, "close 1 ephemeral item")
	case ephemeral > 1:
		parts = append(parts, fmt.Sprintf("close %d ephemeral items", ephemeral))
	}
	if result.ClaimedNext != nil {
		parts = append(parts, "claim "+result.ClaimedNext.ID)
	}
	if len(parts) == 0 {
		return ""
	}
	return "bd: " + strings.Join(parts, "; ")
}

// closeOutcomeIsEphemeral reports whether this landing was on the wisps plane,
// read off the snapshot the close hydrated rather than off a flag the outcome
// does not carry.
//
// A nil Issue counts as DURABLE. The role contract pins Issue non-nil whenever
// Err is nil, so this is unreachable; if it ever happens, naming an id is the
// safer of the two wrong answers, because the alternative silently shortens a
// message the unit-of-work backend reads as "commit nothing".
func closeOutcomeIsEphemeral(outcome publicops.CloseOutcome) bool {
	return outcome.Issue != nil && IsWisp(outcome.Issue)
}

// ExecuteCloseBatch closes every item it can in tx and reports the durable
// tables changed. It is the store-backed body behind the BatchCloser accessor;
// the unit-of-work provider has its own, for the same reason Lifecycle does.
//
// A per-item failure lands in that item's outcome and the loop CONTINUES —
// skipping a refused id and committing the survivors is what closing a list of
// issues has always done, and it is the behavior the CLI's per-id stderr
// reports. Only a request-level failure aborts, and it aborts by returning an
// error, which rolls the whole transaction back.
//
// claimFilter is request.ClaimNext already translated by the caller's request
// builder, because this package speaks WorkFilter. A nil filter skips the
// claim, and so does a batch in which nothing landed — landed meaning an item
// that CHANGED, so a batch whose items were all already closed earns no claim.
func ExecuteCloseBatch(ctx context.Context, tx *sql.Tx, request publicops.CloseBatchRequest, claimFilter *types.WorkFilter) (publicops.CloseBatchResult, ChangedTables, error) {
	result := publicops.CloseBatchResult{Outcomes: make([]publicops.CloseOutcome, len(request.Items))}
	tables := ChangedTables{}
	landed := 0

	for i, item := range request.Items {
		closed, changedTables, err := ExecuteClose(ctx, tx, publicops.CloseRequest{
			Actor:   request.Actor,
			IssueID: item.IssueID,
			Reason:  item.Reason,
			Session: request.Session,
			Force:   request.Force,
		})
		if err != nil {
			result.Outcomes[i] = publicops.CloseOutcome{IssueID: item.IssueID, Err: err}
			continue
		}
		tables.Merge(changedTables)
		if closed.Changed {
			landed++
		}
		result.Outcomes[i] = publicops.CloseOutcome{
			IssueID:      item.IssueID,
			Issue:        closed.Issue,
			Changed:      closed.Changed,
			OpenChildren: closed.OpenChildren,
		}
	}

	if claimFilter != nil && landed > 0 {
		claim, claimTables, err := ExecuteClaimNext(ctx, tx, request.Actor, *claimFilter)
		if err != nil {
			return publicops.CloseBatchResult{}, nil, err
		}
		tables.Merge(claimTables)
		result.ClaimedNext = claim.Claimed
	}

	return result, tables, nil
}
