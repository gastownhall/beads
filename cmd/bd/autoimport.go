package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"gopkg.in/yaml.v3"
)

// readFromGitRef reads file content from a git ref (branch or commit) in the beads repo.
// Returns the raw bytes from git show <ref>:<path>.
// The filePath is automatically converted to forward slashes for Windows compatibility.
// Returns nil, err if the git command fails (e.g., file not found in ref).
// GH#1110: Now uses RepoContext to ensure git commands run in beads repo.
func readFromGitRef(filePath, gitRef string) ([]byte, error) {
	gitPath := filepath.ToSlash(filePath)
	var cmd *exec.Cmd
	if rc, err := beads.GetRepoContext(); err == nil {
		cmd = rc.GitCmd(context.Background(), "show", fmt.Sprintf("%s:%s", gitRef, gitPath))
	} else {
		// Fallback to CWD for tests or repos without beads
		cmd = exec.Command("git", "show", fmt.Sprintf("%s:%s", gitRef, gitPath)) // #nosec G204 - git command with safe args
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read from git: %w", err)
	}
	return output, nil
}

// checkAndAutoImport checks if the database is empty but git has issues.
// If so, it automatically imports them and returns true.
// Returns false if no import was needed or if import failed.
func checkAndAutoImport(ctx context.Context, store storage.Storage) bool {
	// Don't auto-import if auto-import is explicitly disabled
	if noAutoImport {
		return false
	}

	// Skip JSONL import in dolt-native mode — there is no JSONL to import,
	// and GetStatistics can panic with a nil pointer when the Dolt backend
	// connection is not fully initialized.
	if !ShouldImportJSONL(ctx, store) {
		return false
	}

	// Check if database has any issues
	stats, err := store.GetStatistics(ctx)
	if err != nil || stats.TotalIssues > 0 {
		// Either error checking or DB has issues - don't auto-import
		return false
	}

	// Database is empty - check if git has issues
	issueCount, jsonlPath, gitRef := checkGitForIssues()
	if issueCount == 0 {
		// No issues in git either
		return false
	}

	// Found issues in git! Auto-import them
	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Found 0 issues in database but %d in git. Importing...\n", issueCount)
	}

	// Import from git
	if err := importFromGit(ctx, dbPath, store, jsonlPath, gitRef); err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "Try manually: git show %s:%s | bd import -i /dev/stdin\n", gitRef, jsonlPath)
		}
		return false
	}

	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Successfully imported %d issues from git.\n\n", issueCount)
	}

	return true
}

// checkGitForIssues checks if git has issues in .beads/beads.jsonl or issues.jsonl
// When sync-branch is configured, reads from that branch; otherwise reads from HEAD.
// Returns (issue_count, relative_jsonl_path, git_ref)
func checkGitForIssues() (int, string, string) {
	// Try to find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return 0, "", ""
	}

	// Construct relative path from git root
	gitRoot := git.GetRepoRoot()
	if gitRoot == "" {
		return 0, "", ""
	}

	// Resolve symlinks to ensure consistent paths for filepath.Rel()
	// This is necessary because on macOS, /var is a symlink to /private/var,
	// and git rev-parse returns the resolved path while os.Getwd() may not.
	resolvedBeadsDir, err := filepath.EvalSymlinks(beadsDir)
	if err != nil {
		return 0, "", ""
	}
	beadsDir = resolvedBeadsDir
	resolvedGitRoot, err := filepath.EvalSymlinks(gitRoot)
	if err != nil {
		return 0, "", ""
	}
	gitRoot = resolvedGitRoot

	// Clean paths to ensure consistent separators
	beadsDir = filepath.Clean(beadsDir)
	gitRoot = filepath.Clean(gitRoot)

	relBeads, err := filepath.Rel(gitRoot, beadsDir)
	if err != nil {
		return 0, "", ""
	}

	// GH#896: Reject beadsDir that is outside the git repository.
	// This prevents bd init from inheriting issues from a parent hub
	// when initializing a new project in a subdirectory.
	if strings.HasPrefix(relBeads, "..") {
		return 0, "", ""
	}

	// Determine which branch to read from (bd-0is fix)
	// If sync-branch is configured in local config.yaml, use it; otherwise fall back to HEAD
	// We read sync-branch directly from local config file rather than using cached global config
	// to handle cases where CWD has changed since config initialization (e.g., in tests)
	gitRef := "HEAD"
	syncBranch := getLocalSyncBranch(beadsDir)
	if syncBranch != "" {
		// Check if the sync branch exists (locally or on remote)
		// Try origin/<branch> first (more likely to exist in fresh clones),
		// then local <branch>
		// GH#1110: Use RepoContext to ensure we check the beads repo
		for _, ref := range []string{"origin/" + syncBranch, syncBranch} {
			var cmd *exec.Cmd
			if rc, err := beads.GetRepoContext(); err == nil {
				cmd = rc.GitCmd(context.Background(), "rev-parse", "--verify", "--quiet", ref)
			} else {
				cmd = exec.Command("git", "rev-parse", "--verify", "--quiet", ref) // #nosec G204
			}
			if err := cmd.Run(); err == nil {
				gitRef = ref
				break
			}
		}
	}

	// Try canonical JSONL filenames in precedence order (issues.jsonl is canonical)
	candidates := []string{
		filepath.Join(relBeads, "issues.jsonl"),
		filepath.Join(relBeads, "beads.jsonl"),
	}

	for _, relPath := range candidates {
		output, err := readFromGitRef(relPath, gitRef)
		if err == nil && len(output) > 0 {
			lines := bytes.Count(output, []byte("\n"))
			if lines > 0 {
				return lines, relPath, gitRef
			}
		}
	}

	return 0, "", ""
}

