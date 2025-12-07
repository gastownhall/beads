package importer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// OrphanHandling is an alias to sqlite.OrphanHandling for convenience
type OrphanHandling = sqlite.OrphanHandling

const (
	// OrphanStrict fails import on missing parent (safest)
	OrphanStrict = sqlite.OrphanStrict
	// OrphanResurrect auto-resurrects missing parents from JSONL history
	OrphanResurrect = sqlite.OrphanResurrect
	// OrphanSkip skips orphaned issues with warning
	OrphanSkip = sqlite.OrphanSkip
	// OrphanAllow imports orphans without validation (default, works around bugs)
	OrphanAllow = sqlite.OrphanAllow
)

// Options contains import configuration
type Options struct {
	DryRun                     bool           // Preview changes without applying them
	SkipUpdate                 bool           // Skip updating existing issues (create-only mode)
	Strict                     bool           // Fail on any error (dependencies, labels, etc.)
	RenameOnImport             bool           // Rename imported issues to match database prefix
	SkipPrefixValidation       bool           // Skip prefix validation (for auto-import)
	OrphanHandling             OrphanHandling // How to handle missing parent issues (default: allow)
	ClearDuplicateExternalRefs bool           // Clear duplicate external_ref values instead of erroring
	NoGitHistory               bool           // Skip git history backfill for deletions (prevents spurious deletion during JSONL migrations)
	IgnoreDeletions            bool           // Import issues even if they're in the deletions manifest
}

// Result contains statistics about the import operation
type Result struct {
	Created             int               // New issues created
	Updated             int               // Existing issues updated
	Unchanged           int               // Existing issues that matched exactly (idempotent)
	Skipped             int               // Issues skipped (duplicates, errors)
	Collisions          int               // Collisions detected
	IDMapping           map[string]string // Mapping of remapped IDs (old -> new)
	CollisionIDs        []string          // IDs that collided
	PrefixMismatch      bool              // Prefix mismatch detected
	ExpectedPrefix      string            // Database configured prefix
	MismatchPrefixes    map[string]int    // Map of mismatched prefixes to count
	SkippedDependencies []string          // Dependencies skipped due to FK constraint violations
	Purged                int               // Issues purged from DB (found in deletions manifest)
	PurgedIDs             []string          // IDs that were purged
	SkippedDeleted        int               // Issues skipped because they're in deletions manifest
	SkippedDeletedIDs     []string          // IDs that were skipped due to deletions manifest
	ConvertedToTombstone  int               // Legacy deletions.jsonl entries converted to tombstones (bd-wucl)
	ConvertedTombstoneIDs []string          // IDs that were converted to tombstones
}

