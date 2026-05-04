// Package beads provides a minimal public API for extending bd with custom orchestration.
//
// Most extensions should use direct SQL queries against bd's database.
// This package exports only the essential types and functions needed for
// Go-based extensions that want to use bd's storage layer programmatically.
//
// For a working extension example, see examples/bd-example-extension-go.
package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/utils"
)

// CanonicalDatabaseName is the required database filename for all beads repositories
const CanonicalDatabaseName = "beads.db"

// RedirectFileName is the name of the file that redirects to another .beads directory
const RedirectFileName = "redirect"

// SourceDatabaseInfo contains the dolt_database name from a source .beads/metadata.json,
// preserved across a redirect so that the source directory's database identity is not
// lost when the redirect target has a different dolt_database.
//
// When a .beads/redirect points to a shared .beads directory that serves multiple
// databases, the source's metadata.json may specify a different dolt_database than
// the target's. This struct captures the source database name so callers can
// restore it after redirect resolution.
type SourceDatabaseInfo struct {
	// SourceDir is the original .beads directory (before redirect)
	SourceDir string
	// TargetDir is the resolved .beads directory (after redirect)
	TargetDir string
	// WasRedirected is true if a redirect was followed
	WasRedirected bool
	// SourceDatabase is dolt_database from the source metadata.json (raw field,
	// NOT the env-var-aware GetDoltDatabase()). Empty if no source metadata exists
	// or the source has no dolt_database configured.
	SourceDatabase string
}

// ResolveRedirect follows a .beads/redirect file and captures the source directory's
// dolt_database from metadata.json BEFORE following the redirect. This preserves
// the source database identity across redirects.
//
// The env var BEADS_DOLT_SERVER_DATABASE still takes highest priority (handled by
// GetDoltDatabase() in callers). This function only captures the raw config field
// so callers can use it as an override when the env var is not set.
//
// Returns SourceDatabaseInfo with WasRedirected=true if a redirect was followed,
// and SourceDatabase set to the source's dolt_database (if any).
func ResolveRedirect(beadsDir string) SourceDatabaseInfo {
	info := SourceDatabaseInfo{
		SourceDir: beadsDir,
		TargetDir: beadsDir,
	}

	// Read source metadata.json directly (NOT via configfile.Load which may trigger
	// Dolt connections or recursive FollowRedirect calls causing deadlocks).
	// We only need the raw dolt_database field.
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if data, err := os.ReadFile(metadataPath); err == nil {
		var raw struct {
			DoltDatabase string `json:"dolt_database"`
		}
		if json.Unmarshal(data, &raw) == nil {
			info.SourceDatabase = raw.DoltDatabase
		}
	}

	// Follow redirect
	resolved := FollowRedirect(beadsDir)
	if resolved != beadsDir {
		info.WasRedirected = true
		info.TargetDir = resolved
	}

	return info
}

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

	// Canonicalize the target path and prefer a stable branch worktree when the
	// redirect points at a detached snapshot checkout.
	target = canonicalizeBeadsDirPath(target)

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

func canonicalizeBeadsDirPath(beadsDir string) string {
	canonical := utils.CanonicalizePath(beadsDir)
	if stable := preferStableBranchWorktreeBeadsDir(canonical); stable != "" {
		return stable
	}
	return canonical
}

type worktreeInfo struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
}

func preferStableBranchWorktreeBeadsDir(beadsDir string) string {
	if filepath.Base(beadsDir) != ".beads" {
		return ""
	}

	repoRoot := filepath.Dir(beadsDir)
	if !isDetachedCommitWorktreePath(repoRoot) {
		return ""
	}

	branch, err := gitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch != "HEAD" {
		return ""
	}

	head, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return ""
	}

	worktrees, err := listWorktrees(repoRoot)
	if err != nil {
		return ""
	}

	var candidates []worktreeInfo
	for _, wt := range worktrees {
		if wt.Bare || wt.Detached || wt.Branch == "" {
			continue
		}
		if wt.Head != head || utils.PathsEqual(wt.Path, repoRoot) {
			continue
		}
		candidates = append(candidates, wt)
	}

	if len(candidates) == 0 {
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		iStable := !isDetachedCommitWorktreePath(candidates[i].Path)
		jStable := !isDetachedCommitWorktreePath(candidates[j].Path)
		if iStable != jStable {
			return iStable
		}
		return candidates[i].Path < candidates[j].Path
	})

	stableBeadsDir := filepath.Join(candidates[0].Path, ".beads")
	if info, err := os.Stat(stableBeadsDir); err == nil && info.IsDir() {
		return utils.CanonicalizePath(stableBeadsDir)
	}

	return ""
}

