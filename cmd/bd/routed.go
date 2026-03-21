package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/routing"
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

// beadsDirOverride returns true if BEADS_DIR is explicitly set in the environment.
// When set, BEADS_DIR specifies the exact database to use and prefix-based routing
// must be skipped. This matches bd list's behavior (which never routes) and the
// contract expected by all orchestrator callers that set BEADS_DIR (GH#663).
func beadsDirOverride() bool {
	return os.Getenv("BEADS_DIR") != ""
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

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue,
// using routes.jsonl for prefix-based routing if needed.
// This enables cross-repo issue lookups (e.g., `bd show gt-xyz` from ~/gt).
//
// When the ID's prefix routes to a different beads directory, the routed store
// is used directly — the local store is NOT checked first. This prevents
// mutations (close, update) from hitting phantom copies in the wrong database
// (bd-7vk). When routing does not apply (same directory or no route match),
// the local store is used as before.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	if dbPath == "" {
		// No routing without a database path - use local store
		return resolveAndGetFromStore(ctx, localStore, id, false)
	}

	// BEADS_DIR explicitly set — use local store, skip prefix routing (GH#663)
	if beadsDirOverride() {
		return resolveAndGetFromStore(ctx, localStore, id, false)
	}

	// Check if this ID routes to a different beads directory.
	beadsDir := filepath.Dir(dbPath)
	targetDir, routed, routeErr := routing.ResolveBeadsDirForID(ctx, id, beadsDir)
	// Redirects may resolve back to the same .beads directory but with a different
	// dolt_database. ResolveBeadsDirForID sets BEADS_DOLT_SERVER_DATABASE when a
	// redirect changes the database, so we check for that too.
	routesDifferently := routeErr == nil && routed && (targetDir != beadsDir || os.Getenv("BEADS_DOLT_SERVER_DATABASE") != "")

	// When routing says this ID belongs to a different database, go directly
	// to the routed store. Checking the local store first would risk finding
	// phantom/duplicate copies in the wrong database (bd-7vk).
	if routesDifferently {
		routedStore, err := newDoltStoreFromConfig(ctx, targetDir)
		if err != nil {
			return nil, fmt.Errorf("opening routed store at %s: %w", targetDir, err)
		}
		result, err := resolveAndGetFromStore(ctx, routedStore, id, true)
		if err != nil {
			_ = routedStore.Close()
			return nil, err
		}
		result.closeFn = func() { _ = routedStore.Close() }
		return result, nil
	}

	// No cross-database routing — try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	// Commands like show/update/close use this function but previously never
	// checked the auto-routed store, so issues created via contributor
	// auto-routing were invisible to them.
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
// This is the fallback when prefix-based routing and local store both fail (GH#2345).
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

// openStoreForRig opens a read-only storage connection to a different rig's database.
// The rigOrPrefix parameter accepts any format: "beads", "bd-", "bd", etc.
// Returns the opened storage (caller must close) or an error.
func openStoreForRig(ctx context.Context, rigOrPrefix string) (storage.DoltStorage, error) {
	townBeadsDir, err := findTownBeadsDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve rig: %v", err)
	}

	targetBeadsDir, _, err := routing.ResolveBeadsDirForRig(rigOrPrefix, townBeadsDir)
	if err != nil {
		return nil, err
	}

	targetStore, err := newReadOnlyStoreFromConfig(ctx, targetBeadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open rig %q database: %v", rigOrPrefix, err)
	}

	return targetStore, nil
}

// getIssueWithRouting gets an issue, using prefix-based routing when the ID
// belongs to a different beads directory. When routing applies, the routed
// store is used directly (the local store is not checked first, preventing
// phantom-copy issues — bd-7vk). When routing does not apply, the local
// store is used.
//
// Returns a RoutedResult containing the issue and the store to use for related queries.
// The caller MUST call result.Close() when done to release any routed storage.
func getIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	if dbPath == "" || beadsDirOverride() {
		// No routing without a database path, or BEADS_DIR explicitly set (GH#663)
		issue, err := localStore.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, nil
	}

	// Check if this ID routes to a different beads directory.
	beadsDir := filepath.Dir(dbPath)
	targetDir, routed, routeErr := routing.ResolveBeadsDirForID(ctx, id, beadsDir)
	// Redirects may resolve back to the same .beads directory but with a different
	// dolt_database. ResolveBeadsDirForID sets BEADS_DOLT_SERVER_DATABASE when a
	// redirect changes the database, so we check for that too.
	routesDifferently := routeErr == nil && routed && (targetDir != beadsDir || os.Getenv("BEADS_DOLT_SERVER_DATABASE") != "")

	if routesDifferently {
		routedStore, err := newDoltStoreFromConfig(ctx, targetDir)
		if err != nil {
			return nil, fmt.Errorf("opening routed store at %s: %w", targetDir, err)
		}
		routedIssue, routedErr := routedStore.GetIssue(ctx, id)
		if routedErr != nil || routedIssue == nil {
			_ = routedStore.Close()
			if routedErr != nil {
				return nil, routedErr
			}
			return nil, storage.ErrNotFound
		}
		return &RoutedResult{
			Issue:      routedIssue,
			Store:      routedStore,
			Routed:     true,
			ResolvedID: id,
			closeFn:    func() { _ = routedStore.Close() },
		}, nil
	}

	// No cross-database routing — try local store first.
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

// getRoutedStoreForID returns a storage connection for an issue ID if routing is needed.
// Returns nil if no routing is needed (issue should be in local store).
// The caller is responsible for closing the returned storage.
func getRoutedStoreForID(ctx context.Context, id string) (*routing.RoutedStorage, error) {
	if dbPath == "" || beadsDirOverride() {
		return nil, nil
	}

	beadsDir := filepath.Dir(dbPath)
	// Use GetRoutedStorageWithOpener with dolt to respect backend configuration (bd-m2jr)
	return routing.GetRoutedStorageWithOpener(ctx, id, beadsDir, newDoltStoreFromConfig)
}

// needsRouting checks if an ID would be routed to a different beads directory.
// This is used to decide whether to use direct store access for cross-repo lookups.
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
// so external refs (e.g., "external:other-project:op-42zaq") that don't exist in the local
// issues table are silently dropped. This function fills in those gaps by:
// 1. Getting raw dependency records
// 2. Filtering for external refs
// 3. Extracting the issue ID from each ref
// 4. Using routing to look up the issue in the target database
//
// Returns a slice of IssueWithDependencyMetadata for resolved external deps.
func resolveExternalDepsViaRouting(ctx context.Context, issueStore storage.DoltStorage, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
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
// like "external:other-project:op-42zaq") and resolves them to human-readable strings.
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
