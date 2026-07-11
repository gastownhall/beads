package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

type InsertIssueOpts struct {
	UseWispsTable bool
}

type IssueTableOpts struct {
	UseWispsTable bool
}

type ClaimRowResult struct {
	Updated          bool
	CurrentAssignee  string
	CurrentStatus    types.Status
	StartedAtWasZero bool
	OldIssue         *types.Issue
}

type IssueSQLRepository interface {
	Insert(ctx context.Context, issue *types.Issue, actor string, opts InsertIssueOpts) error
	InsertBatch(ctx context.Context, issues []*types.Issue, actor string, opts InsertIssueOpts) error
	Update(ctx context.Context, id string, updates map[string]any, actor string, opts IssueTableOpts) error
	Claim(ctx context.Context, id, actor string, opts IssueTableOpts) (ClaimRowResult, error)
	Get(ctx context.Context, id string, opts IssueTableOpts) (*types.Issue, error)
	AsOf(ctx context.Context, id, ref string) (*types.Issue, error)
	GetByIDs(ctx context.Context, ids []string, opts IssueTableOpts) ([]*types.Issue, error)
	Exists(ctx context.Context, id string, opts IssueTableOpts) (bool, error)
	CountForPrefix(ctx context.Context, prefix string, opts IssueTableOpts) (int, error)
	NextCounterID(ctx context.Context, prefix string) (int, error)
	SearchAcrossIssuesAndWisps(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error)
	SearchAcrossIssuesAndWispsWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error)
	GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error)
	GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error)
	GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)
	Delete(ctx context.Context, id string, opts IssueTableOpts) error
	DeleteByIDs(ctx context.Context, ids []string, opts IssueTableOpts) (int, error)
	PartitionWispIDs(ctx context.Context, ids []string) (wispIDs, regularIDs []string, err error)
	FindAllDependents(ctx context.Context, ids []string) ([]string, error)
	AffectedByDeletion(ctx context.Context, issueIDs, wispIDs []string) (affectedIssues, affectedWisps []string, err error)
	RecomputeIsBlocked(ctx context.Context, issueIDs, wispIDs []string) error
	Close(ctx context.Context, id string, params CloseRowParams, actor string, opts IssueTableOpts) (CloseRowResult, error)
	Reopen(ctx context.Context, id string, params ReopenRowParams, actor string, opts IssueTableOpts) (ReopenRowResult, error)
	GetNewlyUnblockedByClose(ctx context.Context, closedID string) ([]*types.Issue, error)
	ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error)
	ClaimReadyWisp(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error)
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	GetStatistics(ctx context.Context) (*types.Statistics, error)
}

type CloseRowParams struct {
	Reason  string
	Session string
}

type CloseRowResult struct {
	Updated       bool
	AlreadyClosed bool
	IsWisp        bool
}

type ReopenRowParams struct {
	Reason string
}

type ReopenRowResult struct {
	Updated     bool
	AlreadyOpen bool
	IsWisp      bool
}

type DeleteIssuesParams struct {
	IDs                  []string
	Cascade              bool
	DryRun               bool
	UpdateTextReferences bool

	// EnforceCascadePolicy selects embedded-parity dependent handling
	// (issueops.DeleteIssuesInTx). When false — the legacy default used by the
	// proxied-server delete command, which always cascades — deletion expands to
	// all transitive dependents regardless of Cascade/Force. When true,
	// Cascade/Force choose the behavior:
	//   Cascade=true               → delete all transitive dependents
	//   Cascade=false, Force=false → refuse if any external dependent exists
	//   Cascade=false, Force=true  → orphan external dependents (delete only IDs)
	EnforceCascadePolicy bool
	Force                bool
}

type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	ReferencesUpdated int
	// OrphanedIssues lists external dependents left behind by a force delete
	// (Cascade=false, Force=true), or the blocking dependents reported with the
	// refusal error (Cascade=false, Force=false). Empty on the cascade path.
	OrphanedIssues []string
}

type DeletePreview struct {
	Issues          map[string]*types.Issue
	ConnectedIssues map[string]*types.Issue
	DepRecords      map[string][]*types.Dependency
	NotFound        []string
}

type SearchPage struct {
	Items   []*types.Issue
	HasMore bool
}

type SearchCountsPage struct {
	Items   []*types.IssueWithCounts
	HasMore bool
}

type CreateIssueParams struct {
	Issue                   *types.Issue
	ExplicitID              string
	ParentID                string
	Labels                  []string
	InheritLabelsFromParent bool
	Dependencies            []DependencySpec
	WaitsFor                *WaitsForSpec
	DiscoveredFromParent    string
	ForcePrefix             bool
}

type DependencySpec struct {
	Type          types.DependencyType
	TargetID      string
	SwapDirection bool
	Metadata      string
}

type WaitsForSpec struct {
	SpawnerID string
	Gate      string
}

type CreateIssueResult struct {
	Issue            *types.Issue
	InheritedLabels  []string
	PostCreateWrites bool
}

type CreateIssuesResult struct {
	Issues []*types.Issue
}

type GraphPlan struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

type GraphNode struct {
	Key               string
	Issue             *types.Issue
	ParentKey         string
	ParentID          string
	Assignee          string
	AssignAfterCreate bool
	MetadataRefs      map[string]string
	Labels            []string
}

type GraphEdge struct {
	FromKey string
	FromID  string
	ToKey   string
	ToID    string
	Type    types.DependencyType
}

type GraphApplyResult struct {
	IDs map[string]string
}

type ClaimResult struct {
	AlreadyClaimed bool
	PriorAssignee  string
}

type ClaimReadyResult struct {
	Issue   *types.Issue
	Claimed bool
}

type UpdateSpec struct {
	Fields       map[string]any
	Claim        bool
	AddLabels    []string
	RemoveLabels []string
	SetLabels    *[]string
	Reparent     *string
}

