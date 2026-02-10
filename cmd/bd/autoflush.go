package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/utils"
)

// findJSONLPath finds the JSONL file path for the current database.
// Checks BEADS_JSONL env var first, then uses .beads/issues.jsonl next to the database.
// If sync-branch is configured, returns the worktree JSONL path instead.
func findJSONLPath() string {
	if jsonlEnv := os.Getenv("BEADS_JSONL"); jsonlEnv != "" {
		return utils.CanonicalizePath(jsonlEnv)
	}

	jsonlPath := beads.FindJSONLPath(dbPath)

	if jsonlPath == "" {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return ""
		}
		jsonlPath = utils.FindJSONLInDir(beadsDir)
	}

	worktreePath := getWorktreeJSONLPath(jsonlPath)
	if worktreePath != "" {
		jsonlPath = worktreePath
	}

	dbDir := filepath.Dir(jsonlPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		return utils.CanonicalizeIfRelative(jsonlPath)
	}

	return utils.CanonicalizeIfRelative(jsonlPath)
}

// getWorktreeJSONLPath converts a main repo JSONL path to its worktree equivalent.
// Returns empty string if sync-branch isn't configured or worktree doesn't exist.
func getWorktreeJSONLPath(mainJSONLPath string) string {
	ctx := context.Background()

	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return ""
	}

	rc, err := beads.GetRepoContext()
	if err != nil {
		return ""
	}

	if !strings.HasPrefix(mainJSONLPath, rc.RepoRoot) {
		return ""
	}

	cmd := rc.GitCmd(ctx, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	gitCommonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitCommonDir) {
		gitCommonDir = filepath.Join(rc.RepoRoot, gitCommonDir)
	}
	worktreePath := filepath.Join(gitCommonDir, "beads-worktrees", syncBranch)

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		debug.Logf("sync-branch configured but worktree doesn't exist at %s, falling back to main JSONL", worktreePath)
		return ""
	}

	jsonlRelPath, err := filepath.Rel(rc.RepoRoot, mainJSONLPath)
	if err != nil {
		return ""
	}

	return filepath.Join(worktreePath, jsonlRelPath)
}

// detectPrefixFromJSONL extracts the issue prefix from JSONL data.
// Returns empty string if prefix cannot be detected.
func detectPrefixFromJSONL(jsonlData []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			continue
		}

		if issue.ID == "" {
			continue
		}

		if idx := strings.Index(issue.ID, "-"); idx > 0 {
			return issue.ID[:idx]
		}
		return issue.ID
	}
	return ""
}
