// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For detailed guidance on extending bd, see EXTENDING.md.
package beads

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// CanonicalDatabaseName is the required database filename for all beads repositories
const CanonicalDatabaseName = "beads.db"

// LegacyDatabaseNames are old names that should be migrated
var LegacyDatabaseNames = []string{"bd.db", "issues.db", "bugs.db"}

// Issue represents a tracked work item with metadata, dependencies, and status.
type (
	Issue = types.Issue
	// Status represents the current state of an issue (open, in progress, closed, blocked).
	Status = types.Status
	// IssueType represents the type of issue (bug, feature, task, epic, chore).
	IssueType = types.IssueType
	// Dependency represents a relationship between issues.
	Dependency = types.Dependency
	// DependencyType represents the type of dependency (blocks, related, parent-child, discovered-from).
	DependencyType = types.DependencyType
	// Comment represents a user comment on an issue.
	Comment = types.Comment
	// Event represents an audit log event.
	Event = types.Event
	// EventType represents the type of audit event.
	EventType = types.EventType
	// Label represents a tag attached to an issue.
	Label = types.Label
	// BlockedIssue represents an issue with blocking dependencies.
	BlockedIssue = types.BlockedIssue
	// TreeNode represents a node in a dependency tree.
	TreeNode = types.TreeNode
	// Statistics represents project-wide metrics.
	Statistics = types.Statistics
	// IssueFilter represents filtering criteria for issue queries.
	IssueFilter = types.IssueFilter
	// WorkFilter represents filtering criteria for work queries.
	WorkFilter = types.WorkFilter
	// SortPolicy determines how ready work is ordered.
	SortPolicy = types.SortPolicy
	// EpicStatus represents the status of an epic issue.
	EpicStatus = types.EpicStatus
)

// Status constants
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusClosed     = types.StatusClosed
	StatusBlocked    = types.StatusBlocked
)

// IssueType constants
const (
	TypeBug     = types.TypeBug
	TypeFeature = types.TypeFeature
	TypeTask    = types.TypeTask
	TypeEpic    = types.TypeEpic
	TypeChore   = types.TypeChore
)

// DependencyType constants
const (
	DepBlocks         = types.DepBlocks
	DepRelated        = types.DepRelated
	DepParentChild    = types.DepParentChild
	DepDiscoveredFrom = types.DepDiscoveredFrom
)

// SortPolicy constants
const (
	SortPolicyHybrid   = types.SortPolicyHybrid
	SortPolicyPriority = types.SortPolicyPriority
	SortPolicyOldest   = types.SortPolicyOldest
)

// EventType constants
const (
	EventCreated           = types.EventCreated
	EventUpdated           = types.EventUpdated
	EventStatusChanged     = types.EventStatusChanged
	EventCommented         = types.EventCommented
	EventClosed            = types.EventClosed
	EventReopened          = types.EventReopened
	EventDependencyAdded   = types.EventDependencyAdded
	EventDependencyRemoved = types.EventDependencyRemoved
	EventLabelAdded        = types.EventLabelAdded
	EventLabelRemoved      = types.EventLabelRemoved
	EventCompacted         = types.EventCompacted
)

// Storage provides the minimal interface for extension orchestration
type Storage = storage.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = storage.Transaction

// NewSQLiteStorage opens a bd SQLite database for programmatic access.
// Most extensions should use this to query ready work and update issue status.
func NewSQLiteStorage(ctx context.Context, dbPath string) (Storage, error) {
	return sqlite.New(ctx, dbPath)
}

