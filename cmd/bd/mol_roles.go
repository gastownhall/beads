package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// edgeReadAnchorBatch bounds the anchors one EdgeReader call names. The http
// operation behind the role (listDependencies) refuses more than 100 issue_id
// values per request, and the local roles accept any count, so a batch this
// size is one round trip on every backend.
const edgeReadAnchorBatch = 100

// roleMolStore is the molecule commands' reader on the DIRECT route (a
// storage.DoltStorage, which may be a registered Remote backend such as http).
//
// It answers the molecule reads that have no raw method on every backend
// through issueops roles, so `bd mol current`, `bd mol progress`, the molecule
// auto-close after `bd close` and `bd close --continue` read the same thing on
// every backend instead of a remote backend's refusal being swallowed into an
// empty answer:
//
//   - GetDependencyRecordsForIssues -> EdgeReader (batched, parent walk);
//   - GetDependentsWithMetadata     -> Relations (full rows, both planes);
//   - GetIssuesByIDs                -> BatchGetter, or Get per id where the
//     backend does not serve the batch (a capability, not a fault);
//   - GetMoleculeProgress           -> Relations (inbound parent-child);
//   - the in_progress/hooked listing -> Reader.List (see listMoleculeCandidates).
//
// Everything else is promoted from the wrapped store. The proxied-server route
// keeps uowMolReader, which must answer from inside the caller's transaction.
type roleMolStore struct {
	storage.DoltStorage
}

// newRoleMolStore wraps s; a nil store stays nil so the callers' existing
// "no database connection" checks keep firing.
func newRoleMolStore(s storage.DoltStorage) molReader {
	if s == nil {
		return nil
	}
	return roleMolStore{DoltStorage: s}
}

// GetDependencyRecordsForIssues answers each id's stored outgoing edges through
// the EdgeReader role. An id that names nothing is absent from the map, matching
// the raw method.
func (s roleMolStore) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	return readEdgesByAnchor(ctx, s.DoltStorage, issueIDs, nil)
}

// GetIssuesByIDs answers labeled rows for ids in either plane through the
// BatchGetter role. A backend that does not serve the batch (an http server
// without issues.batchGet) answers the same rows one Get at a time; any other
// failure surfaces.
func (s roleMolStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	getter, err := s.BatchGetter()
	if err == nil {
		out := make([]*types.Issue, 0, len(ids))
		for start := 0; start < len(ids); start += issueops.MaxGetManyIDs {
			end := min(start+issueops.MaxGetManyIDs, len(ids))
			res, gerr := getter.GetMany(ctx, issueops.GetManyRequest{IDs: ids[start:end]})
			if gerr != nil {
				err = gerr
				break
			}
			out = append(out, res.Issues...)
		}
		if err == nil {
			return out, nil
		}
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) {
		return nil, err
	}
	out := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		issue, gerr := s.GetIssue(ctx, id)
		if errors.Is(gerr, storage.ErrNotFound) {
			continue
		}
		if gerr != nil {
			return nil, gerr
		}
		if issue != nil {
			out = append(out, issue)
		}
	}
	return out, nil
}

// GetDependentsWithMetadata answers an issue's inbound neighbors through the
// Relations role. Over http that is the full neighbor row; the raw method rides
// getIssue's shallow dependent projection, which zeroes assignee, description,
// labels and timestamps on every step a molecule load reads. An anchor that
// names nothing answers no dependents, matching the raw method.
func (s roleMolStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	relations, err := s.IssueRelations()
	if err != nil {
		return nil, err
	}
	out, err := relations.Related(ctx, issueops.RelatedRequest{ID: issueID, Direction: issueops.RelationIn})
	if errors.Is(err, issueops.ErrNotFound) {
		return nil, nil
	}
	return out, err
}

// GetMoleculeProgress counts a molecule's parent-child steps through the
// Relations role, which every backend serves and which reads both planes.
func (s roleMolStore) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	relations, err := s.IssueRelations()
	if err != nil {
		return nil, err
	}
	children, err := relations.Related(ctx, issueops.RelatedRequest{
		ID:        moleculeID,
		Direction: issueops.RelationIn,
		Types:     []types.DependencyType{types.DepParentChild},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get molecule children: %w", err)
	}
	title := ""
	if root, err := s.GetIssue(ctx, moleculeID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("failed to get molecule: %w", err)
	} else if root != nil {
		title = root.Title
	}
	return moleculeProgressFromChildren(moleculeID, title, children), nil
}