// ImportIssues handles the core import logic used by both manual and auto-import.
// This function:
// - Works with existing storage or opens direct SQLite connection if needed
// - Detects and handles collisions
// - Imports issues, dependencies, labels, and comments
// - Returns detailed results
//
// The caller is responsible for:
// - Reading and parsing JSONL into issues slice
// - Displaying results to the user
// - Setting metadata (e.g., last_import_hash)
//
// Parameters:
// - ctx: Context for cancellation
// - dbPath: Path to SQLite database file
// - store: Existing storage instance (can be nil for direct mode)
// - issues: Parsed issues from JSONL
// - opts: Import options
func ImportIssues(ctx context.Context, dbPath string, store storage.Storage, issues []*types.Issue, opts Options) (*Result, error) {
	result := &Result{
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	// Compute content hashes for all incoming issues (bd-95)
	// Always recompute to avoid stale/incorrect JSONL hashes (bd-1231)
	for _, issue := range issues {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Get or create SQLite store
	sqliteStore, needCloseStore, err := getOrCreateStore(ctx, dbPath, store)
	if err != nil {
		return nil, err
	}
	if needCloseStore {
		defer func() { _ = sqliteStore.Close() }()
	}
	
	// Clear export_hashes before import to prevent staleness (bd-160)
	// Import operations may add/update issues, so export_hashes entries become invalid
	if !opts.DryRun {
		if err := sqliteStore.ClearAllExportHashes(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear export_hashes before import: %v\n", err)
		}
	}
	
	// Read orphan handling from config if not explicitly set
	if opts.OrphanHandling == "" {
		opts.OrphanHandling = sqliteStore.GetOrphanHandling(ctx)
	}

	// Handle deletions manifest and tombstones (bd-dve)
	//
	// Phase 1 (Dual-Write):
	// - Tombstones in JSONL are imported as-is (they're issues with status=tombstone)
	// - Legacy deletions.jsonl entries are converted to tombstones
	// - Non-tombstone issues in deletions manifest are skipped (backwards compat)
	//
	// Note: Tombstones from JSONL take precedence over legacy deletions.jsonl
	if !opts.IgnoreDeletions && dbPath != "" {
		beadsDir := filepath.Dir(dbPath)
		deletionsPath := deletions.DefaultPath(beadsDir)
		loadResult, err := deletions.LoadDeletions(deletionsPath)
		if err == nil && len(loadResult.Records) > 0 {
			// Build a map of existing tombstones from JSONL for quick lookup
			tombstoneIDs := make(map[string]bool)
			for _, issue := range issues {
				if issue.IsTombstone() {
					tombstoneIDs[issue.ID] = true
				}
			}

			var filteredIssues []*types.Issue
			for _, issue := range issues {
				// Tombstones are always imported (they represent deletions in the new format)
				if issue.IsTombstone() {
					filteredIssues = append(filteredIssues, issue)
					continue
				}

				if del, found := loadResult.Records[issue.ID]; found {
					// Non-tombstone issue is in deletions manifest - skip it
					// (this maintains backward compatibility during transition)
					result.SkippedDeleted++
					result.SkippedDeletedIDs = append(result.SkippedDeletedIDs, issue.ID)
					fmt.Fprintf(os.Stderr, "Skipping %s (in deletions manifest: deleted %s by %s)\n",
						issue.ID, del.Timestamp.Format("2006-01-02"), del.Actor)
				} else {
					filteredIssues = append(filteredIssues, issue)
				}
			}

			// Convert legacy deletions.jsonl entries to tombstones if not already in JSONL
			for id, del := range loadResult.Records {
				if tombstoneIDs[id] {
					// Already have a tombstone for this ID in JSONL, skip
					continue
				}
				// Convert this deletion record to a tombstone (bd-wucl)
				tombstone := convertDeletionToTombstone(id, del)
				filteredIssues = append(filteredIssues, tombstone)
				result.ConvertedToTombstone++
				result.ConvertedTombstoneIDs = append(result.ConvertedTombstoneIDs, id)
			}

			issues = filteredIssues
		}
	}

	// Check and handle prefix mismatches
	if err := handlePrefixMismatch(ctx, sqliteStore, issues, opts, result); err != nil {
		return result, err
	}

	// Validate no duplicate external_ref values in batch
	if err := validateNoDuplicateExternalRefs(issues, opts.ClearDuplicateExternalRefs, result); err != nil {
		return result, err
	}

	// Detect and resolve collisions
	issues, err = detectUpdates(ctx, sqliteStore, issues, opts, result)
	if err != nil {
		return result, err
	}
	if opts.DryRun && result.Collisions == 0 {
		return result, nil
	}

	// Upsert issues (create new or update existing)
	if err := upsertIssues(ctx, sqliteStore, issues, opts, result); err != nil {
		return nil, err
	}

	// Import dependencies
	if err := importDependencies(ctx, sqliteStore, issues, opts, result); err != nil {
		return nil, err
	}

	// Import labels
	if err := importLabels(ctx, sqliteStore, issues, opts); err != nil {
		return nil, err
	}

	// Import comments
	if err := importComments(ctx, sqliteStore, issues, opts); err != nil {
		return nil, err
	}

	// Purge deleted issues from DB based on deletions manifest
	// Issues that are in the manifest but not in JSONL should be deleted from DB
	if !opts.DryRun {
		if err := purgeDeletedIssues(ctx, sqliteStore, dbPath, issues, opts, result); err != nil {
			// Non-fatal - just log warning
			fmt.Fprintf(os.Stderr, "Warning: failed to purge deleted issues: %v\n", err)
		}
	}

	// Checkpoint WAL to ensure data persistence and reduce WAL file size
	if err := sqliteStore.CheckpointWAL(ctx); err != nil {
		// Non-fatal - just log warning
		fmt.Fprintf(os.Stderr, "Warning: failed to checkpoint WAL: %v\n", err)
	}

	return result, nil
}

// getOrCreateStore returns an existing storage or creates a new one
func getOrCreateStore(ctx context.Context, dbPath string, store storage.Storage) (*sqlite.SQLiteStorage, bool, error) {
	if store != nil {
		sqliteStore, ok := store.(*sqlite.SQLiteStorage)
		if !ok {
			return nil, false, fmt.Errorf("import requires SQLite storage backend")
		}
		return sqliteStore, false, nil
	}

	// Open direct connection for daemon mode
	if dbPath == "" {
		return nil, false, fmt.Errorf("database path not set")
	}
	sqliteStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return nil, false, fmt.Errorf("failed to open database: %w", err)
	}

	return sqliteStore, true, nil
}

// handlePrefixMismatch checks and handles prefix mismatches
func handlePrefixMismatch(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options, result *Result) error {
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
		prefix := utils.ExtractIssuePrefix(issue.ID)
		if prefix != configuredPrefix {
			result.PrefixMismatch = true
			result.MismatchPrefixes[prefix]++
		}
	}

	// If prefix mismatch detected and not handling it, return error or warning
	if result.PrefixMismatch && !opts.RenameOnImport && !opts.DryRun && !opts.SkipPrefixValidation {
		return fmt.Errorf("prefix mismatch detected: database uses '%s-' but found issues with prefixes: %v (use --rename-on-import to automatically fix)", configuredPrefix, GetPrefixList(result.MismatchPrefixes))
	}

	// Handle rename-on-import if requested
	if result.PrefixMismatch && opts.RenameOnImport && !opts.DryRun {
		if err := RenameImportedIssuePrefixes(issues, configuredPrefix); err != nil {
			return fmt.Errorf("failed to rename prefixes: %w", err)
		}
		// After renaming, clear the mismatch flags since we fixed them
		result.PrefixMismatch = false
		result.MismatchPrefixes = make(map[string]int)
	}

	return nil
}

