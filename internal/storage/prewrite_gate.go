// Package storage — prewrite_gate.go
//
// PreWriteGateStore is the mandatory synchronous admission layer for a
// configured workspace pre_write hook. It sits outside post-commit hooks, so a
// refusal is observed before the underlying store starts a mutation.
package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// PreWriteGate admits a named mutation. Implementations must return nil only
// when the mutation may proceed. It is intentionally small so CLI, HTTP, and
// embedded callers share the exact same storage boundary.
type PreWriteGate interface {
	BeforeWrite(ctx context.Context, operation string) error
}

// Mutation operation names are part of the hook's stable input contract.
const (
	PreWriteIssueCreate        = "issue.create"
	PreWriteIssueUpdate        = "issue.update"
	PreWriteIssueClose         = "issue.close"
	PreWriteIssueReopen        = "issue.reopen"
	PreWriteIssueClaim         = "issue.claim"
	PreWriteIssueClaimReady    = "issue.claim_ready"
	PreWriteIssueComment       = "issue.comment"
	PreWriteIssueBatchCreate   = "issue.batch_create"
	PreWriteIssueBatchClose    = "issue.batch_close"
	PreWriteDependencyAdd      = "dependency.add"
	PreWriteDependencyRemove   = "dependency.remove"
	PreWriteIssueLabel         = "issue.label"
	PreWriteIssueDelete        = "issue.delete"
	PreWriteWorkspaceConfig    = "workspace.config"
	PreWriteWorkspaceMemory    = "workspace.memory"
	PreWriteWorkspaceBootstrap = "workspace.bootstrap"
	PreWriteEventsPrune        = "events.prune"
	PreWriteTransaction        = "transaction.mutation"
)

// PreWriteGateStore wraps a DoltStorage with synchronous pre-write admission.
// A nil gate deliberately behaves as a no-op for embedders that do not
// configure one; the CLI always supplies a hook-backed gate.
type PreWriteGateStore struct {
	DoltStorage
	inner DoltStorage
	gate  PreWriteGate
}

// NewPreWriteGateStore creates a storage boundary that checks gate before each
// supported mutation path.
func NewPreWriteGateStore(store DoltStorage, gate PreWriteGate) *PreWriteGateStore {
	return &PreWriteGateStore{DoltStorage: store, inner: store, gate: gate}
}

// Unwrap returns the store beneath this decorator.
func (p *PreWriteGateStore) Unwrap() DoltStorage { return p.inner }

func (p *PreWriteGateStore) before(ctx context.Context, operation string) error {
	if p.gate == nil {
		return nil
	}
	return p.gate.BeforeWrite(ctx, operation)
}

func (p *PreWriteGateStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := p.before(ctx, PreWriteIssueCreate); err != nil {
		return err
	}
	return p.inner.CreateIssue(ctx, issue, actor)
}

func (p *PreWriteGateStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if err := p.before(ctx, PreWriteIssueBatchCreate); err != nil {
		return err
	}
	return p.inner.CreateIssues(ctx, issues, actor)
}

func (p *PreWriteGateStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := p.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.inner.UpdateIssue(ctx, id, updates, actor)
}

func (p *PreWriteGateStore) UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts UpdateIssueOptions) error {
	if err := p.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.inner.UpdateIssueChecked(ctx, id, updates, actor, opts)
}

func (p *PreWriteGateStore) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	if err := p.before(ctx, PreWriteIssueReopen); err != nil {
		return err
	}
	return p.inner.ReopenIssue(ctx, id, reason, actor)
}

func (p *PreWriteGateStore) UnclaimIssue(ctx context.Context, id string, actor string, force bool) error {
	if err := p.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.inner.UnclaimIssue(ctx, id, actor, force)
}

func (p *PreWriteGateStore) UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error {
	if err := p.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.inner.UnclaimIssueIfAssignee(ctx, id, actor, expectedAssignee)
}

func (p *PreWriteGateStore) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	if err := p.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.inner.UpdateIssueType(ctx, id, issueType, actor)
}

func (p *PreWriteGateStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	if err := p.before(ctx, PreWriteIssueClose); err != nil {
		return err
	}
	return p.inner.CloseIssue(ctx, id, reason, actor, session)
}