type IssueUseCase interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error)
	SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error)
	GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error)
	GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error)
	GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)
	ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (ClaimReadyResult, error)
	GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error)
	GetStatistics(ctx context.Context) (*types.Statistics, error)

	CreateIssue(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error)
	CreateIssues(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]any, actor string) error
	ClaimIssue(ctx context.Context, id, actor string) (ClaimResult, error)
	ClaimIssueIfOpen(ctx context.Context, id, actor string) (ClaimResult, error)
	CloseIssue(ctx context.Context, id string, params CloseIssueParams, actor string) (CloseIssueResult, error)
	ReopenIssue(ctx context.Context, id string, params ReopenIssueParams, actor string) (ReopenIssueResult, error)
	CountOpenChildren(ctx context.Context, id string) (int, error)
	GetNewlyUnblockedByClose(ctx context.Context, closedID string) ([]*types.Issue, error)
	ApplyUpdate(ctx context.Context, id string, spec UpdateSpec, actor string) (*types.Issue, error)
	ApplyIssueGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error)
	AsOf(ctx context.Context, id, ref string) (*types.Issue, error)
	DeleteIssue(ctx context.Context, id, actor string) (DeleteIssuesResult, error)
	DeleteIssues(ctx context.Context, params DeleteIssuesParams, actor string) (DeleteIssuesResult, error)
	PreviewDelete(ctx context.Context, ids []string) (DeletePreview, error)
	DeleteWisp(ctx context.Context, id, actor string) (DeleteIssuesResult, error)
	DeleteWisps(ctx context.Context, params DeleteIssuesParams, actor string) (DeleteIssuesResult, error)
	PreviewDeleteWisp(ctx context.Context, ids []string) (DeletePreview, error)

	GetWisp(ctx context.Context, id string) (*types.Issue, error)
	GetWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	CreateWisp(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error)
	CreateWisps(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error)
	UpdateWisp(ctx context.Context, id string, updates map[string]any, actor string) error
	ClaimWisp(ctx context.Context, id, actor string) (ClaimResult, error)
	ClaimWispIfOpen(ctx context.Context, id, actor string) (ClaimResult, error)
	CloseWisp(ctx context.Context, id string, params CloseIssueParams, actor string) (CloseIssueResult, error)
	ReopenWisp(ctx context.Context, id string, params ReopenIssueParams, actor string) (ReopenIssueResult, error)
	CountOpenWispChildren(ctx context.Context, id string) (int, error)
	GetNewlyUnblockedByCloseWisp(ctx context.Context, closedID string) ([]*types.Issue, error)
	ApplyWispGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error)
	ClaimReadyWisp(ctx context.Context, filter types.WorkFilter, actor string) (ClaimReadyResult, error)
}

type CloseIssueParams struct {
	Reason  string
	Session string
}

type CloseIssueResult struct {
	Issue  *types.Issue
	Closed bool
}

type ReopenIssueParams struct {
	Reason string
}

type ReopenIssueResult struct {
	Issue    *types.Issue
	Reopened bool
}

func NewIssueUseCase(
	issueRepo IssueSQLRepository,
	depRepo DependencySQLRepository,
	labelRepo LabelSQLRepository,
	counterRepo ChildCounterSQLRepository,
	commentRepo CommentSQLRepository,
	cfgRepo ConfigSQLRepository,
	eventsRepo EventsSQLRepository,
	labelUC LabelUseCase,
	depUC DependencyUseCase,
) IssueUseCase {
	return &issueUseCaseImpl{
		issueRepo:   issueRepo,
		depRepo:     depRepo,
		labelRepo:   labelRepo,
		counterRepo: counterRepo,
		commentRepo: commentRepo,
		cfgRepo:     cfgRepo,
		eventsRepo:  eventsRepo,
		labelUC:     labelUC,
		depUC:       depUC,
	}
}

type issueUseCaseImpl struct {
	issueRepo   IssueSQLRepository
	depRepo     DependencySQLRepository
	labelRepo   LabelSQLRepository
	counterRepo ChildCounterSQLRepository
	commentRepo CommentSQLRepository
	cfgRepo     ConfigSQLRepository
	eventsRepo  EventsSQLRepository
	labelUC     LabelUseCase
	depUC       DependencyUseCase
}

var _ IssueUseCase = (*issueUseCaseImpl)(nil)

func (u *issueUseCaseImpl) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return u.get(ctx, id, false)
}

func (u *issueUseCaseImpl) AsOf(ctx context.Context, id, ref string) (*types.Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("as of: id must not be empty")
	}
	issue, err := u.issueRepo.AsOf(ctx, id, ref)
	if err != nil {
		return nil, fmt.Errorf("as of %s @ %s: %w", id, ref, err)
	}
	return issue, nil
}

func (u *issueUseCaseImpl) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	return u.get(ctx, id, true)
}

func (u *issueUseCaseImpl) get(ctx context.Context, id string, useWisp bool) (*types.Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("get: id must not be empty")
	}
	issue, err := u.issueRepo.Get(ctx, id, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	}
	return issue, nil
}

func (u *issueUseCaseImpl) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	return u.getByIDs(ctx, ids, false)
}

func (u *issueUseCaseImpl) GetWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	return u.getByIDs(ctx, ids, true)
}