// detectUpdates detects same-ID scenarios (which are updates with hash IDs, not collisions)
func detectUpdates(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options, result *Result) ([]*types.Issue, error) {
	// Phase 1: Detect (read-only)
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, issues)
	if err != nil {
		return nil, fmt.Errorf("collision detection failed: %w", err)
	}

	result.Collisions = len(collisionResult.Collisions)
	for _, collision := range collisionResult.Collisions {
		result.CollisionIDs = append(result.CollisionIDs, collision.ID)
	}

	// With hash IDs, "collisions" (same ID, different content) are actually UPDATES
	// Hash IDs are based on creation content and remain stable across updates
	// So same ID + different fields = normal update operation, not a collision
	// The collisionResult.Collisions list represents issues that *may* be updated
	// Note: We don't pre-count updates here - upsertIssues will count them after
	// checking timestamps to ensure we only update when incoming is newer (bd-e55c)

	// Phase 4: Renames removed - obsolete with hash IDs (bd-8e05)
	// Hash-based IDs are content-addressed, so renames don't occur

	if opts.DryRun {
		result.Created = len(collisionResult.NewIssues) + len(collisionResult.Renames)
		result.Unchanged = len(collisionResult.ExactMatches)
	}

	return issues, nil
}

// buildHashMap creates a map of content hash → issue for O(1) lookup
func buildHashMap(issues []*types.Issue) map[string]*types.Issue {
	result := make(map[string]*types.Issue)
	for _, issue := range issues {
		if issue.ContentHash != "" {
			result[issue.ContentHash] = issue
		}
	}
	return result
}

// buildIDMap creates a map of ID → issue for O(1) lookup
func buildIDMap(issues []*types.Issue) map[string]*types.Issue {
	result := make(map[string]*types.Issue)
	for _, issue := range issues {
		result[issue.ID] = issue
	}
	return result
}

