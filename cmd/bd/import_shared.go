package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// ImportOptions configures how the import behaves
type ImportOptions struct {
	ResolveCollisions bool // Auto-resolve collisions by remapping to new IDs
	DryRun            bool // Preview changes without applying them
	SkipUpdate        bool // Skip updating existing issues (create-only mode)
	Strict            bool // Fail on any error (dependencies, labels, etc.)
}

// ImportResult contains statistics about the import operation
type ImportResult struct {
	Created      int               // New issues created
	Updated      int               // Existing issues updated
	Skipped      int               // Issues skipped (duplicates, errors)
	Collisions   int               // Collisions detected
	IDMapping    map[string]string // Mapping of remapped IDs (old -> new)
	CollisionIDs []string          // IDs that collided
}

// importIssuesCore handles the core import logic used by both manual and auto-import.
// This function:
// - Opens a direct SQLite connection if needed (daemon mode)
// - Detects and handles collisions
// - Imports issues, dependencies, and labels
// - Returns detailed results
//
// The caller is responsible for:
// - Reading and parsing JSONL into issues slice
// - Displaying results to the user
// - Setting metadata (e.g., last_import_hash)
func importIssuesCore(ctx context.Context, dbPath string, store storage.Storage, issues []*types.Issue, opts ImportOptions) (*ImportResult, error) {
	result := &ImportResult{
		IDMapping: make(map[string]string),
	}

	// Phase 1: Get or create SQLite store
	// Import needs direct SQLite access for collision detection
	var sqliteStore *sqlite.SQLiteStorage
	var needCloseStore bool

	if store != nil {
		// Direct mode - try to use existing store
		var ok bool
		sqliteStore, ok = store.(*sqlite.SQLiteStorage)
		if !ok {
			return nil, fmt.Errorf("collision detection requires SQLite storage backend")
		}
	} else {
		// Daemon mode - open direct connection for import
		if dbPath == "" {
			return nil, fmt.Errorf("database path not set")
		}
		var err error
		sqliteStore, err = sqlite.New(dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
		needCloseStore = true
		defer func() {
			if needCloseStore {
				sqliteStore.Close()
			}
		}()
	}

	// Phase 2: Detect collisions
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, issues)
	if err != nil {
		return nil, fmt.Errorf("collision detection failed: %w", err)
	}

	result.Collisions = len(collisionResult.Collisions)
	for _, collision := range collisionResult.Collisions {
		result.CollisionIDs = append(result.CollisionIDs, collision.ID)
	}

	// Phase 3: Handle collisions
	if len(collisionResult.Collisions) > 0 {
		if opts.DryRun {
			// In dry-run mode, just return collision info
			return result, nil
		}

		if !opts.ResolveCollisions {
			// Default behavior: fail on collision
			return result, fmt.Errorf("collision detected for issues: %v (use ResolveCollisions to auto-resolve)", result.CollisionIDs)
		}

		// Resolve collisions by scoring and remapping
		allExistingIssues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return nil, fmt.Errorf("failed to get existing issues for collision resolution: %w", err)
		}

		// Score collisions
		if err := sqlite.ScoreCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues); err != nil {
			return nil, fmt.Errorf("failed to score collisions: %w", err)
		}

		// Remap collisions
		idMapping, err := sqlite.RemapCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues)
		if err != nil {
			return nil, fmt.Errorf("failed to remap collisions: %w", err)
		}

		result.IDMapping = idMapping
		result.Created = len(collisionResult.Collisions)

		// Remove colliding issues from the list (they're already processed)
		filteredIssues := make([]*types.Issue, 0)
		collidingIDs := make(map[string]bool)
		for _, collision := range collisionResult.Collisions {
			collidingIDs[collision.ID] = true
		}
		for _, issue := range issues {
			if !collidingIDs[issue.ID] {
				filteredIssues = append(filteredIssues, issue)
			}
		}
		issues = filteredIssues
	} else if opts.DryRun {
		// No collisions in dry-run mode
		result.Created = len(collisionResult.NewIssues)
		result.Updated = len(collisionResult.ExactMatches)
		return result, nil
	}

	// Phase 4: Import remaining issues (exact matches and new issues)
	var newIssues []*types.Issue
	seenNew := make(map[string]int) // Track duplicates within import batch

	for _, issue := range issues {
		// Check if issue exists in DB
		existing, err := sqliteStore.GetIssue(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("error checking issue %s: %w", issue.ID, err)
		}

		if existing != nil {
			// Issue exists - update it unless SkipUpdate is set
			if opts.SkipUpdate {
				result.Skipped++
				continue
			}

			// Build updates map
			updates := make(map[string]interface{})
			updates["title"] = issue.Title
			updates["description"] = issue.Description
			updates["status"] = issue.Status
			updates["priority"] = issue.Priority
			updates["issue_type"] = issue.IssueType
			updates["design"] = issue.Design
			updates["acceptance_criteria"] = issue.AcceptanceCriteria
			updates["notes"] = issue.Notes

			if issue.Assignee != "" {
				updates["assignee"] = issue.Assignee
			} else {
				updates["assignee"] = nil
			}

			if issue.ExternalRef != nil && *issue.ExternalRef != "" {
				updates["external_ref"] = *issue.ExternalRef
			} else {
				updates["external_ref"] = nil
			}

			if err := sqliteStore.UpdateIssue(ctx, issue.ID, updates, "import"); err != nil {
				return nil, fmt.Errorf("error updating issue %s: %w", issue.ID, err)
			}
			result.Updated++
		} else {
			// New issue - check for duplicates in import batch
			if idx, seen := seenNew[issue.ID]; seen {
				if opts.Strict {
					return nil, fmt.Errorf("duplicate issue ID %s in import (line %d)", issue.ID, idx)
				}
				result.Skipped++
				continue
			}
			seenNew[issue.ID] = len(newIssues)
			newIssues = append(newIssues, issue)
		}
	}

	// Batch create all new issues
	if len(newIssues) > 0 {
		if err := sqliteStore.CreateIssues(ctx, newIssues, "import"); err != nil {
			return nil, fmt.Errorf("error creating issues: %w", err)
		}
		result.Created += len(newIssues)
	}

	// Sync counters after batch import
	if err := sqliteStore.SyncAllCounters(ctx); err != nil {
		return nil, fmt.Errorf("error syncing counters: %w", err)
	}

	// Phase 5: Import dependencies
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}

		for _, dep := range issue.Dependencies {
			// Check if dependency already exists
			existingDeps, err := sqliteStore.GetDependencyRecords(ctx, dep.IssueID)
			if err != nil {
				return nil, fmt.Errorf("error checking dependencies for %s: %w", dep.IssueID, err)
			}

			// Check for duplicate
			isDuplicate := false
			for _, existing := range existingDeps {
				if existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
					isDuplicate = true
					break
				}
			}

			if isDuplicate {
				continue
			}

			// Add dependency
			if err := sqliteStore.AddDependency(ctx, dep, "import"); err != nil {
				if opts.Strict {
					return nil, fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				// Non-strict mode: just skip this dependency
				continue
			}
		}
	}

	// Phase 6: Import labels
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}

		// Get current labels
		currentLabels, err := sqliteStore.GetLabels(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
		}

		currentLabelSet := make(map[string]bool)
		for _, label := range currentLabels {
			currentLabelSet[label] = true
		}

		// Add missing labels
		for _, label := range issue.Labels {
			if !currentLabelSet[label] {
				if err := sqliteStore.AddLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return nil, fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
					}
					// Non-strict mode: skip this label
					continue
				}
			}
		}
	}

	return result, nil
}