func (p *PreWriteGateStore) CloseIssueChecked(ctx context.Context, id string, actor string, opts CloseIssueOptions) (CloseIssueResult, error) {
	if err := p.before(ctx, PreWriteIssueClose); err != nil {
		return CloseIssueResult{}, err
	}
	return p.inner.CloseIssueChecked(ctx, id, actor, opts)
}

func (p *PreWriteGateStore) DeleteIssue(ctx context.Context, id string) error {
	if err := p.before(ctx, PreWriteIssueDelete); err != nil {
		return err
	}
	return p.inner.DeleteIssue(ctx, id)
}

func (p *PreWriteGateStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if err := p.before(ctx, PreWriteDependencyAdd); err != nil {
		return err
	}
	return p.inner.AddDependency(ctx, dep, actor)
}

func (p *PreWriteGateStore) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts DependencyAddOptions) error {
	if err := p.before(ctx, PreWriteDependencyAdd); err != nil {
		return err
	}
	return p.inner.AddDependencyWithOptions(ctx, dep, actor, opts)
}

func (p *PreWriteGateStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	if err := p.before(ctx, PreWriteDependencyRemove); err != nil {
		return err
	}
	return p.inner.RemoveDependency(ctx, issueID, dependsOnID, actor)
}

func (p *PreWriteGateStore) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts DependencyRemoveOptions) error {
	if err := p.before(ctx, PreWriteDependencyRemove); err != nil {
		return err
	}
	return p.inner.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, opts)
}

func (p *PreWriteGateStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := p.before(ctx, PreWriteIssueLabel); err != nil {
		return err
	}
	return p.inner.AddLabel(ctx, issueID, label, actor)
}

func (p *PreWriteGateStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := p.before(ctx, PreWriteIssueLabel); err != nil {
		return err
	}
	return p.inner.RemoveLabel(ctx, issueID, label, actor)
}

func (p *PreWriteGateStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	if err := p.before(ctx, PreWriteIssueComment); err != nil {
		return nil, err
	}
	return p.inner.AddIssueComment(ctx, issueID, author, text)
}

func (p *PreWriteGateStore) SetConfig(ctx context.Context, key, value string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.SetConfig(ctx, key, value)
}

func (p *PreWriteGateStore) SetLocalMetadata(ctx context.Context, key, value string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.SetLocalMetadata(ctx, key, value)
}

func (p *PreWriteGateStore) MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error) {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return nil, err
	}
	return p.inner.MergeSlotCreate(ctx, actor)
}

func (p *PreWriteGateStore) MergeSlotAcquire(ctx context.Context, holder, actor string, wait bool) (*MergeSlotResult, error) {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return nil, err
	}
	return p.inner.MergeSlotAcquire(ctx, holder, actor, wait)
}

func (p *PreWriteGateStore) MergeSlotRelease(ctx context.Context, holder, actor string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.MergeSlotRelease(ctx, holder, actor)
}

func (p *PreWriteGateStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.SlotSet(ctx, issueID, key, value, actor)
}

func (p *PreWriteGateStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.SlotClear(ctx, issueID, key, actor)
}