// handleRename handles content match with different IDs (rename detected)
// Returns the old ID that was deleted (if any), or empty string if no deletion occurred
func handleRename(ctx context.Context, s *sqlite.SQLiteStorage, existing *types.Issue, incoming *types.Issue) (string, error) {
	// Check if target ID already exists with the same content (race condition)
	// This can happen when multiple clones import the same rename simultaneously
	targetIssue, err := s.GetIssue(ctx, incoming.ID)
	if err == nil && targetIssue != nil {
		// Target ID exists - check if it has the same content
		if targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			// Same content - check if old ID still exists and delete it
			deletedID := ""
			existingCheck, checkErr := s.GetIssue(ctx, existing.ID)
			if checkErr == nil && existingCheck != nil {
				if err := s.DeleteIssue(ctx, existing.ID); err != nil {
					return "", fmt.Errorf("failed to delete old ID %s: %w", existing.ID, err)
				}
				deletedID = existing.ID
			}
			// The rename is already complete in the database
			return deletedID, nil
		}
		// With hash IDs, same content should produce same ID. If we find same content
		// with different IDs, treat it as an update to the existing ID (not a rename).
		// This handles edge cases like test data, legacy data, or data corruption.
		// Keep the existing ID and update fields if incoming has newer timestamp.
		if incoming.UpdatedAt.After(existing.UpdatedAt) {
			// Update existing issue with incoming's fields
			updates := map[string]interface{}{
				"title":               incoming.Title,
				"description":         incoming.Description,
				"design":              incoming.Design,
				"acceptance_criteria": incoming.AcceptanceCriteria,
				"notes":               incoming.Notes,
				"external_ref":        incoming.ExternalRef,
				"status":              incoming.Status,
				"priority":            incoming.Priority,
				"issue_type":          incoming.IssueType,
				"assignee":            incoming.Assignee,
			}
			if err := s.UpdateIssue(ctx, existing.ID, updates, "importer"); err != nil {
				return "", fmt.Errorf("failed to update issue %s: %w", existing.ID, err)
			}
		}
		return "", nil
		
		/* OLD CODE REMOVED (bd-8e05)
		// Different content - this is a collision during rename
		// Allocate a new ID for the incoming issue instead of using the desired ID
		prefix, err := s.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix == "" {
			prefix = "bd"
		}
		
		oldID := existing.ID
		
		// Retry up to 3 times to handle concurrent ID allocation
		const maxRetries = 3
		for attempt := 0; attempt < maxRetries; attempt++ {
			newID, err := s.AllocateNextID(ctx, prefix)
			if err != nil {
				return "", fmt.Errorf("failed to generate new ID for rename collision: %w", err)
			}
			
			// Update incoming issue to use the new ID
			incoming.ID = newID
			
			// Delete old ID (only on first attempt)
			if attempt == 0 {
				if err := s.DeleteIssue(ctx, oldID); err != nil {
					return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
				}
			}
			
			// Create with new ID
			err = s.CreateIssue(ctx, incoming, "import-rename-collision")
			if err == nil {
				// Success!
				return oldID, nil
			}
			
			// Check if it's a UNIQUE constraint error
			if !sqlite.IsUniqueConstraintError(err) {
				// Not a UNIQUE constraint error, fail immediately
				return "", fmt.Errorf("failed to create renamed issue with collision resolution %s: %w", newID, err)
			}
			
			// UNIQUE constraint error - retry with new ID
			if attempt == maxRetries-1 {
				// Last attempt failed
				return "", fmt.Errorf("failed to create renamed issue with collision resolution after %d retries: %w", maxRetries, err)
			}
		}
		
		// Note: We don't update text references here because it would be too expensive
		// to scan all issues during every import. Text references to the old ID will
		// eventually be cleaned up by manual reference updates or remain as stale.
		// This is acceptable because the old ID no longer exists in the system.
		
		return oldID, nil
		*/
	}

	// Check if old ID still exists (it might have been deleted by another clone)
	existingCheck, checkErr := s.GetIssue(ctx, existing.ID)
	if checkErr != nil || existingCheck == nil {
		// Old ID doesn't exist - the rename must have been completed by another clone
		// Verify that target exists with correct content
		targetCheck, targetErr := s.GetIssue(ctx, incoming.ID)
		if targetErr == nil && targetCheck != nil && targetCheck.ComputeContentHash() == incoming.ComputeContentHash() {
			return "", nil
		}
		return "", fmt.Errorf("old ID %s doesn't exist and target ID %s is not as expected", existing.ID, incoming.ID)
	}

	// Delete old ID
	oldID := existing.ID
	if err := s.DeleteIssue(ctx, oldID); err != nil {
		return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
	}

	// Create with new ID
	if err := s.CreateIssue(ctx, incoming, "import-rename"); err != nil {
		// If UNIQUE constraint error, it's likely another clone created it concurrently
		if sqlite.IsUniqueConstraintError(err) {
			// Check if target exists with same content
			targetIssue, getErr := s.GetIssue(ctx, incoming.ID)
			if getErr == nil && targetIssue != nil && targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
				// Same content - rename already complete, this is OK
				return oldID, nil
			}
		}
		return "", fmt.Errorf("failed to create renamed issue %s: %w", incoming.ID, err)
	}

	// Reference updates removed - obsolete with hash IDs (bd-8e05)
	// Hash-based IDs are deterministic, so no reference rewriting needed

	return oldID, nil
}

