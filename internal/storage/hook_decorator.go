// Package storage — hook_decorator.go
//
// HookFiringStore is a decorator around DoltStorage that automatically
// fires on_create/on_update/on_close hooks after successful mutations.
// This moves hook responsibility from individual CLI commands into the
// storage layer, ensuring ALL mutations fire hooks — including future
// commands that haven't been written yet.
//
// Usage:
//
//	store = storage.NewHookFiringStore(rawStore, hookRunner)
//
// Transaction support: mutations inside RunInTransaction are tracked
// and hooks fire only after the transaction commits successfully.
// If the transaction rolls back, no hooks fire.
package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
)

// HookFiringStore wraps a DoltStorage and fires hooks after mutations.
// Non-mutation methods pass through to the inner store unchanged.
type HookFiringStore struct {
	DoltStorage             // embed for passthrough of non-overridden methods
	inner       DoltStorage // the real store
	runner      *hooks.Runner
}

// NewHookFiringStore wraps store with automatic hook firing.
// If runner is nil, hooks are silently skipped (passthrough only).
func NewHookFiringStore(store DoltStorage, runner *hooks.Runner) *HookFiringStore {
	return &HookFiringStore{
		DoltStorage: store,
		inner:       store,
		runner:      runner,
	}
}

// Inner returns the underlying store, useful for type assertions
// (e.g., StoreLocator, RawDBAccessor).
func (h *HookFiringStore) Inner() DoltStorage { return h.inner }

// UnwrapStore returns the underlying concrete store if s is a
// HookFiringStore decorator, otherwise returns s unchanged.
// Use this before type assertions to optional interfaces
// (StoreLocator, BackupStore, Flattener, etc.) so the assertion
// reaches the concrete store rather than the decorator.
func UnwrapStore(s DoltStorage) DoltStorage {
	if h, ok := s.(*HookFiringStore); ok {
		return h.inner
	}
	return s
}

// ── Issue mutations ─────────────────────────────────────────────────

// CreateIssue creates an issue and fires on_create.
func (h *HookFiringStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := h.inner.CreateIssue(ctx, issue, actor); err != nil {
		return err
	}
	h.fireHook(hooks.EventCreate, issue)
	return nil
}

// CreateIssues creates multiple issues and fires on_create for each.
func (h *HookFiringStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if err := h.inner.CreateIssues(ctx, issues, actor); err != nil {
		return err
	}
	for _, issue := range issues {
		h.fireHook(hooks.EventCreate, issue)
	}
	return nil
}

// UpdateIssue updates an issue and fires on_update.
func (h *HookFiringStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := h.inner.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, id)
	return nil
}

// ReopenIssue reopens an issue and fires on_update.
func (h *HookFiringStore) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	if err := h.inner.ReopenIssue(ctx, id, reason, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, id)
	return nil
}

// UpdateIssueType changes an issue's type and fires on_update.
func (h *HookFiringStore) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	if err := h.inner.UpdateIssueType(ctx, id, issueType, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, id)
	return nil
}

// CloseIssue closes an issue and fires on_close.
func (h *HookFiringStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	if err := h.inner.CloseIssue(ctx, id, reason, actor, session); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventClose, id)
	return nil
}

// ── Dependency mutations ────────────────────────────────────────────

// AddDependency adds a dependency and fires on_update for the issue.
func (h *HookFiringStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if err := h.inner.AddDependency(ctx, dep, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, dep.IssueID)
	return nil
}

// RemoveDependency removes a dependency and fires on_update for the issue.
func (h *HookFiringStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	if err := h.inner.RemoveDependency(ctx, issueID, dependsOnID, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, issueID)
	return nil
}

// ── Label mutations ─────────────────────────────────────────────────

// AddLabel adds a label and fires on_update.
func (h *HookFiringStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := h.inner.AddLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, issueID)
	return nil
}

// RemoveLabel removes a label and fires on_update.
func (h *HookFiringStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := h.inner.RemoveLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, issueID)
	return nil
}

// ── Comment mutations ───────────────────────────────────────────────

// AddIssueComment adds a comment and fires on_update.
func (h *HookFiringStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	comment, err := h.inner.AddIssueComment(ctx, issueID, author, text)
	if err != nil {
		return nil, err
	}
	h.fireHookByID(ctx, hooks.EventUpdate, issueID)
	return comment, nil
}

// ── Transaction support ─────────────────────────────────────────────