// FindDatabasePath discovers the bd database path using bd's standard search order:
//  1. $BEADS_DIR environment variable (points to .beads directory)
//  2. $BEADS_DB environment variable (points directly to database file, deprecated)
//  3. .beads/*.db in current directory or ancestors
//
// Returns empty string if no database is found.
func FindDatabasePath() string {
	// 1. Check BEADS_DIR environment variable (preferred)
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		// Canonicalize the path to prevent nested .beads directories
		absBeadsDir := utils.CanonicalizePath(beadsDir)

		// Check for config.json first (single source of truth)
		if cfg, err := configfile.Load(absBeadsDir); err == nil && cfg != nil {
			dbPath := cfg.DatabasePath(absBeadsDir)
			if _, err := os.Stat(dbPath); err == nil {
				return dbPath
			}
		}

		// Fall back to canonical beads.db for backward compatibility
		canonicalDB := filepath.Join(absBeadsDir, CanonicalDatabaseName)
		if _, err := os.Stat(canonicalDB); err == nil {
			return canonicalDB
		}

		// Look for any .db file in the beads directory
		matches, err := filepath.Glob(filepath.Join(absBeadsDir, "*.db"))
		if err == nil && len(matches) > 0 {
			// Filter out backup files only
			var validDBs []string
			for _, match := range matches {
				baseName := filepath.Base(match)
				if !strings.Contains(baseName, ".backup") {
					validDBs = append(validDBs, match)
				}
			}
			if len(validDBs) > 0 {
				return validDBs[0]
			}
		}

		// BEADS_DIR is set but no database found - this is OK for --no-db mode
		// Return empty string and let the caller handle it
	}

	// 2. Check BEADS_DB environment variable (deprecated but still supported)
	if envDB := os.Getenv("BEADS_DB"); envDB != "" {
		// Canonicalize the path to prevent nested .beads directories
		if absDB, err := filepath.Abs(envDB); err == nil {
			if canonical, err := filepath.EvalSymlinks(absDB); err == nil {
				return canonical
			}
			return absDB // Return absolute path even if symlink resolution fails
		}
		return envDB // Fallback to original if Abs fails
	}

	// 3. Search for .beads/*.db in current directory and ancestors
	if foundDB := findDatabaseInTree(); foundDB != "" {
		// Canonicalize found path
		if absDB, err := filepath.Abs(foundDB); err == nil {
			if canonical, err := filepath.EvalSymlinks(absDB); err == nil {
				return canonical
			}
			return absDB
		}
		return foundDB
	}

	// No fallback to ~/.beads - return empty string
	return ""
}

// FindBeadsDir finds the .beads/ directory in the current directory tree
// Returns empty string if not found. Supports both database and JSONL-only mode.
// This is useful for commands that need to detect beads projects without requiring a database.
func FindBeadsDir() string {
	// 1. Check BEADS_DIR environment variable (preferred)
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		absBeadsDir := utils.CanonicalizePath(beadsDir)
		if info, err := os.Stat(absBeadsDir); err == nil && info.IsDir() {
			return absBeadsDir
		}
	}

	// 2. Search for .beads/ in current directory and ancestors
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}
	}

	return ""
}

// FindJSONLPath returns the expected JSONL file path for the given database path.
// It searches for existing *.jsonl files in the database directory and returns
// the first one found, preferring beads.jsonl over issues.jsonl (bd-afd).
//
// This function does not create directories or files - it only discovers paths.
// Use this when you need to know where bd stores its JSONL export.
func FindJSONLPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}

	// Get the directory containing the database
	dbDir := filepath.Dir(dbPath)

	// Look for existing .jsonl files in the .beads directory
	pattern := filepath.Join(dbDir, "*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err == nil && len(matches) > 0 {
		// bd-afd: Prefer beads.jsonl over issues.jsonl (canonical name)
		for _, match := range matches {
			if filepath.Base(match) == "beads.jsonl" {
				return match
			}
		}
		// Return the first .jsonl file found if beads.jsonl not present
		return matches[0]
	}

	// bd-afd: Default to beads.jsonl (was issues.jsonl)
	return filepath.Join(dbDir, "beads.jsonl")
}

// DatabaseInfo contains information about a discovered beads database
type DatabaseInfo struct {
	Path      string // Full path to the .db file
	BeadsDir  string // Parent .beads directory
	IssueCount int   // Number of issues (-1 if unknown)
}