// upsertIssues creates new issues or updates existing ones using content-first matching
func upsertIssues(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options, result *Result) error {
	// Get all DB issues once
	dbIssues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get DB issues: %w", err)
	}
	
	dbByHash := buildHashMap(dbIssues)
	dbByID := buildIDMap(dbIssues)
	
	// Build external_ref map for O(1) lookup
	dbByExternalRef := make(map[string]*types.Issue)
	for _, issue := range dbIssues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			dbByExternalRef[*issue.ExternalRef] = issue
		}
	}

	// Track what we need to create
	var newIssues []*types.Issue
	seenHashes := make(map[string]bool)

	for _, incoming := range issues {
		hash := incoming.ContentHash
		if hash == "" {
			// Shouldn't happen (computed earlier), but be defensive
			hash = incoming.ComputeContentHash()
			incoming.ContentHash = hash
		}

		// Skip duplicates within incoming batch
		if seenHashes[hash] {
			result.Skipped++
			continue
		}
		seenHashes[hash] = true
		
		// Phase 0: Match by external_ref first (if present)
		// This enables re-syncing from external systems (Jira, GitHub, Linear)
		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			if existing, found := dbByExternalRef[*incoming.ExternalRef]; found {
				// Found match by external_ref - update the existing issue
				if !opts.SkipUpdate {
					// Check timestamps - only update if incoming is newer (bd-e55c)
					if !incoming.UpdatedAt.After(existing.UpdatedAt) {
						// Local version is newer or same - skip update
						result.Unchanged++
						continue
					}
					
					// Build updates map
					updates := make(map[string]interface{})
					updates["title"] = incoming.Title
					updates["description"] = incoming.Description
					updates["status"] = incoming.Status
					updates["priority"] = incoming.Priority
					updates["issue_type"] = incoming.IssueType
					updates["design"] = incoming.Design
					updates["acceptance_criteria"] = incoming.AcceptanceCriteria
					updates["notes"] = incoming.Notes
					updates["closed_at"] = incoming.ClosedAt
					
					if incoming.Assignee != "" {
					 updates["assignee"] = incoming.Assignee
					} else {
					 updates["assignee"] = nil
					}
					
					if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
					 updates["external_ref"] = *incoming.ExternalRef
					} else {
					 updates["external_ref"] = nil
				}
					
					// Only update if data actually changed
					if IssueDataChanged(existing, updates) {
						if err := sqliteStore.UpdateIssue(ctx, existing.ID, updates, "import"); err != nil {
							return fmt.Errorf("error updating issue %s (matched by external_ref): %w", existing.ID, err)
						}
						result.Updated++
					} else {
						result.Unchanged++
					}
				} else {
					result.Skipped++
				}
				continue
			}
		}

		// Phase 1: Match by content hash
		if existing, found := dbByHash[hash]; found {
			// Same content exists
			if existing.ID == incoming.ID {
				// Exact match (same content, same ID) - idempotent case
				result.Unchanged++
			} else {
				// Same content, different ID - rename detected
				if !opts.SkipUpdate {
					deletedID, err := handleRename(ctx, sqliteStore, existing, incoming)
					if err != nil {
						return fmt.Errorf("failed to handle rename %s -> %s: %w", existing.ID, incoming.ID, err)
					}
					// Remove the deleted ID from the map to prevent stale references
					if deletedID != "" {
						delete(dbByID, deletedID)
					}
					result.Updated++
				} else {
					result.Skipped++
				}
			}
			continue
		}

		// Phase 2: New content - check for ID collision
		if existingWithID, found := dbByID[incoming.ID]; found {
			// ID exists but different content - this is a collision
			// The update should have been detected earlier by detectUpdates
			// If we reach here, it means collision wasn't resolved - treat as update
			if !opts.SkipUpdate {
				// Check timestamps - only update if incoming is newer (bd-e55c)
				if !incoming.UpdatedAt.After(existingWithID.UpdatedAt) {
					// Local version is newer or same - skip update
					result.Unchanged++
					continue
				}
				
				// Build updates map
				updates := make(map[string]interface{})
				updates["title"] = incoming.Title
				updates["description"] = incoming.Description
				updates["status"] = incoming.Status
				updates["priority"] = incoming.Priority
				updates["issue_type"] = incoming.IssueType
				updates["design"] = incoming.Design
				updates["acceptance_criteria"] = incoming.AcceptanceCriteria
				updates["notes"] = incoming.Notes
			updates["closed_at"] = incoming.ClosedAt

				if incoming.Assignee != "" {
				 updates["assignee"] = incoming.Assignee
				} else {
				 updates["assignee"] = nil
			}

				if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
				 updates["external_ref"] = *incoming.ExternalRef
				} else {
				 updates["external_ref"] = nil
			}

				// Only update if data actually changed
				if IssueDataChanged(existingWithID, updates) {
					if err := sqliteStore.UpdateIssue(ctx, incoming.ID, updates, "import"); err != nil {
						return fmt.Errorf("error updating issue %s: %w", incoming.ID, err)
					}
					result.Updated++
				} else {
					result.Unchanged++
				}
			} else {
				result.Skipped++
			}
		} else {
			// Truly new issue
			newIssues = append(newIssues, incoming)
		}
	}

// Batch create all new issues
// Sort by hierarchy depth to ensure parents are created before children
if len(newIssues) > 0 {
 sort.Slice(newIssues, func(i, j int) bool {
  depthI := strings.Count(newIssues[i].ID, ".")
 depthJ := strings.Count(newIssues[j].ID, ".")
			if depthI != depthJ {
  return depthI < depthJ // Shallower first
 }
 return newIssues[i].ID < newIssues[j].ID // Stable sort
})

// Create in batches by depth level (max depth 3)
		for depth := 0; depth <= 3; depth++ {
   var batchForDepth []*types.Issue
   for _, issue := range newIssues {
    if strings.Count(issue.ID, ".") == depth {
    batchForDepth = append(batchForDepth, issue)
				}
			}
			if len(batchForDepth) > 0 {
				batchOpts := sqlite.BatchCreateOptions{
					OrphanHandling:       opts.OrphanHandling,
					SkipPrefixValidation: opts.SkipPrefixValidation,
				}
				if err := sqliteStore.CreateIssuesWithFullOptions(ctx, batchForDepth, "import", batchOpts); err != nil {
					return fmt.Errorf("error creating depth-%d issues: %w", depth, err)
				}
				result.Created += len(batchForDepth)
			}
		}
	}

	// REMOVED (bd-c7af): Counter sync after import - no longer needed with hash IDs

	return nil
}