// isDetachedCommitWorktreePath checks if a path follows the megarepo convention
// of placing detached worktrees under refs/commits/<sha>.
func isDetachedCommitWorktreePath(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/refs/commits/")
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // args are internal, not user-supplied
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func listWorktrees(repoRoot string) ([]worktreeInfo, error) {
	output, err := gitOutput(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var worktrees []worktreeInfo
	var current *worktreeInfo

	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current != nil {
				worktrees = append(worktrees, *current)
			}
			current = &worktreeInfo{
				Path: strings.TrimPrefix(line, "worktree "),
			}
		case current == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			current.Detached = true
		case line == "bare":
			current.Bare = true
		}
	}

	if current != nil {
		worktrees = append(worktrees, *current)
	}

	return worktrees, nil
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
		return canonicalizeBeadsDirPath(beadsDir)
	}

	// For worktrees, check worktree-local redirect first (per-worktree override).
	// Returns the raw worktree .beads dir (not the resolved target) since
	// findLocalBeadsDir doesn't follow redirects — callers use FollowRedirect.
	if git.IsWorktree() {
		if root := git.GetRepoRoot(); root != "" {
			wt := filepath.Join(root, ".beads")
			// Check for redirect file first
			if _, err := os.Stat(filepath.Join(wt, "redirect")); err == nil {
				return wt
			}
			// Check for worktree's own .beads with project files (separate-DB mode)
			if info, err := os.Stat(wt); err == nil && info.IsDir() {
				if hasBeadsProjectFiles(wt) {
					return wt
				}
			}
		}
	}

	if beadsDir := GetWorktreeFallbackBeadsDir(); beadsDir != "" {
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

// findDatabaseInBeadsDir searches for a database within a .beads directory.
// Checks metadata.json for the Dolt database path. For server mode, no local
// directory is required. For embedded mode, checks both the embeddeddolt/
// directory (where the embedded engine stores data) and the legacy dolt/ path.
// Returns empty string if no database is found.
func findDatabaseInBeadsDir(beadsDir string, _ bool) string {
	// Check for metadata.json first (single source of truth)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		// For Dolt server mode, database is on the server - no local directory required
		if cfg.IsDoltServerMode() {
			return cfg.DatabasePath(beadsDir)
		}
		// For embedded Dolt, the engine stores data under .beads/embeddeddolt/,
		// not .beads/dolt/. Check the actual embedded data directory first.
		embeddedPath := filepath.Join(beadsDir, "embeddeddolt")
		if info, err := os.Stat(embeddedPath); err == nil && info.IsDir() {
			return embeddedPath
		}
		// Fall back to configured database path (e.g. .beads/dolt/ for
		// server-mode installs or legacy setups that pre-date embeddeddolt).
		doltPath := cfg.DatabasePath(beadsDir)
		if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
			return doltPath
		}
	}

	// Fall back: check if embeddeddolt or dolt directory exists without metadata.json
	embeddedPath := filepath.Join(beadsDir, "embeddeddolt")
	if info, err := os.Stat(embeddedPath); err == nil && info.IsDir() {
		return embeddedPath
	}
	doltPath := filepath.Join(beadsDir, "dolt")
	if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
		return doltPath
	}

	return ""
}

// Storage provides the minimal interface for extension orchestration
type Storage = storage.Storage

