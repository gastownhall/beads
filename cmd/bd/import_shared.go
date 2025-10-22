package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// ImportOptions configures how the import behaves
type ImportOptions struct {
	ResolveCollisions  bool // Auto-resolve collisions by remapping to new IDs
	DryRun             bool // Preview changes without applying them
	SkipUpdate         bool // Skip updating existing issues (create-only mode)
	Strict             bool // Fail on any error (dependencies, labels, etc.)
	RenameOnImport     bool // Rename imported issues to match database prefix
	SkipPrefixValidation bool // Skip prefix validation (for auto-import)
}

// ImportResult contains statistics about the import operation
type ImportResult struct {
	Created         int               // New issues created
	Updated         int               // Existing issues updated
	Skipped         int               // Issues skipped (duplicates, errors)
	Collisions      int               // Collisions detected
	IDMapping       map[string]string // Mapping of remapped IDs (old -> new)
	CollisionIDs    []string          // IDs that collided
	PrefixMismatch  bool              // Prefix mismatch detected
	ExpectedPrefix  string            // Database configured prefix
	MismatchPrefixes map[string]int    // Map of mismatched prefixes to count
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
		IDMapping:         make(map[string]string),
		MismatchPrefixes: make(map[string]int),
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

	// Phase 1.5: Check for prefix mismatches
	configuredPrefix, err := sqliteStore.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return nil, fmt.Errorf("failed to get configured prefix: %w", err)
	}

	// Only validate prefixes if a prefix is configured
	if strings.TrimSpace(configuredPrefix) != "" {
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
			return result, fmt.Errorf("prefix mismatch detected: database uses '%s-' but found issues with prefixes: %v (use --rename-on-import to automatically fix)", configuredPrefix, getPrefixList(result.MismatchPrefixes))
		}

		// Handle rename-on-import if requested
		if result.PrefixMismatch && opts.RenameOnImport && !opts.DryRun {
			if err := renameImportedIssuePrefixes(issues, configuredPrefix); err != nil {
				return nil, fmt.Errorf("failed to rename prefixes: %w", err)
			}
			// After renaming, clear the mismatch flags since we fixed them
			result.PrefixMismatch = false
			result.MismatchPrefixes = make(map[string]int)
		}
	} else if opts.RenameOnImport {
		// No prefix configured but rename was requested
		return nil, fmt.Errorf("cannot rename: issue_prefix not configured in database")
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

	// Phase 7: Import comments
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}

		// Get current comments to avoid duplicates
		currentComments, err := sqliteStore.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("error getting comments for %s: %w", issue.ID, err)
		}

		// Build a set of existing comments (by author+text+timestamp)
		existingComments := make(map[string]bool)
		for _, c := range currentComments {
			key := fmt.Sprintf("%s:%s:%s", c.Author, c.Text, c.CreatedAt.Format(time.RFC3339))
			existingComments[key] = true
		}

		// Add missing comments
		for _, comment := range issue.Comments {
			key := fmt.Sprintf("%s:%s:%s", comment.Author, comment.Text, comment.CreatedAt.Format(time.RFC3339))
			if !existingComments[key] {
				if _, err := sqliteStore.AddIssueComment(ctx, issue.ID, comment.Author, comment.Text); err != nil {
					if opts.Strict {
						return nil, fmt.Errorf("error adding comment to %s: %w", issue.ID, err)
					}
					// Non-strict mode: skip this comment
					continue
				}
			}
		}
	}

	return result, nil
}

// extractPrefix extracts the prefix from an issue ID (e.g., "bd-123" -> "bd")
func extractPrefix(issueID string) string {
	parts := strings.SplitN(issueID, "-", 2)
	if len(parts) < 2 {
		return "" // No prefix found
	}
	return parts[0]
}

// getPrefixList returns a sorted list of prefixes with their counts
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

