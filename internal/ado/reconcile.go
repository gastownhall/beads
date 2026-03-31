package ado

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/steveyegge/beads/internal/storage"
)

const (
	// DefaultReconcileInterval is how many syncs between reconciliation scans.
	DefaultReconcileInterval = 10

	// configReconcileInterval is the config key for reconcile interval.
	configReconcileInterval = "ado.reconcile_interval"

	// configSyncsSinceReconcile tracks syncs since last reconciliation.
	configSyncsSinceReconcile = "ado.syncs_since_reconcile"
)

// ReconcileResult holds the outcome of a reconciliation scan.
type ReconcileResult struct {
	Checked int      // Total work items checked
	Deleted []string // Work item IDs confirmed deleted (404)
	Denied  []string // Work item IDs with permission denied (403)
	Errors  []error  // Non-fatal errors encountered
}

// Reconciler detects deleted or inaccessible ADO work items.
type Reconciler struct {
	Client *Client
	Store  storage.Storage
}

// NewReconciler creates a new Reconciler.
func NewReconciler(client *Client, store storage.Storage) *Reconciler {
	return &Reconciler{Client: client, Store: store}
}

// ShouldReconcile checks whether a reconciliation scan is due based on
// the sync counter and configured interval. Returns true if the counter
// has reached the interval threshold.
func (r *Reconciler) ShouldReconcile(ctx context.Context) bool {
	interval := r.getInterval(ctx)
	counter := r.getCounter(ctx)
	return counter >= interval
}

// IncrementCounter increments the sync counter. Call after each successful sync.
func (r *Reconciler) IncrementCounter(ctx context.Context) error {
	counter := r.getCounter(ctx)
	return r.Store.SetConfig(ctx, configSyncsSinceReconcile, strconv.Itoa(counter+1))
}

// ResetCounter resets the counter to 0 after a reconciliation scan.
func (r *Reconciler) ResetCounter(ctx context.Context) error {
	return r.Store.SetConfig(ctx, configSyncsSinceReconcile, "0")
}

// Reconcile performs a reconciliation scan against the given ADO work item IDs.
// It batch-fetches them from ADO and identifies which ones are deleted (404)
// or inaccessible (403).
//
// The caller is responsible for collecting the relevant work item IDs from
// local storage (issues with ADO external refs) and for acting on the results
// (closing deleted issues, etc.).
func (r *Reconciler) Reconcile(ctx context.Context, workItemIDs []int) (*ReconcileResult, error) {
	result := &ReconcileResult{}
	if len(workItemIDs) == 0 {
		return result, nil
	}

	result.Checked = len(workItemIDs)

	for start := 0; start < len(workItemIDs); start += MaxBatchSize {
		end := start + MaxBatchSize
		if end > len(workItemIDs) {
			end = len(workItemIDs)
		}
		batch := workItemIDs[start:end]

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		items, err := r.Client.FetchWorkItems(ctx, batch)
		if err != nil {
			// Batch failed — check each item individually to determine status.
			for _, id := range batch {
				r.checkSingleItem(ctx, id, result)
			}
			continue
		}

		// Build set of returned IDs.
		returned := make(map[int]struct{}, len(items))
		for i := range items {
			returned[items[i].ID] = struct{}{}
		}

		// Any ID not in the response needs individual verification.
		for _, id := range batch {
			if _, ok := returned[id]; !ok {
				r.checkSingleItem(ctx, id, result)
			}
		}
	}

	return result, nil
}

// checkSingleItem fetches a single work item and categorizes the result.
func (r *Reconciler) checkSingleItem(ctx context.Context, id int, result *ReconcileResult) {
	_, err := r.Client.FetchWorkItems(ctx, []int{id})
	if err == nil {
		return // Item exists and is accessible
	}

	idStr := strconv.Itoa(id)

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound:
			result.Deleted = append(result.Deleted, idStr)
			return
		case http.StatusForbidden:
			result.Denied = append(result.Denied, idStr)
			return
		}
	}
	result.Errors = append(result.Errors, fmt.Errorf("work item %d: %w", id, err))
}

func (r *Reconciler) getInterval(ctx context.Context) int {
	val, err := r.Store.GetConfig(ctx, configReconcileInterval)
	if err != nil || val == "" {
		return DefaultReconcileInterval
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return DefaultReconcileInterval
	}
	return n
}

func (r *Reconciler) getCounter(ctx context.Context) int {
	val, err := r.Store.GetConfig(ctx, configSyncsSinceReconcile)
	if err != nil || val == "" {
		return 0
	}
	n, _ := strconv.Atoi(val)
	return n
}
