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

// HistoryEntry names the version-control entry a guarded mutation records: the
// caller's own provenance label when it supplied one, otherwise the
// implementation's default. It decides how the entry READS, never whether one
// is recorded — that stays a question about what the mutation wrote.
//
// Every guarded mutation whose request carries a label calls this. Create is
// the one that does not, and the reason is NOT that no surface names the
// created issue — several do (internal/storage/dolt/issues.go writes
// "bd: create <id>", reached from cmd/bd/create_atomic.go). It is that
// CreateRequest has no Provenance field for a caller to set, so there is no
// label for this function to prefer. That is a gap rather than a principle:
// the proxied route's create message changed from "bd: create <id>" to
// "create issue" for exactly this reason, and closing it means either giving
// CreateRequest the field or having the role compose an id-bearing default the
// way BatchCloser and ClaimNext do. Tracked as its own decision.
func HistoryEntry(provenance, fallback string) string {
	if provenance != "" {
		return provenance
	}
	return fallback
}

// ExecuteCreate applies a guarded create in tx and reports durable tables changed.
func ExecuteCreate(ctx context.Context, tx *sql.Tx, request publicops.CreateRequest) (publicops.CreateResult, ChangedTables, error) {
	attempt := CloneCreateRequest(request)
	if err := ValidatePublicCreateRequest(attempt); err != nil {
		return publicops.CreateResult{}, nil, err
	}
	batch, err := NewBatchContext(ctx, tx, storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: attempt.ForceIDPrefix})
	if err != nil {
		return publicops.CreateResult{}, nil, err
	}
	attempt, err = PreparePublicCreateRequest(attempt, PublicCreateContext{
		IssuePrefix: batch.ConfigPrefix, AllowedPrefixes: batch.AllowedPrefixes,
		CustomStatuses: batch.CustomStatuses, CustomTypes: batch.CustomTypes,
	})
	if err != nil {
		return publicops.CreateResult{}, nil, err
	}
	issue := attempt.Issue
	// Configured infra types live in the wisp tables, the same routing the
	// stores' own CreateIssue applies (internal/storage/dolt/issues.go). Mark
	// the issue before its ID is assigned so ID generation, the create-only
	// guard, and table routing all agree on the destination. A no-history
	// create keeps its own retention mode.
	if !issue.Ephemeral && !issue.NoHistory && ResolveInfraTypesInTx(ctx, tx)[string(issue.IssueType)] {
		issue.Ephemeral = true
	}
	if attempt.InheritLabelsFromParent && attempt.ParentID != "" {
		labels, err := GetLabelsInTx(ctx, tx, "", attempt.ParentID)
		if err != nil {
			return publicops.CreateResult{}, nil, err
		}
		issue.Labels = append(issue.Labels, labels...)
	}
	childCounterChanged := false
	if issue.ID == "" && attempt.ParentID != "" {
		parentIsWisp := IsActiveWispInTx(ctx, tx, attempt.ParentID)
		issue.ID, err = GetNextChildIDTx(ctx, tx, attempt.ParentID)
		if err != nil {
			return publicops.CreateResult{}, nil, ClassifyPublicCreateError(err)
		}
		childCounterChanged = !parentIsWisp
	}
	if err := assignCreateIssueIDInTx(ctx, tx, batch, issue, attempt.Actor); err != nil {
		return publicops.CreateResult{}, nil, ClassifyPublicCreateError(err)
	}
	issue.Dependencies = storage.CreatePublicCreateDependencies(issue.ID, attempt)
	var skipped []skippedDependency
	created, err := CreateIssuesInTxWithResult(ctx, tx, []*types.Issue{issue}, attempt.Actor, storage.BatchCreateOptions{
		CreateOnly: true, SkipPrefixValidation: attempt.ForceIDPrefix,
		OnSkippedDependency: func(issueID, dependsOnID, reason string) {
			skipped = append(skipped, skippedDependency{issueID: issueID, dependsOnID: dependsOnID, reason: reason})
		},
	})
	if err != nil {
		return publicops.CreateResult{}, nil, ClassifyPublicCreateError(err)
	}
	if len(skipped) > 0 {
		return publicops.CreateResult{}, nil, publicCreateValidationError(skippedDependencyError(skipped))
	}
	tables := ChangedTables{}
	tables.Merge(CreateIssuesDirtyTables(ctx, []*types.Issue{issue}, created))
	if childCounterChanged {
		tables.Add("child_counters")
	}
	hydrated, err := HydrateIssueOperationResult(ctx, tx, issue.ID, true)
	if err != nil {
		return publicops.CreateResult{}, nil, err
	}
	return publicops.CreateResult{Issue: hydrated}, tables, nil
}

// skippedDependency records an edge the batch engine declined to write.
type skippedDependency struct{ issueID, dependsOnID, reason string }