func (u *issueUseCaseImpl) getByIDs(ctx context.Context, ids []string, useWisp bool) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := u.issueRepo.GetByIDs(ctx, ids, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("getByIDs: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) UpdateIssue(ctx context.Context, id string, updates map[string]any, actor string) error {
	return u.update(ctx, id, updates, actor, false)
}

func (u *issueUseCaseImpl) UpdateWisp(ctx context.Context, id string, updates map[string]any, actor string) error {
	return u.update(ctx, id, updates, actor, true)
}

func (u *issueUseCaseImpl) update(ctx context.Context, id string, updates map[string]any, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("update: id must not be empty")
	}
	if len(updates) == 0 {
		return nil
	}
	if rawType, ok := updates["issue_type"]; ok {
		if issueType, ok := rawType.(string); ok && issueType != "" {
			customTypes, err := u.cfgRepo.GetCustomTypes(ctx)
			if err != nil {
				return fmt.Errorf("update: read custom types: %w", err)
			}
			if !types.IssueType(issueType).IsValidWithCustom(customTypes) {
				return fmt.Errorf("invalid issue type: %s", issueType)
			}
		}
	}
	return u.issueRepo.Update(ctx, id, updates, actor, IssueTableOpts{UseWispsTable: useWisp})
}

func (u *issueUseCaseImpl) ClaimIssue(ctx context.Context, id, actor string) (ClaimResult, error) {
	return u.claim(ctx, id, actor, false)
}

func (u *issueUseCaseImpl) ClaimWisp(ctx context.Context, id, actor string) (ClaimResult, error) {
	return u.claim(ctx, id, actor, true)
}

func (u *issueUseCaseImpl) claim(ctx context.Context, id, actor string, useWisp bool) (ClaimResult, error) {
	if id == "" {
		return ClaimResult{}, fmt.Errorf("claim: id must not be empty")
	}
	if actor == "" {
		return ClaimResult{}, fmt.Errorf("claim: actor must not be empty")
	}
	row, err := u.issueRepo.Claim(ctx, id, actor, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return ClaimResult{}, fmt.Errorf("claim %s: %w", id, err)
	}
	if row.Updated {
		return ClaimResult{}, nil
	}
	if row.CurrentAssignee == actor && row.CurrentStatus == types.StatusInProgress {
		return ClaimResult{AlreadyClaimed: true, PriorAssignee: actor}, nil
	}
	if row.CurrentAssignee != "" && row.CurrentAssignee != actor {
		return ClaimResult{}, fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, row.CurrentAssignee)
	}
	return ClaimResult{}, fmt.Errorf("%w: status %s", storage.ErrNotClaimable, row.CurrentStatus)
}

func (u *issueUseCaseImpl) ApplyUpdate(ctx context.Context, id string, spec UpdateSpec, actor string) (*types.Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("ApplyUpdate: id must not be empty")
	}

	useWisp, err := u.isWispID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ApplyUpdate %s: %w", id, err)
	}

	if spec.Claim {
		if useWisp {
			if _, err := u.ClaimWisp(ctx, id, actor); err != nil {
				return nil, err
			}
		} else {
			if _, err := u.ClaimIssue(ctx, id, actor); err != nil {
				return nil, err
			}
		}
	}

	if len(spec.Fields) > 0 {
		if useWisp {
			if err := u.UpdateWisp(ctx, id, spec.Fields, actor); err != nil {
				return nil, err
			}
		} else {
			if err := u.UpdateIssue(ctx, id, spec.Fields, actor); err != nil {
				return nil, err
			}
		}
	}

	if spec.SetLabels != nil {
		if useWisp {
			if err := u.labelUC.SetWispLabels(ctx, id, *spec.SetLabels, actor); err != nil {
				return nil, err
			}
		} else {
			if err := u.labelUC.SetLabels(ctx, id, *spec.SetLabels, actor); err != nil {
				return nil, err
			}
		}
	} else {
		if len(spec.AddLabels) > 0 {
			if useWisp {
				if err := u.labelUC.AddWispLabels(ctx, id, spec.AddLabels, actor); err != nil {
					return nil, err
				}
			} else {
				if err := u.labelUC.AddLabels(ctx, id, spec.AddLabels, actor); err != nil {
					return nil, err
				}
			}
		}
		if len(spec.RemoveLabels) > 0 {
			if useWisp {
				if err := u.labelUC.RemoveWispLabels(ctx, id, spec.RemoveLabels, actor); err != nil {
					return nil, err
				}
			} else {
				if err := u.labelUC.RemoveLabels(ctx, id, spec.RemoveLabels, actor); err != nil {
					return nil, err
				}
			}
		}
	}

	if spec.Reparent != nil {
		if useWisp {
			if err := u.depUC.ReparentWisp(ctx, id, *spec.Reparent, actor); err != nil {
				return nil, err
			}
		} else {
			if err := u.depUC.Reparent(ctx, id, *spec.Reparent, actor); err != nil {
				return nil, err
			}
		}
	}

	var issue *types.Issue
	if useWisp {
		issue, err = u.GetWisp(ctx, id)
	} else {
		issue, err = u.GetIssue(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("ApplyUpdate: re-fetch %s: %w", id, err)
	}
	return issue, nil
}

func (u *issueUseCaseImpl) isWispID(ctx context.Context, id string) (bool, error) {
	found, err := u.issueRepo.Exists(ctx, id, IssueTableOpts{UseWispsTable: true})
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("probe wisps table: %w", err)
	}
	return found, nil
}

