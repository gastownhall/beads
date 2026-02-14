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
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// CanonicalDatabaseName is the required database filename for all beads repositories
const CanonicalDatabaseName = "beads.db"

// RedirectFileName is the name of the file that redirects to another .beads directory
const RedirectFileName = "redirect"

// LegacyDatabaseNames are old names that should be migrated
var LegacyDatabaseNames = []string{"bd.db", "issues.db", "bugs.db"}

// FollowRedirect checks if a .beads directory contains a redirect file and follows it.
// If a redirect file exists, it returns the target .beads directory path.
// If no redirect exists or there's an error, it returns the original path unchanged.
//
// The redirect file should contain a single path (relative or absolute) to the target
// .beads directory. Relative paths are resolved from the parent directory of the
// original .beads directory (i.e., the project root).
//
// Redirect chains are not followed - only one level of redirection is supported.
// This prevents infinite loops and keeps the behavior predictable.
func FollowRedirect(beadsDir string) string {
	redirectFile := filepath.Join(beadsDir, RedirectFileName)
	data, err := os.ReadFile(redirectFile)
	if err != nil {
		// No redirect file or can't read it - use original path
		return beadsDir
	}

	// Parse the redirect target (trim whitespace and handle comments)
	target := strings.TrimSpace(string(data))

	// Skip empty lines and comments to find the actual path
	lines := strings.Split(target, "\n")
	target = ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			target = line
			break
		}
	}

	if target == "" {
		return beadsDir
	}

	// Resolve relative paths from the parent of the .beads directory (project root)
	if !filepath.IsAbs(target) {
		projectRoot := filepath.Dir(beadsDir)
		target = filepath.Join(projectRoot, target)
	}

	// Canonicalize the target path
	target = utils.CanonicalizePath(target)

	// Verify the target exists and is a directory
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		// Invalid redirect target - fall back to original
		fmt.Fprintf(os.Stderr, "Warning: redirect target does not exist or is not a directory: %s\n", target)
		return beadsDir
	}

	// Prevent redirect chains - don't follow if target also has a redirect
	targetRedirect := filepath.Join(target, RedirectFileName)
	if _, err := os.Stat(targetRedirect); err == nil {
		fmt.Fprintf(os.Stderr, "Warning: redirect chains not allowed, ignoring redirect in %s\n", target)
	}

	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Followed redirect from %s -> %s\n", beadsDir, target)
	}

	return target
}

// RedirectInfo contains information about a beads directory redirect.
type RedirectInfo struct {
	// IsRedirected is true if the local .beads has a redirect file
	IsRedirected bool
	// LocalDir is the local .beads directory (the one with the redirect file)
	LocalDir string
	// TargetDir is the actual .beads directory being used (after following redirect)
	TargetDir string
}

// GetRedirectInfo checks if the current beads directory is redirected.
// It searches for the local .beads/ directory and checks if it contains a redirect file.
// Returns RedirectInfo with IsRedirected=true if a redirect is active.
//
// bd-wayc3: This function now also checks the git repo's local .beads directory even when
// BEADS_DIR is set. This handles the case where BEADS_DIR is pre-set to the redirect target
// (e.g., by shell environment or tooling), but we still need to detect that a redirect exists.
func GetRedirectInfo() RedirectInfo {
	// First, always check the git repo's local .beads directory for redirects
	// This handles the case where BEADS_DIR is pre-set to the redirect target
	if localBeadsDir := findLocalBdsDirInRepo(); localBeadsDir != "" {
		if info := checkRedirectInDir(localBeadsDir); info.IsRedirected {
			return info
		}
	}

	// Fall back to original logic for non-git-repo cases
	if localBeadsDir := findLocalBeadsDir(); localBeadsDir != "" {
		return checkRedirectInDir(localBeadsDir)
	}

	return RedirectInfo{}
}

