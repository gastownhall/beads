package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// isBusyLockError checks if an error is a transient SQLite busy/locked error.
func isBusyLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

// withBusyRetry retries an operation with bounded exponential backoff on transient busy locks.
func withBusyRetry(op func() error) error {
	const maxAttempts = 6
	backoff := 10 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if !isBusyLockError(err) {
			return err
		}
		lastErr = err
		if attempt < maxAttempts {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return lastErr
}

// OrphanHandling is an alias for storage.OrphanHandling for backward compatibility.
// Deprecated: Use storage.OrphanHandling directly.
type OrphanHandling = storage.OrphanHandling

// Orphan handling constants - aliases for storage package constants.
// Deprecated: Use storage.OrphanStrict, etc. directly.
const (
	OrphanStrict    = storage.OrphanStrict
	OrphanResurrect = storage.OrphanResurrect
	OrphanSkip      = storage.OrphanSkip
	OrphanAllow     = storage.OrphanAllow
)

// Options contains import configuration
type Options struct {
	DryRun                     bool                 // Preview changes without applying them
	SkipUpdate                 bool                 // Skip updating existing issues (create-only mode)
	Strict                     bool                 // Fail on any error (dependencies, labels, etc.)
	RenameOnImport             bool                 // Rename imported issues to match database prefix
	SkipPrefixValidation       bool                 // Skip prefix validation (for auto-import)
	OrphanHandling             OrphanHandling       // How to handle missing parent issues (default: allow)
	ClearDuplicateExternalRefs bool                 // Clear duplicate external_ref values instead of erroring
	ProtectLocalExportIDs      map[string]time.Time // IDs from left snapshot with timestamps for timestamp-aware protection (GH#865)
	DeletionIDs                []string             // IDs to delete (from JSONL deletion markers)
}

// Result contains statistics about the import operation
type Result struct {
	Created             int               // New issues created
	Updated             int               // Existing issues updated
	Unchanged           int               // Existing issues that matched exactly (idempotent)
	Skipped             int               // Issues skipped (duplicates, errors)
	Deleted             int               // Issues deleted (from deletion markers)
	Collisions          int               // Collisions detected
	IDMapping           map[string]string // Mapping of remapped IDs (old -> new)
	CollisionIDs        []string          // IDs that collided
	PrefixMismatch      bool              // Prefix mismatch detected
	ExpectedPrefix      string            // Database configured prefix
	MismatchPrefixes    map[string]int    // Map of mismatched prefixes to count
	SkippedDependencies []string          // Dependencies skipped due to FK constraint violations
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

	if store == nil {
		return nil, fmt.Errorf("import requires an initialized storage backend")
	}

	// Normalize Linear external_refs to canonical form to avoid slug-based duplicates.
	for _, issue := range issues {
		if issue.ExternalRef == nil || *issue.ExternalRef == "" {
			continue
		}
		if linear.IsLinearExternalRef(*issue.ExternalRef) {
			if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
				issue.ExternalRef = &canonical
			}
		}
	}

	// Compute content hashes for all incoming issues
	// Always recompute to avoid stale/incorrect JSONL hashes
	for _, issue := range issues {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Auto-detect wisps by ID pattern and set ephemeral flag
	// This prevents orphaned wisp entries in JSONL from polluting bd ready
	// Pattern: *-wisp-* indicates ephemeral patrol/workflow instances
	for _, issue := range issues {
		if strings.Contains(issue.ID, "-wisp-") && !issue.Ephemeral {
			issue.Ephemeral = true
		}
	}

	// GH#686: In multi-repo mode, skip prefix validation for all issues.
	// Issues from additional repos have their own prefixes which are expected and correct.
	if config.GetMultiRepoConfig() != nil && !opts.SkipPrefixValidation {
		opts.SkipPrefixValidation = true
	}

	// Clear export_hashes before import to prevent staleness
	// Import operations may add/update issues, so export_hashes entries become invalid
	if !opts.DryRun {
		if err := store.ClearAllExportHashes(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear export_hashes before import: %v\n", err)
		}
	}

	// Read orphan handling from config if not explicitly set
	if opts.OrphanHandling == "" {
		value, err := store.GetConfig(ctx, "import.orphan_handling")
		if err == nil && value != "" {
			switch OrphanHandling(value) {
			case OrphanStrict, OrphanResurrect, OrphanSkip, OrphanAllow:
				opts.OrphanHandling = OrphanHandling(value)
			default:
				opts.OrphanHandling = OrphanAllow
			}
		} else {
			opts.OrphanHandling = OrphanAllow
		}
	}

	// Check and handle prefix mismatches
	var err error
	issues, err = handlePrefixMismatch(ctx, store, issues, opts, result)
	if err != nil {
		return result, err
	}

	// Validate no duplicate external_ref values in batch
	if err := validateNoDuplicateExternalRefs(issues, opts.ClearDuplicateExternalRefs, result); err != nil {
		return result, err
	}

	// Process deletion markers before issue upserts
	// This ensures deletions are applied before any updates that might conflict
	if len(opts.DeletionIDs) > 0 {
		if opts.DryRun {
			// In dry-run mode, just count how many would be deleted
			for _, id := range opts.DeletionIDs {
				// Check if issue exists
				if _, err := store.GetIssue(ctx, id); err == nil {
					result.Deleted++
				}
			}
		} else {
			for _, id := range opts.DeletionIDs {
				// Check if issue exists before trying to delete
				if _, err := store.GetIssue(ctx, id); err != nil {
					// Issue doesn't exist - might already be deleted, skip silently
					continue
				}
				if err := store.DeleteIssue(ctx, id); err != nil {
					if opts.Strict {
						return result, fmt.Errorf("failed to delete issue %s: %w", id, err)
					}
					// Non-strict mode: log warning and continue
					fmt.Fprintf(os.Stderr, "Warning: failed to delete issue %s: %v\n", id, err)
					continue
				}
				result.Deleted++
			}
		}
	}

	// Detect and resolve collisions
	issues, err = detectUpdates(ctx, store, issues, opts, result)
	if err != nil {
		return result, err
	}
	if opts.DryRun {
		return result, nil
	}

	// Apply changes atomically when transactions are supported.
	if err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Upsert issues (create new or update existing)
		if err := upsertIssuesTx(ctx, tx, store, issues, opts, result); err != nil {
			return err
		}
		// Import dependencies
		if err := importDependenciesTx(ctx, tx, issues, opts, result); err != nil {
			return err
		}
		// Import labels
		if err := importLabelsTx(ctx, tx, issues, opts); err != nil {
			return err
		}
		// Import comments (timestamp-preserving)
		if err := importCommentsTx(ctx, tx, issues, opts); err != nil {
			return err
		}
		return nil
	}); err != nil {
		// Some backends (e.g., --no-db) don't support transactions.
		// Fall back to non-transactional behavior in that case.
		if strings.Contains(err.Error(), "not supported") {
			if err := upsertIssues(ctx, store, issues, opts, result); err != nil {
				return nil, err
			}
			if err := importDependencies(ctx, store, issues, opts, result); err != nil {
				return nil, err
			}
			if err := importLabels(ctx, store, issues, opts); err != nil {
				return nil, err
			}
			if err := importComments(ctx, store, issues, opts); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return result, nil
}

// handlePrefixMismatch checks and handles prefix mismatches.
// Returns a filtered issues slice with mismatched-prefix issues reported.
func handlePrefixMismatch(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options, result *Result) ([]*types.Issue, error) {
	configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return nil, fmt.Errorf("failed to get configured prefix: %w", err)
	}

	// Only validate prefixes if a prefix is configured
	if strings.TrimSpace(configuredPrefix) == "" {
		if opts.RenameOnImport {
			return nil, fmt.Errorf("cannot rename: issue_prefix not configured in database")
		}
		return issues, nil
	}

	result.ExpectedPrefix = configuredPrefix

	// Read allowed_prefixes config for additional valid prefixes (e.g., mol-*)
	allowedPrefixesConfig, _ := store.GetConfig(ctx, "allowed_prefixes")

	// Get beads directory from database path for route lookup
	beadsDir := filepath.Dir(store.Path())

	// GH#686: In multi-repo mode, allow all prefixes (nil = allow all)
	// Also include prefixes from routes.jsonl for multi-rig setups (Gas Town)
	allowedPrefixes := buildAllowedPrefixSet(configuredPrefix, allowedPrefixesConfig, beadsDir)
	if allowedPrefixes == nil {
		return issues, nil
	}

	// Analyze prefixes in imported issues
	var filteredIssues []*types.Issue

	for _, issue := range issues {
		// GH#422: Check if issue ID starts with configured prefix directly
		// rather than extracting/guessing. This handles multi-hyphen prefixes
		// like "asianops-audit-" correctly.
		// Also check against allowed_prefixes config
		prefixMatches := false
		for prefix := range allowedPrefixes {
			if strings.HasPrefix(issue.ID, prefix+"-") {
				prefixMatches = true
				break
			}
		}
		if !prefixMatches {
			// Extract prefix for error reporting (best effort)
			prefix := utils.ExtractIssuePrefix(issue.ID)
			result.PrefixMismatch = true
			result.MismatchPrefixes[prefix]++
			filteredIssues = append(filteredIssues, issue)
		} else {
			// Correct prefix - keep the issue
			filteredIssues = append(filteredIssues, issue)
		}
	}

	if result.PrefixMismatch {
		// If not handling the mismatch, return error
		if !opts.RenameOnImport && !opts.DryRun && !opts.SkipPrefixValidation {
			return nil, fmt.Errorf("prefix mismatch detected: database uses '%s-' but found issues with prefixes: %v (use --rename-on-import to automatically fix)", configuredPrefix, GetPrefixList(result.MismatchPrefixes))
		}
	}

	// Handle rename-on-import if requested
	if result.PrefixMismatch && opts.RenameOnImport && !opts.DryRun {
		if err := RenameImportedIssuePrefixes(issues, configuredPrefix); err != nil {
			return nil, fmt.Errorf("failed to rename prefixes: %w", err)
		}
		// After renaming, clear the mismatch flags since we fixed them
		result.PrefixMismatch = false
		result.MismatchPrefixes = make(map[string]int)
		return issues, nil
	}

	// Return original issues if no filtering needed
	return issues, nil
}