// skippedDependencyError refuses a guarded create whose requested edges were
// not all written. The batch engine drops a dangling edge so a partial import
// still lands, but a guarded create that reported success while silently
// discarding a parent, waits-for, or explicit dependency is data loss: the
// caller has no way to learn the relationship is missing. Refusing rolls the
// whole create back with the enclosing transaction.
func skippedDependencyError(skipped []skippedDependency) error {
	edges := make([]string, 0, len(skipped))
	for _, edge := range skipped {
		edges = append(edges, fmt.Sprintf("%s -> %s (%s)", edge.issueID, edge.dependsOnID, edge.reason))
	}
	return fmt.Errorf("create: dependencies could not be created: %s: %w", strings.Join(edges, "; "), storage.ErrNotFound)
}

// ExecuteUpdate applies a guarded update in tx and reports durable tables changed.
func ExecuteUpdate(ctx context.Context, tx *sql.Tx, request publicops.UpdateRequest) (publicops.UpdateResult, ChangedTables, error) {
	attempt := CloneUpdateRequest(request)
	if attempt.Actor == "" || attempt.IssueID == "" {
		return publicops.UpdateResult{}, nil, fmt.Errorf("%w: update requires actor and issue ID", storage.ErrValidation)
	}
	if err := ValidateUpdateRequest(attempt); err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if err := ValidateMetadataPatch(attempt.Patch.Metadata); err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	// The plane restriction is resolved HERE, inside the update's own
	// transaction, so a caller that serves durable issues only cannot be handed
	// a wisp by a resolve that ran earlier.
	if attempt.IssuePlaneOnly && IsActiveWispInTx(ctx, tx, attempt.IssueID) {
		return publicops.UpdateResult{}, nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, attempt.IssueID)
	}
	tables := ChangedTables{}
	before, err := GetIssueInTx(ctx, tx, attempt.IssueID)
	if err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if attempt.ExpectedVersion != nil {
		if err := CheckVersionInTx(ctx, tx, attempt.IssueID, *attempt.ExpectedVersion); err != nil {
			return publicops.UpdateResult{}, nil, err
		}
	}
	if attempt.ExpectedStatus != nil {
		status := string(*attempt.ExpectedStatus)
		attempt.ExpectedStatus = nil
		if err := CheckExpectedFieldsInTx(ctx, tx, attempt.IssueID, attempt.ExpectedAssignee, &status); err != nil {
			return publicops.UpdateResult{}, nil, err
		}
	} else if err := CheckExpectedFieldsInTx(ctx, tx, attempt.IssueID, attempt.ExpectedAssignee, nil); err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if err := AuthorizeAssigneeTransfer(ctx, tx, before, attempt); err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	changedAny := false
	if attempt.Claim {
		claimed, err := ClaimIssueInTx(ctx, tx, attempt.IssueID, attempt.Actor)
		if err != nil {
			return publicops.UpdateResult{}, nil, err
		}
		if claimAdvancedTheRow(claimed, attempt.Actor) {
			changedAny = true
			issueTable, _, eventTable, _ := WispTableRouting(claimed.IsWisp)
			tables.Add(issueTable, eventTable)
		}
	}
	current := before
	if attempt.Claim {
		current, err = GetIssueInTx(ctx, tx, attempt.IssueID)
		if err != nil {
			return publicops.UpdateResult{}, nil, err
		}
	}
	updates := UpdateFields(attempt.Patch)
	// A metadata patch rides the same row write as the field edits. Writing it
	// separately recorded a second event for one atomic mutation, fabricating
	// an update that never happened for every consumer of the event stream.
	// ApplyMetadataPatch already resolved the patch against the in-transaction
	// row, so the concrete value goes in alongside the other columns and its
	// no-op elision is preserved by the changed flag.
	metadata, metadataChanged, err := ApplyMetadataPatch(current.Metadata, attempt.Patch.Metadata)
	if err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if metadataChanged {
		updates["metadata"] = metadata
	}
	// The override only means anything alongside a status change, so it is spelled
	// only then: an update that carries it with no status would be handing the
	// funnel a key it has no question to answer.
	if attempt.ForceClosePolicy && attempt.Patch.Status.Set {
		updates[OpForceClosePolicy] = true
	}
	if len(updates) > 0 {
		updated, err := UpdateIssueInTx(ctx, tx, attempt.IssueID, updates, attempt.Actor)
		if err != nil {
			return publicops.UpdateResult{}, nil, err
		}
		if updated.Changed {
			changedAny = true
			issueTable, _, eventTable, _ := WispTableRouting(updated.IsWisp)
			tables.Add(issueTable, eventTable)
			if updated.IssueRowsChanged {
				tables.Add("issues")
			}
		}
	}
	labelsChanged, err := ApplyLabelPatch(ctx, tx, current, attempt.Patch.Labels, attempt.Actor)
	if err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if labelsChanged {
		changedAny = true
		_, labelTable, eventTable, _ := WispTableRouting(IsActiveWispInTx(ctx, tx, attempt.IssueID))
		tables.Add(labelTable, eventTable)
	}
	parentResult, err := ApplyParentPatch(ctx, tx, current, attempt.Patch.ParentID, attempt.Actor)
	if err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	if parentResult.Changed {
		changedAny = true
		_, _, _, dependencyTable := WispTableRouting(IsActiveWispInTx(ctx, tx, attempt.IssueID))
		tables.Add(dependencyTable)
		if parentResult.IssueRowsChanged {
			tables.Add("issues")
		}
	}
	if attempt.Patch.Persistence.Set {
		current, err := GetIssueInTx(ctx, tx, attempt.IssueID)
		if err != nil {
			return publicops.UpdateResult{}, nil, err
		}
		moved, err := MoveIssuePersistenceInTx(ctx, tx, current, attempt.Patch.Persistence.Value, attempt.Actor)
		if err != nil {
			return publicops.UpdateResult{}, nil, err
		}
		if moved.Changed {
			changedAny = true
			tables.Merge(moved.ChangedTables)
		}
	}
	hydrated, err := HydrateIssueOperationResult(ctx, tx, attempt.IssueID, false)
	if err != nil {
		return publicops.UpdateResult{}, nil, err
	}
	return publicops.UpdateResult{Issue: hydrated, Changed: changedAny}, tables, nil
}