// importDependencies imports dependency relationships
func importDependencies(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options, result *Result) error {
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
				// Check for FOREIGN KEY constraint violation
				if sqlite.IsForeignKeyConstraintError(err) {
					// Log warning and track skipped dependency
					depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
					fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to missing reference: %s\n", depDesc)
					if result != nil {
						result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
					}
					continue
				}

				// For non-FK errors, respect strict mode
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				continue
			}
		}
	}

	return nil
}

// importLabels imports labels for issues
func importLabels(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options) error {
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
					continue
				}
			}
		}
	}

	return nil
}

// importComments imports comments for issues
func importComments(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, issues []*types.Issue, opts Options) error {
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
					continue
				}
			}
		}
	}

	return nil
}

// purgeDeletedIssues converts DB issues to tombstones if they are in the deletions
// manifest but not in the incoming JSONL. This enables deletion propagation across clones.
// Also uses git history fallback for deletions that were pruned from the manifest,
// unless opts.NoGitHistory is set (useful during JSONL filename migrations).
//
// Note (bd-dve): With inline tombstones, most deletions are now handled during import
// via convertDeletionToTombstone. This function primarily handles:
// 1. DB-only issues that need to be tombstoned (not in JSONL at all)
// 2. Git history fallback for pruned deletions
func purgeDeletedIssues(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, dbPath string, jsonlIssues []*types.Issue, opts Options, result *Result) error {
	// Get deletions manifest path (same directory as database)
	beadsDir := filepath.Dir(dbPath)
	deletionsPath := deletions.DefaultPath(beadsDir)

	// Load deletions manifest (gracefully handles missing/empty file)
	loadResult, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		return fmt.Errorf("failed to load deletions manifest: %w", err)
	}

	// Log any warnings from loading
	for _, warning := range loadResult.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}

	// Build set of IDs in the incoming JSONL for O(1) lookup
	jsonlIDs := make(map[string]bool, len(jsonlIssues))
	for _, issue := range jsonlIssues {
		jsonlIDs[issue.ID] = true
	}

	// Get all DB issues (exclude existing tombstones - they're already deleted)
	dbIssues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get DB issues: %w", err)
	}

	// Collect IDs that need git history check (not in JSONL, not in manifest)
	var needGitCheck []string

	// Find DB issues that:
	// 1. Are NOT in the JSONL (not synced from remote)
	// 2. ARE in the deletions manifest (were deleted elsewhere)
	// 3. Are NOT already tombstones
	for _, dbIssue := range dbIssues {
		if jsonlIDs[dbIssue.ID] {
			// Issue is in JSONL, keep it (tombstone or not)
			continue
		}

		if del, found := loadResult.Records[dbIssue.ID]; found {
			// Issue is in deletions manifest - convert to tombstone (bd-dve)
			if err := sqliteStore.CreateTombstone(ctx, dbIssue.ID, del.Actor, del.Reason); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create tombstone for %s: %v\n", dbIssue.ID, err)
				continue
			}

			// Log the tombstone creation with metadata
			fmt.Fprintf(os.Stderr, "Tombstoned %s (deleted %s by %s", dbIssue.ID, del.Timestamp.Format("2006-01-02 15:04:05"), del.Actor)
			if del.Reason != "" {
				fmt.Fprintf(os.Stderr, ", reason: %s", del.Reason)
			}
			fmt.Fprintf(os.Stderr, ")\n")

			result.Purged++
			result.PurgedIDs = append(result.PurgedIDs, dbIssue.ID)
		} else {
			// Not in JSONL and not in deletions manifest
			// This could be:
			// 1. Local work (new issue not yet exported)
			// 2. Deletion was pruned from manifest (check git history)
			needGitCheck = append(needGitCheck, dbIssue.ID)
		}
	}

	// Git history fallback for potential pruned deletions
	// Skip if --no-git-history flag is set (prevents spurious deletions during JSONL migrations)
	if len(needGitCheck) > 0 && !opts.NoGitHistory {
		deletedViaGit := checkGitHistoryForDeletions(beadsDir, needGitCheck)

		// Safety guard (bd-21a): Prevent mass deletion when JSONL appears reset
		// If git-history-backfill would delete a large percentage of issues,
		// this likely indicates the JSONL was reset (git reset, branch switch, etc.)
		// rather than intentional deletions
		totalDBIssues := len(dbIssues)
		deleteCount := len(deletedViaGit)

		if deleteCount > 0 && totalDBIssues > 0 {
			deletePercent := float64(deleteCount) / float64(totalDBIssues) * 100

			// Abort if would delete >50% of issues - this is almost certainly a reset
			if deletePercent > 50 {
				fmt.Fprintf(os.Stderr, "Warning: git-history-backfill would tombstone %d of %d issues (%.1f%%) - aborting\n",
					deleteCount, totalDBIssues, deletePercent)
				fmt.Fprintf(os.Stderr, "This usually means the JSONL was reset (git reset, branch switch, etc.)\n")
				fmt.Fprintf(os.Stderr, "If these are legitimate deletions, add them to deletions.jsonl manually\n")
				// Don't delete anything - abort the backfill
				deleteCount = 0
				deletedViaGit = nil
			} else if deleteCount > 10 {
				// Warn (but proceed) if deleting >10 issues
				fmt.Fprintf(os.Stderr, "Warning: git-history-backfill will tombstone %d issues (%.1f%% of %d total)\n",
					deleteCount, deletePercent, totalDBIssues)
			}
		}

		for _, id := range deletedViaGit {
			// Backfill the deletions manifest (self-healing)
			backfillRecord := deletions.DeletionRecord{
				ID:        id,
				Timestamp: time.Now().UTC(),
				Actor:     "git-history-backfill",
				Reason:    "recovered from git history (pruned from manifest)",
			}
			if err := deletions.AppendDeletion(deletionsPath, backfillRecord); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to backfill deletion record for %s: %v\n", id, err)
			}

			// Convert to tombstone (bd-dve)
			if err := sqliteStore.CreateTombstone(ctx, id, "git-history-backfill", "recovered from git history (pruned from manifest)"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create tombstone for %s (git-recovered): %v\n", id, err)
				continue
			}

			fmt.Fprintf(os.Stderr, "Tombstoned %s (recovered from git history, pruned from manifest)\n", id)
			result.Purged++
			result.PurgedIDs = append(result.PurgedIDs, id)
		}
	} else if len(needGitCheck) > 0 && opts.NoGitHistory {
		// Log that we skipped git history check due to flag
		fmt.Fprintf(os.Stderr, "Skipped git history check for %d issue(s) (--no-git-history flag set)\n", len(needGitCheck))
	}

	return nil
}

