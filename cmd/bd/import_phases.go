package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// Phase 1: Get or create SQLite store for import
func getOrCreateStore(_ context.Context, dbPath string, store storage.Storage) (*sqlite.SQLiteStorage, bool, error) {
	var sqliteStore *sqlite.SQLiteStorage
	var needCloseStore bool

	if store != nil {
		// Direct mode - try to use existing store
		var ok bool
		sqliteStore, ok = store.(*sqlite.SQLiteStorage)
		if !ok {
			return nil, false, fmt.Errorf("import requires SQLite storage backend")
		}
	} else {
		// Daemon mode - open direct connection for import
		if dbPath == "" {
			return nil, false, fmt.Errorf("database path not set")
		}
		var err error
		sqliteStore, err = sqlite.New(dbPath)
		if err != nil {
			return nil, false, fmt.Errorf("failed to open database: %w", err)
		}
		needCloseStore = true
	}

	return sqliteStore, needCloseStore, nil
}

// Phase 2: Check and handle prefix mismatches
func handlePrefixMismatch(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions, result *ImportResult) error {
	configuredPrefix, err := sqliteStore.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return fmt.Errorf("failed to get configured prefix: %w", err)
	}

	// Only validate prefixes if a prefix is configured
	if strings.TrimSpace(configuredPrefix) == "" {
		if opts.RenameOnImport {
			return fmt.Errorf("cannot rename: issue_prefix not configured in database")
		}
		return nil
	}

	result.ExpectedPrefix = configuredPrefix

	// Analyze prefixes in imported issues
	for _, issue := range issues {
		prefix := extractPrefix(issue.ID)
		if prefix != configuredPrefix {
			result.PrefixMismatch = true
			result.MismatchPrefixes[prefix]++
		}
	}

	// If prefix mismatch detected and not handling it, return error or warning
	if result.PrefixMismatch && !opts.RenameOnImport && !opts.DryRun && !opts.SkipPrefixValidation {
		return fmt.Errorf("prefix mismatch detected: database uses '%s-' but found issues with prefixes: %v (use --rename-on-import to automatically fix)", configuredPrefix, getPrefixList(result.MismatchPrefixes))
	}

	// Handle rename-on-import if requested
	if result.PrefixMismatch && opts.RenameOnImport && !opts.DryRun {
		if err := renameImportedIssuePrefixes(issues, configuredPrefix); err != nil {
			return fmt.Errorf("failed to rename prefixes: %w", err)
		}
		// After renaming, clear the mismatch flags since we fixed them
		result.PrefixMismatch = false
		result.MismatchPrefixes = make(map[string]int)
	}

	return nil
}

// Phase 3: Detect and resolve collisions
func handleCollisions(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions, result *ImportResult) ([]*types.Issue, error) {
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, issues)
	if err != nil {
		return nil, fmt.Errorf("collision detection failed: %w", err)
	}

	result.Collisions = len(collisionResult.Collisions)
	for _, collision := range collisionResult.Collisions {
		result.CollisionIDs = append(result.CollisionIDs, collision.ID)
	}

	// Handle collisions
	if len(collisionResult.Collisions) > 0 {
		if opts.DryRun {
			// In dry-run mode, just return collision info
			return issues, nil
		}

		if !opts.ResolveCollisions {
			// Default behavior: fail on collision
			return nil, fmt.Errorf("collision detected for issues: %v (use --resolve-collisions to auto-resolve)", result.CollisionIDs)
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
		return filteredIssues, nil
	}

	if opts.DryRun {
		// No collisions in dry-run mode
		result.Created = len(collisionResult.NewIssues)
		// bd-88: ExactMatches are unchanged issues (idempotent), not updates
		result.Unchanged = len(collisionResult.ExactMatches)
	}

	return issues, nil
}

