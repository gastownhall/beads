package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

type molReader interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error)
	GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error)
	GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error)
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error)
	GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error)
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error)
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetConfig(ctx context.Context, key string) (string, error)
	IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool
	GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error)
	GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error)
	GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error)
	FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error)
}

var _ molReader = storage.DoltStorage(nil)

type molConfigWriter interface {
	molReader
	SetConfig(ctx context.Context, key, value string) error
}

var _ molConfigWriter = storage.DoltStorage(nil)

type molWriter interface {
	molReader
	CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	AddLabel(ctx context.Context, issueID, label, actor string) error
	UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error
	CloseIssue(ctx context.Context, id, reason, actor string) error
	DeleteIssue(ctx context.Context, id, actor string) error
	SetConfig(ctx context.Context, key, value string) error
	ClaimStepIfOpen(ctx context.Context, id, actor string) error
}

type storeMolWriter struct {
	storage.DoltStorage
	tx storage.Transaction
}

func (w storeMolWriter) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return w.tx.CreateIssue(ctx, issue, actor)
}

func (w storeMolWriter) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return w.tx.AddDependency(ctx, dep, actor)
}

func (w storeMolWriter) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return w.tx.AddLabel(ctx, issueID, label, actor)
}

func (w storeMolWriter) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return w.tx.UpdateIssue(ctx, id, updates, actor)
}

func (w storeMolWriter) CloseIssue(ctx context.Context, id, reason, actor string) error {
	return w.tx.CloseIssue(ctx, id, reason, actor, "")
}

func (w storeMolWriter) DeleteIssue(ctx context.Context, id, _ string) error {
	return w.tx.DeleteIssue(ctx, id)
}

func (w storeMolWriter) SetConfig(ctx context.Context, key, value string) error {
	return w.tx.SetConfig(ctx, key, value)
}

func (w storeMolWriter) ClaimStepIfOpen(ctx context.Context, id, actor string) error {
	return w.DoltStorage.RunInTransaction(ctx, fmt.Sprintf("bd: advance to step %s", id), func(tx storage.Transaction) error {
		current, err := tx.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("step %s not found", id)
		}
		if current.Status != types.StatusOpen {
			return fmt.Errorf("step %s already claimed (status: %s)", id, current.Status)
		}
		return tx.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusInProgress}, actor)
	})
}

func newStandaloneStoreMolWriter(store storage.DoltStorage) storeMolWriter {
	return storeMolWriter{DoltStorage: store}
}

type uowMolReader struct {
	uw uow.UnitOfWork
}

func (r uowMolReader) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, isWisp, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(r.uw), id)
	if errors.Is(rerr, storage.ErrNotFound) {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	if rerr != nil {
		return nil, fmt.Errorf("resolving %s: %w", id, rerr)
	}
	var labels []string
	var err error
	if isWisp {
		labels, err = r.uw.LabelUseCase().GetWispLabels(ctx, id)
	} else {
		labels, err = r.uw.LabelUseCase().GetLabels(ctx, id)
	}
	if err == nil {
		issue.Labels = labels
	}
	return issue, nil
}

func (r uowMolReader) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	issues, err := r.uw.IssueUseCase().GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if labelMap, err := r.uw.LabelUseCase().GetLabelsForIssues(ctx, ids); err == nil {
		for _, issue := range issues {
			issue.Labels = labelMap[issue.ID]
		}
	}

	wisps, err := r.uw.IssueUseCase().GetWispsByIDs(ctx, ids)
	if err != nil {
		wisps = nil //nolint:staticcheck // wisps table may not exist; issues result still valid
	}
	if len(wisps) > 0 {
		if labelMap, err := r.uw.LabelUseCase().GetLabelsForWisps(ctx, ids); err == nil {
			for _, wisp := range wisps {
				wisp.Labels = labelMap[wisp.ID]
			}
		}
	}

	return append(issues, wisps...), nil
}