// findDatabaseInTree walks up the directory tree looking for .beads/*.db
// Prefers config.json, falls back to beads.db, and returns an error if multiple .db files exist
func findDatabaseInTree() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Resolve symlinks in working directory to ensure consistent path handling
	// This prevents issues when repos are accessed via symlinks (e.g. /Users/user/Code -> /Users/user/Documents/Code)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolvedDir
	}

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Check for config.json first (single source of truth)
			if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
				dbPath := cfg.DatabasePath(beadsDir)
				if _, err := os.Stat(dbPath); err == nil {
					return dbPath
				}
			}

			// Fall back to canonical beads.db for backward compatibility
			canonicalDB := filepath.Join(beadsDir, CanonicalDatabaseName)
			if _, err := os.Stat(canonicalDB); err == nil {
			return canonicalDB
			}

			// Found .beads/ directory, look for *.db files
			matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
			if err == nil && len(matches) > 0 {
			// Filter out backup files and vc.db
			var validDBs []string
			for _, match := range matches {
			baseName := filepath.Base(match)
			// Skip backup files (contains ".backup" in name) and vc.db
			if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
			validDBs = append(validDBs, match)
			}
			}

			if len(validDBs) > 1 {
			// Multiple databases found - this is ambiguous
			// Print error to stderr but return the first one for backward compatibility
			fmt.Fprintf(os.Stderr, "Warning: Multiple database files found in %s:\n", beadsDir)
			for _, db := range validDBs {
			fmt.Fprintf(os.Stderr, "  - %s\n", filepath.Base(db))
			}
			fmt.Fprintf(os.Stderr, "Run 'bd init' to migrate to %s or manually remove old databases.\n\n", CanonicalDatabaseName)
			}

			if len(validDBs) > 0 {
			// Check if using legacy name and warn
			 dbName := filepath.Base(validDBs[0])
			  if dbName != CanonicalDatabaseName {
					isLegacy := false
					for _, legacy := range LegacyDatabaseNames {
						if dbName == legacy {
							isLegacy = true
							break
						}
					}
					if isLegacy {
						fmt.Fprintf(os.Stderr, "WARNING: Using legacy database name: %s\n", dbName)
						fmt.Fprintf(os.Stderr, "Run 'bd migrate' to upgrade to canonical name: %s\n\n", CanonicalDatabaseName)
					}
				}
				return validDBs[0]
			}
		}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return ""
}

// FindAllDatabases scans the directory hierarchy for all .beads directories
// Returns a slice of DatabaseInfo for each database found, starting from the
// closest to CWD (most relevant) to the furthest (least relevant).
func FindAllDatabases() []DatabaseInfo {
	databases := []DatabaseInfo{} // Initialize to empty slice, never return nil
	seen := make(map[string]bool) // Track canonical paths to avoid duplicates

	dir, err := os.Getwd()
	if err != nil {
		return databases
	}

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Found .beads/ directory, look for *.db files
			matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
			if err == nil && len(matches) > 0 {
				dbPath := matches[0]

				// Resolve symlinks to get canonical path for deduplication
				canonicalPath := dbPath
				if resolved, err := filepath.EvalSymlinks(dbPath); err == nil {
					canonicalPath = resolved
				}

				// Skip if we've already seen this database (via symlink or other path)
				if seen[canonicalPath] {
					// Move up one directory
					parent := filepath.Dir(dir)
					if parent == dir {
						break
					}
					dir = parent
					continue
				}
				seen[canonicalPath] = true

				// Count issues if we can open the database (best-effort)
				issueCount := -1
				// Don't fail if we can't open/query the database - it might be locked
				// or corrupted, but we still want to detect and warn about it
				ctx := context.Background()
				store, err := sqlite.New(ctx, dbPath)
				if err == nil {
					if issues, err := store.SearchIssues(ctx, "", types.IssueFilter{}); err == nil {
						issueCount = len(issues)
					}
					_ = store.Close()
				}

				databases = append(databases, DatabaseInfo{
					Path:       dbPath,
					BeadsDir:   beadsDir,
					IssueCount: issueCount,
				})
			}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return databases
}