// localConfig represents the subset of config.yaml we need for auto-import, no-db, and prefer-dolt detection.
// Using proper YAML parsing handles edge cases like comments, indentation, and special characters.
type localConfig struct {
	SyncBranch string `yaml:"sync-branch"`
	NoDb       bool   `yaml:"no-db"`
	PreferDolt bool   `yaml:"prefer-dolt"`
}

// isNoDbModeConfigured checks if no-db: true is set in config.yaml.
// Uses proper YAML parsing to avoid false matches in comments or nested keys.
func isNoDbModeConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg localConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		debug.Logf("Warning: failed to parse config.yaml for no-db check: %v", err)
		return false
	}

	return cfg.NoDb
}

// isPreferDoltConfigured checks if prefer-dolt: true is set in config.yaml.
// Uses proper YAML parsing to avoid false matches in comments or nested keys.
func isPreferDoltConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg localConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		debug.Logf("Warning: failed to parse config.yaml for prefer-dolt check: %v", err)
		return false
	}

	return cfg.PreferDolt
}

// getLocalSyncBranch reads sync-branch from the local config.yaml file.
// This reads directly from the file rather than using cached config to handle
// cases where CWD has changed since config initialization.
func getLocalSyncBranch(beadsDir string) string {
	// First check environment variable (highest priority)
	if envBranch := os.Getenv(syncbranch.EnvVar); envBranch != "" {
		return envBranch
	}

	// Read config.yaml directly from the .beads directory
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from findBeadsDir
	if err != nil {
		return ""
	}

	// Parse YAML properly to handle edge cases (comments, indentation, special chars)
	var cfg localConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		debug.Logf("Warning: failed to parse config.yaml for sync-branch: %v", err)
		return ""
	}

	return cfg.SyncBranch
}



// importFromJSONLData imports issues from raw JSONL bytes.
// This is the shared implementation used by both importFromGit and importFromLocalJSONL.
// Returns the number of issues imported and any error.
func importFromJSONLData(ctx context.Context, dbFilePath string, store storage.Storage, jsonlData []byte) (int, error) {
	// Parse JSONL data
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	// Increase buffer size to handle large JSONL lines (e.g., big descriptions)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024) // allow up to 64MB per line
	var issues []*types.Issue

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return 0, fmt.Errorf("failed to parse issue: %w", err)
		}
		issue.SetDefaults() // Apply defaults for omitted fields
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("failed to scan JSONL: %w", err)
	}

	// CRITICAL: Set issue_prefix from first imported issue if missing
	// This prevents derivePrefixFromPath fallback which caused duplicate issues
	if len(issues) > 0 {
		configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err == nil && strings.TrimSpace(configuredPrefix) == "" {
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
					return 0, fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
				}
			}
		}
	}

	// Use existing import logic with auto-resolve collisions
	// Note: SkipPrefixValidation allows mixed prefixes during auto-import
	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	_, err := importIssuesCore(ctx, dbFilePath, store, issues, opts)
	if err != nil {
		return 0, err
	}

	return len(issues), nil
}

// importFromLocalJSONL imports issues from a local JSONL file on disk.
// Unlike importFromGit, this reads from the current working tree, preserving
// any manual cleanup done to the JSONL file (e.g., via bd compact --purge-tombstones).
// Returns the number of issues imported and any error.
func importFromLocalJSONL(ctx context.Context, dbFilePath string, store storage.Storage, localPath string) (int, error) {
	// #nosec G304 -- path provided by bd init command
	jsonlData, err := os.ReadFile(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read local JSONL file: %w", err)
	}
	return importFromJSONLData(ctx, dbFilePath, store, jsonlData)
}

// importFromGit imports issues from git at the specified ref (bd-0is: supports sync-branch)
func importFromGit(ctx context.Context, dbFilePath string, store storage.Storage, jsonlPath, gitRef string) error {
	jsonlData, err := readFromGitRef(jsonlPath, gitRef)
	if err != nil {
		return err
	}
	_, err = importFromJSONLData(ctx, dbFilePath, store, jsonlData)
	return err
}