func (u *issueUseCaseImpl) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error) {
	out, err := u.issueRepo.SearchAcrossIssuesAndWisps(ctx, query, filter)
	if err != nil {
		return SearchPage{}, fmt.Errorf("SearchIssues: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error) {
	out, err := u.issueRepo.SearchAcrossIssuesAndWispsWithCounts(ctx, query, filter)
	if err != nil {
		return SearchCountsPage{}, fmt.Errorf("SearchIssuesWithCounts: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error) {
	out, err := u.issueRepo.GetReadyWork(ctx, filter)
	if err != nil {
		return SearchPage{}, fmt.Errorf("GetReadyWork: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error) {
	out, err := u.issueRepo.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		return SearchCountsPage{}, fmt.Errorf("GetReadyWorkWithCounts: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	if rootID == "" {
		return nil, fmt.Errorf("GetDescendants: rootID must not be empty")
	}
	out, err := u.issueRepo.GetDescendants(ctx, rootID, filter)
	if err != nil {
		return nil, fmt.Errorf("GetDescendants: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) CreateIssue(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error) {
	return u.create(ctx, params, actor, false)
}

func (u *issueUseCaseImpl) CreateWisp(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error) {
	return u.create(ctx, params, actor, true)
}

func (u *issueUseCaseImpl) create(ctx context.Context, params CreateIssueParams, actor string, useWisp bool) (CreateIssueResult, error) {
	if params.Issue == nil {
		return CreateIssueResult{}, fmt.Errorf("create: Issue must not be nil")
	}
	issue := params.Issue

	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}

	// Set CreatedAt before the mint path: GenerateHashID hashes timestamp,
	// so it must be stable across the candidate loop and the eventual
	// db.Insert (which would otherwise normalize a zero value to a later
	// time and break candidate reproducibility on retry).
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = time.Now().UTC()
	}

	switch {
	case params.ExplicitID != "":
		issue.ID = params.ExplicitID
	case params.ParentID != "":
		childID, err := u.counterRepo.NextChildID(ctx, params.ParentID, ChildCounterOpts{UseWispsTable: useWisp})
		if err != nil {
			return CreateIssueResult{}, fmt.Errorf("create: next child ID for %s: %w", params.ParentID, err)
		}
		issue.ID = childID
	case issue.ID == "":
		minted, err := u.mintTopLevelID(ctx, issue, actor, useWisp)
		if err != nil {
			return CreateIssueResult{}, fmt.Errorf("create: mint top-level ID: %w", err)
		}
		issue.ID = minted
	}

	if params.DiscoveredFromParent != "" {
		if parent, err := u.GetIssue(ctx, params.DiscoveredFromParent); err == nil && parent.SourceRepo != "" {
			issue.SourceRepo = parent.SourceRepo
		}
	}

	insertOpts := InsertIssueOpts{UseWispsTable: useWisp}
	if err := u.issueRepo.Insert(ctx, issue, actor, insertOpts); err != nil {
		return CreateIssueResult{}, fmt.Errorf("create: insert: %w", err)
	}

	result := CreateIssueResult{Issue: issue}

	if params.ParentID != "" {
		pcDep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: params.ParentID,
			Type:        types.DepParentChild,
		}
		if err := u.depRepo.Insert(ctx, pcDep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add parent-child dep: %w", err)
		}
		result.PostCreateWrites = true
	}

	if params.InheritLabelsFromParent && params.ParentID != "" {
		parentLabels, err := u.labelRepo.List(ctx, params.ParentID, LabelOpts{UseWispsTable: useWisp})
		switch {
		case dberrors.IsTableNotExist(err):
			// Older schemas may lack the wisp label table; nothing to inherit.
		case err != nil:
			// Swallowing this silently created children missing their
			// inherited labels (bd-6dnrw.44 P3); the create is transactional,
			// so failing loud is safe.
			return result, fmt.Errorf("create: read parent labels for inheritance from %s: %w", params.ParentID, err)
		default:
			existing := make(map[string]bool, len(params.Labels))
			for _, l := range params.Labels {
				existing[l] = true
			}
			for _, l := range parentLabels {
				if !existing[l] {
					result.InheritedLabels = append(result.InheritedLabels, l)
				}
			}
		}
	}

	for _, label := range params.Labels {
		if err := u.labelRepo.Insert(ctx, issue.ID, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add label %s: %w", label, err)
		}
		result.PostCreateWrites = true
	}
	for _, label := range result.InheritedLabels {
		if err := u.labelRepo.Insert(ctx, issue.ID, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add inherited label %s: %w", label, err)
		}
		result.PostCreateWrites = true
	}

	for _, spec := range params.Dependencies {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: spec.TargetID,
			Type:        spec.Type,
			Metadata:    spec.Metadata,
		}
		if spec.SwapDirection {
			dep.IssueID, dep.DependsOnID = dep.DependsOnID, dep.IssueID
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add dep %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
		}
		result.PostCreateWrites = true
	}

	if params.WaitsFor != nil {
		gate := params.WaitsFor.Gate
		if gate == "" {
			gate = types.WaitsForAllChildren
		}
		metaJSON, err := json.Marshal(types.WaitsForMeta{Gate: gate})
		if err != nil {
			return result, fmt.Errorf("create: marshal waits-for meta: %w", err)
		}
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: params.WaitsFor.SpawnerID,
			Type:        types.DepWaitsFor,
			Metadata:    string(metaJSON),
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add waits-for: %w", err)
		}
		result.PostCreateWrites = true
	}

	return result, nil
}

func (u *issueUseCaseImpl) CreateIssues(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error) {
	return u.createMany(ctx, params, actor, false)
}

func (u *issueUseCaseImpl) CreateWisps(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error) {
	return u.createMany(ctx, params, actor, true)
}

func (u *issueUseCaseImpl) createMany(ctx context.Context, params []CreateIssueParams, actor string, useWisp bool) (CreateIssuesResult, error) {
	result := CreateIssuesResult{}
	for i := range params {
		r, err := u.create(ctx, params[i], actor, useWisp)
		if err != nil {
			return result, fmt.Errorf("createMany[%d]: %w", i, err)
		}
		result.Issues = append(result.Issues, r.Issue)
	}
	return result, nil
}

func (u *issueUseCaseImpl) ApplyIssueGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error) {
	return u.applyGraph(ctx, plan, actor, false)
}

func (u *issueUseCaseImpl) ApplyWispGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error) {
	return u.applyGraph(ctx, plan, actor, true)
}

func (u *issueUseCaseImpl) applyGraph(ctx context.Context, plan GraphPlan, actor string, useWisp bool) (GraphApplyResult, error) {
	keyToID := make(map[string]string, len(plan.Nodes))
	pendingAssignees := make(map[int]string, len(plan.Nodes))

	// Pass 1 — create every node as a top-level issue. We deliberately do
	// not pass ParentID to u.create: graph nodes with parent_key/parent_id
	// receive top-level hash (or counter) IDs and have their parent linkage
	// added as a separate parent-child dep below. This matches embedded
	// executeGraphApply (cmd/bd/graph_apply.go) and lets children precede
	// parents in plan order — keyToID is only consulted after every node
	// has minted its ID.
	for i, node := range plan.Nodes {
		if node.Issue == nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %d (key=%q) has nil Issue", i, node.Key)
		}

		if node.AssignAfterCreate {
			pendingAssignees[i] = node.Assignee
			node.Issue.Assignee = ""
		} else if node.Assignee != "" {
			node.Issue.Assignee = node.Assignee
		}

		params := CreateIssueParams{
			Issue:  node.Issue,
			Labels: node.Labels,
		}
		r, err := u.create(ctx, params, actor, useWisp)
		if err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: %w", node.Key, err)
		}
		keyToID[node.Key] = r.Issue.ID
	}

	// Pass 2 — resolve MetadataRefs now that every node has a minted ID.
	// Merges the resolved IDs into the issue's existing metadata JSON and
	// writes the result back via Update. Kept inside applyGraph so the CLI
	// cannot bypass this step; the proxied caller used to do it post-call.
	for _, node := range plan.Nodes {
		if len(node.MetadataRefs) == 0 {
			continue
		}
		merged := make(map[string]string, len(node.MetadataRefs))
		if len(node.Issue.Metadata) > 0 {
			if err := json.Unmarshal(node.Issue.Metadata, &merged); err != nil {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: re-parsing metadata: %w", node.Key, err)
			}
		}
		for metaKey, refKey := range node.MetadataRefs {
			resolvedID, ok := keyToID[refKey]
			if !ok {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: metadata_ref %q references unknown key %q", node.Key, metaKey, refKey)
			}
			merged[metaKey] = resolvedID
		}
		metaJSON, err := json.Marshal(merged)
		if err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: marshaling merged metadata: %w", node.Key, err)
		}
		updates := map[string]any{"metadata": json.RawMessage(metaJSON)}
		if err := u.issueRepo.Update(ctx, keyToID[node.Key], updates, actor, IssueTableOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: updating metadata refs: %w", node.Key, err)
		}
	}

	// Build the (childID, parentID) pair set and validate that any planned
	// parent-child link does not close a cycle through planned edges or
	// already-existing dependencies in the store. This must run before any
	// dep inserts to catch the violation before we've written anything.
	parentDepPairs := graphParentDepPairs(plan.Nodes, keyToID)
	if err := u.validatePlannedBlockingPaths(ctx, plan, keyToID, parentDepPairs); err != nil {
		return GraphApplyResult{}, err
	}
	if err := u.validatePlannedBlockingCycles(ctx, plan, keyToID); err != nil {
		return GraphApplyResult{}, err
	}

	// Pass 3 — insert edge deps. Deduplicate against the parent-child pairs:
	//   - Same pair, parent-child type → skip (pass 4 will insert it).
	//   - Same pair, different type   → error (conflicting edge over a parent-child link).
	//   - Reverse pair, blocking type → error (creates a parent → child blocking cycle).
	//
	// Blocking cycles are already proven absent by validatePlannedBlockingCycles
	// above (whole-graph preflight over planned + existing blocking edges, the
	// same strategy as embedded executeGraphApply), so the edge insert loop no
	// longer runs a per-edge HasCycle SQL probe.
	for i, edge := range plan.Edges {
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		if fromID == "" {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d references undefined from_key %q", i, edge.FromKey)
		}
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if toID == "" {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d references undefined to_key %q", i, edge.ToKey)
		}
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}

		if parentDepPairs[depPairKey(fromID, toID)] {
			if depType == types.DepParentChild {
				continue
			}
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d %s->%s duplicates a parent-child relationship with dependency type %q", i, fromID, toID, depType)
		}
		if parentDepPairs[depPairKey(toID, fromID)] && cycleRelevantDepType(depType) {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d %s->%s creates a blocking reverse of a parent-child relationship", i, fromID, toID)
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        depType,
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d (%s -> %s): %w", i, fromID, toID, err)
		}
	}

	// Pass 4 — insert parent-child deps now that all IDs are known.
	for _, node := range plan.Nodes {
		parentID := node.ParentID
		if node.ParentKey != "" {
			parentID = keyToID[node.ParentKey]
		}
		if parentID == "" {
			continue
		}
		childID := keyToID[node.Key]
		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: parentID,
			Type:        types.DepParentChild,
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: parent-child dep %s->%s: %w", node.Key, childID, parentID, err)
		}
	}

	// Pass 5 — apply deferred assignees.
	for i, assignee := range pendingAssignees {
		if assignee == "" {
			continue
		}
		id := keyToID[plan.Nodes[i].Key]
		if err := u.issueRepo.Update(ctx, id, map[string]any{"assignee": assignee}, actor, IssueTableOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: defer assignee: %w", plan.Nodes[i].Key, err)
		}
	}

	return GraphApplyResult{IDs: keyToID}, nil
}