// detectUpdates detects same-ID scenarios (which are updates with hash IDs, not collisions)
func detectUpdates(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options, result *Result) ([]*types.Issue, error) {
	// Backend-agnostic collision detection:
	// "collision" here means: same ID exists but content hash differs.
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, fmt.Errorf("collision detection failed: %w", err)
	}
	dbByID := buildIDMap(dbIssues)

	newCount := 0
	exactCount := 0
	collisionCount := 0
	for _, incoming := range issues {
		existing, ok := dbByID[incoming.ID]
		if !ok || existing == nil {
			newCount++
			continue
		}
		// Exact content match is idempotent.
		if existing.ContentHash != "" && incoming.ContentHash != "" && existing.ContentHash == incoming.ContentHash {
			exactCount++
			continue
		}
		// Treat same ID + different content as an update candidate.
		collisionCount++
		result.CollisionIDs = append(result.CollisionIDs, incoming.ID)
	}

	result.Collisions = collisionCount
	if opts.DryRun {
		result.Created = newCount
		result.Updated = collisionCount
		result.Unchanged = exactCount
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
func handleRename(ctx context.Context, s storage.Storage, existing *types.Issue, incoming *types.Issue) (string, error) {
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
				if err := withBusyRetry(func() error { return s.DeleteIssue(ctx, existing.ID) }); err != nil {
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
	if err := withBusyRetry(func() error { return s.DeleteIssue(ctx, oldID) }); err != nil {
		return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
	}

	// Create with new ID
	if err := withBusyRetry(func() error { return s.CreateIssue(ctx, incoming, "import-rename") }); err != nil {
		// Another writer may have created the target concurrently. If the target now exists
		// with the same content, treat the rename as already complete.
		targetIssue, getErr := s.GetIssue(ctx, incoming.ID)
		if getErr == nil && targetIssue != nil && targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			return oldID, nil
		}
		return "", fmt.Errorf("failed to create renamed issue %s: %w", incoming.ID, err)
	}

	// Reference updates removed - obsolete with hash IDs
	// Hash-based IDs are deterministic, so no reference rewriting needed

	return oldID, nil
}

// handleRenameTx is the transaction-aware version of handleRename.
// It operates within an existing transaction instead of starting new ones.
func handleRenameTx(ctx context.Context, tx storage.Transaction, existing *types.Issue, incoming *types.Issue) (string, error) {
	// Check if target ID already exists with the same content (race condition)
	// This can happen when multiple clones import the same rename simultaneously
	targetIssue, err := tx.GetIssue(ctx, incoming.ID)
	if err == nil && targetIssue != nil {
		// Target ID exists - check if it has the same content
		if targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			// Same content - check if old ID still exists and delete it
			deletedID := ""
			existingCheck, checkErr := tx.GetIssue(ctx, existing.ID)
			if checkErr == nil && existingCheck != nil {
				if err := withBusyRetry(func() error { return tx.DeleteIssue(ctx, existing.ID) }); err != nil {
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
			if err := tx.UpdateIssue(ctx, existing.ID, updates, "importer"); err != nil {
				return "", fmt.Errorf("failed to update issue %s: %w", existing.ID, err)
			}
		}
		return "", nil
	}

	// Check if old ID still exists (it might have been deleted by another clone)
	existingCheck, checkErr := tx.GetIssue(ctx, existing.ID)
	if checkErr != nil || existingCheck == nil {
		// Old ID doesn't exist - the rename must have been completed by another clone
		// Verify that target exists with correct content
		targetCheck, targetErr := tx.GetIssue(ctx, incoming.ID)
		if targetErr == nil && targetCheck != nil && targetCheck.ComputeContentHash() == incoming.ComputeContentHash() {
			return "", nil
		}
		return "", fmt.Errorf("old ID %s doesn't exist and target ID %s is not as expected", existing.ID, incoming.ID)
	}

	// Delete old ID
	oldID := existing.ID
	if err := withBusyRetry(func() error { return tx.DeleteIssue(ctx, oldID) }); err != nil {
		return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
	}

	// Create with new ID
	if err := withBusyRetry(func() error { return tx.CreateIssue(ctx, incoming, "import-rename") }); err != nil {
		// Another writer may have created the target concurrently. If the target now exists
		// with the same content, treat the rename as already complete.
		targetIssue, getErr := tx.GetIssue(ctx, incoming.ID)
		if getErr == nil && targetIssue != nil && targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			return oldID, nil
		}
		return "", fmt.Errorf("failed to create renamed issue %s: %w", incoming.ID, err)
	}

	return oldID, nil
}

// upsertIssues creates new issues or updates existing ones using content-first matching
func upsertIssues(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options, result *Result) error {
	// Get all DB issues once for matching
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
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
			if linear.IsLinearExternalRef(*issue.ExternalRef) {
				if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
					dbByExternalRef[canonical] = issue
				}
			}
		}
	}

	// Track what we need to create
	var newIssues []*types.Issue
	seenHashes := make(map[string]bool)
	seenIDs := make(map[string]bool) // Track IDs to prevent UNIQUE constraint errors

	for _, incoming := range issues {
		hash := incoming.ContentHash
		if hash == "" {
			// Shouldn't happen (computed earlier), but be defensive
			hash = incoming.ComputeContentHash()
			incoming.ContentHash = hash
		}

		// Skip duplicates within incoming batch (by content hash)
		if seenHashes[hash] {
			result.Skipped++
			continue
		}
		seenHashes[hash] = true

		// Skip duplicates by ID to prevent UNIQUE constraint violations
		// This handles JSONL files with multiple versions of the same issue
		if seenIDs[incoming.ID] {
			result.Skipped++
			continue
		}
		seenIDs[incoming.ID] = true

		// Phase 0: Match by external_ref first (if present)
		// This enables re-syncing from external systems (Jira, GitHub, Linear)
		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			if existing, found := dbByExternalRef[*incoming.ExternalRef]; found {
				// Found match by external_ref - update the existing issue
				if !opts.SkipUpdate {
					// GH#865: Check timestamp-aware protection first
					// If local snapshot has a newer version, protect it from being overwritten
					if shouldProtectFromUpdate(existing.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
						debugLogProtection(existing.ID, opts.ProtectLocalExportIDs[existing.ID], incoming.UpdatedAt)
						result.Skipped++
						continue
					}
					// Check timestamps - only update if incoming is newer
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
					// Pinned field: Only update if explicitly true in JSONL
					// (omitempty means false values are absent, so false = don't change existing)
					if incoming.Pinned {
						updates["pinned"] = incoming.Pinned
					}

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
						if err := store.UpdateIssue(ctx, existing.ID, updates, "import"); err != nil {
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
				// Same content, different ID - check if this is a rename or cross-prefix duplicate
				existingPrefix := utils.ExtractIssuePrefix(existing.ID)
				incomingPrefix := utils.ExtractIssuePrefix(incoming.ID)

				if existingPrefix != incomingPrefix {
					// Cross-prefix content match: same content but different projects/prefixes.
					// This is NOT a rename - it's a duplicate from another project.
					// Skip the incoming issue and keep the existing one unchanged.
					// Calling handleRename would fail because CreateIssue validates prefix.
					result.Skipped++
				} else if !opts.SkipUpdate {
					// Same prefix, different ID suffix - this is a true rename
					deletedID, err := handleRename(ctx, store, existing, incoming)
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
				// GH#865: Check timestamp-aware protection first
				// If local snapshot has a newer version, protect it from being overwritten
				if shouldProtectFromUpdate(incoming.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
					debugLogProtection(incoming.ID, opts.ProtectLocalExportIDs[incoming.ID], incoming.UpdatedAt)
					result.Skipped++
					continue
				}
				// Check timestamps - only update if incoming is newer
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
				// Pinned field: Only update if explicitly true in JSONL
				// (omitempty means false values are absent, so false = don't change existing)
				if incoming.Pinned {
					updates["pinned"] = incoming.Pinned
				}

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
					if err := store.UpdateIssue(ctx, incoming.ID, updates, "import"); err != nil {
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

	// Filter out orphaned issues if orphan_handling is set to skip
	// Pre-filter before batch creation to prevent orphans from being created then ID-cleared
	if opts.OrphanHandling == OrphanSkip {
		var filteredNewIssues []*types.Issue
		for _, issue := range newIssues {
			// Check if this is a hierarchical child whose parent doesn't exist
			if isHierarchical, parentID := isHierarchicalID(issue.ID); isHierarchical {

				// Check if parent exists in either existing DB issues or in newIssues batch
				var parentExists bool
				for _, dbIssue := range dbIssues {
					if dbIssue.ID == parentID {
						parentExists = true
						break
					}
				}
				if !parentExists {
					for _, newIssue := range newIssues {
						if newIssue.ID == parentID {
							parentExists = true
							break
						}
					}
				}

				if !parentExists {
					// Skip this orphaned issue
					result.Skipped++
					continue
				}
			}
			filteredNewIssues = append(filteredNewIssues, issue)
		}
		newIssues = filteredNewIssues
	}

	// OrphanStrict: fail-fast if any new hierarchical child has no parent in DB or import batch.
	if opts.OrphanHandling == OrphanStrict {
		newIDSet := make(map[string]bool, len(newIssues))
		for _, issue := range newIssues {
			newIDSet[issue.ID] = true
		}
		for _, issue := range newIssues {
			if isHierarchical, parentID := isHierarchicalID(issue.ID); isHierarchical {
				if dbByID[parentID] == nil && !newIDSet[parentID] {
					return fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
				}
			}
		}
	}

	// OrphanResurrect: if any hierarchical parents are missing, attempt to resurrect them
	// from local JSONL history by creating placeholder parents (status=closed).
	if opts.OrphanHandling == OrphanResurrect {
		if err := addResurrectedParents(store, dbByID, issues, &newIssues); err != nil {
			return err
		}
	}

	// Batch create all new issues
	// Sort by hierarchy depth to ensure parents are created before children
	if len(newIssues) > 0 {
		sort.Slice(newIssues, func(i, j int) bool {
			depthI := hierarchyDepth(newIssues[i].ID)
			depthJ := hierarchyDepth(newIssues[j].ID)
			if depthI != depthJ {
				return depthI < depthJ // Shallower first
			}
			return newIssues[i].ID < newIssues[j].ID // Stable sort
		})

		// Create in batches by depth level (max depth 3)
		for depth := 0; depth <= 3; depth++ {
			var batchForDepth []*types.Issue
			for _, issue := range newIssues {
				if hierarchyDepth(issue.ID) == depth {
					batchForDepth = append(batchForDepth, issue)
				}
			}
			if len(batchForDepth) > 0 {
				// Use the backend-agnostic batch creation API with full options.
				batchOpts := storage.BatchCreateOptions{
					OrphanHandling:       opts.OrphanHandling,
					SkipPrefixValidation: opts.SkipPrefixValidation,
				}
				if err := store.CreateIssuesWithFullOptions(ctx, batchForDepth, "import", batchOpts); err != nil {
					return fmt.Errorf("error creating depth-%d issues: %w", depth, err)
				}
				result.Created += len(batchForDepth)
			}
		}
	}

	// REMOVED: Counter sync after import - no longer needed with hash IDs

	return nil
}

// upsertIssuesTx performs upsert using a transaction for atomicity.
func upsertIssuesTx(ctx context.Context, tx storage.Transaction, store storage.Storage, issues []*types.Issue, opts Options, result *Result) error {
	// Use transaction-scoped reads for consistency.
	dbIssues, err := tx.SearchIssues(ctx, "", types.IssueFilter{})
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
			if linear.IsLinearExternalRef(*issue.ExternalRef) {
				if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
					dbByExternalRef[canonical] = issue
				}
			}
		}
	}

	// Track what we need to create
	var newIssues []*types.Issue
	seenHashes := make(map[string]bool)
	seenIDs := make(map[string]bool)

	for _, incoming := range issues {
		hash := incoming.ContentHash
		if hash == "" {
			hash = incoming.ComputeContentHash()
			incoming.ContentHash = hash
		}

		if seenHashes[hash] {
			result.Skipped++
			continue
		}
		seenHashes[hash] = true

		if seenIDs[incoming.ID] {
			result.Skipped++
			continue
		}
		seenIDs[incoming.ID] = true

		// Phase 0: external_ref
		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			if existing, found := dbByExternalRef[*incoming.ExternalRef]; found && existing != nil {
				if !opts.SkipUpdate {
					if shouldProtectFromUpdate(existing.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
						debugLogProtection(existing.ID, opts.ProtectLocalExportIDs[existing.ID], incoming.UpdatedAt)
						result.Skipped++
						continue
					}
					if !incoming.UpdatedAt.After(existing.UpdatedAt) {
						result.Unchanged++
						continue
					}
					updates := map[string]interface{}{
						"title":               incoming.Title,
						"description":         incoming.Description,
						"status":              incoming.Status,
						"priority":            incoming.Priority,
						"issue_type":          incoming.IssueType,
						"design":              incoming.Design,
						"acceptance_criteria": incoming.AcceptanceCriteria,
						"notes":               incoming.Notes,
						"closed_at":           incoming.ClosedAt,
					}
					if incoming.Pinned {
						updates["pinned"] = incoming.Pinned
					}
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
					if IssueDataChanged(existing, updates) {
						if err := tx.UpdateIssue(ctx, existing.ID, updates, "import"); err != nil {
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

		// Phase 1: content hash
		if existing, found := dbByHash[hash]; found && existing != nil {
			if existing.ID == incoming.ID {
				result.Unchanged++
			} else {
				existingPrefix := utils.ExtractIssuePrefix(existing.ID)
				incomingPrefix := utils.ExtractIssuePrefix(incoming.ID)
				if existingPrefix != incomingPrefix {
					result.Skipped++
				} else if !opts.SkipUpdate {
					deletedID, err := handleRenameTx(ctx, tx, existing, incoming)
					if err != nil {
						return fmt.Errorf("failed to handle rename %s -> %s: %w", existing.ID, incoming.ID, err)
					}
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

		// Phase 2: same ID exists -> update candidate
		if existingWithID, found := dbByID[incoming.ID]; found && existingWithID != nil {
			if !opts.SkipUpdate {
				if shouldProtectFromUpdate(incoming.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
					debugLogProtection(incoming.ID, opts.ProtectLocalExportIDs[incoming.ID], incoming.UpdatedAt)
					result.Skipped++
					continue
				}
				if !incoming.UpdatedAt.After(existingWithID.UpdatedAt) {
					result.Unchanged++
					continue
				}
				updates := map[string]interface{}{
					"title":               incoming.Title,
					"description":         incoming.Description,
					"status":              incoming.Status,
					"priority":            incoming.Priority,
					"issue_type":          incoming.IssueType,
					"design":              incoming.Design,
					"acceptance_criteria": incoming.AcceptanceCriteria,
					"notes":               incoming.Notes,
					"closed_at":           incoming.ClosedAt,
				}
				if incoming.Pinned {
					updates["pinned"] = incoming.Pinned
				}
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
				if IssueDataChanged(existingWithID, updates) {
					if err := tx.UpdateIssue(ctx, incoming.ID, updates, "import"); err != nil {
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
			newIssues = append(newIssues, incoming)
		}
	}

	// OrphanSkip/Strict/Resurrect handled using the same helpers as non-tx path
	if opts.OrphanHandling == OrphanSkip {
		var filtered []*types.Issue
		for _, issue := range newIssues {
			if isHier, parentID := isHierarchicalID(issue.ID); isHier {
				if dbByID[parentID] == nil {
					// parent might be created in this batch; check newIssues
					found := false
					for _, ni := range newIssues {
						if ni.ID == parentID {
							found = true
							break
						}
					}
					if !found {
						result.Skipped++
						continue
					}
				}
			}
			filtered = append(filtered, issue)
		}
		newIssues = filtered
	}
	if opts.OrphanHandling == OrphanStrict {
		newIDSet := make(map[string]bool, len(newIssues))
		for _, issue := range newIssues {
			newIDSet[issue.ID] = true
		}
		for _, issue := range newIssues {
			if isHier, parentID := isHierarchicalID(issue.ID); isHier {
				if dbByID[parentID] == nil && !newIDSet[parentID] {
					return fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
				}
			}
		}
	}
	if opts.OrphanHandling == OrphanResurrect {
		if err := addResurrectedParents(store, dbByID, issues, &newIssues); err != nil {
			return err
		}
	}

	// Create new issues in deterministic depth order using tx.
	if len(newIssues) > 0 {
		sort.Slice(newIssues, func(i, j int) bool {
			di := hierarchyDepth(newIssues[i].ID)
			dj := hierarchyDepth(newIssues[j].ID)
			if di != dj {
				return di < dj
			}
			return newIssues[i].ID < newIssues[j].ID
		})

		type importCreator interface {
			CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error
		}
		for _, iss := range newIssues {
			if ic, ok := tx.(importCreator); ok {
				if err := ic.CreateIssueImport(ctx, iss, "import", opts.SkipPrefixValidation); err != nil {
					return err
				}
			} else {
				if err := tx.CreateIssue(ctx, iss, "import"); err != nil {
					return err
				}
			}
			result.Created++
		}
	}

	return nil
}

func importDependenciesTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts Options, result *Result) error {
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}

		existingDeps, err := tx.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error checking dependencies for %s: %w", issue.ID, err)
		}
		existingSet := make(map[string]bool)
		for _, existing := range existingDeps {
			key := fmt.Sprintf("%s|%s", existing.DependsOnID, existing.Type)
			existingSet[key] = true
		}

		for _, dep := range issue.Dependencies {
			key := fmt.Sprintf("%s|%s", dep.DependsOnID, dep.Type)
			if existingSet[key] {
				continue
			}
			if err := tx.AddDependency(ctx, dep, "import"); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to error: %s (%v)\n", depDesc, err)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
				continue
			}
		}
	}
	return nil
}

func importLabelsTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts Options) error {
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}
		currentLabels, err := tx.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
		}
		currentSet := make(map[string]bool, len(currentLabels))
		for _, l := range currentLabels {
			currentSet[l] = true
		}
		importSet := make(map[string]bool, len(issue.Labels))
		for _, label := range issue.Labels {
			importSet[label] = true
			if currentSet[label] {
				continue
			}
			if err := tx.AddLabel(ctx, issue.ID, label, "import"); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
				}
			}
		}
		// Remove labels that exist in DB but not in import data
		for _, label := range currentLabels {
			if !importSet[label] {
				if err := tx.RemoveLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error removing label %s from %s: %w", label, issue.ID, err)
					}
				}
			}
		}
	}
	return nil
}

func importCommentsTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts Options) error {
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}
		currentComments, err := tx.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting comments for %s: %w", issue.ID, err)
		}
		existing := make(map[string]bool)
		for _, c := range currentComments {
			key := fmt.Sprintf("%s:%s", c.Author, strings.TrimSpace(c.Text))
			existing[key] = true
		}
		for _, comment := range issue.Comments {
			key := fmt.Sprintf("%s:%s", comment.Author, strings.TrimSpace(comment.Text))
			if existing[key] {
				continue
			}
			if _, err := tx.ImportIssueComment(ctx, issue.ID, comment.Author, comment.Text, comment.CreatedAt); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding comment to %s: %w", issue.ID, err)
				}
			}
		}
	}
	return nil
}

// addResurrectedParents ensures missing hierarchical parents exist by adding placeholder parent
// issues to newIssues (if needed). Parents are sourced from the local JSONL file when possible.
func addResurrectedParents(store storage.Storage, dbByID map[string]*types.Issue, allIncoming []*types.Issue, newIssues *[]*types.Issue) error {
	// Track which IDs will exist after creation
	willExist := make(map[string]bool, len(dbByID)+len(*newIssues))
	for id, iss := range dbByID {
		if iss != nil {
			willExist[id] = true
		}
	}
	for _, iss := range *newIssues {
		willExist[iss.ID] = true
	}

	// Helper to ensure a single parent exists (and its ancestors).
	var ensureParent func(parentID string) error
	ensureParent = func(parentID string) error {
		if willExist[parentID] {
			return nil
		}
		// Ensure ancestors first (root-to-leaf)
		if isHier, grandParent := isHierarchicalID(parentID); isHier {
			if err := ensureParent(grandParent); err != nil {
				return err
			}
		}

		// Try to find the issue in the incoming batch first (already being imported)
		for _, iss := range allIncoming {
			if iss.ID == parentID {
				willExist[parentID] = true
				return nil
			}
		}

		// Try local JSONL history
		beadsDir := filepath.Dir(store.Path())
		found, err := findIssueInLocalJSONL(filepath.Join(beadsDir, "issues.jsonl"), parentID)
		if err != nil {
			return fmt.Errorf("parent issue %s does not exist and could not be resurrected: %w", parentID, err)
		}
		if found == nil {
			return fmt.Errorf("parent issue %s does not exist and cannot be resurrected from local JSONL history", parentID)
		}

		now := time.Now().UTC()
		closedAt := now
		placeholder := &types.Issue{
			ID:        found.ID,
			Title:     found.Title,
			IssueType: found.IssueType,
			Status:    types.StatusClosed,
			Priority:  4,
			CreatedAt: found.CreatedAt,
			UpdatedAt: now,
			ClosedAt:  &closedAt,
			// Keep content deterministic-ish but signal it's resurrected.
			Description: "[RESURRECTED] Recreated as closed to preserve hierarchical structure.",
		}
		// Compute hash (ImportIssues computed hashes for original slice only)
		placeholder.ContentHash = placeholder.ComputeContentHash()

		*newIssues = append(*newIssues, placeholder)
		willExist[parentID] = true
		return nil
	}

	// Walk newIssues and ensure parents exist
	for _, iss := range *newIssues {
		if isHier, parentID := isHierarchicalID(iss.ID); isHier {
			if err := ensureParent(parentID); err != nil {
				return err
			}
		}
	}
	return nil
}