func (p *PreWriteGateStore) MergeMetadata(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error {
	if err := p.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.inner.MergeMetadata(ctx, issueID, key, value, actor)
}

// RunInTransaction checks each write on the transaction facade. A later
// denial returns from the callback and lets the underlying transaction roll
// back every earlier write, so a denied multi-step request never commits a
// partial mutation.
func (p *PreWriteGateStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error {
	return p.inner.RunInTransaction(ctx, commitMsg, func(tx Transaction) error {
		return fn(&preWriteTransaction{Transaction: tx, parent: p})
	})
}

func (p *PreWriteGateStore) RunInIssueLifecycleTransaction(ctx context.Context, commitMsg string, fn func(tx IssueLifecycleTransaction) error) error {
	return p.inner.RunInIssueLifecycleTransaction(ctx, commitMsg, func(tx IssueLifecycleTransaction) error {
		return fn(&preWriteLifecycleTransaction{
			preWriteTransaction: &preWriteTransaction{Transaction: tx, parent: p},
			inner:               tx,
		})
	})
}

// IssueLifecycle, IssueClaimer, ReadyClaimer, BatchCreator, BatchCloser,
// DependencyEditor, and Commenter cover the typed API front doors. Each is
// rebuilt on this decorator so an accessor cannot silently bypass admission.
func (p *PreWriteGateStore) IssueLifecycle() (issueops.Lifecycle, error) {
	inner, err := p.inner.IssueLifecycle()
	if err != nil {
		return nil, err
	}
	return &preWriteLifecycle{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) IssueClaimer() (issueops.Claimer, error) {
	inner, err := p.inner.IssueClaimer()
	if err != nil {
		return nil, err
	}
	return &preWriteClaimer{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	inner, err := p.inner.ReadyClaimer()
	if err != nil {
		return nil, err
	}
	return &preWriteReadyClaimer{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) BatchCreator() (issueops.BatchCreator, error) {
	inner, err := p.inner.BatchCreator()
	if err != nil {
		return nil, err
	}
	return &preWriteBatchCreator{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) BatchCloser() (issueops.BatchCloser, error) {
	inner, err := p.inner.BatchCloser()
	if err != nil {
		return nil, err
	}
	return &preWriteBatchCloser{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) DependencyEditor() (issueops.DependencyEditor, error) {
	inner, err := p.inner.DependencyEditor()
	if err != nil {
		return nil, err
	}
	return &preWriteDependencyEditor{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) Commenter() (issueops.Commenter, error) {
	inner, err := p.inner.Commenter()
	if err != nil {
		return nil, err
	}
	return &preWriteCommenter{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	inner, err := p.inner.WorkspaceConfig()
	if err != nil {
		return nil, err
	}
	return &preWriteWorkspaceConfig{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) Memories() (memoryops.Memories, error) {
	inner, err := p.inner.Memories()
	if err != nil {
		return nil, err
	}
	return &preWriteMemories{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) Deleter() (issueops.Deleter, error) {
	inner, err := p.inner.Deleter()
	if err != nil {
		return nil, err
	}
	return &preWriteDeleter{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) Sweeper() (issueops.Sweeper, error) {
	inner, err := p.inner.Sweeper()
	if err != nil {
		return nil, err
	}
	return &preWriteSweeper{inner: inner, parent: p}, nil
}

func (p *PreWriteGateStore) Bootstrapper() (issueops.Bootstrapper, error) {
	inner, err := p.inner.Bootstrapper()
	if err != nil {
		return nil, err
	}
	return &preWriteBootstrapper{inner: inner, parent: p}, nil
}

type preWriteLifecycle struct {
	inner  issueops.Lifecycle
	parent *PreWriteGateStore
}

func (p *preWriteLifecycle) Create(ctx context.Context, req issueops.CreateRequest) (issueops.CreateResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueCreate); err != nil {
		return issueops.CreateResult{}, err
	}
	return p.inner.Create(ctx, req)
}

func (p *preWriteLifecycle) Update(ctx context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueUpdate); err != nil {
		return issueops.UpdateResult{}, err
	}
	return p.inner.Update(ctx, req)
}

func (p *preWriteLifecycle) Close(ctx context.Context, req issueops.CloseRequest) (issueops.CloseResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueClose); err != nil {
		return issueops.CloseResult{}, err
	}
	return p.inner.Close(ctx, req)
}

func (p *preWriteLifecycle) Reopen(ctx context.Context, req issueops.ReopenRequest) (issueops.ReopenResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueReopen); err != nil {
		return issueops.ReopenResult{}, err
	}
	return p.inner.Reopen(ctx, req)
}

type preWriteClaimer struct {
	inner  issueops.Claimer
	parent *PreWriteGateStore
}

func (p *preWriteClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueClaim); err != nil {
		return issueops.ClaimResult{}, err
	}
	return p.inner.Claim(ctx, req)
}

type preWriteReadyClaimer struct {
	inner  issueops.ReadyClaimer
	parent *PreWriteGateStore
}

func (p *preWriteReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueClaimReady); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	return p.inner.ClaimNext(ctx, req)
}

type preWriteBatchCreator struct {
	inner  issueops.BatchCreator
	parent *PreWriteGateStore
}

func (p *preWriteBatchCreator) CreateBatch(ctx context.Context, req issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueBatchCreate); err != nil {
		return issueops.CreateBatchResult{}, err
	}
	return p.inner.CreateBatch(ctx, req)
}

type preWriteBatchCloser struct {
	inner  issueops.BatchCloser
	parent *PreWriteGateStore
}

func (p *preWriteBatchCloser) CloseBatch(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueBatchClose); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return p.inner.CloseBatch(ctx, req)
}

type preWriteDependencyEditor struct {
	inner  issueops.DependencyEditor
	parent *PreWriteGateStore
}

func (p *preWriteDependencyEditor) AddDependencies(ctx context.Context, req issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	if err := p.parent.before(ctx, PreWriteDependencyAdd); err != nil {
		return issueops.AddDependenciesResult{}, err
	}
	return p.inner.AddDependencies(ctx, req)
}

func (p *preWriteDependencyEditor) RemoveDependency(ctx context.Context, req issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	if err := p.parent.before(ctx, PreWriteDependencyRemove); err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	return p.inner.RemoveDependency(ctx, req)
}

type preWriteCommenter struct {
	inner  issueops.Commenter
	parent *PreWriteGateStore
}

func (p *preWriteCommenter) AddComment(ctx context.Context, req issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueComment); err != nil {
		return issueops.AddCommentResult{}, err
	}
	return p.inner.AddComment(ctx, req)
}

type preWriteWorkspaceConfig struct {
	inner  issueops.WorkspaceConfig
	parent *PreWriteGateStore
}

func (p *preWriteWorkspaceConfig) GetSetting(ctx context.Context, req issueops.GetSettingRequest) (issueops.SettingResult, error) {
	return p.inner.GetSetting(ctx, req)
}

func (p *preWriteWorkspaceConfig) ListSettings(ctx context.Context, req issueops.ListSettingsRequest) (issueops.ListSettingsResult, error) {
	return p.inner.ListSettings(ctx, req)
}

func (p *preWriteWorkspaceConfig) SetSetting(ctx context.Context, req issueops.SetSettingRequest) (issueops.SetSettingResult, error) {
	if err := p.parent.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return issueops.SetSettingResult{}, err
	}
	return p.inner.SetSetting(ctx, req)
}

func (p *preWriteWorkspaceConfig) UnsetSetting(ctx context.Context, req issueops.UnsetSettingRequest) (issueops.UnsetSettingResult, error) {
	if err := p.parent.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return issueops.UnsetSettingResult{}, err
	}
	return p.inner.UnsetSetting(ctx, req)
}

type preWriteMemories struct {
	inner  memoryops.Memories
	parent *PreWriteGateStore
}

func (p *preWriteMemories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	if err := p.parent.before(ctx, PreWriteWorkspaceMemory); err != nil {
		return memoryops.RememberResult{}, err
	}
	return p.inner.Remember(ctx, req)
}

func (p *preWriteMemories) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	return p.inner.Recall(ctx, req)
}