// graphParentDepPairs encodes the (childID, parentID) parent-child pairs
// implied by the plan's node ParentKey/ParentID fields. Used by applyGraph
// to dedup explicit edges against implicit parent-child relationships and
// to seed the in-memory adjacency for live cycle detection.
func graphParentDepPairs(nodes []GraphNode, keyToID map[string]string) map[string]bool {
	pairs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		parentID := n.ParentID
		if n.ParentKey != "" {
			parentID = keyToID[n.ParentKey]
		}
		childID := keyToID[n.Key]
		if childID == "" || parentID == "" {
			continue
		}
		pairs[depPairKey(childID, parentID)] = true
	}
	return pairs
}

// depPairKey encodes an ordered (issueID, dependsOnID) pair using a NUL
// separator so the two halves can be recovered unambiguously without ID
// characters colliding with the delimiter.
func depPairKey(issueID, dependsOnID string) string {
	return issueID + "\x00" + dependsOnID
}

// depPairIDs decodes a key produced by depPairKey, returning (from, to, ok).
func depPairIDs(pair string) (string, string, bool) {
	for i := 0; i < len(pair); i++ {
		if pair[i] == 0 {
			return pair[:i], pair[i+1:], true
		}
	}
	return "", "", false
}

// cycleRelevantDepType returns true for dep types whose presence in the
// reverse direction of a parent-child link would form a cycle.
func cycleRelevantDepType(t types.DependencyType) bool {
	return t == types.DepBlocks || t == types.DepConditionalBlocks
}

