package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// beadsDirOverride returns true if BEADS_DIR is explicitly set in the environment.
// When set, BEADS_DIR specifies the exact database to use and prefix-based routing
// must be skipped. This matches bd list's behavior (which never routes) and the
// contract expected by all gastown callers that set BEADS_DIR (GH#663).
func beadsDirOverride() bool {
	return os.Getenv("BEADS_DIR") != ""
}

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

	// BEADS_DIR explicitly set — use local store, skip prefix routing (GH#663)
	if beadsDirOverride() {
		return resolveAndGetFromStore(ctx, localStore, id, false)
	}

	beadsDir := filepath.Dir(dbPath)
	// Use factory.NewFromConfig as the storage opener to respect backend configuration
	routedStorage, err := routing.GetRoutedStorageWithOpener(ctx, id, beadsDir, factory.NewFromConfig)
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

// openStoreForRig opens a read-only storage connection to a different rig's database.
// The rigOrPrefix parameter accepts any format: "beads", "bd-", "bd", etc.
// Returns the opened storage (caller must close) or an error.
func openStoreForRig(ctx context.Context, rigOrPrefix string) (storage.Storage, error) {
	townBeadsDir, err := findTownBeadsDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve rig: %v", err)
	}

	targetBeadsDir, _, err := routing.ResolveBeadsDirForRig(rigOrPrefix, townBeadsDir)
	if err != nil {
		return nil, err
	}

	targetStore, err := factory.NewFromConfigWithOptions(ctx, targetBeadsDir, factory.Options{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open rig %q database: %v", rigOrPrefix, err)
	}

	return targetStore, nil
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
	if dbPath == "" || beadsDirOverride() {
		// No routing without a database path, or BEADS_DIR explicitly set (GH#663)
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, err
	}

	beadsDir := filepath.Dir(dbPath)
	// Use GetRoutedStorageWithOpener with factory to respect backend configuration (bd-m2jr)
	routedStorage, routeErr := routing.GetRoutedStorageWithOpener(ctx, id, beadsDir, factory.NewFromConfig)
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
	if dbPath == "" || beadsDirOverride() {
		return nil, nil
	}

	beadsDir := filepath.Dir(dbPath)
	// Use GetRoutedStorageWithOpener with factory to respect backend configuration (bd-m2jr)
	return routing.GetRoutedStorageWithOpener(ctx, id, beadsDir, factory.NewFromConfig)
}

// needsRouting checks if an ID would be routed to a different beads directory.
// This is used to decide whether to bypass the daemon for cross-repo lookups.
func needsRouting(id string) bool {
	if dbPath == "" || beadsDirOverride() {
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

// resolveExternalDepsViaRouting resolves external dependency references by following
// prefix routes to locate and query the target database.
//
// GetDependenciesWithMetadata uses a JOIN between dependencies and issues tables,
// so external refs (e.g., "external:gastown:gt-42zaq") that don't exist in the local
// issues table are silently dropped. This function fills in those gaps by:
// 1. Getting raw dependency records
// 2. Filtering for external refs
// 3. Extracting the issue ID from each ref
// 4. Using routing to look up the issue in the target database
//
// Returns a slice of IssueWithDependencyMetadata for resolved external deps.
func resolveExternalDepsViaRouting(ctx context.Context, issueStore storage.Storage, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	// Get raw dependency records to find external refs
	deps, err := issueStore.GetDependencyRecords(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Filter for external refs
	var externalDeps []*types.Dependency
	for _, dep := range deps {
		if strings.HasPrefix(dep.DependsOnID, "external:") {
			externalDeps = append(externalDeps, dep)
		}
	}

	if len(externalDeps) == 0 {
		return nil, nil
	}

	var results []*types.IssueWithDependencyMetadata

	for _, dep := range externalDeps {
		// Parse external:project:id — the third part is the actual issue ID
		parts := strings.SplitN(dep.DependsOnID, ":", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		targetID := parts[2]

		// Use routing to resolve the target issue
		result, routeErr := resolveAndGetIssueWithRouting(ctx, store, targetID)
		if routeErr != nil || result == nil || result.Issue == nil {
			// Can't resolve — create a placeholder with the external ref as ID
			results = append(results, &types.IssueWithDependencyMetadata{
				Issue: types.Issue{
					ID:     dep.DependsOnID,
					Title:  "(unresolved external dependency)",
					Status: types.StatusOpen,
				},
				DependencyType: dep.Type,
			})
			if result != nil {
				result.Close()
			}
			continue
		}

		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *result.Issue,
			DependencyType: dep.Type,
		})
		result.Close()
	}

	return results, nil
}

// resolveBlockedByRefs takes a list of blocker IDs (which may include external refs
// like "external:gastown:gt-42zaq") and resolves them to human-readable strings.
// Local IDs pass through unchanged. External refs are resolved via routing to show
// the actual issue ID and title from the target database.
func resolveBlockedByRefs(ctx context.Context, refs []string) []string {
	resolved := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "external:") {
			resolved = append(resolved, ref)
			continue
		}
		// Parse external:project:id
		parts := strings.SplitN(ref, ":", 3)
		if len(parts) != 3 || parts[2] == "" {
			resolved = append(resolved, ref)
			continue
		}
		targetID := parts[2]
		result, err := resolveAndGetIssueWithRouting(ctx, store, targetID)
		if err != nil || result == nil || result.Issue == nil {
			// Can't resolve — show the raw issue ID from the ref
			resolved = append(resolved, targetID)
			if result != nil {
				result.Close()
			}
			continue
		}
		resolved = append(resolved, fmt.Sprintf("%s: %s", result.Issue.ID, result.Issue.Title))
		result.Close()
	}
	return resolved
}