func (p *preWriteMemories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	if err := p.parent.before(ctx, PreWriteWorkspaceMemory); err != nil {
		return memoryops.ForgetResult{}, err
	}
	return p.inner.Forget(ctx, req)
}

func (p *preWriteMemories) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	return p.inner.List(ctx, req)
}

type preWriteDeleter struct {
	inner  issueops.Deleter
	parent *PreWriteGateStore
}

func (p *preWriteDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if err := p.parent.before(ctx, PreWriteIssueDelete); err != nil {
		return issueops.DeleteResult{}, err
	}
	return p.inner.Delete(ctx, req)
}

type preWriteSweeper struct {
	inner  issueops.Sweeper
	parent *PreWriteGateStore
}

func (p *preWriteSweeper) Sweep(ctx context.Context, req issueops.SweepRequest) (issueops.SweepResult, error) {
	if req.DryRun {
		return p.inner.Sweep(ctx, req)
	}
	if err := p.parent.before(ctx, PreWriteIssueDelete); err != nil {
		return issueops.SweepResult{}, err
	}
	return p.inner.Sweep(ctx, req)
}

type preWriteBootstrapper struct {
	inner  issueops.Bootstrapper
	parent *PreWriteGateStore
}

func (p *preWriteBootstrapper) Bootstrap(ctx context.Context, req issueops.BootstrapRequest) (issueops.BootstrapResult, error) {
	if err := p.parent.before(ctx, PreWriteWorkspaceBootstrap); err != nil {
		return issueops.BootstrapResult{}, err
	}
	return p.inner.Bootstrap(ctx, req)
}

