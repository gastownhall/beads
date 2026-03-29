package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// isNotFoundErr returns true if the error indicates the issue was not found.
// This covers both storage.ErrNotFound (from GetIssue) and the plain error
// from ResolvePartialID which doesn't wrap the sentinel.
func isNotFoundErr(err error) bool {
	if errors.Is(err, storage.ErrNotFound) {
		return true
	}
	if err != nil && strings.Contains(err.Error(), "no issue found matching") {
		return true
	}
	return false
}

// RoutedResult contains the result of a routed issue lookup
type RoutedResult struct {
	Issue      *types.Issue
	Store      storage.DoltStorage // The store that contains this issue (may be routed)
	Routed     bool                // true if the issue was found via routing
	ResolvedID string              // The resolved (full) issue ID
	closeFn    func()              // Function to close routed storage (if any)
}

// Close closes any routed storage. Safe to call if Routed is false.
func (r *RoutedResult) Close() {
	if r.closeFn != nil {
		r.closeFn()
	}
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue.
// Tries the local store first, then falls back to contributor auto-routing.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	// Try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}

// resolveAndGetFromStore resolves a partial ID and gets the issue from a specific store.
func resolveAndGetFromStore(ctx context.Context, s storage.DoltStorage, id string, routed bool) (*RoutedResult, error) {
	// First, resolve the partial ID
	resolvedID, err := utils.ResolvePartialID(ctx, s, id)
	if err != nil {
		return nil, err
	}

	// Then get the issue
	issue, err := s.GetIssue(ctx, resolvedID)
	if err != nil {
		return nil, err
	}

	return &RoutedResult{
		Issue:      issue,
		Store:      s,
		Routed:     routed,
		ResolvedID: resolvedID,
	}, nil
}

// resolveViaAutoRouting attempts to find an issue using contributor auto-routing.
// This is the fallback when the local store doesn't have the issue (GH#2345).
// Returns a RoutedResult if the issue is found in the auto-routed store.
func resolveViaAutoRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	routedStore, routed, err := openRoutedReadStore(ctx, localStore)
	if err != nil || !routed {
		return nil, fmt.Errorf("no auto-routed store available")
	}

	result, err := resolveAndGetFromStore(ctx, routedStore, id, true)
	if err != nil {
		_ = routedStore.Close()
		return nil, err
	}
	result.closeFn = func() { _ = routedStore.Close() }
	return result, nil
}

// getIssueWithRouting gets an issue by exact ID.
// Tries the local store first, then falls back to contributor auto-routing.
//
// Returns a RoutedResult containing the issue and the store to use for related queries.
// The caller MUST call result.Close() when done to release any routed storage.
func getIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	// Try local store first.
	issue, err := localStore.GetIssue(ctx, id)
	if err == nil {
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, nil
	}

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}
