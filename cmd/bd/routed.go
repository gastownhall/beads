package main

import (
	"context"
	"path/filepath"

	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// RoutedResult contains the result of a routed issue lookup
type RoutedResult struct {
	Issue      *types.Issue
	Store      storage.Storage // The store that contains this issue (may be routed)
	Routed     bool            // true if the issue was found via routing
	ResolvedID string          // The resolved (full) issue ID
	closeFn    func()          // Function to close routed storage (if any)
}

// Close closes any routed storage. Safe to call if Routed is false.
func (r *RoutedResult) Close() {
	if r.closeFn != nil {
		r.closeFn()
	}
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue,
// using routes.jsonl for prefix-based routing if needed.
// This enables cross-repo issue lookups (e.g., `bd show gt-xyz` from ~/gt).
//
// The resolution happens in the correct store based on the ID prefix.
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.Storage, id string) (*RoutedResult, error) {
	// Step 1: Check if routing is needed based on ID prefix
	if dbPath == "" {
		// No routing without a database path - use local store
		return resolveAndGetFromStore(ctx, localStore, id, false)
	}

	beadsDir := filepath.Dir(dbPath)
	routedStorage, err := routing.GetRoutedStorageForID(ctx, id, beadsDir)
	if err != nil {
		return nil, err
	}

	if routedStorage != nil {
		// Step 2: Resolve and get from routed store
		result, err := resolveAndGetFromStore(ctx, routedStorage.Storage, id, true)
		if err != nil {
			_ = routedStorage.Close()
			return nil, err
		}
		if result != nil {
			result.closeFn = func() { _ = routedStorage.Close() }
			return result, nil
		}
		_ = routedStorage.Close()
	}

	// Step 3: Fall back to local store
	return resolveAndGetFromStore(ctx, localStore, id, false)
}

// resolveAndGetFromStore resolves a partial ID and gets the issue from a specific store.
func resolveAndGetFromStore(ctx context.Context, s storage.Storage, id string, routed bool) (*RoutedResult, error) {
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
	if issue == nil {
		return nil, nil
	}

	return &RoutedResult{
		Issue:      issue,
		Store:      s,
		Routed:     routed,
		ResolvedID: resolvedID,
	}, nil
}

// getIssueWithRouting tries to get an issue from the local store first,
// then falls back to checking routes.jsonl for prefix-based routing.
// This enables cross-repo issue lookups (e.g., `bd show gt-xyz` from ~/gt).
//
// Returns a RoutedResult containing the issue and the store to use for related queries.
// The caller MUST call result.Close() when done to release any routed storage.
func getIssueWithRouting(ctx context.Context, localStore storage.Storage, id string) (*RoutedResult, error) {
	// Step 1: Try local store first (current behavior)
	issue, err := localStore.GetIssue(ctx, id)
	if err == nil && issue != nil {
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, nil
	}

	// Step 2: Check routes.jsonl for prefix-based routing
	if dbPath == "" {
		// No routing without a database path - return original result
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, err
	}

	beadsDir := filepath.Dir(dbPath)
	routedStorage, routeErr := routing.GetRoutedStorageForID(ctx, id, beadsDir)
	if routeErr != nil || routedStorage == nil {
		// No routing found or error - return original result
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, err
	}

	// Step 3: Try the routed storage
	routedIssue, routedErr := routedStorage.Storage.GetIssue(ctx, id)
	if routedErr != nil || routedIssue == nil {
		_ = routedStorage.Close()
		// Return the original error if routing also failed
		if err != nil {
			return nil, err
		}
		return nil, routedErr
	}

	// Return the issue with the routed store
	return &RoutedResult{
		Issue:      routedIssue,
		Store:      routedStorage.Storage,
		Routed:     true,
		ResolvedID: id,
		closeFn: func() {
			_ = routedStorage.Close()
		},
	}, nil
}

// getRoutedStoreForID returns a storage connection for an issue ID if routing is needed.
// Returns nil if no routing is needed (issue should be in local store).
// The caller is responsible for closing the returned storage.
func getRoutedStoreForID(ctx context.Context, id string) (*routing.RoutedStorage, error) {
	if dbPath == "" {
		return nil, nil
	}

	beadsDir := filepath.Dir(dbPath)
	return routing.GetRoutedStorageForID(ctx, id, beadsDir)
}

// needsRouting checks if an ID would be routed to a different beads directory.
// This is used to decide whether to bypass the daemon for cross-repo lookups.
func needsRouting(id string) bool {
	if dbPath == "" {
		return false
	}

	beadsDir := filepath.Dir(dbPath)
	targetDir, routed, err := routing.ResolveBeadsDirForID(context.Background(), id, beadsDir)
	if err != nil || !routed {
		return false
	}

	// Check if the routed directory is different from the current one
	return targetDir != beadsDir
}