// checkGitHistoryForDeletions checks if IDs were ever in the JSONL history.
// Returns the IDs that were found in git history (meaning they were deleted,
// and the deletion record was pruned from the manifest).
//
// Uses batched git log search for efficiency when checking multiple IDs.
func checkGitHistoryForDeletions(beadsDir string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}

	// Find the actual git repo root using git rev-parse (bd-bhd)
	// This handles monorepos and nested projects where .beads isn't at repo root
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = beadsDir
	output, err := cmd.Output()
	if err != nil {
		// Not in a git repo or git not available - can't do history check
		return nil
	}
	repoRoot := strings.TrimSpace(string(output))

	// Compute relative path from repo root to issues.jsonl
	// beadsDir is absolute, compute its path relative to repoRoot
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return nil
	}

	relBeadsDir, err := filepath.Rel(repoRoot, absBeadsDir)
	if err != nil {
		return nil
	}

	// Build JSONL path relative to repo root (bd-6xd: issues.jsonl is canonical)
	jsonlPath := filepath.Join(relBeadsDir, "issues.jsonl")

	var deleted []string

	// For efficiency, batch IDs into a single git command when possible
	// We use git log with -S to search for string additions/removals
	if len(ids) <= 10 {
		// Small batch: check each ID individually for accuracy
		for _, id := range ids {
			if wasEverInJSONL(repoRoot, jsonlPath, id) {
				deleted = append(deleted, id)
			}
		}
	} else {
		// Large batch: use grep pattern for efficiency
		// This may have some false positives, but is much faster
		deleted = batchCheckGitHistory(repoRoot, jsonlPath, ids)
	}

	return deleted
}

// gitHistoryTimeout is the maximum time to wait for git history searches.
// Prevents hangs on large repositories (bd-f0n).
const gitHistoryTimeout = 30 * time.Second

// wasEverInJSONL checks if a single ID was ever present in the JSONL via git history.
// Returns true if the ID was found in any commit (added or removed).
// The caller is responsible for confirming the ID is NOT currently in JSONL
// to determine that it was deleted (vs still present).
func wasEverInJSONL(repoRoot, jsonlPath, id string) bool {
	// git log --all -S "\"id\":\"bd-xxx\"" --oneline -- .beads/issues.jsonl
	// This searches for commits that added or removed the ID string
	// Note: -S uses literal string matching, not regex, so no escaping needed
	searchPattern := fmt.Sprintf(`"id":"%s"`, id)

	// Use context with timeout to prevent hangs on large repos (bd-f0n)
	ctx, cancel := context.WithTimeout(context.Background(), gitHistoryTimeout)
	defer cancel()

	// #nosec G204 - searchPattern is constructed from validated issue IDs
	cmd := exec.CommandContext(ctx, "git", "log", "--all", "-S", searchPattern, "--oneline", "--", jsonlPath)
	cmd.Dir = repoRoot

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // Ignore stderr

	if err := cmd.Run(); err != nil {
		// Git command failed - could be shallow clone, not a git repo, timeout, etc.
		// Conservative: assume issue is local work, don't delete
		return false
	}

	// If output is non-empty, the ID was found in git history (was once in JSONL).
	// Since caller already verified ID is NOT currently in JSONL, this means deleted.
	return len(bytes.TrimSpace(stdout.Bytes())) > 0
}