// checkRedirectInDir checks if a beads directory has a redirect file and returns redirect info.
// Returns RedirectInfo with IsRedirected=true if a valid redirect exists.
func checkRedirectInDir(beadsDir string) RedirectInfo {
	info := RedirectInfo{LocalDir: beadsDir}

	// Check if this directory has a redirect file
	redirectFile := filepath.Join(beadsDir, RedirectFileName)
	if _, err := os.Stat(redirectFile); err != nil {
		// No redirect file
		return info
	}

	// There's a redirect - find the target
	targetDir := FollowRedirect(beadsDir)
	if targetDir == beadsDir {
		// Redirect file exists but failed to resolve (invalid target)
		return info
	}

	info.IsRedirected = true
	info.TargetDir = targetDir
	return info
}

// findLocalBdsDirInRepo finds the .beads directory relative to the git repo root.
// This ignores BEADS_DIR to find the "true local" .beads for redirect detection.
// bd-wayc3: Added to detect redirects even when BEADS_DIR is pre-set.
func findLocalBdsDirInRepo() string {
	// Get git repo root
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return ""
	}

	beadsDir := filepath.Join(repoRoot, ".beads")
	if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
		return beadsDir
	}

	return ""
}

// findLocalBeadsDir finds the local .beads directory without following redirects.
// This is used to detect if a redirect is configured.
func findLocalBeadsDir() string {
	// Check BEADS_DIR environment variable first
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		return utils.CanonicalizePath(beadsDir)
	}

	// Check for worktree - use main repo's .beads
	// Note: GetMainRepoRoot() is safe to call outside a git repo - it returns an error
	mainRepoRoot, err := git.GetMainRepoRoot()
	if err == nil && mainRepoRoot != "" {
		beadsDir := filepath.Join(mainRepoRoot, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}
	}

	// Walk up directory tree
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	for dir := cwd; dir != "/" && dir != "."; {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root (works on both Unix and Windows)
			// On Unix: filepath.Dir("/") returns "/"
			// On Windows: filepath.Dir("C:\\") returns "C:\\"
			break
		}
		dir = parent
	}

	return ""
}

// findDatabaseInBeadsDir searches for a database file within a .beads directory.
// It implements the standard search order:
// 1. Check metadata.json first (single source of truth)
//   - For SQLite backend: returns path to .db file
//   - For Dolt backend: returns path to dolt/ directory
//
// 2. Fall back to canonical beads.db
// 3. Search for *.db files, filtering out backups and vc.db
//
// If warnOnIssues is true, warnings are printed to stderr for:
// - Multiple databases found (ambiguous state)
// - Legacy database names that should be migrated
//
// Returns empty string if no database is found.
func findDatabaseInBeadsDir(beadsDir string, warnOnIssues bool) string {
	// Check for metadata.json first (single source of truth)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		backend := cfg.GetBackend()
		if backend == configfile.BackendDolt {
			// For Dolt server mode, database is on the server - no local directory required
			if cfg.IsDoltServerMode() {
				return cfg.DatabasePath(beadsDir)
			}
			// For embedded Dolt, check if the configured database directory exists
			doltPath := cfg.DatabasePath(beadsDir)
			if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
				return doltPath
			}
		} else {
			// For SQLite, check if the .db file exists
			dbPath := cfg.DatabasePath(beadsDir)
			if _, err := os.Stat(dbPath); err == nil {
				return dbPath
			}
		}
	}

	// Fall back to canonical beads.db for backward compatibility
	canonicalDB := filepath.Join(beadsDir, CanonicalDatabaseName)
	if _, err := os.Stat(canonicalDB); err == nil {
		return canonicalDB
	}

	// Look for any .db file in the beads directory
	matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	if err != nil || len(matches) == 0 {
		return ""
	}

	// Filter out backup files and vc.db
	var validDBs []string
	for _, match := range matches {
		baseName := filepath.Base(match)
		// Skip backup files (contains ".backup" in name) and vc.db
		if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
			validDBs = append(validDBs, match)
		}
	}

	if len(validDBs) == 0 {
		return ""
	}

	if warnOnIssues {
		// Warn about multiple databases found
		if len(validDBs) > 1 {
			fmt.Fprintf(os.Stderr, "Warning: Multiple database files found in %s:\n", beadsDir)
			for _, db := range validDBs {
				fmt.Fprintf(os.Stderr, "  - %s\n", filepath.Base(db))
			}
			fmt.Fprintf(os.Stderr, "Run 'bd init' to migrate to %s or manually remove old databases.\n\n", CanonicalDatabaseName)
		}

		// Warn about legacy database names
		dbName := filepath.Base(validDBs[0])
		if dbName != CanonicalDatabaseName {
			for _, legacy := range LegacyDatabaseNames {
				if dbName == legacy {
					fmt.Fprintf(os.Stderr, "WARNING: Using legacy database name: %s\n", dbName)
					fmt.Fprintf(os.Stderr, "Run 'bd migrate' to upgrade to canonical name: %s\n\n", CanonicalDatabaseName)
					break
				}
			}
		}
	}

	return validDBs[0]
}

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
)