// Transaction provides atomic multi-operation support within a database transaction.
// Use Storage.RunInTransaction() to obtain a Transaction instance.
type Transaction = storage.Transaction

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
		absBeadsDir := canonicalizeBeadsDirPath(beadsDir)

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
		absDB := utils.CanonicalizePath(envDB)
		// If BEADS_DB points to a directory rather than a file, treat it
		// like BEADS_DIR to avoid filepath.Dir() resolving one level too
		// high in the caller (cmd/bd/main.go). See GH#2548.
		if info, err := os.Stat(absDB); err == nil && info.IsDir() {
			if dbPath := findDatabaseInBeadsDir(absDB, false); dbPath != "" {
				return dbPath
			}
		}
		return absDB
	}

	// 3. Search for .beads/*.db in current directory and ancestors
	if foundDB := findDatabaseInTree(); foundDB != "" {
		return utils.CanonicalizePath(foundDB)
	}

	// No fallback to ~/.beads - return empty string
	return ""
}

// FindBeadsDirFrom finds the effective .beads/ directory as if discovery
// started from startDir, without changing the process working directory.
func FindBeadsDirFrom(startDir string) string {
	if startDir == "" {
		return ""
	}

	info, err := os.Stat(startDir)
	if err != nil || !info.IsDir() {
		return ""
	}

	startDir = utils.CanonicalizePath(startDir)
	repoRoot := ""
	if out, err := gitOutput(startDir, "rev-parse", "--show-toplevel"); err == nil {
		repoRoot = utils.CanonicalizePath(out)
	}
	fallbackBeadsDir := ""
	fallbackHasDB := false
	if repoRoot != "" {
		fallbackBeadsDir = worktreeFallbackBeadsDirForRepo(repoRoot)
		if fallbackBeadsDir != "" {
			if fbInfo, err := os.Stat(fallbackBeadsDir); err == nil && fbInfo.IsDir() {
				fallbackHasDB = hasBeadsDatabase(FollowRedirect(fallbackBeadsDir))
			}
		}
	}

	for dir := startDir; dir != "/" && dir != "."; {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			resolved := FollowRedirect(beadsDir)
			isWorktreeRoot := repoRoot != "" && utils.PathsEqual(dir, repoRoot)
			if isWorktreeRoot && fallbackHasDB && !hasBeadsDatabase(resolved) {
				// A worktree root can contain tracked .beads metadata without
				// owning the ignored database directory. Match FindBeadsDir by
				// preferring the shared worktree database in that case.
			} else if hasBeadsProjectFiles(resolved) {
				return resolved
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if fallbackBeadsDir != "" {
		if info, err := os.Stat(fallbackBeadsDir); err == nil && info.IsDir() {
			resolved := FollowRedirect(fallbackBeadsDir)
			if hasBeadsProjectFiles(resolved) {
				return resolved
			}
		}
	}

	return ""
}

// hasBeadsProjectFiles checks if a .beads directory contains actual project files.
// Returns true if the directory contains any of:
// - metadata.json or config.yaml (project configuration)
// - Any *.db file (excluding backups and vc.db)
// - A dolt/ directory (Dolt database)
//
// Returns false for directories that only contain legacy registry files.
// This prevents FindBeadsDir from returning ~/.beads/ which only has registry.json.
func hasBeadsProjectFiles(beadsDir string) bool {
	// Check for project configuration files
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "config.yaml")); err == nil {
		return true
	}

	// Check for Dolt database directory (server mode uses dolt/, embedded uses embeddeddolt/)
	if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err == nil && info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); err == nil && info.IsDir() {
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

	return false
}

// hasBeadsDatabase is the strict counterpart to hasBeadsProjectFiles: it
// returns true only when beadsDir contains an actual database — a dolt/
// directory, an embeddeddolt/ directory, or a non-backup *.db file. Mere
// presence of metadata.json / config.yaml / issues.jsonl does not count.
//
// Used by FindBeadsDir's worktree-separate-DB branch to distinguish a
// genuine separate-database worktree (which owns its own Dolt data) from
// a worktree that has inherited tracked .beads/ artifacts through a git
// checkout of the parent repo's working-tree snapshot. Without this strict
// check, the separate-DB branch would match on inherited metadata.json and
// return a broken directory, short-circuiting the shared-DB fallback.
func hasBeadsDatabase(beadsDir string) bool {
	if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err == nil && info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); err == nil && info.IsDir() {
		return true
	}
	dbMatches, _ := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	for _, match := range dbMatches {
		baseName := filepath.Base(match)
		if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
			return true
		}
	}
	return false
}