type preWriteTransaction struct {
	Transaction
	parent *PreWriteGateStore
}

func (p *preWriteTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := p.parent.before(ctx, PreWriteIssueCreate); err != nil {
		return err
	}
	return p.Transaction.CreateIssue(ctx, issue, actor)
}

func (p *preWriteTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if err := p.parent.before(ctx, PreWriteIssueBatchCreate); err != nil {
		return err
	}
	return p.Transaction.CreateIssues(ctx, issues, actor)
}

func (p *preWriteTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := p.parent.before(ctx, PreWriteIssueUpdate); err != nil {
		return err
	}
	return p.Transaction.UpdateIssue(ctx, id, updates, actor)
}

func (p *preWriteTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	if err := p.parent.before(ctx, PreWriteIssueClose); err != nil {
		return err
	}
	return p.Transaction.CloseIssue(ctx, id, reason, actor, session)
}

func (p *preWriteTransaction) DeleteIssue(ctx context.Context, id string) error {
	if err := p.parent.before(ctx, PreWriteIssueDelete); err != nil {
		return err
	}
	return p.Transaction.DeleteIssue(ctx, id)
}

func (p *preWriteTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if err := p.parent.before(ctx, PreWriteDependencyAdd); err != nil {
		return err
	}
	return p.Transaction.AddDependency(ctx, dep, actor)
}

func (p *preWriteTransaction) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts DependencyAddOptions) error {
	if err := p.parent.before(ctx, PreWriteDependencyAdd); err != nil {
		return err
	}
	return p.Transaction.AddDependencyWithOptions(ctx, dep, actor, opts)
}

func (p *preWriteTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	if err := p.parent.before(ctx, PreWriteDependencyRemove); err != nil {
		return err
	}
	return p.Transaction.RemoveDependency(ctx, issueID, dependsOnID, actor)
}

func (p *preWriteTransaction) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts DependencyRemoveOptions) error {
	if err := p.parent.before(ctx, PreWriteDependencyRemove); err != nil {
		return err
	}
	return p.Transaction.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, opts)
}

func (p *preWriteTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := p.parent.before(ctx, PreWriteIssueLabel); err != nil {
		return err
	}
	return p.Transaction.AddLabel(ctx, issueID, label, actor)
}

func (p *preWriteTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := p.parent.before(ctx, PreWriteIssueLabel); err != nil {
		return err
	}
	return p.Transaction.RemoveLabel(ctx, issueID, label, actor)
}

func (p *preWriteTransaction) SetConfig(ctx context.Context, key, value string) error {
	if err := p.parent.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.Transaction.SetConfig(ctx, key, value)
}

func (p *preWriteTransaction) SetMetadata(ctx context.Context, key, value string) error {
	if err := p.parent.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.Transaction.SetMetadata(ctx, key, value)
}

func (p *preWriteTransaction) SetLocalMetadata(ctx context.Context, key, value string) error {
	if err := p.parent.before(ctx, PreWriteWorkspaceConfig); err != nil {
		return err
	}
	return p.Transaction.SetLocalMetadata(ctx, key, value)
}

func (p *preWriteTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	if err := p.parent.before(ctx, PreWriteIssueComment); err != nil {
		return err
	}
	return p.Transaction.AddComment(ctx, issueID, actor, comment)
}

func (p *preWriteTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	if err := p.parent.before(ctx, PreWriteIssueComment); err != nil {
		return nil, err
	}
	return p.Transaction.ImportIssueComment(ctx, issueID, author, text, createdAt)
}

type preWriteLifecycleTransaction struct {
	*preWriteTransaction
	inner IssueLifecycleTransaction
}

func (p *preWriteLifecycleTransaction) ReopenIssueWithResult(ctx context.Context, id string, reason string, actor string) (bool, error) {
	if err := p.parent.before(ctx, PreWriteIssueReopen); err != nil {
		return false, err
	}
	return p.inner.ReopenIssueWithResult(ctx, id, reason, actor)
}