func findIssueInLocalJSONL(jsonlPath, issueID string) (*types.Issue, error) {
	if jsonlPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(jsonlPath); err != nil {
		return nil, nil // No JSONL file available
	}

	f, err := os.Open(jsonlPath) // #nosec G304 -- jsonlPath derived from beadsDir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var last *types.Issue
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.Contains(line, `"`+issueID+`"`) {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			// Skip malformed lines (best-effort resurrection)
			continue
		}
		if iss.ID == issueID {
			iss.SetDefaults()
			copy := iss
			last = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

// importDependencies imports dependency relationships
func importDependencies(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options, result *Result) error {
	// Backend-agnostic existence check map to avoid relying on backend-specific FK errors.
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to load issues for dependency validation: %w", err)
	}
	exists := make(map[string]bool, len(dbIssues))
	for _, iss := range dbIssues {
		if iss != nil {
			exists[iss.ID] = true
		}
	}

	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}

		// Fetch existing dependencies once per issue
		existingDeps, err := store.GetDependencyRecords(ctx, issue.ID)
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
			// Validate referenced issues exist (after upsert).
			if !exists[dep.IssueID] || !exists[dep.DependsOnID] {
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				if opts.Strict {
					return fmt.Errorf("missing reference for dependency: %s", depDesc)
				}
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to missing reference: %s\n", depDesc)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
				continue
			}

			// Check for duplicate using set
			key := fmt.Sprintf("%s|%s", dep.DependsOnID, dep.Type)
			if existingSet[key] {
				continue
			}

			// Add dependency
			if err := store.AddDependency(ctx, dep, "import"); err != nil {
				// Backend-agnostic: treat dependency insert errors as non-fatal unless strict mode is enabled.
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to error: %s (%v)\n", depDesc, err)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
				continue
			}
		}
	}

	return nil
}

