package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// closeDirectItem is one argument that survived `bd close`'s own close policy,
// paired with the store that actually owns it. arg is the argument position, so
// an outcome can be folded back onto the list the caller typed.
type closeDirectItem struct {
	arg    int
	id     string
	reason string
	store  storage.DoltStorage
}

// closeDirectPlan is the argument list after the CLI's own close policy has
// run: the items the batch is handed, and the refusal each rejected argument
// earned.
//
// refusals is indexed by ARGUMENT position rather than by item so a refusal
// decided here and one decided by the batch still print in the order the ids
// were typed. An entry is non-empty exactly when that argument never became an
// item.
type closeDirectPlan struct {
	items    []closeDirectItem
	refusals []string
}

// closeDirectPreflight applies `bd close`'s own close policy to every resolved
// argument, and hands the batch only the items that survived.
//
// Neither check is library policy — the template/pin/assignee fence and gate
// satisfaction read the issue and nothing else, and the role has no vocabulary
// for either — which is why they stay in cmd/bd. It is closeProxiedRunPreflight's
// job on this route.
//
// The open-children guard is NOT here, and that is the point: the engine
// enforces it inside the close's own transaction, refusing with a
// CloseOpenChildrenError whose message is the one this route used to compose
// by hand. Keeping a copy here would be a second implementation of a rule the
// role already states, and a read-then-write window besides — one that made
// `bd close <child> <parent>` depend on argument order.
func closeDirectPreflight(results []*RoutedResult, resolvedIDs, reasons []string, force bool) closeDirectPlan {
	plan := closeDirectPlan{refusals: make([]string, len(resolvedIDs))}
	for i, id := range resolvedIDs {
		if refusal := closeDirectCheckOne(id, results[i].Issue, force); refusal != "" {
			plan.refusals[i] = refusal
			continue
		}
		plan.items = append(plan.items, closeDirectItem{
			arg:    i,
			id:     id,
			reason: reasonForCloseIndex(reasons, i),
			store:  results[i].Store,
		})
	}
	return plan
}

// closeDirectCheckOne returns one argument's refusal, or "" when it may go to
// the batch.
func closeDirectCheckOne(id string, issue *types.Issue, force bool) string {
	// Close validation guards a state change; a row already at literal
	// StatusClosed has none to guard, so skip it and let the re-close reach the
	// engine as the idempotent no-op it has always been (ga-ktn9pe.4.8).
	// Without this, a forced close of a boolean-pinned bead leaves pinned=true
	// (the close write never touches the column — deliberately, it is the
	// deletion-protection flag bd gc/purge/cleanup honor) and the plain retry
	// hits NotPinned and exits nonzero, which strands the molecule auto-close
	// the unchanged branch re-drives. Only a literal closed status qualifies:
	// reaching a configured done status is still a real close, mirroring the
	// engine's isClosedInTx. The snapshot only decides whether validation runs,
	// never what is written — the engine's in-transaction `status != closed`
	// guard remains the authority on whether the close is a no-op, so a
	// concurrent close still converges. Mirrored in closeProxiedCheckOne.
	if issue == nil || issue.Status != types.StatusClosed {
		if err := validateIssueClosable(id, issue, actor, force); err != nil {
			return err.Error()
		}
	}

	// Gate satisfaction for machine-checkable gates (GH#1467).
	if !force {
		if err := checkGateSatisfaction(issue); err != nil {
			return fmt.Sprintf("cannot close %s: %s", id, err)
		}
	}

	return ""
}

// closeDirectBatch is one store's slice of the argument list.
type closeDirectBatch struct {
	store storage.DoltStorage
	items []closeDirectItem
}

// closeDirectBatches groups the surviving items by the store that owns them,
// keeping both the order the stores first appear and the argument order within
// each store.
//
// One request per store rather than one per id is the whole point of the role:
// the REQUEST is the transaction boundary, so N ids on one store land as one
// transaction with one Dolt commit and one history entry instead of N. A second
// store is a second boundary because a transaction cannot span two databases —
// `bd close` reaches one only when its ids route to another repo.
func closeDirectBatches(items []closeDirectItem) []closeDirectBatch {
	var batches []closeDirectBatch
	at := make(map[storage.DoltStorage]int, len(items))
	for _, item := range items {
		i, ok := at[item.store]
		if !ok {
			i = len(batches)
			at[item.store] = i
			batches = append(batches, closeDirectBatch{store: item.store})
		}
		batches[i].items = append(batches[i].items, item)
	}
	return batches
}

