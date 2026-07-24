//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// RunInTransaction executes a function within a database transaction.
// After the SQL transaction commits, dirty tables are selectively staged
// and a Dolt version commit is created with the given message.
func (s *EmbeddedDoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	var tracker versioncontrolops.DirtyTableTracker

	if err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		return fn(&embeddedTransaction{tx: tx, dirty: &tracker})
	}); err != nil {
		return err
	}

	// Create a Dolt version commit from the working set changes.
	if commitMsg != "" && len(tracker.DirtyTables()) > 0 {
		return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return versioncontrolops.StageAndCommit(ctx, db, tracker.DirtyTables(), commitMsg, commitAuthor)
		})
	}
	return nil
}

type embeddedTransaction struct {
	tx    *sql.Tx
	dirty *versioncontrolops.DirtyTableTracker
}

func (t *embeddedTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}
	result, err := issueops.CreateIssueInTxWithResult(ctx, t.tx, bc, issue, actor)
	if err != nil {
		return err
	}
	for table := range issueops.CreateIssueDirtyTables(ctx, issue, result) {
		t.dirty.MarkDirty(table)
	}
	return nil
}

func (t *embeddedTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	result, err := issueops.CreateIssuesInTxWithResult(ctx, t.tx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: true,
	})
	if err != nil {
		return err
	}
	for table := range issueops.CreateIssuesDirtyTables(ctx, issues, result) {
		t.dirty.MarkDirty(table)
	}
	return nil
}

func (t *embeddedTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
	_, err := issueops.UpdateIssueInTx(ctx, t.tx, id, updates, actor)
	return err
}

func (t *embeddedTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("events")
	_, err := issueops.CloseIssueInTx(ctx, t.tx, id, reason, actor, session)
	return err
}

func (t *embeddedTransaction) DeleteIssue(ctx context.Context, id string) error {
	t.dirty.MarkDirty("issues")
	t.dirty.MarkDirty("dependencies")
	t.dirty.MarkDirty("labels")
	t.dirty.MarkDirty("comments")
	t.dirty.MarkDirty("events")
	return issueops.DeleteIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return issueops.GetIssueInTx(ctx, t.tx, id)
}

func (t *embeddedTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return issueops.SearchIssuesInTx(ctx, t.tx, query, filter)
}

// SearchIssueIDs returns matching IDs only via issueops.SearchIssueIDsInTx.
func (t *embeddedTransaction) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	return issueops.SearchIssueIDsInTx(ctx, t.tx, query, filter)
}

func (t *embeddedTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (t *embeddedTransaction) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, addOpts storage.DependencyAddOptions) error {
	_, _, eventTable, depTable := issueops.WispTableRouting(issueops.IsActiveWispInTx(ctx, t.tx, dep.IssueID))
	eventWritten, err := issueops.AddDependencyInTx(ctx, t.tx, dep, actor, issueops.AddDependencyOpts{
		IsCrossPrefix:  types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID),
		SkipCycleCheck: addOpts.SkipCycleCheck,
		EmitEvent:      addOpts.EmitEvent,
	})
	if err != nil {
		return err
	}
	t.dirty.MarkDirty(depTable)
	// AddDependencyInTx records a dependency_added event on the source's event
	// table only for a genuine emit (explicit verb + new edge); stage it so the
	// event commits with the edge. A structural or idempotent add writes no event.
	if eventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	return nil
}

// CycleThroughEdges reports a scheduling cycle through one of the new edges,
// including the transaction's own uncommitted dependency writes
// (bd-6dnrw.8, bd-578h9.9).
func (t *embeddedTransaction) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	graph := make(map[string][]string)
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.tx, []string{"dependencies", "wisp_dependencies"}, graph); err != nil {
		return "", err
	}
	return issueops.CycleThroughEdgesInGraph(graph, edges), nil
}

func (t *embeddedTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return t.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, storage.DependencyRemoveOptions{})
}

func (t *embeddedTransaction) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, rmOpts storage.DependencyRemoveOptions) error {
	// Route dirty marking on the source's wisp status: a wisp-source remove
	// stages wisp_dependencies/wisp_events, a permanent one dependencies/events.
	_, _, eventTable, depTable := issueops.WispTableRouting(issueops.IsActiveWispInTx(ctx, t.tx, issueID))
	eventWritten, err := issueops.RemoveDependencyInTx(ctx, t.tx, issueID, dependsOnID, actor, rmOpts.EmitEvent)
	if err != nil {
		return err
	}
	t.dirty.MarkDirty(depTable)
	// RemoveDependencyInTx records a dependency_removed event on the source's
	// event table only for a genuine emit (explicit verb + edge removal); stage
	// it so it commits with the edge. A structural or missing-edge remove writes
	// no event.
	if eventWritten {
		t.dirty.MarkDirty(eventTable)
	}
	return nil
}

func (t *embeddedTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	m, err := issueops.GetDependencyRecordsForIssuesInTx(ctx, t.tx, []string{issueID})
	if err != nil {
		return nil, err
	}
	return m[issueID], nil
}