// importLabels imports labels for issues
func importLabels(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options) error {
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}

		// Get current labels
		currentLabels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
		}

		currentLabelSet := make(map[string]bool)
		for _, label := range currentLabels {
			currentLabelSet[label] = true
		}

		importLabelSet := make(map[string]bool, len(issue.Labels))

		// Add missing labels
		for _, label := range issue.Labels {
			importLabelSet[label] = true
			if !currentLabelSet[label] {
				if err := store.AddLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
					}
					continue
				}
			}
		}

		// Remove labels that exist in DB but not in import data
		for _, label := range currentLabels {
			if !importLabelSet[label] {
				if err := store.RemoveLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error removing label %s from %s: %w", label, issue.ID, err)
					}
					continue
				}
			}
		}
	}

	return nil
}

// importComments imports comments for issues
func importComments(ctx context.Context, store storage.Storage, issues []*types.Issue, opts Options) error {
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}

		// Get current comments to avoid duplicates
		currentComments, err := store.GetIssueComments(ctx, issue.ID)
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
				if _, err := store.ImportIssueComment(ctx, issue.ID, comment.Author, comment.Text, comment.CreatedAt); err != nil {
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

// shouldProtectFromUpdate checks if an update should be skipped due to timestamp-aware protection (GH#865).
// Returns true if the update should be skipped (local is newer), false if the update should proceed.
// If the issue is not in the protection map, returns false (allow update).
func shouldProtectFromUpdate(issueID string, incomingTime time.Time, protectMap map[string]time.Time) bool {
	if protectMap == nil {
		return false
	}
	localTime, exists := protectMap[issueID]
	if !exists {
		// Issue not in protection map - allow update
		return false
	}
	// Only protect if local snapshot is newer than or equal to incoming
	// If incoming is newer, allow the update
	return !incomingTime.After(localTime)
}

// debugLogProtection logs when timestamp-aware protection triggers (for debugging sync issues).
func debugLogProtection(issueID string, localTime, incomingTime time.Time) {
	if os.Getenv("BD_DEBUG_SYNC") != "" {
		fmt.Fprintf(os.Stderr, "[debug] Protected %s: local=%s >= incoming=%s\n",
			issueID, localTime.Format(time.RFC3339), incomingTime.Format(time.RFC3339))
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

// isHierarchicalID returns true if id is a hierarchical child ID of the form "<parent>.<n>" (n is digits).
// This intentionally avoids treating prefixes that contain dots (e.g., "my.project-abc") as hierarchical.
func isHierarchicalID(id string) (bool, string) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot <= 0 || lastDot == len(id)-1 {
		return false, ""
	}
	suffix := id[lastDot+1:]
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false, ""
		}
	}
	return true, id[:lastDot]
}