// batchCheckGitHistory checks multiple IDs at once using git log with pattern matching.
// Returns the IDs that were found in git history.
func batchCheckGitHistory(repoRoot, jsonlPath string, ids []string) []string {
	// Build a regex pattern to match any of the IDs
	// Pattern: "id":"bd-xxx"|"id":"bd-yyy"|...
	// Escape regex special characters in IDs to avoid malformed patterns (bd-bgs)
	patterns := make([]string, 0, len(ids))
	for _, id := range ids {
		escapedID := regexp.QuoteMeta(id)
		patterns = append(patterns, fmt.Sprintf(`"id":"%s"`, escapedID))
	}
	searchPattern := strings.Join(patterns, "|")

	// Use context with timeout to prevent hangs on large repos (bd-f0n)
	ctx, cancel := context.WithTimeout(context.Background(), gitHistoryTimeout)
	defer cancel()

	// Use git log -G (regex) for batch search
	// #nosec G204 - searchPattern is constructed from validated issue IDs
	cmd := exec.CommandContext(ctx, "git", "log", "--all", "-G", searchPattern, "-p", "--", jsonlPath)
	cmd.Dir = repoRoot

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // Ignore stderr

	if err := cmd.Run(); err != nil {
		// Git command failed (timeout, shallow clone, etc.) - fall back to individual checks
		// Individual checks also have timeout protection
		var deleted []string
		for _, id := range ids {
			if wasEverInJSONL(repoRoot, jsonlPath, id) {
				deleted = append(deleted, id)
			}
		}
		return deleted
	}

	output := stdout.String()
	if output == "" {
		return nil
	}

	// Parse output to find which IDs were actually in history
	var deleted []string
	for _, id := range ids {
		searchStr := fmt.Sprintf(`"id":"%s"`, id)
		if strings.Contains(output, searchStr) {
			deleted = append(deleted, id)
		}
	}

	return deleted
}

// Helper functions

// convertDeletionToTombstone converts a legacy DeletionRecord to a tombstone Issue.
// This is used during import to migrate from deletions.jsonl to inline tombstones (bd-dve).
// Note: We use zero for priority to indicate unknown (bd-9auw).
// IssueType must be a valid type for validation, so we use TypeTask as default.
func convertDeletionToTombstone(id string, del deletions.DeletionRecord) *types.Issue {
	deletedAt := del.Timestamp
	return &types.Issue{
		ID:           id,
		Title:        "(deleted)",
		Description:  "",
		Status:       types.StatusTombstone,
		Priority:     0,              // Unknown priority (0 = unset, distinguishes from user-set values)
		IssueType:    types.TypeTask, // Default type (must be valid for validation)
		CreatedAt:    del.Timestamp,
		UpdatedAt:    del.Timestamp,
		DeletedAt:    &deletedAt,
		DeletedBy:    del.Actor,
		DeleteReason: del.Reason,
		OriginalType: "", // Not available in legacy deletions.jsonl
	}
}

func GetPrefixList(prefixes map[string]int) []string {
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

func validateNoDuplicateExternalRefs(issues []*types.Issue, clearDuplicates bool, result *Result) error {
	seen := make(map[string][]string)
	
	for _, issue := range issues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			ref := *issue.ExternalRef
			seen[ref] = append(seen[ref], issue.ID)
		}
	}

	var duplicates []string
	duplicateIssueIDs := make(map[string]bool)
	for ref, issueIDs := range seen {
		if len(issueIDs) > 1 {
			duplicates = append(duplicates, fmt.Sprintf("external_ref '%s' appears in issues: %v", ref, issueIDs))
			// Track all duplicate issue IDs except the first one (keep first, clear rest)
			for i := 1; i < len(issueIDs); i++ {
				duplicateIssueIDs[issueIDs[i]] = true
			}
		}
	}

	if len(duplicates) > 0 {
		if clearDuplicates {
			// Clear duplicate external_refs (keep first occurrence, clear rest)
			for _, issue := range issues {
				if duplicateIssueIDs[issue.ID] {
					issue.ExternalRef = nil
				}
			}
			// Track how many were cleared in result
			if result != nil {
				result.Skipped += len(duplicateIssueIDs)
			}
			return nil
		}
		
		sort.Strings(duplicates)
		return fmt.Errorf("batch import contains duplicate external_ref values:\n%s\n\nUse --clear-duplicate-external-refs to automatically clear duplicates", strings.Join(duplicates, "\n"))
	}

	return nil
}