func (t *embeddedTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	t.dirty.MarkDirty("labels")
	return issueops.AddLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	t.dirty.MarkDirty("labels")
	return issueops.RemoveLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

func (t *embeddedTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return issueops.GetLabelsInTx(ctx, t.tx, "", issueID)
}

func (t *embeddedTransaction) SetConfig(ctx context.Context, key, value string) error {
	t.dirty.MarkDirty("config")
	if err := issueops.SetConfigInTx(ctx, t.tx, key, value); err != nil {
		return err
	}
	// Sync normalized tables when config keys change
	switch key {
	case "status.custom":
		t.dirty.MarkDirty("custom_statuses")
		if err := issueops.SyncCustomStatusesTable(ctx, t.tx, value); err != nil {
			return fmt.Errorf("syncing custom_statuses table: %w", err)
		}
	case "types.custom":
		t.dirty.MarkDirty("custom_types")
		if err := issueops.SyncCustomTypesTable(ctx, t.tx, value); err != nil {
			return fmt.Errorf("syncing custom_types table: %w", err)
		}
	}
	return nil
}

func (t *embeddedTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	return issueops.GetConfigInTx(ctx, t.tx, key)
}

func (t *embeddedTransaction) SetMetadata(ctx context.Context, key, value string) error {
	t.dirty.MarkDirty("metadata")
	return issueops.SetMetadataInTx(ctx, t.tx, key, value)
}

func (t *embeddedTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	return issueops.GetMetadataInTx(ctx, t.tx, key)
}

func (t *embeddedTransaction) SetLocalMetadata(ctx context.Context, key, value string) error {
	return issueops.SetLocalMetadataInTx(ctx, t.tx, key, value)
}

func (t *embeddedTransaction) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	return issueops.GetLocalMetadataInTx(ctx, t.tx, key)
}

func (t *embeddedTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return fmt.Errorf("embeddedTransaction: AddComment not implemented")
}

func (t *embeddedTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	return nil, fmt.Errorf("embeddedTransaction: ImportIssueComment not implemented")
}

func (t *embeddedTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return nil, fmt.Errorf("embeddedTransaction: GetIssueComments not implemented")
}

func (t *embeddedTransaction) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: skipPrefixValidation})
	if err != nil {
		return err
	}
	result, err := issueops.CreateIssueInTxWithResult(ctx, t.tx, bc, issue, actor)
	if err != nil {
		return err
	}
	for table := range issueops.CreateIssueDirtyTables(ctx, issue, result) {
		t.dirty.MarkDirty(table)
	}
	return nil
}

// The composite-view reads below all run on the single embedded transaction
// handle. Unlike the classic Dolt store, the embedded store has no durable/wisp
// session split, so every read here — including the both-tiers-spanning ones —
// is read-your-writes on both tiers; the InTx functions do their own wisp
// routing on the one handle. The two-session wisp caveat in the
// storage.Transaction doc applies only to the server (Dolt) backend.

// GetIssueCommentsPage returns one keyset page of an issue's comments within the
// transaction. Mirrors EmbeddedDoltStore.GetIssueCommentsPage's issueops
// delegation.
func (t *embeddedTransaction) GetIssueCommentsPage(ctx context.Context, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	return issueops.GetIssueCommentsPageInTx(ctx, t.tx, issueID, after, limit)
}

// CountIssuesByGroup returns per-group issue counts within the transaction.
// Mirrors EmbeddedDoltStore.CountIssuesByGroup's issueops delegation.
func (t *embeddedTransaction) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	return issueops.CountIssuesByGroupInTx(ctx, t.tx, filter, groupBy)
}

// GetDependentRecords returns the raw inbound dependency rows of targetID within
// the transaction. Mirrors EmbeddedDoltStore.GetDependentRecords; the InTx
// function spans both dependency tables itself.
func (t *embeddedTransaction) GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error) {
	return issueops.GetDependentRecordsInTx(ctx, t.tx, targetID, depType, limit, afterID)
}

// GetDependentRecordsForIssues returns the raw inbound dependency rows for a set
// of target ids within the transaction, keyed by target id. Mirrors
// EmbeddedDoltStore.GetDependentRecordsForIssues.
func (t *embeddedTransaction) GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	return issueops.GetDependentRecordsForIssuesInTx(ctx, t.tx, targetIDs)
}

// CountDependentRecords returns the total inbound-edge count of targetID within
// the transaction. Mirrors EmbeddedDoltStore.CountDependentRecords.
func (t *embeddedTransaction) CountDependentRecords(ctx context.Context, targetID string, depType string) (int, error) {
	return issueops.CountDependentRecordsInTx(ctx, t.tx, targetID, depType)
}

// IsBlocked reports the denormalized transitive is_blocked flag and direct
// blockers of issueID within the transaction. Mirrors
// EmbeddedDoltStore.IsBlocked's issueops delegation.
func (t *embeddedTransaction) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return issueops.IsBlockedInTx(ctx, t.tx, issueID)
}

// IsBlockedBatch reports the denormalized transitive is_blocked flag for a page
// of ids within the transaction. Mirrors EmbeddedDoltStore.IsBlockedBatch; the
// InTx function batches over both the issues and wisps tables itself.
func (t *embeddedTransaction) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	return issueops.IsBlockedBatchInTx(ctx, t.tx, ids)
}

// EventsSince returns durable events strictly after the keyset cursor within the
// transaction. Mirrors EmbeddedDoltStore.EventsSince's issueops delegation.
func (t *embeddedTransaction) EventsSince(ctx context.Context, cursor storage.EventCursor, issueID string, limit int) ([]*types.Event, error) {
	return issueops.EventsSinceInTx(ctx, t.tx, cursor.CreatedAt, cursor.ID, issueID, limit)
}