// Status constants
const (
	StatusOpen       = types.StatusOpen
	StatusInProgress = types.StatusInProgress
	StatusBlocked    = types.StatusBlocked
	StatusClosed     = types.StatusClosed
)

// IssueType constants (core types only - Gas Town types removed)
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

// Storage provides the minimal interface for extension orchestration
type Storage = storage.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = storage.Transaction

// NewStorage opens a bd database for programmatic access.
// Deprecated: callers should use storage/factory package to respect backend configuration.
// This function is retained for backward compatibility but will be removed in a future release.
func NewStorage(ctx context.Context, dbPath string) (Storage, error) {
	// Cannot use factory here due to import cycle (beads -> factory -> beads).
	// Callers should use storage/factory.New() directly instead.
	return nil, fmt.Errorf("NewStorage is deprecated: use storage/factory.New() to create storage backends")
}

// GetConfiguredBackend returns the backend type from the beads directory config.
// Returns "dolt" if no config exists or backend is not specified.
func GetConfiguredBackend(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return configfile.BackendDolt
	}
	backend := cfg.GetBackend()
	if backend == "" || backend == configfile.BackendSQLite {
		return configfile.BackendDolt
	}
	return backend
}

// FindDatabasePath discovers the bd database path using bd's standard search order:
//  1. $BEADS_DIR environment variable (points to .beads directory)
//  2. $BEADS_DB environment variable (points directly to database file, deprecated)
//  3. .beads/*.db in current directory or ancestors
//
// Redirect files are supported: if a .beads/redirect file exists, its contents
// are used as the actual .beads directory path.
//
// Returns empty string if no database is found.
func FindDatabasePath() string {
	// 1. Check BEADS_DIR environment variable (preferred)
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		// Canonicalize the path to prevent nested .beads directories
		absBeadsDir := utils.CanonicalizePath(beadsDir)

		// Follow redirect if present
		absBeadsDir = FollowRedirect(absBeadsDir)

		// Use helper to find database (no warnings for BEADS_DIR - user explicitly set it)
		if dbPath := findDatabaseInBeadsDir(absBeadsDir, false); dbPath != "" {
			return dbPath
		}

		// BEADS_DIR is set but no database found - this is OK for --no-db mode
		// Return empty string and let the caller handle it
	}

	// 2. Check BEADS_DB environment variable (deprecated but still supported)
	if envDB := os.Getenv("BEADS_DB"); envDB != "" {
		return utils.CanonicalizePath(envDB)
	}

	// 3. Search for .beads/*.db in current directory and ancestors
	if foundDB := findDatabaseInTree(); foundDB != "" {
		return utils.CanonicalizePath(foundDB)
	}

	// No fallback to ~/.beads - return empty string
	return ""
}

