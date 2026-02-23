package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// ImportOptions configures import behavior.
type ImportOptions struct {
	DryRun                     bool
	SkipUpdate                 bool
	Strict                     bool
	RenameOnImport             bool
	ClearDuplicateExternalRefs bool
	OrphanHandling             string
	DeletionIDs                []string
	SkipPrefixValidation       bool
	ProtectLocalExportIDs      map[string]time.Time
}

// ImportResult describes what an import operation did.
type ImportResult struct {
	Created             int
	Updated             int
	Unchanged           int
	Skipped             int
	Deleted             int
	Collisions          int
	IDMapping           map[string]string
	CollisionIDs        []string
	PrefixMismatch      bool
	ExpectedPrefix      string
	MismatchPrefixes    map[string]int
	SkippedDependencies []string
}

// importIssuesCore imports issues into the Dolt store.
// This is a bridge function that delegates to the Dolt store's batch creation.
func importIssuesCore(ctx context.Context, _ string, store *dolt.DoltStore, issues []*types.Issue, opts ImportOptions) (*ImportResult, error) {
	if opts.DryRun || len(issues) == 0 {
		return &ImportResult{Skipped: len(issues)}, nil
	}

	err := store.CreateIssuesWithFullOptions(ctx, issues, getActorWithGit(), storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: opts.SkipPrefixValidation,
	})
	if err != nil {
		return nil, err
	}

	return &ImportResult{Created: len(issues)}, nil
}

// importFromLocalJSONL imports issues from a local JSONL file on disk into the Dolt store.
// Unlike git-based import, this reads from the current working tree, preserving
// any manual cleanup done to the JSONL file (e.g., via bd compact --purge-tombstones).
// Returns the number of issues imported and any error.
func importFromLocalJSONL(ctx context.Context, store *dolt.DoltStore, localPath string) (int, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read JSONL file %s: %w", localPath, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	// Allow up to 64MB per line for large descriptions
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var issues []*types.Issue

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return 0, fmt.Errorf("failed to parse issue from JSONL: %w", err)
		}
		issue.SetDefaults()
		issues = append(issues, &issue)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("failed to scan JSONL: %w", err)
	}

	if len(issues) == 0 {
		return 0, nil
	}

	// Auto-detect prefix from first issue if not already configured
	configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
	if err == nil && strings.TrimSpace(configuredPrefix) == "" {
		firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
		if firstPrefix != "" {
			if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
				return 0, fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
			}
		}
	}

	opts := ImportOptions{
		SkipPrefixValidation: true,
	}
	_, err = importIssuesCore(ctx, "", store, issues, opts)
	if err != nil {
		return 0, err
	}

	return len(issues), nil
}
