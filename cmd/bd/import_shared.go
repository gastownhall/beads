package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// fieldComparator handles comparison logic for a specific field type
type fieldComparator struct {
	// Helper to safely extract string from interface (handles string and *string)
	strFrom func(v interface{}) (string, bool)
	// Helper to safely extract int from interface
	intFrom func(v interface{}) (int64, bool)
}

func newFieldComparator() *fieldComparator {
	fc := &fieldComparator{}
	
	fc.strFrom = func(v interface{}) (string, bool) {
		switch t := v.(type) {
		case string:
			return t, true
		case *string:
			if t == nil {
				return "", true
			}
			return *t, true
		case nil:
			return "", true
		default:
			return "", false
		}
	}
	
	fc.intFrom = func(v interface{}) (int64, bool) {
		switch t := v.(type) {
		case int:
			return int64(t), true
		case int32:
			return int64(t), true
		case int64:
			return t, true
		case float64:
			// Only accept whole numbers
			if t == float64(int64(t)) {
				return int64(t), true
			}
			return 0, false
		default:
			return 0, false
		}
	}
	
	return fc
}

// equalStr compares string field (treats empty and nil as equal)
func (fc *fieldComparator) equalStr(existingVal string, newVal interface{}) bool {
	s, ok := fc.strFrom(newVal)
	if !ok {
		return false // Type mismatch means changed
	}
	return existingVal == s
}

// equalPtrStr compares *string field (treats empty and nil as equal)
func (fc *fieldComparator) equalPtrStr(existing *string, newVal interface{}) bool {
	s, ok := fc.strFrom(newVal)
	if !ok {
		return false // Type mismatch means changed
	}
	if existing == nil {
		return s == ""
	}
	return *existing == s
}

// equalStatus compares Status field
func (fc *fieldComparator) equalStatus(existing types.Status, newVal interface{}) bool {
	switch t := newVal.(type) {
	case types.Status:
		return existing == t
	case string:
		return string(existing) == t
	default:
		return false // Unknown type means changed
	}
}

// equalIssueType compares IssueType field
func (fc *fieldComparator) equalIssueType(existing types.IssueType, newVal interface{}) bool {
	switch t := newVal.(type) {
	case types.IssueType:
		return existing == t
	case string:
		return string(existing) == t
	default:
		return false // Unknown type means changed
	}
}

// equalPriority compares priority field
func (fc *fieldComparator) equalPriority(existing int, newVal interface{}) bool {
	p, ok := fc.intFrom(newVal)
	if !ok {
		return false
	}
	return existing == int(p)
}

// checkFieldChanged checks if a specific field has changed
func (fc *fieldComparator) checkFieldChanged(key string, existing *types.Issue, newVal interface{}) bool {
	switch key {
	case "title":
		return !fc.equalStr(existing.Title, newVal)
	case "description":
		return !fc.equalStr(existing.Description, newVal)
	case "status":
		return !fc.equalStatus(existing.Status, newVal)
	case "priority":
		return !fc.equalPriority(existing.Priority, newVal)
	case "issue_type":
		return !fc.equalIssueType(existing.IssueType, newVal)
	case "design":
		return !fc.equalStr(existing.Design, newVal)
	case "acceptance_criteria":
		return !fc.equalStr(existing.AcceptanceCriteria, newVal)
	case "notes":
		return !fc.equalStr(existing.Notes, newVal)
	case "assignee":
		return !fc.equalStr(existing.Assignee, newVal)
	case "external_ref":
		return !fc.equalPtrStr(existing.ExternalRef, newVal)
	default:
		// Unknown field - treat as changed to be conservative
		// This prevents skipping updates when new fields are added
		return true
	}
}

// issueDataChanged checks if any fields in the updates map differ from the existing issue
// Returns true if any field changed, false if all fields match
func issueDataChanged(existing *types.Issue, updates map[string]interface{}) bool {
	fc := newFieldComparator()
	
	// Check each field in updates map
	for key, newVal := range updates {
		if fc.checkFieldChanged(key, existing, newVal) {
			return true
		}
	}
	
	return false // No changes detected
}

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
	Unchanged       int               // Existing issues that matched exactly (idempotent)
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
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	// Phase 1: Get or create SQLite store
	sqliteStore, needCloseStore, err := getOrCreateStore(ctx, dbPath, store)
	if err != nil {
		return nil, err
	}
	if needCloseStore {
		defer func() { _ = sqliteStore.Close() }()
	}

	// Phase 2: Check and handle prefix mismatches
	if err := handlePrefixMismatch(ctx, sqliteStore, issues, opts, result); err != nil {
		return result, err
	}

	// Phase 3: Detect and resolve collisions
	issues, err = handleCollisions(ctx, sqliteStore, issues, opts, result)
	if err != nil {
		return result, err
	}
	if opts.DryRun && result.Collisions == 0 {
		return result, nil
	}

	// Phase 4: Upsert issues (create new or update existing)
	if err := upsertIssues(ctx, sqliteStore, issues, opts, result); err != nil {
		return nil, err
	}

	// Phase 5: Import dependencies
	if err := importDependencies(ctx, sqliteStore, issues, opts); err != nil {
		return nil, err
	}

	// Phase 6: Import labels
	if err := importLabels(ctx, sqliteStore, issues, opts); err != nil {
		return nil, err
	}

	// Phase 7: Import comments
	if err := importComments(ctx, sqliteStore, issues, opts); err != nil {
		return nil, err
	}

	// Phase 8: Checkpoint WAL to update main .db file timestamp
	// This ensures staleness detection sees the database as fresh
	if err := sqliteStore.CheckpointWAL(ctx); err != nil {
		// Non-fatal - just log warning
		fmt.Fprintf(os.Stderr, "Warning: failed to checkpoint WAL: %v\n", err)
	}

	return result, nil
}



// renameImportedIssuePrefixes renames all issues and their references to match the target prefix
func renameImportedIssuePrefixes(issues []*types.Issue, targetPrefix string) error {
	// Build a mapping of old IDs to new IDs
	idMapping := make(map[string]string)

	for _, issue := range issues {
		oldPrefix := utils.ExtractIssuePrefix(issue.ID)
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
	return (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-'
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