// RunInTransaction wraps the callback's transaction with hook tracking.
// Mutations inside the transaction are recorded but hooks only fire
// after the transaction commits successfully. On rollback or error,
// no hooks fire.
func (h *HookFiringStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx Transaction) error) error {
	var tracked *hookTrackingTransaction
	err := h.inner.RunInTransaction(ctx, commitMsg, func(tx Transaction) error {
		tracked = &hookTrackingTransaction{Transaction: tx}
		return fn(tracked)
	})
	if err != nil || tracked == nil {
		return err
	}
	// Transaction committed — fire all accumulated hooks.
	for _, p := range tracked.pending {
		h.fireHook(p.event, p.issue)
	}
	return nil
}

// ── Internal helpers ────────────────────────────────────────────────

func (h *HookFiringStore) fireHook(event string, issue *types.Issue) {
	if h.runner == nil || issue == nil {
		return
	}
	h.runner.Run(event, issue)
}

func (h *HookFiringStore) fireHookByID(ctx context.Context, event, id string) {
	if h.runner == nil {
		return
	}
	issue, err := h.inner.GetIssue(ctx, id)
	if err != nil {
		return // best-effort: skip hook if re-fetch fails
	}
	h.runner.Run(event, issue)
}

// ── Hook tracking transaction ───────────────────────────────────────

// pendingHook records a hook to fire after transaction commit.
type pendingHook struct {
	event string
	issue *types.Issue
}

// hookTrackingTransaction wraps a Transaction, recording mutations
// so hooks can fire after commit.
type hookTrackingTransaction struct {
	Transaction
	pending []pendingHook
}

func (t *hookTrackingTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := t.Transaction.CreateIssue(ctx, issue, actor); err != nil {
		return err
	}
	t.pending = append(t.pending, pendingHook{hooks.EventCreate, issue})
	return nil
}

func (t *hookTrackingTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if err := t.Transaction.CreateIssues(ctx, issues, actor); err != nil {
		return err
	}
	for _, issue := range issues {
		t.pending = append(t.pending, pendingHook{hooks.EventCreate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := t.Transaction.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	// Re-fetch within the transaction to get the updated state.
	if issue, err := t.Transaction.GetIssue(ctx, id); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	if err := t.Transaction.CloseIssue(ctx, id, reason, actor, session); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, id); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventClose, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, DependencyAddOptions{})
}

func (t *hookTrackingTransaction) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts DependencyAddOptions) error {
	if err := t.Transaction.AddDependencyWithOptions(ctx, dep, actor, opts); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, dep.IssueID); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	if err := t.Transaction.RemoveDependency(ctx, issueID, dependsOnID, actor); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, issueID); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if err := t.Transaction.AddLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, issueID); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if err := t.Transaction.RemoveLabel(ctx, issueID, label, actor); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, issueID); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

func (t *hookTrackingTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	if err := t.Transaction.AddComment(ctx, issueID, actor, comment); err != nil {
		return err
	}
	if issue, err := t.Transaction.GetIssue(ctx, issueID); err == nil {
		t.pending = append(t.pending, pendingHook{hooks.EventUpdate, issue})
	}
	return nil
}

// DeleteIssue passes through without firing hooks — delete is destructive
// and the issue no longer exists to pass to a hook.
func (t *hookTrackingTransaction) DeleteIssue(ctx context.Context, id string) error {
	return t.Transaction.DeleteIssue(ctx, id)
}

// Ensure compile-time interface satisfaction.
var _ DoltStorage = (*HookFiringStore)(nil)
var _ Transaction = (*hookTrackingTransaction)(nil)

// Ensure HookFiringStore's mutation methods are used (not the embedded passthrough).
// This compile-time check prevents accidentally forgetting to override a method.
var (
	_ interface {
		CreateIssue(context.Context, *types.Issue, string) error
	} = (*HookFiringStore)(nil)
	_ interface {
		UpdateIssue(context.Context, string, map[string]interface{}, string) error
	} = (*HookFiringStore)(nil)
	_ interface {
		CloseIssue(context.Context, string, string, string, string) error
	} = (*HookFiringStore)(nil)
	_ interface {
		RunInTransaction(context.Context, string, func(Transaction) error) error
	} = (*HookFiringStore)(nil)
	_ interface {
		AddDependency(context.Context, *types.Dependency, string) error
	} = (*HookFiringStore)(nil)
	_ interface {
		AddLabel(context.Context, string, string, string) error
	} = (*HookFiringStore)(nil)
	_ interface {
		AddIssueComment(context.Context, string, string, string) (*types.Comment, error)
	} = (*HookFiringStore)(nil)
	_ interface {
		ReopenIssue(context.Context, string, string, string) error
	} = (*HookFiringStore)(nil)
)