func (r uowMolReader) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	page, err := r.uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{Labels: []string{label}})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (r uowMolReader) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	withMeta, err := r.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]*types.Issue, len(withMeta))
	for i, m := range withMeta {
		issue := m.Issue
		out[i] = &issue
	}
	return out, nil
}

func (r uowMolReader) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	filter := domain.DepListFilter{Direction: domain.DepDirectionIn}
	return r.uw.DependencyUseCase().ListWithIssueMetadata(ctx, issueID, filter)
}

func (r uowMolReader) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	out, err := r.uw.DependencyUseCase().GetForIssueIDs(ctx, []string{issueID})
	if err != nil {
		return nil, err
	}
	return out[issueID], nil
}

func (r uowMolReader) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	return r.uw.DependencyUseCase().GetForIssueIDs(ctx, issueIDs)
}

func (r uowMolReader) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	page, err := r.uw.IssueUseCase().SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (r uowMolReader) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	return r.uw.IssueUseCase().SearchIssueIDs(ctx, query, filter)
}

func (r uowMolReader) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	page, err := r.uw.IssueUseCase().GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (r uowMolReader) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	return r.uw.IssueUseCase().GetBlockedIssues(ctx, filter)
}

func (r uowMolReader) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	return r.uw.IssueUseCase().GetEpicsEligibleForClosure(ctx)
}

func (r uowMolReader) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return r.uw.LabelUseCase().GetLabels(ctx, issueID)
}

func (r uowMolReader) GetConfig(ctx context.Context, key string) (string, error) {
	return r.uw.ConfigUseCase().GetConfig(ctx, key)
}

func (r uowMolReader) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	ok, err := r.uw.ConfigUseCase().IsInfraTypeCtx(ctx, t)
	return err == nil && ok
}

func (r uowMolReader) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	return r.uw.ConfigUseCase().GetCustomStatuses(ctx)
}

func (r uowMolReader) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	stats := &types.MoleculeProgressStats{MoleculeID: moleculeID}

	root, err := r.GetIssue(ctx, moleculeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get molecule: %w", err)
	}
	if root != nil {
		stats.MoleculeTitle = root.Title
	}

	dependents, err := r.GetDependentsWithMetadata(ctx, moleculeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get molecule children: %w", err)
	}

	for _, dependent := range dependents {
		if dependent.DependencyType != types.DepParentChild {
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
	return stats, nil
}

func (r uowMolReader) GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	dependents, err := r.GetDependentsWithMetadata(ctx, moleculeID)
	if err != nil {
		return nil, fmt.Errorf("get molecule children: %w", err)
	}

	var children []types.Issue
	for _, dependent := range dependents {
		if dependent.DependencyType != types.DepParentChild {
			continue
		}
		children = append(children, dependent.Issue)
	}

	if len(children) == 0 {
		root, err := r.GetIssue(ctx, moleculeID)
		if err != nil {
			return nil, fmt.Errorf("molecule %s not found: %w", moleculeID, err)
		}
		return &types.MoleculeLastActivity{
			MoleculeID:   moleculeID,
			LastActivity: root.UpdatedAt,
			Source:       "molecule_updated",
		}, nil
	}

	var lastUpdatedAt time.Time
	var lastUpdatedID string
	var lastClosedAt time.Time
	var lastClosedID string
	haveClosed := false

	for _, child := range children {
		if child.UpdatedAt.After(lastUpdatedAt) {
			lastUpdatedAt = child.UpdatedAt
			lastUpdatedID = child.ID
		}
		if child.ClosedAt != nil && (!haveClosed || child.ClosedAt.After(lastClosedAt)) {
			lastClosedAt = *child.ClosedAt
			lastClosedID = child.ID
			haveClosed = true
		}
	}

	result := &types.MoleculeLastActivity{
		MoleculeID:   moleculeID,
		LastActivity: lastUpdatedAt,
		Source:       "step_updated",
		SourceStepID: lastUpdatedID,
	}
	if haveClosed && lastClosedAt.After(lastUpdatedAt) {
		result.LastActivity = lastClosedAt
		result.Source = "step_closed"
		result.SourceStepID = lastClosedID
	}
	return result, nil
}