// claimAdvancedTheRow reports whether ClaimIssueInTx's CAS represents a real
// mutation from the pre-image it read, as opposed to an idempotent re-claim
// that matched the CAS but changed nothing worth staging. Comparing the
// pre-image assignee to actor verbatim (ga-v2k49, steveyegge's #5479
// re-review) mistook a holder re-claiming under a respelled identity — a
// dotted alias vs. its sanitized form, ga-wzl83's repro shape — for a fresh
// mutation: ClaimIssueInTx's own CAS already treats the two spellings as the
// same holder and writes nothing, but this check disagreed and staged a
// phantom issues+events mutation for a request that changed no bytes.
func claimAdvancedTheRow(claimed *ClaimResult, actor string) bool {
	return claimed.OldIssue.Status != types.StatusInProgress || !actorMatches(claimed.OldIssue.Assignee, actor)
}

// ExecuteClose applies a guarded close in tx and reports durable tables changed.
func ExecuteClose(ctx context.Context, tx *sql.Tx, request publicops.CloseRequest) (publicops.CloseResult, ChangedTables, error) {
	attempt := CloneCloseRequest(request)
	if attempt.Actor == "" || attempt.IssueID == "" {
		return publicops.CloseResult{}, nil, fmt.Errorf("%w: close requires actor and issue ID", storage.ErrValidation)
	}
	closed, err := CloseIssueCheckedInTx(ctx, tx, attempt.IssueID, attempt.Reason, attempt.Actor, attempt.Session, attempt.Force, attempt.ExpectedVersion)
	if err != nil {
		return publicops.CloseResult{}, nil, err
	}
	tables := ChangedTables{}
	changed := !closed.AlreadyClosed
	if changed {
		issueTable, _, eventTable, _ := WispTableRouting(closed.IsWisp)
		tables.Add(issueTable, eventTable)
	}
	if closed.IssueRowsChanged {
		tables.Add("issues")
	}
	hydrated, err := HydrateIssueOperationResult(ctx, tx, attempt.IssueID, false)
	if err != nil {
		return publicops.CloseResult{}, nil, err
	}
	return publicops.CloseResult{Issue: hydrated, Changed: changed, OpenChildren: closed.OpenChildren}, tables, nil
}

// ExecuteReopen applies a guarded reopen in tx and reports durable tables changed.
func ExecuteReopen(ctx context.Context, tx *sql.Tx, request publicops.ReopenRequest) (publicops.ReopenResult, ChangedTables, error) {
	attempt := CloneReopenRequest(request)
	if attempt.Actor == "" || attempt.IssueID == "" {
		return publicops.ReopenResult{}, nil, fmt.Errorf("%w: reopen requires actor and issue ID", storage.ErrValidation)
	}
	if attempt.ExpectedVersion != nil {
		if err := CheckVersionInTx(ctx, tx, attempt.IssueID, *attempt.ExpectedVersion); err != nil {
			return publicops.ReopenResult{}, nil, err
		}
	}
	reopened, err := ReopenIssueInTx(ctx, tx, attempt.IssueID, attempt.Reason, attempt.Actor)
	if err != nil {
		return publicops.ReopenResult{}, nil, err
	}
	tables := ChangedTables{}
	if reopened.Changed {
		issueTable, _, eventTable, _ := WispTableRouting(reopened.IsWisp)
		tables.Add(issueTable, eventTable)
	}
	if reopened.IssueRowsChanged {
		tables.Add("issues")
	}
	hydrated, err := HydrateIssueOperationResult(ctx, tx, attempt.IssueID, false)
	if err != nil {
		return publicops.ReopenResult{}, nil, err
	}
	return publicops.ReopenResult{Issue: hydrated, Changed: reopened.Changed}, tables, nil
}
