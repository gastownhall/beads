package sqlite

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// CollisionResult categorizes incoming issues by their relationship to existing DB state
type CollisionResult struct {
	ExactMatches []string           // IDs that match exactly (idempotent import)
	Collisions   []*CollisionDetail // Issues with same ID but different content
	NewIssues    []string           // IDs that don't exist in DB yet
}

// CollisionDetail provides detailed information about a collision
type CollisionDetail struct {
	ID                string        // The issue ID that collided
	IncomingIssue     *types.Issue  // The issue from the import file
	ExistingIssue     *types.Issue  // The issue currently in the database
	ConflictingFields []string      // List of field names that differ
	ReferenceScore    int           // Number of references to this issue (for scoring)
}

// DetectCollisions compares incoming JSONL issues against DB state
// It distinguishes between:
//  1. Exact match (idempotent) - ID and content are identical
//  2. ID match but different content (collision) - same ID, different fields
//  3. New issue - ID doesn't exist in DB
//
// Returns a CollisionResult categorizing all incoming issues.
func DetectCollisions(ctx context.Context, s *SQLiteStorage, incomingIssues []*types.Issue) (*CollisionResult, error) {
	result := &CollisionResult{
		ExactMatches: make([]string, 0),
		Collisions:   make([]*CollisionDetail, 0),
		NewIssues:    make([]string, 0),
	}

	// Phase 1: Deduplicate within incoming batch
	// Group by content hash to find duplicates with different IDs
	deduped := deduplicateIncomingIssues(incomingIssues)

	for _, incoming := range deduped {
		// Check if issue exists in database
		existing, err := s.GetIssue(ctx, incoming.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to check issue %s: %w", incoming.ID, err)
		}

		if existing == nil {
			// Issue doesn't exist in DB - it's new
			result.NewIssues = append(result.NewIssues, incoming.ID)
			continue
		}

		// Issue exists - compare content
		conflicts := compareIssues(existing, incoming)
		if len(conflicts) == 0 {
			// No differences - exact match (idempotent)
			result.ExactMatches = append(result.ExactMatches, incoming.ID)
		} else {
			// Same ID but different content - collision
			result.Collisions = append(result.Collisions, &CollisionDetail{
				ID:                incoming.ID,
				IncomingIssue:     incoming,
				ExistingIssue:     existing,
				ConflictingFields: conflicts,
			})
		}
	}

	return result, nil
}

// compareIssues compares two issues and returns a list of field names that differ
// Timestamps (CreatedAt, UpdatedAt, ClosedAt) are intentionally not compared
// Dependencies are also not compared (handled separately in import)
func compareIssues(existing, incoming *types.Issue) []string {
	conflicts := make([]string, 0)

	// Compare all relevant fields
	if existing.Title != incoming.Title {
		conflicts = append(conflicts, "title")
	}
	if existing.Description != incoming.Description {
		conflicts = append(conflicts, "description")
	}
	if existing.Design != incoming.Design {
		conflicts = append(conflicts, "design")
	}
	if existing.AcceptanceCriteria != incoming.AcceptanceCriteria {
		conflicts = append(conflicts, "acceptance_criteria")
	}
	if existing.Notes != incoming.Notes {
		conflicts = append(conflicts, "notes")
	}
	if existing.Status != incoming.Status {
		conflicts = append(conflicts, "status")
	}
	if existing.Priority != incoming.Priority {
		conflicts = append(conflicts, "priority")
	}
	if existing.IssueType != incoming.IssueType {
		conflicts = append(conflicts, "issue_type")
	}
	if existing.Assignee != incoming.Assignee {
		conflicts = append(conflicts, "assignee")
	}

	// Compare EstimatedMinutes (handle nil cases)
	if !equalIntPtr(existing.EstimatedMinutes, incoming.EstimatedMinutes) {
		conflicts = append(conflicts, "estimated_minutes")
	}

	// Compare ExternalRef (handle nil cases)
	if !equalStringPtr(existing.ExternalRef, incoming.ExternalRef) {
		conflicts = append(conflicts, "external_ref")
	}

	return conflicts
}