// FindBeadsDir finds the .beads/ directory in the current directory tree.
// Returns empty string if not found.
//
// Resolution order:
//  1. BEADS_DIR environment variable (highest priority)
//  2. Walk up from CWD toward repo root boundary, checking each directory
//     for .beads/ with valid project files. For worktrees, stops at the
//     worktree root; for non-worktrees, stops at the git root.
//  3. Worktree-specific fallback: per-worktree redirect, worktree's own
//     .beads (separate-DB mode), shared .beads via git-common-dir.
//  4. Extended walk from the boundary to the main repo root (worktrees)
//     or checks the git root itself (non-worktrees).
//
// Validates that directories contain actual project files (metadata.json,
// config.yaml, dolt/, embeddeddolt/, or *.db).
// Redirect files are supported: if a .beads/redirect file exists, its
// contents are used as the actual .beads directory path.
func FindBeadsDir() string {
	// 1. Check BEADS_DIR environment variable (preferred)
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		absBeadsDir := canonicalizeBeadsDirPath(beadsDir)

		// Follow redirect if present
		absBeadsDir = FollowRedirect(absBeadsDir)

		if info, err := os.Stat(absBeadsDir); err == nil && info.IsDir() {
			// Validate directory contains actual project files
			if hasBeadsProjectFiles(absBeadsDir) {
				return absBeadsDir
			}
		}
	}

	// 2. Walk up from CWD toward the repo root, checking each directory for .beads/.
	// This replaces the former step 1b (CWD-only check) with a proper ancestor walk,
	// fixing the case where CWD is a subdirectory within a rig (not the rig root itself).
	// For worktrees, the walk stops at the worktree root boundary to avoid finding
	// git-tracked .beads/ at the worktree root that has metadata but no database.
	// The worktree-specific fallback logic (step 3) handles worktree root resolution.
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	gitRoot := findGitRoot()

	// Determine the walk-up boundary: worktree root for worktrees, git root otherwise.
	// We stop BEFORE the boundary so worktree fallback logic can handle the root's .beads/.
	isWt := git.IsWorktree()
	walkBoundary := gitRoot
	if isWt {
		// For worktrees, stop the walk at the worktree root.
		// The worktree root's .beads/ may be git-tracked metadata without a real database;
		// the worktree fallback logic (step 3) handles this correctly.
		walkBoundary = git.GetRepoRoot()
	}

	// Canonicalize both walk start and walk boundary so the `dir == walkBoundary`
	// comparison below works even when the two come from different sources
	// (os.Getwd() often returns unresolved symlinks like /var/... on macOS
	// while git rev-parse returns the canonical /private/var/... form). Without
	// this, the boundary check silently never matches and the walk overshoots
	// the worktree root — finding an inherited .beads/ directory there and
	// short-circuiting the worktree-fallback logic in step 3.
	cwdCanonical := utils.CanonicalizePath(cwd)
	walkBoundaryCanonical := ""
	if walkBoundary != "" {
		walkBoundaryCanonical = utils.CanonicalizePath(walkBoundary)
	}
	for dir := cwdCanonical; dir != "/" && dir != "."; {
		// Stop at the walk boundary (exclusive — don't check this directory).
		// For worktrees: stops before worktree root so step 3 handles it.
		// For non-worktrees: stops before git root (which is checked below in the
		// post-worktree walk, step 4).
		if walkBoundaryCanonical != "" && dir == walkBoundaryCanonical {
			break
		}

		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			beadsDir = FollowRedirect(beadsDir)
			if hasBeadsProjectFiles(beadsDir) {
				return beadsDir
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// 3. Worktree-specific fallback: redirect, own .beads, shared .beads.
	// This runs after the walk-up so that rig subdirectories win, but before
	// the extended walk (step 4) so worktree-aware logic is preferred.
	var mainRepoRoot string
	if isWt {
		// 3a. Per-worktree redirect override
		if target := worktreeRedirectTarget(); target != "" {
			if info, err := os.Stat(target); err == nil && info.IsDir() {
				if hasBeadsProjectFiles(target) {
					return target
				}
			}
		}

		// 3b. Worktree's own .beads (separate-DB mode, no redirect).
		//
		// Only accept the worktree-local .beads/ as separate-DB if it owns an
		// actual database (dolt/, embeddeddolt/, or a *.db file). A worktree
		// that only has metadata.json / config.yaml / issues.jsonl is almost
		// certainly carrying tracked artifacts from the parent repo's
		// working-tree snapshot — `git worktree add` checks them out, but the
		// dolt/ data directory is gitignored and therefore absent. Returning
		// such a directory short-circuits the shared-DB fallback (3c) and
		// causes bd to spawn a sidecar Dolt server against an empty data
		// directory, which cannot serve the project's database.
		//
		// If no fallback is available (non-worktree edge case, or the main
		// repo itself has no .beads/), fall back to hasBeadsProjectFiles so a
		// fresh `bd init` can still locate the nascent project directory.
		if worktreeRoot := git.GetRepoRoot(); worktreeRoot != "" {
			worktreeBeadsDir := filepath.Join(worktreeRoot, ".beads")
			if info, err := os.Stat(worktreeBeadsDir); err == nil && info.IsDir() {
				if hasBeadsDatabase(worktreeBeadsDir) {
					return worktreeBeadsDir
				}
				// Lenient acceptance only when there is no shared .beads with
				// a real database to fall back to.
				fallback := GetWorktreeFallbackBeadsDir()
				fallbackHasDB := false
				if fallback != "" {
					if fbInfo, err := os.Stat(fallback); err == nil && fbInfo.IsDir() {
						resolved := FollowRedirect(fallback)
						fallbackHasDB = hasBeadsDatabase(resolved)
					}
				}
				if !fallbackHasDB && hasBeadsProjectFiles(worktreeBeadsDir) {
					return worktreeBeadsDir
				}
			}
		}

		// 3c. Fall back to the canonical shared .beads for this worktree.
		if fallbackBeadsDir := GetWorktreeFallbackBeadsDir(); fallbackBeadsDir != "" {
			if info, err := os.Stat(fallbackBeadsDir); err == nil && info.IsDir() {
				fallbackBeadsDir = FollowRedirect(fallbackBeadsDir)
				if hasBeadsProjectFiles(fallbackBeadsDir) {
					return fallbackBeadsDir
				}
			}
		}

		var err error
		mainRepoRoot, err = git.GetMainRepoRoot()
		if err != nil {
			mainRepoRoot = ""
		}
	}

	// 4. Extended walk: from walk boundary to git/main-repo root.
	// For non-worktrees, this checks the git root itself (the walk-up in step 2
	// stopped before it). For worktrees, this walks from worktree root to main
	// repo root, handling edge cases where .beads/ is between the two.
	// Skip if there was no walk boundary (step 2 already searched everything).
	if walkBoundary != "" {
		extendedRoot := gitRoot
		if isWt && mainRepoRoot != "" {
			extendedRoot = mainRepoRoot
		}
		// Canonicalize the extended-root so the `dir == extendedRoot` check
		// matches when extendedRoot came from a git helper (canonical) and
		// the starting `dir` came from walkBoundary (also canonicalized
		// above). Keeps the walk bounded on macOS-style /var → /private/var.
		extendedRootCanonical := ""
		if extendedRoot != "" {
			extendedRootCanonical = utils.CanonicalizePath(extendedRoot)
		}

		for dir := walkBoundaryCanonical; dir != "/" && dir != "."; {
			beadsDir := filepath.Join(dir, ".beads")
			if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
				beadsDir = FollowRedirect(beadsDir)
				if hasBeadsProjectFiles(beadsDir) {
					return beadsDir
				}
			}

			// Stop at the extended root
			if extendedRootCanonical != "" && dir == extendedRootCanonical {
				break
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return ""
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

// GetWorktreeFallbackBeadsDir returns the canonical shared .beads location for
// the current git worktree when no local redirect or worktree-local .beads is present.
func GetWorktreeFallbackBeadsDir() string {
	if !git.IsWorktree() {
		return ""
	}

	commonDir, err := git.GetGitCommonDir()
	if err != nil || commonDir == "" {
		return ""
	}

	commonDir = utils.CanonicalizePath(commonDir)
	if filepath.Base(commonDir) == ".git" {
		return filepath.Join(filepath.Dir(commonDir), ".beads")
	}

	return filepath.Join(commonDir, ".beads")
}

// ResolveBeadsDirForRepo returns the effective .beads directory for a repo path.
// It prefers a local .beads directory and otherwise falls back to the shared
// worktree location derived from git-common-dir.
//
// Unlike FindBeadsDir, this helper does not use BEADS_DIR and does not walk up
// from CWD. Callers that care about nested rig directories should resolve those
// before falling back to this repo-scoped helper.
func ResolveBeadsDirForRepo(repoPath string) string {
	repoPath = utils.CanonicalizePath(repoPath)
	localBeadsDir := filepath.Join(repoPath, ".beads")
	if info, err := os.Stat(localBeadsDir); err == nil && info.IsDir() {
		return FollowRedirect(localBeadsDir)
	}

	if fallback := worktreeFallbackBeadsDirForRepo(repoPath); fallback != "" {
		return FollowRedirect(fallback)
	}

	return FollowRedirect(localBeadsDir)
}

func worktreeFallbackBeadsDirForRepo(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}

	gitDir := gitPathForRepo(repoPath, strings.TrimSpace(lines[0]))
	commonDir := gitPathForRepo(repoPath, strings.TrimSpace(lines[1]))
	if gitDir == "" || commonDir == "" || utils.PathsEqual(gitDir, commonDir) {
		return ""
	}

	if filepath.Base(commonDir) == ".git" {
		return filepath.Join(filepath.Dir(commonDir), ".beads")
	}

	return filepath.Join(commonDir, ".beads")
}

func gitPathForRepo(repoPath, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, path)
	}
	return utils.CanonicalizePath(path)
}

// worktreeRedirectTarget returns the resolved redirect target for the current
// worktree's .beads/redirect file, or empty string if not in a worktree or no
// redirect exists. This centralizes the per-worktree redirect override logic
// used by findLocalBeadsDir, FindBeadsDir, and findDatabaseInTree.
func worktreeRedirectTarget() string {
	if !git.IsWorktree() {
		return ""
	}
	worktreeRoot := git.GetRepoRoot()
	if worktreeRoot == "" {
		return ""
	}
	worktreeBeadsDir := filepath.Join(worktreeRoot, ".beads")
	redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
	if _, err := os.Stat(redirectFile); err != nil {
		return ""
	}
	target := FollowRedirect(worktreeBeadsDir)
	if target == worktreeBeadsDir {
		// Redirect file exists but FollowRedirect returned the original path
		// (empty/invalid content). Return the raw .beads dir so callers that
		// only need to know a redirect *exists* (findLocalBeadsDir) still work.
		return worktreeBeadsDir
	}
	return target
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

	// Canonicalize the starting directory so the gitRoot boundary comparison
	// below matches. utils.CanonicalizePath resolves symlinks AND normalizes
	// case on macOS/Windows — the same form git helpers return — so the
	// eventual `dir == gitRoot` check doesn't silently overshoot on
	// /var → /private/var or case-insensitive filesystems. Matches the
	// canonicalization strategy in FindBeadsDir.
	dir = utils.CanonicalizePath(dir)

	// Check cwd first — a rig subdirectory with its own .beads/ takes
	// priority over the git root's .beads/ (same fix as FindBeadsDir step 1b).
	//
	// In a worktree, skip the CWD check at the worktree root. The worktree
	// root's .beads/ may contain git-tracked metadata (metadata.json with
	// dolt_mode=server) inherited from the parent repo checkout, but NOT the
	// gitignored dolt/ data directory. In server mode, findDatabaseInBeadsDir
	// returns a path regardless of whether the data dir exists, which would
	// short-circuit the worktree-aware fallback below — causing each worktree
	// to spawn its own dolt server against an empty data directory.
	// The worktree-specific code (below) handles this correctly.
	{
		skipCwdCheck := git.IsWorktree() && dir == utils.CanonicalizePath(git.GetRepoRoot())
		if !skipCwdCheck {
			cwdBeadsDir := filepath.Join(dir, ".beads")
			if info, err := os.Stat(cwdBeadsDir); err == nil && info.IsDir() {
				cwdBeadsDir = FollowRedirect(cwdBeadsDir)
				if dbPath := findDatabaseInBeadsDir(cwdBeadsDir, true); dbPath != "" {
					return dbPath
				}
			}
		}
	}

	// Check if we're in a git worktree
	var mainRepoRoot string
	if git.IsWorktree() {
		// Per-worktree redirect override
		if target := worktreeRedirectTarget(); target != "" {
			if dbPath := findDatabaseInBeadsDir(target, true); dbPath != "" {
				return dbPath
			}
		}

		// Worktree's own .beads (separate-DB mode, no redirect).
		// Only use it if the worktree has an actual database (dolt/, embeddeddolt/,
		// or *.db). A worktree that only has git-tracked metadata (metadata.json
		// with dolt_mode=server, config.yaml, etc.) should fall through to the
		// shared fallback below. This mirrors FindBeadsDir step 3b's
		// hasBeadsDatabase guard and prevents duplicate server spawns.
		if worktreeRoot := git.GetRepoRoot(); worktreeRoot != "" {
			worktreeBeadsDir := filepath.Join(worktreeRoot, ".beads")
			if info, err := os.Stat(worktreeBeadsDir); err == nil && info.IsDir() {
				if hasBeadsDatabase(worktreeBeadsDir) {
					if dbPath := findDatabaseInBeadsDir(worktreeBeadsDir, true); dbPath != "" {
						return dbPath
					}
				}
			}
		}

		// Fall back: search the canonical shared .beads for this worktree.
		if fallbackBeadsDir := GetWorktreeFallbackBeadsDir(); fallbackBeadsDir != "" {
			if info, err := os.Stat(fallbackBeadsDir); err == nil && info.IsDir() {
				fallbackBeadsDir = FollowRedirect(fallbackBeadsDir)
				if dbPath := findDatabaseInBeadsDir(fallbackBeadsDir, true); dbPath != "" {
					return dbPath
				}
			}
		}
		var err error
		mainRepoRoot, err = git.GetMainRepoRoot()
		if err != nil {
			mainRepoRoot = ""
		}
		// If not found in main repo, fall back to worktree search below
	}

	// Find git root to limit the search
	gitRoot := findGitRoot()
	if git.IsWorktree() && mainRepoRoot != "" {
		// For worktrees, extend search boundary to include main repo
		gitRoot = mainRepoRoot
	}
	// Canonicalize the boundary so the `dir == gitRoot` comparison is robust
	// against symlink or case-form mismatches between git helpers and os.Getwd.
	gitRootCanonical := ""
	if gitRoot != "" {
		gitRootCanonical = utils.CanonicalizePath(gitRoot)
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
		if gitRootCanonical != "" && dir == gitRootCanonical {
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

			// Look for database: dolt directory first, then legacy *.db files
			dbPath := ""
			doltDir := filepath.Join(beadsDir, "dolt")
			if dInfo, dErr := os.Stat(doltDir); dErr == nil && dInfo.IsDir() {
				dbPath = doltDir
			} else {
				// Legacy: check for *.db files (pre-migration)
				matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
				if err == nil && len(matches) > 0 {
					dbPath = matches[0]
				}
			}

			if dbPath != "" {
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

				databases = append(databases, DatabaseInfo{
					Path:       dbPath,
					BeadsDir:   beadsDir,
					IssueCount: -1,
				})

				// Stop searching upward - the closest .beads is the one to use
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