// readyPathDepType reports whether a dependency type affects ready-work. It is
// the broad predicate used when walking existing deps for parent→child
// blocking-path validation, in contrast to the blocking-only
// cycleRelevantDepType used for pure blocking-cycle detection. The two must
// stay distinct: narrowing the parent-path walk would miss real ready-work
// deadlocks, while broadening the blocking-cycle walk would reject edges that
// plain `bd dep add` accepts.
func readyPathDepType(t types.DependencyType) bool {
	return t.AffectsReadyWork()
}

// resolveEdgeRef returns the ID for an edge endpoint: the keyToID lookup
// when key is set, else the explicit id. Returns "" when neither resolves,
// which the caller should treat as a structural error.
func resolveEdgeRef(key, id string, keyToID map[string]string) string {
	if key != "" {
		return keyToID[key]
	}
	return id
}

// validatePlannedBlockingPaths rejects plans that would close a cycle
// once the parent-child deps are inserted. The adjacency it walks combines
// the planned parent-child pairs (child → parent), the cycle-relevant
// planned edges (excluding reverse-of-parent-child which is rejected by
// applyGraph's dedup pass), and live AffectsReadyWork dependencies pulled
// lazily from the store via depRepo. Mirrors embedded
// validateGraphApplyPlannedParentBlockingPaths.
func (u *issueUseCaseImpl) validatePlannedBlockingPaths(
	ctx context.Context,
	plan GraphPlan,
	keyToID map[string]string,
	parentDepPairs map[string]bool,
) error {
	adj := make(map[string][]string)
	for pair := range parentDepPairs {
		fromID, toID, ok := depPairIDs(pair)
		if ok {
			adj[fromID] = append(adj[fromID], toID)
		}
	}
	for _, edge := range plan.Edges {
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}
		if !readyPathDepType(depType) {
			continue
		}
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			continue
		}
		// Skip the reverse-of-parent-child case for cycle-relevant types —
		// applyGraph's edge dedup already errors on those with a clearer
		// message, so we don't want them showing up here as ambiguous
		// "blocking path" errors.
		if cycleRelevantDepType(depType) && parentDepPairs[depPairKey(toID, fromID)] {
			continue
		}
		adj[fromID] = append(adj[fromID], toID)
	}

	depCache := make(map[string][]*types.Dependency)
	for _, node := range plan.Nodes {
		parentID := node.ParentID
		if node.ParentKey != "" {
			parentID = keyToID[node.ParentKey]
		}
		childID := keyToID[node.Key]
		if childID == "" || parentID == "" {
			continue
		}
		hasPath, err := u.graphHasPath(ctx, adj, depCache, parentID, childID, readyPathDepType)
		if err != nil {
			return err
		}
		if hasPath {
			return fmt.Errorf("applyGraph: node %q: planned blocking dependencies create a path from parent %q to child %q", node.Key, parentID, childID)
		}
	}
	return nil
}

// validatePlannedBlockingCycles rejects planned blocking edges that would close
// a blocking-dependency cycle, evaluated whole-graph before any insert. It
// mirrors embedded validateGraphApplyPlannedBlockingCycles and the storage
// per-edge SQL cycle check (depRepo.HasCycle): both the planned adjacency and
// the existing-dep walk are restricted to blocks/conditional-blocks via
// cycleRelevantDepType, so graph-apply stays consistent with `bd dep add` and
// does not reject a blocking edge whose return path runs through an existing
// parent-child or waits-for dep.
func (u *issueUseCaseImpl) validatePlannedBlockingCycles(
	ctx context.Context,
	plan GraphPlan,
	keyToID map[string]string,
) error {
	type plannedEdge struct {
		index  int
		fromID string
		toID   string
	}

	adj := make(map[string][]string)
	checks := make([]plannedEdge, 0, len(plan.Edges))
	for i, edge := range plan.Edges {
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}
		if !cycleRelevantDepType(depType) {
			continue
		}
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			continue
		}
		if fromID == toID {
			return fmt.Errorf("applyGraph: edge %d %s->%s creates a blocking dependency cycle", i, fromID, toID)
		}
		adj[fromID] = append(adj[fromID], toID)
		checks = append(checks, plannedEdge{index: i, fromID: fromID, toID: toID})
	}

	depCache := make(map[string][]*types.Dependency)
	for _, edge := range checks {
		hasPath, err := u.graphHasPath(ctx, adj, depCache, edge.toID, edge.fromID, cycleRelevantDepType)
		if err != nil {
			return fmt.Errorf("applyGraph: edge %d %s->%s: checking planned blocking cycle: %w", edge.index, edge.fromID, edge.toID, err)
		}
		if hasPath {
			return fmt.Errorf("applyGraph: edge %d %s->%s creates a blocking dependency cycle", edge.index, edge.fromID, edge.toID)
		}
	}
	return nil
}