func (r uowMolReader) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	return r.uw.IssueUseCase().FindWispDependentsRecursive(ctx, ids)
}

type uowMolWriter struct {
	uowMolReader
	wispIDs    map[string]bool
	notWispIDs map[string]bool
}

func newUOWMolWriter(uw uow.UnitOfWork) *uowMolWriter {
	return &uowMolWriter{
		uowMolReader: uowMolReader{uw: uw},
		wispIDs:      make(map[string]bool),
		notWispIDs:   make(map[string]bool),
	}
}

func (w *uowMolWriter) isWisp(ctx context.Context, id string) (bool, error) {
	if w.wispIDs[id] {
		return true, nil
	}
	if w.notWispIDs[id] {
		return false, nil
	}
	_, err := w.uw.IssueUseCase().GetWisp(ctx, id)
	if err == nil {
		w.wispIDs[id] = true
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		w.notWispIDs[id] = true
		return false, nil
	}
	return false, fmt.Errorf("determining wisp status for %s: %w", id, err)
}

func (w *uowMolWriter) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	params := domain.CreateIssueParams{Issue: issue, ExplicitID: issue.ID, Labels: issue.Labels}
	var err error
	if issue.Ephemeral || issue.NoHistory {
		_, err = w.uw.IssueUseCase().CreateWisp(ctx, params, actor)
		if err == nil {
			w.wispIDs[issue.ID] = true
		}
	} else {
		_, err = w.uw.IssueUseCase().CreateIssue(ctx, params, actor)
		if err == nil {
			w.notWispIDs[issue.ID] = true
		}
	}
	return err
}

func (w *uowMolWriter) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	isWisp, err := w.isWisp(ctx, dep.IssueID)
	if err != nil {
		return err
	}
	if isWisp {
		return w.uw.DependencyUseCase().AddWispDependency(ctx, dep, actor)
	}
	return w.uw.DependencyUseCase().AddDependency(ctx, dep, actor)
}

func (w *uowMolWriter) AddLabel(ctx context.Context, issueID, label, actor string) error {
	isWisp, err := w.isWisp(ctx, issueID)
	if err != nil {
		return err
	}
	if isWisp {
		return w.uw.LabelUseCase().AddWispLabel(ctx, issueID, label, actor)
	}
	return w.uw.LabelUseCase().AddLabel(ctx, issueID, label, actor)
}

func (w *uowMolWriter) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	isWisp, err := w.isWisp(ctx, id)
	if err != nil {
		return err
	}
	if isWisp {
		return w.uw.IssueUseCase().UpdateWisp(ctx, id, updates, actor)
	}
	return w.uw.IssueUseCase().UpdateIssue(ctx, id, updates, actor)
}

func (w *uowMolWriter) CloseIssue(ctx context.Context, id, reason, actor string) error {
	params := domain.CloseIssueParams{Reason: reason}
	isWisp, err := w.isWisp(ctx, id)
	if err != nil {
		return err
	}
	if isWisp {
		_, err = w.uw.IssueUseCase().CloseWisp(ctx, id, params, actor)
	} else {
		_, err = w.uw.IssueUseCase().CloseIssue(ctx, id, params, actor)
	}
	return err
}

func (w *uowMolWriter) DeleteIssue(ctx context.Context, id, actor string) error {
	_, err := w.uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs:                  []string{id},
		UpdateTextReferences: true,
	}, actor)
	return err
}

func (w *uowMolWriter) SetConfig(ctx context.Context, key, value string) error {
	return w.uw.ConfigUseCase().SetConfig(ctx, key, value)
}

func (w *uowMolWriter) ClaimStepIfOpen(ctx context.Context, id, actor string) error {
	isWisp, err := w.isWisp(ctx, id)
	if err != nil {
		return err
	}
	if isWisp {
		_, err := w.uw.IssueUseCase().ClaimWispIfOpen(ctx, id, actor)
		return err
	}
	_, err = w.uw.IssueUseCase().ClaimIssueIfOpen(ctx, id, actor)
	return err
}