// hasBeadsProjectFiles checks if a .beads directory contains actual project files.
// Returns true if the directory contains any of:
// - metadata.json or config.yaml (project configuration)
// - Any *.db file (excluding backups and vc.db)
// - Any *.jsonl file (JSONL-only mode or git-tracked issues)
//
// Returns false for directories that only contain daemon registry files.
// This prevents FindBeadsDir from returning ~/.beads/ which only has registry.json.
func hasBeadsProjectFiles(beadsDir string) bool {
	// Check for project configuration files
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "config.yaml")); err == nil {
		return true
	}

	// Check for database files (excluding backups and vc.db)
	dbMatches, _ := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	for _, match := range dbMatches {
		baseName := filepath.Base(match)
		if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
			return true
		}
	}

	// Check for JSONL files (JSONL-only mode or fresh clone)
	jsonlMatches, _ := filepath.Glob(filepath.Join(beadsDir, "*.jsonl"))
	if len(jsonlMatches) > 0 {
		return true
	}

	return false
}

// FindBeadsDir finds the .beads/ directory in the current directory tree
// Returns empty string if not found. Supports both database and JSONL-only mode.
// Stops at the git repository root to avoid finding unrelated directories.
// Validates that the directory contains actual project files.
// Redirect files are supported: if a .beads/redirect file exists, its contents
// are used as the actual .beads directory path.
// For worktrees, prioritizes the main repository's .beads directory.
// This is useful for commands that need to detect beads projects without requiring a database.
func FindBeadsDir() string {
	// 1. Check BEADS_DIR environment variable (preferred)
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		absBeadsDir := utils.CanonicalizePath(beadsDir)

		// Follow redirect if present
		absBeadsDir = FollowRedirect(absBeadsDir)

		if info, err := os.Stat(absBeadsDir); err == nil && info.IsDir() {
			// Validate directory contains actual project files
			if hasBeadsProjectFiles(absBeadsDir) {
				return absBeadsDir
			}
		}
	}

	// 2. For worktrees, check main repository root first
	var mainRepoRoot string
	if git.IsWorktree() {
		var err error
		mainRepoRoot, err = git.GetMainRepoRoot()
		if err == nil && mainRepoRoot != "" {
			beadsDir := filepath.Join(mainRepoRoot, ".beads")
			if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
				// Follow redirect if present
				beadsDir = FollowRedirect(beadsDir)

				// Validate directory contains actual project files
				if hasBeadsProjectFiles(beadsDir) {
					return beadsDir
				}
			}
		}
	}

	// 3. Search for .beads/ in current directory and ancestors
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Find git root to limit the search
	gitRoot := findGitRoot()
	worktreeRoot := gitRoot // save worktree-specific boundary
	if git.IsWorktree() && mainRepoRoot != "" {
		// For worktrees, extend search boundary to include main repo
		gitRoot = mainRepoRoot
	}

	for dir := cwd; dir != "/" && dir != "."; {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Follow redirect if present
			beadsDir = FollowRedirect(beadsDir)

			// Validate directory contains actual project files
			if hasBeadsProjectFiles(beadsDir) {
				return beadsDir
			}
		}

		// Stop at git root to avoid finding unrelated directories
		if gitRoot != "" && dir == gitRoot {
			break
		}

		// Also stop at worktree root when it differs from main repo root
		// This prevents escaping the worktree boundary into unrelated directories
		if worktreeRoot != "" && worktreeRoot != gitRoot && dir == worktreeRoot {
			break
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root (works on both Unix and Windows)
			// On Unix: filepath.Dir("/") returns "/"
			// On Windows: filepath.Dir("C:\\") returns "C:\\"
			break
		}
		dir = parent
	}

	return ""
}

// FindJSONLPath returns the expected JSONL file path for the given database path.
// It searches for existing *.jsonl files in the database directory and returns
// the first one found, preferring issues.jsonl over beads.jsonl.
//
// This function does not create directories or files - it only discovers paths.
// Use this when you need to know where bd stores its JSONL export.
func FindJSONLPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}

	// Get the directory containing the database and delegate to shared utility
	return utils.FindJSONLInDir(filepath.Dir(dbPath))
}