// graphHasPath returns true if fromID can reach toID by following the
// in-memory adjacency (planned parent-child + planned blocking edges) and
// existing deps loaded lazily from the store. followExistingDep selects which
// existing dep types the walk traverses, so callers can mirror either the
// blocking-only SQL cycle check or the broader ready-work graph. Per-node dep
// fetches are cached so each visited node hits the DB at most once.
//
// Existing deps are loaded from BOTH dependency tables. The per-edge
// depRepo.HasCycle probe this walk replaced traversed dependencies ∪
// wisp_dependencies (and the embedded path's GetDependencyRecords selects the
// table per node), so a blocking cycle that closes through the other table —
// e.g. an existing wisp edge reached during a regular graph-apply — must still
// be detected regardless of which table this graph-apply primarily writes.
func (u *issueUseCaseImpl) graphHasPath(
	ctx context.Context,
	adj map[string][]string,
	depCache map[string][]*types.Dependency,
	fromID, toID string,
	followExistingDep func(types.DependencyType) bool,
) (bool, error) {
	seen := make(map[string]bool)
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if id == toID {
			return true, nil
		}
		if seen[id] {
			return false, nil
		}
		seen[id] = true
		for _, next := range adj[id] {
			found, err := visit(next)
			if err != nil || found {
				return found, err
			}
		}
		deps, ok := depCache[id]
		if !ok {
			regular, err := u.depRepo.ListByIssueIDs(ctx, []string{id}, DepListOpts{
				Direction:     DepDirectionOut,
				UseWispsTable: false,
			})
			if err != nil {
				return false, fmt.Errorf("applyGraph: read existing deps for %s: %w", id, err)
			}
			wisp, err := u.depRepo.ListByIssueIDs(ctx, []string{id}, DepListOpts{
				Direction:     DepDirectionOut,
				UseWispsTable: true,
			})
			if err != nil {
				return false, fmt.Errorf("applyGraph: read existing wisp deps for %s: %w", id, err)
			}
			deps = append(regular.Outgoing[id], wisp.Outgoing[id]...)
			depCache[id] = deps
		}
		for _, dep := range deps {
			if !followExistingDep(dep.Type) {
				continue
			}
			found, err := visit(dep.DependsOnID)
			if err != nil || found {
				return found, err
			}
		}
		return false, nil
	}
	return visit(fromID)
}

// resolveTopLevelPrefix picks the prefix for a freshly-minted top-level ID,
// mirroring the embedded path's precedence (issueops/create.go:88-96 and
// dolt/wisps.go wispPrefix). Reads issue_prefix from config once and trims
// the trailing hyphen so a config value of "bd-" yields "bd-<hash>" rather
// than "bd--<hash>".
func (u *issueUseCaseImpl) resolveTopLevelPrefix(ctx context.Context, issue *types.Issue, useWisp bool) (string, error) {
	if issue.PrefixOverride != "" {
		return issue.PrefixOverride, nil
	}

	configPrefix, err := u.cfgRepo.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return "", fmt.Errorf("read issue_prefix: %w", err)
	}
	configPrefix = strings.TrimSuffix(configPrefix, "-")
	if configPrefix == "" {
		return "", fmt.Errorf("issue_prefix config is missing")
	}

	switch {
	case issue.IDPrefix != "":
		return configPrefix + "-" + issue.IDPrefix, nil
	case useWisp:
		return configPrefix + "-wisp", nil
	}
	return configPrefix, nil
}