// equalIntPtr compares two *int pointers for equality
func equalIntPtr(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// equalStringPtr compares two *string pointers for equality
func equalStringPtr(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// ScoreCollisions calculates reference scores for all colliding issues and sorts them
// by score ascending (fewest references first). This minimizes the total number of
// updates needed during renumbering - issues with fewer references are renumbered first.
//
// Reference score = text mentions + dependency references
func ScoreCollisions(ctx context.Context, s *SQLiteStorage, collisions []*CollisionDetail, allIssues []*types.Issue) error {
	// Get all dependency records for efficient lookup
	allDeps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependency records: %w", err)
	}

	// Calculate reference score for each collision
	for _, collision := range collisions {
		score, err := countReferences(collision.ID, allIssues, allDeps)
		if err != nil {
			return fmt.Errorf("failed to count references for %s: %w", collision.ID, err)
		}
		collision.ReferenceScore = score
	}

	// Sort collisions by reference score ascending (fewest first)
	sort.Slice(collisions, func(i, j int) bool {
		return collisions[i].ReferenceScore < collisions[j].ReferenceScore
	})

	return nil
}

// countReferences counts how many times an issue ID is referenced
// Returns: text mentions + dependency references
func countReferences(issueID string, allIssues []*types.Issue, allDeps map[string][]*types.Dependency) (int, error) {
	count := 0

	// Count text mentions in all issues' text fields
	// Use word boundary regex to match exact IDs (e.g., "bd-10" but not "bd-100")
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(issueID))
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to compile regex for %s: %w", issueID, err)
	}

	for _, issue := range allIssues {
		// Skip counting references in the issue itself
		if issue.ID == issueID {
			continue
		}

		// Count mentions in description
		count += len(re.FindAllString(issue.Description, -1))

		// Count mentions in design
		count += len(re.FindAllString(issue.Design, -1))

		// Count mentions in notes
		count += len(re.FindAllString(issue.Notes, -1))

		// Count mentions in acceptance criteria
		count += len(re.FindAllString(issue.AcceptanceCriteria, -1))
	}

	// Count dependency references
	// An issue can be referenced as either IssueID or DependsOnID
	for _, deps := range allDeps {
		for _, dep := range deps {
			// Skip self-references
			if dep.IssueID == issueID && dep.DependsOnID == issueID {
				continue
			}

			// Count if this issue is the source (IssueID)
			if dep.IssueID == issueID {
				count++
			}

			// Count if this issue is the target (DependsOnID)
			if dep.DependsOnID == issueID {
				count++
			}
		}
	}

	return count, nil
}

// deduplicateIncomingIssues removes content-duplicate issues within the incoming batch
// Returns deduplicated slice, keeping the first issue ID (lexicographically) for each unique content
func deduplicateIncomingIssues(issues []*types.Issue) []*types.Issue {
	// Group issues by content hash (ignoring ID and timestamps)
	type contentKey struct {
		title              string
		description        string
		design             string
		acceptanceCriteria string
		notes              string
		status             string
		priority           int
		issueType          string
		assignee           string
	}

	seen := make(map[contentKey]*types.Issue)
	result := make([]*types.Issue, 0, len(issues))

	for _, issue := range issues {
		key := contentKey{
			title:              issue.Title,
			description:        issue.Description,
			design:             issue.Design,
			acceptanceCriteria: issue.AcceptanceCriteria,
			notes:              issue.Notes,
			status:             string(issue.Status),
			priority:           issue.Priority,
			issueType:          string(issue.IssueType),
			assignee:           issue.Assignee,
		}

		if existing, found := seen[key]; found {
			// Duplicate found - keep the one with lexicographically smaller ID
			if issue.ID < existing.ID {
				// Replace existing with this one (smaller ID)
				for i, r := range result {
					if r.ID == existing.ID {
						result[i] = issue
						break
					}
				}
				seen[key] = issue
			}
			// Otherwise skip this duplicate
		} else {
			// First time seeing this content
			seen[key] = issue
			result = append(result, issue)
		}
	}

	return result
}