// renameImportedIssuePrefixes renames all issues and their references to match the target prefix
func renameImportedIssuePrefixes(issues []*types.Issue, targetPrefix string) error {
	// Build a mapping of old IDs to new IDs
	idMapping := make(map[string]string)
	
	for _, issue := range issues {
		oldPrefix := extractPrefix(issue.ID)
		if oldPrefix == "" {
			return fmt.Errorf("cannot rename issue %s: malformed ID (no hyphen found)", issue.ID)
		}
		
		if oldPrefix != targetPrefix {
			// Extract the numeric part
			numPart := strings.TrimPrefix(issue.ID, oldPrefix+"-")
			
			// Validate that the numeric part is actually numeric
			if numPart == "" || !isNumeric(numPart) {
				return fmt.Errorf("cannot rename issue %s: non-numeric suffix '%s'", issue.ID, numPart)
			}
			
			newID := fmt.Sprintf("%s-%s", targetPrefix, numPart)
			idMapping[issue.ID] = newID
		}
	}
	
	// Now update all issues and their references
	for _, issue := range issues {
		// Update the issue ID itself if it needs renaming
		if newID, ok := idMapping[issue.ID]; ok {
			issue.ID = newID
		}
		
		// Update all text references in issue fields
		issue.Title = replaceIDReferences(issue.Title, idMapping)
		issue.Description = replaceIDReferences(issue.Description, idMapping)
		if issue.Design != "" {
			issue.Design = replaceIDReferences(issue.Design, idMapping)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = replaceIDReferences(issue.AcceptanceCriteria, idMapping)
		}
		if issue.Notes != "" {
			issue.Notes = replaceIDReferences(issue.Notes, idMapping)
		}
		
		// Update dependency references
		for i := range issue.Dependencies {
			if newID, ok := idMapping[issue.Dependencies[i].IssueID]; ok {
				issue.Dependencies[i].IssueID = newID
			}
			if newID, ok := idMapping[issue.Dependencies[i].DependsOnID]; ok {
				issue.Dependencies[i].DependsOnID = newID
			}
		}
		
		// Update comment references
		for i := range issue.Comments {
			issue.Comments[i].Text = replaceIDReferences(issue.Comments[i].Text, idMapping)
		}
	}
	
	return nil
}

// replaceIDReferences replaces all old issue ID references with new ones in text
// Uses boundary-aware matching to avoid partial replacements (e.g., wy-1 inside wy-10)
func replaceIDReferences(text string, idMapping map[string]string) string {
	if len(idMapping) == 0 {
		return text
	}
	
	// Sort old IDs by length descending to handle longer IDs first
	// This prevents "wy-1" from being replaced inside "wy-10"
	oldIDs := make([]string, 0, len(idMapping))
	for oldID := range idMapping {
		oldIDs = append(oldIDs, oldID)
	}
	sort.Slice(oldIDs, func(i, j int) bool {
		return len(oldIDs[i]) > len(oldIDs[j])
	})
	
	result := text
	for _, oldID := range oldIDs {
		newID := idMapping[oldID]
		// Replace with boundary checking
		result = replaceBoundaryAware(result, oldID, newID)
	}
	return result
}

// replaceBoundaryAware replaces oldID with newID only when surrounded by boundaries
func replaceBoundaryAware(text, oldID, newID string) string {
	if !strings.Contains(text, oldID) {
		return text
	}
	
	var result strings.Builder
	result.Grow(len(text))
	
	for i := 0; i < len(text); {
		// Check if we match oldID at this position
		if strings.HasPrefix(text[i:], oldID) {
			// Check boundaries before and after
			beforeOK := i == 0 || isBoundary(text[i-1])
			afterOK := (i+len(oldID) >= len(text)) || isBoundary(text[i+len(oldID)])
			
			if beforeOK && afterOK {
				// Valid match - replace
				result.WriteString(newID)
				i += len(oldID)
				continue
			}
		}
		
		// Not a match or invalid boundaries - keep original character
		result.WriteByte(text[i])
		i++
	}
	
	return result.String()
}

// isBoundary returns true if the character is not part of an issue ID
func isBoundary(c byte) bool {
	// Issue IDs contain: lowercase letters, digits, and hyphens
	// Boundaries are anything else (space, punctuation, etc.)
	return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-')
}

// isNumeric returns true if the string contains only digits
func isNumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