// Phase 4: Upsert issues (create new or update existing)
func upsertIssues(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions, result *ImportResult) error {
	var newIssues []*types.Issue
	seenNew := make(map[string]int) // Track duplicates within import batch

	for _, issue := range issues {
		// Check if issue exists in DB
		existing, err := sqliteStore.GetIssue(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error checking issue %s: %w", issue.ID, err)
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

			// bd-88: Only update if data actually changed (prevents timestamp churn)
			if issueDataChanged(existing, updates) {
				if err := sqliteStore.UpdateIssue(ctx, issue.ID, updates, "import"); err != nil {
					return fmt.Errorf("error updating issue %s: %w", issue.ID, err)
				}
				result.Updated++
			} else {
				// bd-88: Track unchanged issues separately for accurate reporting
				result.Unchanged++
			}
		} else {
			// New issue - check for duplicates in import batch
			if idx, seen := seenNew[issue.ID]; seen {
				if opts.Strict {
					return fmt.Errorf("duplicate issue ID %s in import (line %d)", issue.ID, idx)
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
			return fmt.Errorf("error creating issues: %w", err)
		}
		result.Created += len(newIssues)
	}

	// Sync counters after batch import
	if err := sqliteStore.SyncAllCounters(ctx); err != nil {
		return fmt.Errorf("error syncing counters: %w", err)
	}

	return nil
}

// Phase 5: Import dependencies
func importDependencies(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}

		// Fetch existing dependencies once per issue
		existingDeps, err := sqliteStore.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error checking dependencies for %s: %w", issue.ID, err)
		}

		// Build set of existing dependencies for O(1) lookup
		existingSet := make(map[string]bool)
		for _, existing := range existingDeps {
			key := fmt.Sprintf("%s|%s", existing.DependsOnID, existing.Type)
			existingSet[key] = true
		}

		for _, dep := range issue.Dependencies {
			// Check for duplicate using set
			key := fmt.Sprintf("%s|%s", dep.DependsOnID, dep.Type)
			if existingSet[key] {
				continue
			}

			// Add dependency
			if err := sqliteStore.AddDependency(ctx, dep, "import"); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				// Non-strict mode: just skip this dependency
				continue
			}
		}
	}

	return nil
}

// Phase 6: Import labels
func importLabels(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}

		// Get current labels
		currentLabels, err := sqliteStore.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
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
						return fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
					}
					// Non-strict mode: skip this label
					continue
				}
			}
		}
	}

	return nil
}

// Phase 7: Import comments
func importComments(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}

		// Get current comments to avoid duplicates
		currentComments, err := sqliteStore.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting comments for %s: %w", issue.ID, err)
		}

		// Build a set of existing comments (by author+normalized text)
		// Note: We ignore CreatedAt since AddIssueComment generates its own timestamp
		existingComments := make(map[string]bool)
		for _, c := range currentComments {
			key := fmt.Sprintf("%s:%s", c.Author, strings.TrimSpace(c.Text))
			existingComments[key] = true
		}

		// Add missing comments
		for _, comment := range issue.Comments {
			key := fmt.Sprintf("%s:%s", comment.Author, strings.TrimSpace(comment.Text))
			if !existingComments[key] {
				if _, err := sqliteStore.AddIssueComment(ctx, issue.ID, comment.Author, comment.Text); err != nil {
					if opts.Strict {
						return fmt.Errorf("error adding comment to %s: %w", issue.ID, err)
					}
					// Non-strict mode: skip this comment
					continue
				}
			}
		}
	}

	return nil
}

// Helper functions

func extractPrefix(issueID string) string {
	parts := strings.SplitN(issueID, "-", 2)
	if len(parts) < 2 {
		return "" // No prefix found
	}
	return parts[0]
}

func getPrefixList(prefixes map[string]int) []string {
	var result []string
	keys := make([]string, 0, len(prefixes))
	for k := range prefixes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, prefix := range keys {
		count := prefixes[prefix]
		result = append(result, fmt.Sprintf("%s- (%d issues)", prefix, count))
	}
	return result
}