// DatabaseInfo contains information about a discovered beads database
type DatabaseInfo struct {
	Path       string // Full path to the .db file
	BeadsDir   string // Parent .beads directory
	IssueCount int    // Number of issues (-1 if unknown)
}

// findGitRoot returns the root directory of the current git repository,
// or empty string if not in a git repository. Used to limit directory
// tree walking to within the current git repo.
//
// This function delegates to git.GetRepoRoot() which is worktree-aware
// and handles Windows path normalization.
func findGitRoot() string {
	return git.GetRepoRoot()
}

// findDatabaseInTree walks up the directory tree looking for .beads/*.db
// Stops at the git repository root to avoid finding unrelated databases.
// For worktrees, searches the main repository root first, then falls back to worktree.
// Prefers config.json, falls back to beads.db, and warns if multiple .db files exist.
// Redirect files are supported: if a .beads/redirect file exists, its contents
// are used as the actual .beads directory path.
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

	// Check if we're in a git worktree
	var mainRepoRoot string
	if git.IsWorktree() {
		// For worktrees, search main repository root first
		var err error
		mainRepoRoot, err = git.GetMainRepoRoot()
		if err == nil && mainRepoRoot != "" {
			beadsDir := filepath.Join(mainRepoRoot, ".beads")
			if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
				// Follow redirect if present
				beadsDir = FollowRedirect(beadsDir)

				// Use helper to find database (with warnings for auto-discovery)
				if dbPath := findDatabaseInBeadsDir(beadsDir, true); dbPath != "" {
					return dbPath
				}
			}
		}
		// If not found in main repo, fall back to worktree search below
	}

	// Find git root to limit the search
	gitRoot := findGitRoot()
	if git.IsWorktree() && mainRepoRoot != "" {
		// For worktrees, extend search boundary to include main repo
		gitRoot = mainRepoRoot
	}

	// Walk up directory tree (regular repository or worktree fallback)
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Follow redirect if present
			beadsDir = FollowRedirect(beadsDir)

			// Use helper to find database (with warnings for auto-discovery)
			if dbPath := findDatabaseInBeadsDir(beadsDir, true); dbPath != "" {
				return dbPath
			}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}

		// Stop at git root to avoid finding unrelated databases
		if gitRoot != "" && dir == gitRoot {
			break
		}

		dir = parent
	}

	return ""
}

// FindAllDatabases scans the directory hierarchy for the closest .beads directory.
// Returns a slice with at most one DatabaseInfo - the closest database to CWD.
// Stops searching upward as soon as a .beads directory is found,
// because in multi-workspace setups, nested .beads directories
// are intentional and separate - parent directories are out of scope.
// Redirect files are supported: if a .beads/redirect file exists, its contents
// are used as the actual .beads directory path.
func FindAllDatabases() []DatabaseInfo {
	databases := []DatabaseInfo{} // Initialize to empty slice, never return nil
	seen := make(map[string]bool) // Track canonical paths to avoid duplicates

	dir, err := os.Getwd()
	if err != nil {
		return databases
	}

	// Find git root to limit the search
	gitRoot := findGitRoot()

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Follow redirect if present
			beadsDir = FollowRedirect(beadsDir)

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
				//
				// Note: Issue counting requires storage/factory which cannot be imported
				// here due to import cycle constraints. Callers needing issue counts
				// should use storage/factory directly.

				databases = append(databases, DatabaseInfo{
					Path:       dbPath,
					BeadsDir:   beadsDir,
					IssueCount: issueCount,
				})

				// Stop searching upward - the closest .beads is the one to use
				// Parent directories are out of scope in multi-workspace setups
				break
			}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}

		// Stop at git root to avoid finding unrelated databases
		if gitRoot != "" && dir == gitRoot {
			break
		}

		dir = parent
	}

	return databases
}