// RemapCollisions handles ID remapping for colliding issues
// Takes sorted collisions (fewest references first) and remaps them to new IDs
// Returns a map of old ID -> new ID for reporting
//
// NOTE: This function is not atomic - it performs multiple separate database operations.
// If an error occurs partway through, some issues may be created without their references
// being updated. This is a known limitation that requires storage layer refactoring to fix.
// See issue bd-25 for transaction support.
func RemapCollisions(ctx context.Context, s *SQLiteStorage, collisions []*CollisionDetail, _ []*types.Issue) (map[string]string, error) {
	idMapping := make(map[string]string)

	// Sync counters before remapping to avoid ID collisions
	if err := s.SyncAllCounters(ctx); err != nil {
		return nil, fmt.Errorf("failed to sync ID counters: %w", err)
	}

	// For each collision (in order of ascending reference score)
	for _, collision := range collisions {
		oldID := collision.ID

		// Allocate new ID using atomic counter
		prefix, err := s.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix == "" {
			prefix = "bd"
		}
		nextID, err := s.getNextIDForPrefix(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("failed to generate new ID for collision %s: %w", oldID, err)
		}
		newID := fmt.Sprintf("%s-%d", prefix, nextID)

		// Record mapping
		idMapping[oldID] = newID

		// Update the issue ID in the incoming issue
		collision.IncomingIssue.ID = newID

		// Create the issue with new ID
		// Note: CreateIssue will use the ID we set
		if err := s.CreateIssue(ctx, collision.IncomingIssue, "import-remap"); err != nil {
			return nil, fmt.Errorf("failed to create remapped issue %s -> %s: %w", oldID, newID, err)
		}
	}

	// Now update all references in text fields and dependencies
	if err := updateReferences(ctx, s, idMapping); err != nil {
		return nil, fmt.Errorf("failed to update references: %w", err)
	}

	return idMapping, nil
}

// updateReferences updates all text field references and dependency records
// to point to new IDs based on the idMapping
func updateReferences(ctx context.Context, s *SQLiteStorage, idMapping map[string]string) error {
	// Pre-compile all regexes once for the entire operation
	// This avoids recompiling the same patterns for each text field
	cache, err := buildReplacementCache(idMapping)
	if err != nil {
		return fmt.Errorf("failed to build replacement cache: %w", err)
	}

	// Update text fields in all issues (both DB and incoming)
	// We need to update issues in the database
	dbIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get all issues from DB: %w", err)
	}

	for _, issue := range dbIssues {
		updates := make(map[string]interface{})

		// Update description using cached regexes
		newDesc := replaceIDReferencesWithCache(issue.Description, cache)
		if newDesc != issue.Description {
			updates["description"] = newDesc
		}

		// Update design using cached regexes
		newDesign := replaceIDReferencesWithCache(issue.Design, cache)
		if newDesign != issue.Design {
			updates["design"] = newDesign
		}

		// Update notes using cached regexes
		newNotes := replaceIDReferencesWithCache(issue.Notes, cache)
		if newNotes != issue.Notes {
			updates["notes"] = newNotes
		}

		// Update acceptance criteria using cached regexes
		newAC := replaceIDReferencesWithCache(issue.AcceptanceCriteria, cache)
		if newAC != issue.AcceptanceCriteria {
			updates["acceptance_criteria"] = newAC
		}

		// If there are updates, apply them
		if len(updates) > 0 {
			if err := s.UpdateIssue(ctx, issue.ID, updates, "import-remap"); err != nil {
				return fmt.Errorf("failed to update references in issue %s: %w", issue.ID, err)
			}
		}
	}

	// Update dependency records
	if err := updateDependencyReferences(ctx, s, idMapping); err != nil {
		return fmt.Errorf("failed to update dependency references: %w", err)
	}

	return nil
}

// idReplacementCache stores pre-compiled regexes for ID replacements
// This avoids recompiling the same regex patterns for each text field
type idReplacementCache struct {
	oldID       string
	newID       string
	placeholder string
	regex       *regexp.Regexp
}

// buildReplacementCache pre-compiles all regex patterns for an ID mapping
// This cache should be created once per ID mapping and reused for all text replacements
func buildReplacementCache(idMapping map[string]string) ([]*idReplacementCache, error) {
	cache := make([]*idReplacementCache, 0, len(idMapping))
	i := 0
	for oldID, newID := range idMapping {
		// Use word boundary regex for exact matching
		pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(oldID))
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("failed to compile regex for %s: %w", oldID, err)
		}

		cache = append(cache, &idReplacementCache{
			oldID:       oldID,
			newID:       newID,
			placeholder: fmt.Sprintf("\x00REMAP\x00_%d_\x00", i),
			regex:       re,
		})
		i++
	}
	return cache, nil
}

// replaceIDReferencesWithCache replaces all occurrences of old IDs with new IDs using a pre-compiled cache
// Uses a two-phase approach to avoid replacement conflicts: first replace with placeholders, then replace with new IDs
func replaceIDReferencesWithCache(text string, cache []*idReplacementCache) string {
	if len(cache) == 0 || text == "" {
		return text
	}

	// Phase 1: Replace all old IDs with unique placeholders
	result := text
	for _, entry := range cache {
		result = entry.regex.ReplaceAllString(result, entry.placeholder)
	}

	// Phase 2: Replace all placeholders with new IDs
	for _, entry := range cache {
		result = strings.ReplaceAll(result, entry.placeholder, entry.newID)
	}

	return result
}

