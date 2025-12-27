// Package sqlite implements multi-repo export for the SQLite storage backend.
package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
)

// ExportToMultiRepo writes issues to their respective JSONL files based on source_repo.
// Issues are grouped by source_repo and written atomically to each repository.
// Returns a map of repo path -> exported issue count.
// Returns nil with no error if not in multi-repo mode (backward compatibility).
func (s *SQLiteStorage) ExportToMultiRepo(ctx context.Context) (map[string]int, error) {
	// Get multi-repo config
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		// Single-repo mode - not an error, just no-op
		return nil, nil
	}

	// Get all issues including tombstones for sync propagation (bd-dve)
	allIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return nil, fmt.Errorf("failed to query issues: %w", err)
	}

	// Populate dependencies for all issues (avoid N+1)
	allDeps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range allIssues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range allIssues {
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Filter out wisps - they should never be exported to JSONL (bd-687g)
	// Wisps exist only in SQLite and are shared via .beads/redirect, not JSONL.
	filtered := make([]*types.Issue, 0, len(allIssues))
	for _, issue := range allIssues {
		if !issue.Ephemeral {
			filtered = append(filtered, issue)
		}
	}
	allIssues = filtered

	// Group issues by source_repo
	issuesByRepo := make(map[string][]*types.Issue)
	for _, issue := range allIssues {
		sourceRepo := issue.SourceRepo
		if sourceRepo == "" {
			sourceRepo = "." // Default to primary repo
		}
		issuesByRepo[sourceRepo] = append(issuesByRepo[sourceRepo], issue)
	}

	results := make(map[string]int)

	// Export primary repo
	if issues, ok := issuesByRepo["."]; ok {
		repoPath := multiRepo.Primary
		if repoPath == "" {
			repoPath = "."
		}
		count, err := s.exportToRepo(ctx, repoPath, issues)
		if err != nil {
			return nil, fmt.Errorf("failed to export primary repo: %w", err)
		}
		results["."] = count
	}

	// Export additional repos
	for _, repoPath := range multiRepo.Additional {
		issues := issuesByRepo[repoPath]
		if len(issues) == 0 {
			// No issues for this repo - write empty JSONL to keep in sync
			count, err := s.exportToRepo(ctx, repoPath, []*types.Issue{})
			if err != nil {
				return nil, fmt.Errorf("failed to export repo %s: %w", repoPath, err)
			}
			results[repoPath] = count
			continue
		}

		count, err := s.exportToRepo(ctx, repoPath, issues)
		if err != nil {
			return nil, fmt.Errorf("failed to export repo %s: %w", repoPath, err)
		}
		results[repoPath] = count
	}

	return results, nil
}

// exportToRepo writes issues to a single repository's JSONL file atomically.
func (s *SQLiteStorage) exportToRepo(ctx context.Context, repoPath string, issues []*types.Issue) (int, error) {
	// Expand tilde in path
	expandedPath, err := expandTilde(repoPath)
	if err != nil {
		return 0, fmt.Errorf("failed to expand path: %w", err)
	}

	// Get absolute path
	absRepoPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return 0, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Construct JSONL path
	jsonlPath := filepath.Join(absRepoPath, ".beads", "issues.jsonl")

	// Ensure .beads directory exists
	beadsDir := filepath.Dir(jsonlPath)
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create .beads directory: %w", err)
	}

	// Sort issues by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Write atomically using temp file + rename
	tempPath := fmt.Sprintf("%s.tmp.%d", jsonlPath, os.Getpid())
	f, err := os.Create(tempPath) // #nosec G304 -- tempPath derived from trusted jsonlPath
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Ensure cleanup on failure
	defer func() {
		if f != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()

	// Write JSONL
	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return 0, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
	}

	// Close before rename
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("failed to close temp file: %w", err)
	}
	f = nil // Prevent defer cleanup

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Set file permissions
	// Skip chmod for symlinks - os.Chmod follows symlinks and would change the target's
	// permissions, which may be in a read-only location (e.g., /nix/store on NixOS).
	if info, statErr := os.Lstat(jsonlPath); statErr == nil && info.Mode()&os.ModeSymlink == 0 {
		if err := os.Chmod(jsonlPath, 0644); err != nil { // nolint:gosec // G302: 0644 intentional for git-tracked files
			// Non-fatal
			debug.Logf("Debug: failed to set permissions on %s: %v\n", jsonlPath, err)
		}
	}

	// Update mtime cache for this repo
	// Use Lstat to get the symlink's own mtime, not the target's (NixOS fix).
	fileInfo, err := os.Lstat(jsonlPath)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `
			INSERT OR REPLACE INTO repo_mtimes (repo_path, jsonl_path, mtime_ns, last_checked)
			VALUES (?, ?, ?, datetime('now'))
		`, absRepoPath, jsonlPath, fileInfo.ModTime().UnixNano())
		if err != nil {
			debug.Logf("Debug: failed to update mtime cache for %s: %v\n", absRepoPath, err)
		}
	}

	return len(issues), nil
}