// hierarchyDepth returns the number of hierarchical segments in an ID.
// Examples:
// - "bd-abc" -> 0
// - "bd-abc.1" -> 1
// - "bd-abc.1.2" -> 2
func hierarchyDepth(id string) int {
	depth := 0
	cur := id
	for {
		isHier, parent := isHierarchicalID(cur)
		if !isHier {
			return depth
		}
		depth++
		cur = parent
	}
}

// buildAllowedPrefixSet returns allowed prefixes, or nil to allow all (GH#686).
// In multi-repo mode, additional repos have their own prefixes - allow all.
// Also accepts allowedPrefixesConfig (comma-separated list like "gt-,mol-").
// Also loads prefixes from routes.jsonl for multi-rig setups (Gas Town).
func buildAllowedPrefixSet(primaryPrefix string, allowedPrefixesConfig string, beadsDir string) map[string]bool {
	if config.GetMultiRepoConfig() != nil {
		return nil // Multi-repo: allow all prefixes
	}

	allowed := map[string]bool{primaryPrefix: true}

	// Parse allowed_prefixes config (comma-separated, with or without trailing -)
	if allowedPrefixesConfig != "" {
		for _, prefix := range strings.Split(allowedPrefixesConfig, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			// Normalize: remove trailing - if present (we match without it)
			prefix = strings.TrimSuffix(prefix, "-")
			allowed[prefix] = true
		}
	}

	// Load prefixes from routes.jsonl for multi-rig setups (Gas Town)
	// This allows issues from other rigs to coexist in the same JSONL
	// Use LoadTownRoutes to find routes at town level (~/gt/.beads/routes.jsonl)
	if beadsDir != "" {
		routes, _ := routing.LoadTownRoutes(beadsDir)
		for _, route := range routes {
			// Normalize: remove trailing - if present
			prefix := strings.TrimSuffix(route.Prefix, "-")
			if prefix != "" {
				allowed[prefix] = true
			}
		}
	}

	return allowed
}
