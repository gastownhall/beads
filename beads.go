// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For detailed guidance on extending bd, see EXTENDING.md.
package beads

import (
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// Core types for working with issues
type (
	Issue      = types.Issue
	Status     = types.Status
	IssueType  = types.IssueType
	WorkFilter = types.WorkFilter
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

// Storage provides the minimal interface for extension orchestration
type Storage = storage.Storage

// NewSQLiteStorage opens a bd SQLite database for programmatic access.
// Most extensions should use this to query ready work and update issue status.
func NewSQLiteStorage(dbPath string) (Storage, error) {
	return sqlite.New(dbPath)
}

// FindDatabasePath discovers the bd database path using bd's standard search order:
//  1. $BEADS_DB environment variable
//  2. .beads/*.db in current directory or ancestors
//  3. ~/.beads/default.db (fallback)
//
// Returns empty string if no database is found at (1) or (2) and (3) doesn't exist.
func FindDatabasePath() string {
	// 1. Check environment variable
	if envDB := os.Getenv("BEADS_DB"); envDB != "" {
		return envDB
	}

	// 2. Search for .beads/*.db in current directory and ancestors
	if foundDB := findDatabaseInTree(); foundDB != "" {
		return foundDB
	}

	// 3. Try home directory default
	if home, err := os.UserHomeDir(); err == nil {
		defaultDB := filepath.Join(home, ".beads", "default.db")
		// Only return if it exists
		if _, err := os.Stat(defaultDB); err == nil {
			return defaultDB
		}
	}

	return ""
}

// FindJSONLPath returns the expected JSONL file path for the given database path.
// It searches for existing *.jsonl files in the database directory and returns
// the first one found, or defaults to "issues.jsonl".
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
		// Return the first .jsonl file found
		return matches[0]
	}

	// Default to issues.jsonl
	return filepath.Join(dbDir, "issues.jsonl")
}

// findDatabaseInTree walks up the directory tree looking for .beads/*.db
func findDatabaseInTree() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Found .beads/ directory, look for *.db files
			matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
			if err == nil && len(matches) > 0 {
				// Return first .db file found
				return matches[0]
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
