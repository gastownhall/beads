package main

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/importer"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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
	DryRun                     bool                 // Preview changes without applying them
	SkipUpdate                 bool                 // Skip updating existing issues (create-only mode)
	Strict                     bool                 // Fail on any error (dependencies, labels, etc.)
	RenameOnImport             bool                 // Rename imported issues to match database prefix
	SkipPrefixValidation       bool                 // Skip prefix validation (for auto-import)
	ClearDuplicateExternalRefs bool                 // Clear duplicate external_ref values instead of erroring
	OrphanHandling             string               // Orphan handling mode: strict/resurrect/skip/allow (empty = use config)
	ProtectLocalExportIDs      map[string]time.Time // IDs from left snapshot with timestamps for timestamp-aware protection (GH#865)
	DeletionIDs                []string             // IDs to delete (from JSONL deletion markers)
}

// ImportResult contains statistics about the import operation
type ImportResult struct {
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
	// Determine orphan handling: flag > config > default (allow)
	orphanHandling := opts.OrphanHandling
	if orphanHandling == "" && store != nil {
		// Read from config if flag not specified
		configValue, err := store.GetConfig(ctx, "import.missing_parents")
		if err == nil && configValue != "" {
			orphanHandling = configValue
		} else {
			// Default to allow (most permissive)
			orphanHandling = "allow"
		}
	} else if orphanHandling == "" {
		// No store available, default to allow
		orphanHandling = "allow"
	}

	// Convert ImportOptions to importer.Options
	importerOpts := importer.Options{
		DryRun:                     opts.DryRun,
		SkipUpdate:                 opts.SkipUpdate,
		Strict:                     opts.Strict,
		RenameOnImport:             opts.RenameOnImport,
		SkipPrefixValidation:       opts.SkipPrefixValidation,
		ClearDuplicateExternalRefs: opts.ClearDuplicateExternalRefs,
		OrphanHandling:             importer.OrphanHandling(orphanHandling),
		ProtectLocalExportIDs:      opts.ProtectLocalExportIDs,
		DeletionIDs:                opts.DeletionIDs,
	}

	// Delegate to the importer package
	result, err := importer.ImportIssues(ctx, dbPath, store, issues, importerOpts)
	if err != nil {
		return nil, err
	}

	// Convert importer.Result to ImportResult
	return &ImportResult{
		Created:             result.Created,
		Updated:             result.Updated,
		Unchanged:           result.Unchanged,
		Skipped:             result.Skipped,
		Deleted:             result.Deleted,
		Collisions:          result.Collisions,
		IDMapping:           result.IDMapping,
		CollisionIDs:        result.CollisionIDs,
		PrefixMismatch:      result.PrefixMismatch,
		ExpectedPrefix:      result.ExpectedPrefix,
		MismatchPrefixes:    result.MismatchPrefixes,
		SkippedDependencies: result.SkippedDependencies,
	}, nil
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