// moleculeProgressFromChildren folds a molecule's inbound dependents into its
// progress counts. Both direct-route and proxied-route readers use it, so the
// two answer one rule.
func moleculeProgressFromChildren(moleculeID, title string, dependents []*types.IssueWithDependencyMetadata) *types.MoleculeProgressStats {
	stats := &types.MoleculeProgressStats{MoleculeID: moleculeID, MoleculeTitle: title}
	for _, dependent := range dependents {
		if dependent == nil || dependent.DependencyType != types.DepParentChild {
			continue
		}
		stats.Total++
		switch dependent.Status {
		case types.StatusClosed:
			stats.Completed++
		case types.StatusInProgress:
			stats.InProgress++
			if stats.CurrentStepID == "" {
				stats.CurrentStepID = dependent.ID
			}
		}
	}
	return stats
}

// readEdgesByAnchor reads the outgoing edges of ids through the EdgeReader
// role in batches the http operation accepts. Missing anchors are omitted.
func readEdgesByAnchor(ctx context.Context, st storage.DoltStorage, ids []string, depTypes []types.DependencyType) (map[string][]*types.Dependency, error) {
	out := make(map[string][]*types.Dependency, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	reader, err := st.EdgeReader()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(ids); start += edgeReadAnchorBatch {
		end := min(start+edgeReadAnchorBatch, len(ids))
		res, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: ids[start:end], Types: depTypes})
		if err != nil {
			return nil, err
		}
		for _, anchor := range res.Anchors {
			if anchor.Missing {
				continue
			}
			out[anchor.ID] = anchor.Edges
		}
	}
	return out, nil
}

// issueReaderSource is the role accessor a direct-route molecule reader carries.
type issueReaderSource interface {
	IssueReader() (issueops.Reader, error)
}

// listMoleculeCandidates lists the issues in one status (assigned to agent when
// agent is set) that the molecule finders start from.
//
// A reader that carries the Reader role (every storage.DoltStorage, including a
// remote backend) answers through Reader.List — the raw SearchIssues a remote
// backend cannot express. The proxied route's in-transaction port has no role
// and keeps SearchIssues. Either way a failure is returned, never an empty list.
func listMoleculeCandidates(ctx context.Context, s molReader, status types.Status, agent string) ([]*types.Issue, error) {
	if src, ok := s.(issueReaderSource); ok {
		reader, err := src.IssueReader()
		if err != nil {
			return nil, err
		}
		unlimited := 0
		page, err := reader.List(ctx, issueops.ListRequest{
			Status:          string(status),
			Assignee:        agent,
			IncludeAllTypes: true,
			Limit:           &unlimited,
		})
		if err != nil {
			return nil, err
		}
		out := make([]*types.Issue, 0, len(page.Items))
		for _, row := range page.Items {
			if row != nil && row.Issue != nil {
				out = append(out, row.Issue)
			}
		}
		return out, nil
	}
	return searchMoleculeCandidates(ctx, s, status, agent)
}

// claimStepIfOpenViaLifecycle moves an OPEN step to in_progress through the
// Lifecycle role's ExpectedStatus compare-and-set. It is the direct route's
// `bd close --continue` auto-claim: one guarded update every backend serves,
// where a raw RunInTransaction is refused by a remote backend.
func claimStepIfOpenViaLifecycle(ctx context.Context, st storage.DoltStorage, id, actor string) error {
	lifecycle, err := st.IssueLifecycle()
	if err != nil {
		return err
	}
	open := types.StatusOpen
	_, err = lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor:          actor,
		IssueID:        id,
		Patch:          issueops.IssuePatch{Status: issueops.Field[types.Status]{Set: true, Value: types.StatusInProgress}},
		ExpectedStatus: &open,
	})
	return err
}

var _ molReader = roleMolStore{}

// closeCompletedMoleculeRoot closes a molecule root whose steps are all done,
// through the Lifecycle role every backend serves. The close is unforced: a
// root that close policy refuses (a live blocker) stays open and the refusal is
// returned for the caller to report.
func closeCompletedMoleculeRoot(ctx context.Context, st storage.DoltStorage, root *types.Issue, actor, session string) error {
	lifecycle, err := st.IssueLifecycle()
	if err != nil {
		return err
	}
	_, err = lifecycle.Close(ctx, issueops.CloseRequest{
		Actor:   actor,
		IssueID: root.ID,
		Reason:  "all steps complete",
		Session: session,
	})
	return err
}