// closeDirectRequest is one store's slice as the BatchCloser role expresses it.
// It is the same request closeProxiedServer builds from the same flags: the
// per-item reasons `bd close a b c -r x -r y -r z` has always mapped
// positionally, one request-wide session, one request-wide --force.
//
// claimNext rides on the batch of claimStore and no other, because --claim-next
// hands out ONE claim however many stores the ids spanned.
func closeDirectRequest(batch closeDirectBatch, session string, force bool, claimStore storage.DoltStorage, claimNext *issueops.ReadyRequest) issueops.CloseBatchRequest {
	request := issueops.CloseBatchRequest{
		Actor:   actor,
		Items:   make([]issueops.BatchCloseItem, 0, len(batch.items)),
		Session: session,
		Force:   force,
	}
	if claimNext != nil && batch.store == claimStore {
		request.ClaimNext = claimNext
	}
	for _, item := range batch.items {
		request.Items = append(request.Items, issueops.BatchCloseItem{IssueID: item.id, Reason: item.reason})
	}
	return request
}

// closeDirectRun hands each store's slice to that store's BatchCloser and folds
// the outcomes back onto the argument list. A nil entry in the returned slice is
// an argument the batch never saw, either because the CLI's own policy refused
// it or because it was never an argument at all.
//
// A batch-level error means the batch never ran — request validation,
// cancellation or infrastructure — so every id in it is reported the way a
// single failed close has always been reported here, and the remaining stores'
// batches still run. That is the same skip-and-continue the per-id loop had:
// one store's outage does not silently drop another store's closes.
func closeDirectRun(ctx context.Context, batches []closeDirectBatch, argCount int, session string, force bool, claimStore storage.DoltStorage, claimNext *issueops.ReadyRequest) ([]*issueops.CloseOutcome, *types.IssueWithCounts) {
	outcomes := make([]*issueops.CloseOutcome, argCount)
	var claimed *types.IssueWithCounts

	for _, batch := range batches {
		result, err := closeDirectCloseBatch(ctx, batch.store, closeDirectRequest(batch, session, force, claimStore, claimNext))
		if err != nil {
			for _, item := range batch.items {
				refused := issueops.CloseOutcome{IssueID: item.id, Err: err}
				outcomes[item.arg] = &refused
			}
			continue
		}
		// Outcomes mirror Items index for index, which is what lets a per-id
		// report be walked against the caller's own argument list.
		for j := range result.Outcomes {
			outcome := result.Outcomes[j]
			outcomes[batch.items[j].arg] = &outcome
		}
		if result.ClaimedNext != nil {
			claimed = result.ClaimedNext
		}
	}

	return outcomes, claimed
}

// closeDirectCloseBatch reaches the role through the store's OWN accessor, the
// same two-step proxiedBatchCloser performs on the other route and for the same
// reason: the accessor is where each storage decorator adds its layer, so a
// closer built any other way would be missing the hooks and telemetry the store
// itself carries.
func closeDirectCloseBatch(ctx context.Context, st storage.DoltStorage, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	closer, err := st.BatchCloser()
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return closer.CloseBatch(ctx, request)
}

// closeDirectRefusal spells one item's typed refusal the way this route has
// always spelled it. The vocabulary is matched with errors.Is rather than by
// reading the message, which is the point of the outcome carrying a typed error
// at all.
//
// Both close-policy refusals print BARE, because each error already names its
// own subject: ErrCloseBlocked names the blockers and gets the --force hint
// appended, and CloseOpenChildrenError's message is character for character the
// line this route used to compose before the guard moved into the transaction.
// Everything else is an unexpected failure and is reported against the id.
func closeDirectRefusal(id string, err error) string {
	switch {
	case errors.Is(err, storage.ErrCloseBlocked):
		return fmt.Sprintf("%v (use --force to override)", err)
	case errors.Is(err, storage.ErrCloseOpenChildren):
		return err.Error()
	default:
		return fmt.Sprintf("Error closing %s: %v", id, err)
	}
}