// mintTopLevelID generates a fresh top-level ID for an issue that has no
// ExplicitID and no ParentID. Honors counter mode for non-wisps (config key
// issue_id_mode=counter); otherwise uses adaptive hash-mode IDs that mirror
// issueops.GenerateIssueIDInTable. Reads issue.CreatedAt (caller must have
// stabilized it before this call so retries hash the same value).
func (u *issueUseCaseImpl) mintTopLevelID(ctx context.Context, issue *types.Issue, actor string, useWisp bool) (string, error) {
	prefix, err := u.resolveTopLevelPrefix(ctx, issue, useWisp)
	if err != nil {
		return "", err
	}

	// Counter mode applies only to the issues table — wisps always hash-mint
	// because there is no wisp_counter table and ephemeral churn would make
	// a monotonic counter meaningless.
	if !useWisp {
		mode, err := u.cfgRepo.GetConfig(ctx, "issue_id_mode")
		if err != nil {
			return "", fmt.Errorf("read issue_id_mode: %w", err)
		}
		if mode == "counter" {
			n, err := u.issueRepo.NextCounterID(ctx, prefix)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s-%d", prefix, n), nil
		}
	}

	cfg, err := u.cfgRepo.GetAdaptiveIDConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("read adaptive id config: %w", err)
	}
	tableOpts := IssueTableOpts{UseWispsTable: useWisp}

	count, err := u.issueRepo.CountForPrefix(ctx, prefix, tableOpts)
	if err != nil {
		return "", err
	}
	baseLength := ComputeAdaptiveLength(count, cfg)
	if baseLength > cfg.MaxLength {
		baseLength = cfg.MaxLength
	}

	for length := baseLength; length <= cfg.MaxLength; length++ {
		for nonce := 0; nonce < 10; nonce++ {
			candidate := idgen.GenerateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)
			exists, err := u.issueRepo.Exists(ctx, candidate, tableOpts)
			if err != nil {
				return "", err
			}
			if !exists {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("failed to generate unique ID for prefix %q after lengths %d..%d with 10 nonces each", prefix, baseLength, cfg.MaxLength)
}

func (u *issueUseCaseImpl) CloseIssue(ctx context.Context, id string, params CloseIssueParams, actor string) (CloseIssueResult, error) {
	return u.close(ctx, id, params, actor, false)
}

func (u *issueUseCaseImpl) CloseWisp(ctx context.Context, id string, params CloseIssueParams, actor string) (CloseIssueResult, error) {
	return u.close(ctx, id, params, actor, true)
}

func (u *issueUseCaseImpl) close(ctx context.Context, id string, params CloseIssueParams, actor string, useWisp bool) (CloseIssueResult, error) {
	if id == "" {
		return CloseIssueResult{}, fmt.Errorf("close: id must not be empty")
	}
	if actor == "" {
		return CloseIssueResult{}, fmt.Errorf("close: actor must not be empty")
	}
	row, err := u.issueRepo.Close(ctx, id, CloseRowParams{Reason: params.Reason, Session: params.Session}, actor, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return CloseIssueResult{}, fmt.Errorf("close %s: %w", id, err)
	}
	issue, err := u.issueRepo.Get(ctx, id, IssueTableOpts{UseWispsTable: row.IsWisp})
	if err != nil {
		return CloseIssueResult{}, fmt.Errorf("close %s: reload: %w", id, err)
	}
	return CloseIssueResult{
		Issue:  issue,
		Closed: !row.AlreadyClosed,
	}, nil
}

func (u *issueUseCaseImpl) ReopenIssue(ctx context.Context, id string, params ReopenIssueParams, actor string) (ReopenIssueResult, error) {
	return u.reopen(ctx, id, params, actor, false)
}

func (u *issueUseCaseImpl) ReopenWisp(ctx context.Context, id string, params ReopenIssueParams, actor string) (ReopenIssueResult, error) {
	return u.reopen(ctx, id, params, actor, true)
}

func (u *issueUseCaseImpl) reopen(ctx context.Context, id string, params ReopenIssueParams, actor string, useWisp bool) (ReopenIssueResult, error) {
	if id == "" {
		return ReopenIssueResult{}, fmt.Errorf("reopen: id must not be empty")
	}
	if actor == "" {
		return ReopenIssueResult{}, fmt.Errorf("reopen: actor must not be empty")
	}
	row, err := u.issueRepo.Reopen(ctx, id, ReopenRowParams{Reason: params.Reason}, actor, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return ReopenIssueResult{}, fmt.Errorf("reopen %s: %w", id, err)
	}
	issue, err := u.issueRepo.Get(ctx, id, IssueTableOpts{UseWispsTable: row.IsWisp})
	if err != nil {
		return ReopenIssueResult{}, fmt.Errorf("reopen %s: reload: %w", id, err)
	}
	return ReopenIssueResult{
		Issue:    issue,
		Reopened: !row.AlreadyOpen,
	}, nil
}

func (u *issueUseCaseImpl) ClaimIssueIfOpen(ctx context.Context, id, actor string) (ClaimResult, error) {
	return u.claim(ctx, id, actor, false)
}

func (u *issueUseCaseImpl) ClaimWispIfOpen(ctx context.Context, id, actor string) (ClaimResult, error) {
	return u.claim(ctx, id, actor, true)
}

func (u *issueUseCaseImpl) CountOpenChildren(ctx context.Context, id string) (int, error) {
	return u.countOpenChildren(ctx, id, false)
}

func (u *issueUseCaseImpl) CountOpenWispChildren(ctx context.Context, id string) (int, error) {
	return u.countOpenChildren(ctx, id, true)
}

func (u *issueUseCaseImpl) countOpenChildren(ctx context.Context, id string, useWisp bool) (int, error) {
	if id == "" {
		return 0, fmt.Errorf("CountOpenChildren: id must not be empty")
	}
	children, err := u.depRepo.ListWithIssueMetadata(ctx, id, DepListOpts{
		Types:         []types.DependencyType{types.DepParentChild},
		Direction:     DepDirectionIn,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return 0, fmt.Errorf("CountOpenChildren %s: %w", id, err)
	}
	open := 0
	for _, child := range children {
		if child.Status != types.StatusClosed {
			open++
		}
	}
	return open, nil
}

func (u *issueUseCaseImpl) GetNewlyUnblockedByClose(ctx context.Context, closedID string) ([]*types.Issue, error) {
	return u.getNewlyUnblockedByClose(ctx, closedID)
}

func (u *issueUseCaseImpl) GetNewlyUnblockedByCloseWisp(ctx context.Context, closedID string) ([]*types.Issue, error) {
	return u.getNewlyUnblockedByClose(ctx, closedID)
}

func (u *issueUseCaseImpl) getNewlyUnblockedByClose(ctx context.Context, closedID string) ([]*types.Issue, error) {
	if closedID == "" {
		return nil, fmt.Errorf("GetNewlyUnblockedByClose: closedID must not be empty")
	}
	out, err := u.issueRepo.GetNewlyUnblockedByClose(ctx, closedID)
	if err != nil {
		return nil, fmt.Errorf("GetNewlyUnblockedByClose %s: %w", closedID, err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (ClaimReadyResult, error) {
	return u.claimReady(ctx, filter, actor, false)
}

func (u *issueUseCaseImpl) ClaimReadyWisp(ctx context.Context, filter types.WorkFilter, actor string) (ClaimReadyResult, error) {
	return u.claimReady(ctx, filter, actor, true)
}

func (u *issueUseCaseImpl) claimReady(ctx context.Context, filter types.WorkFilter, actor string, useWisp bool) (ClaimReadyResult, error) {
	var (
		issue *types.Issue
		err   error
	)
	if useWisp {
		issue, err = u.issueRepo.ClaimReadyWisp(ctx, filter, actor)
	} else {
		issue, err = u.issueRepo.ClaimReadyIssue(ctx, filter, actor)
	}
	if err != nil {
		if useWisp {
			return ClaimReadyResult{}, fmt.Errorf("ClaimReadyWisp: %w", err)
		}
		return ClaimReadyResult{}, fmt.Errorf("ClaimReadyIssue: %w", err)
	}
	return ClaimReadyResult{Issue: issue, Claimed: issue != nil}, nil
}

func (u *issueUseCaseImpl) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	out, err := u.issueRepo.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("GetBlockedIssues: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	out, err := u.issueRepo.GetStatistics(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetStatistics: %w", err)
	}
	return out, nil
}