// replaceIDReferences replaces all occurrences of old IDs with new IDs in text
// Uses word-boundary regex to ensure exact matches (bd-10 but not bd-100)
// Uses a two-phase approach to avoid replacement conflicts: first replace with
// placeholders, then replace placeholders with new IDs
//
// Note: This function compiles regexes on every call. For better performance when
// processing multiple text fields with the same ID mapping, use buildReplacementCache()
// and replaceIDReferencesWithCache() instead.
func replaceIDReferences(text string, idMapping map[string]string) string {
	// Build cache (compiles regexes)
	cache, err := buildReplacementCache(idMapping)
	if err != nil {
		// Fallback to no replacement if regex compilation fails
		return text
	}
	return replaceIDReferencesWithCache(text, cache)
}

// updateDependencyReferences updates dependency records to use new IDs
// This handles both IssueID and DependsOnID fields
// IMPORTANT: Only updates dependencies belonging to REMAPPED issues (with new IDs from idMapping).
// Dependencies belonging to existing issues are left untouched.
//
// NOTE: During normal import flow, this is effectively a no-op because imported dependencies
// haven't been added to the database yet when RemapCollisions runs. Dependencies are imported
// later in Phase 5 of import_shared.go. However, this function still serves as a safety guard
// and handles edge cases where dependencies might exist with the new remapped IDs.
func updateDependencyReferences(ctx context.Context, s *SQLiteStorage, idMapping map[string]string) error {
	// Build set of NEW remapped IDs (idMapping values)
	// Only dependencies with these IDs as IssueID should be updated
	newRemappedIDs := make(map[string]bool)
	for _, newID := range idMapping {
		newRemappedIDs[newID] = true
	}

	// Get all dependency records
	allDeps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get all dependencies: %w", err)
	}

	// Phase 1: Collect all changes to avoid race conditions while iterating
	type depUpdate struct {
		oldIssueID     string
		oldDependsOnID string
		newDep         *types.Dependency
	}
	var updates []depUpdate

	for _, deps := range allDeps {
		for _, dep := range deps {
			// CRITICAL FIX: Only update dependencies that belong to REMAPPED issues
			// A dependency belongs to a remapped issue if its IssueID is a NEW remapped ID
			// (one of the VALUES in idMapping, not the keys)
			//
			// We must NOT check against idMapping keys (old IDs) because those are the same
			// as existing issue IDs in the database, and we'd incorrectly modify their dependencies.
			if !newRemappedIDs[dep.IssueID] {
				// This dependency does not belong to a remapped issue - skip it
				continue
			}

			needsUpdate := false
			newIssueID := dep.IssueID
			newDependsOnID := dep.DependsOnID

			// Check if either ID was remapped
			if mappedID, ok := idMapping[dep.IssueID]; ok {
				newIssueID = mappedID
				needsUpdate = true
			}
			if mappedID, ok := idMapping[dep.DependsOnID]; ok {
				newDependsOnID = mappedID
				needsUpdate = true
			}

			if needsUpdate {
				updates = append(updates, depUpdate{
					oldIssueID:     dep.IssueID,
					oldDependsOnID: dep.DependsOnID,
					newDep: &types.Dependency{
						IssueID:     newIssueID,
						DependsOnID: newDependsOnID,
						Type:        dep.Type,
					},
				})
			}
		}
	}

	// Phase 2: Apply all collected changes
	for _, update := range updates {
		// Remove old dependency - use RemoveDependencyIfExists which doesn't error on missing deps
		if err := s.removeDependencyIfExists(ctx, update.oldIssueID, update.oldDependsOnID, "import-remap"); err != nil {
			return fmt.Errorf("failed to remove old dependency %s -> %s: %w",
				update.oldIssueID, update.oldDependsOnID, err)
		}

		// Add new dependency with updated IDs
		// Use addDependencyUnchecked to skip semantic validation (like parent-child direction)
		// since we're just remapping existing dependencies that were already validated
		if err := s.addDependencyUnchecked(ctx, update.newDep, "import-remap"); err != nil {
			return fmt.Errorf("failed to add updated dependency %s -> %s: %w",
				update.newDep.IssueID, update.newDep.DependsOnID, err)
		}
	}

	return nil
}
