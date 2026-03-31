package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// exportAutoState tracks auto-export state to avoid redundant work.
type exportAutoState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
	Issues         int       `json:"issues"`
	Memories       int       `json:"memories"`
}

const exportAutoStateFile = "export-state.json"

// maybeAutoExport writes a git-tracked JSONL file if enabled and due.
// Called from PersistentPostRun after auto-backup.
func maybeAutoExport(ctx context.Context) {
	// Skip when running as a git hook to avoid re-export during pre-commit.
	if os.Getenv("BD_GIT_HOOK") == "1" {
		debug.Logf("auto-export: skipping — running as git hook\n")
		return
	}

	if !config.GetBool("export.auto") {
		return
	}
	if store == nil {
		return
	}
	if lm, ok := storage.UnwrapStore(store).(storage.LifecycleManager); ok && lm.IsClosed() {
		return
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	// Load state and check throttle
	state := loadExportAutoState(beadsDir)
	interval := config.GetDuration("export.interval")
	if interval == 0 {
		interval = 60 * time.Second
	}
	if !state.Timestamp.IsZero() && time.Since(state.Timestamp) < interval {
		debug.Logf("auto-export: throttled (last export %s ago, interval %s)\n",
			time.Since(state.Timestamp).Round(time.Second), interval)
		return
	}

	// Change detection via Dolt commit hash
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		debug.Logf("auto-export: failed to get current commit: %v\n", err)
		return
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("auto-export: no changes since last export\n")
		return
	}

	// Determine output path
	exportPath := config.GetString("export.path")
	if exportPath == "" {
		exportPath = "export.jsonl"
	}
	fullPath := filepath.Join(beadsDir, exportPath)

	// Run the export
	issueCount, memoryCount, err := exportToFile(ctx, fullPath, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-export failed: %v\n", err)
		return
	}

	debug.Logf("auto-export: wrote %d issues and %d memories to %s\n",
		issueCount, memoryCount, fullPath)

	// Optional git add
	if config.GetBool("export.git-add") {
		if err := gitAddFile(fullPath); err != nil {
			debug.Logf("auto-export: git add failed: %v\n", err)
		}
	}

	// Save state
	newState := exportAutoState{
		LastDoltCommit: currentCommit,
		Timestamp:      time.Now(),
		Issues:         issueCount,
		Memories:       memoryCount,
	}
	saveExportAutoState(beadsDir, &newState)
}

// exportToFile exports issues + memories to the given file path.
// Used by both `bd export -o` and auto-export.
func exportToFile(ctx context.Context, path string, includeMemories bool) (issueCount, memoryCount int, err error) {
	f, err := os.Create(path) //nolint:gosec // user-configured output path
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create export file: %w", err)
	}
	defer f.Close()

	// Build filter: exclude infra types and templates
	filter := types.IssueFilter{Limit: 0}
	var infraTypes []string
	if store != nil {
		infraSet := store.GetInfraTypes(ctx)
		if len(infraSet) > 0 {
			for t := range infraSet {
				infraTypes = append(infraTypes, t)
			}
		}
	}
	if len(infraTypes) == 0 {
		infraTypes = dolt.DefaultInfraTypes()
	}
	for _, t := range infraTypes {
		filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
	}
	isTemplate := false
	filter.IsTemplate = &isTemplate

	// Fetch issues
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to search issues: %w", err)
	}

	// Also fetch wisps
	ephemeral := true
	wispFilter := filter
	wispFilter.Ephemeral = &ephemeral
	wispIssues, err := store.SearchIssues(ctx, "", wispFilter)
	if err == nil && len(wispIssues) > 0 {
		issues = append(issues, wispIssues...)
	}

	// Bulk-load relational data
	if len(issues) > 0 {
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
		allDeps, _ := store.GetDependencyRecordsForIssues(ctx, issueIDs)
		commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
		depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)

		for _, issue := range issues {
			issue.Labels = labelsMap[issue.ID]
			issue.Dependencies = allDeps[issue.ID]
		}

		// Write issues
		enc := json.NewEncoder(f)
		for _, issue := range issues {
			counts := depCounts[issue.ID]
			if counts == nil {
				counts = &types.DependencyCounts{}
			}
			sanitizeZeroTime(issue)
			record := &types.IssueWithCounts{
				Issue:           issue,
				DependencyCount: counts.DependencyCount,
				DependentCount:  counts.DependentCount,
				CommentCount:    commentCounts[issue.ID],
			}
			if err := enc.Encode(record); err != nil {
				return 0, 0, fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
			}
			issueCount++
		}
	}

	// Write memories
	if includeMemories {
		allConfig, err := store.GetAllConfig(ctx)
		if err == nil {
			fullPrefix := kvPrefix + memoryPrefix
			for k, v := range allConfig {
				if !strings.HasPrefix(k, fullPrefix) {
					continue
				}
				userKey := strings.TrimPrefix(k, fullPrefix)
				record := map[string]string{
					"_type": "memory",
					"key":   userKey,
					"value": v,
				}
				data, err := json.Marshal(record)
				if err != nil {
					continue
				}
				if _, err := f.Write(data); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write memory: %w", err)
				}
				if _, err := f.Write([]byte{'\n'}); err != nil {
					return issueCount, memoryCount, fmt.Errorf("failed to write newline: %w", err)
				}
				memoryCount++
			}
		}
	}

	if err := f.Sync(); err != nil {
		return issueCount, memoryCount, fmt.Errorf("failed to sync: %w", err)
	}

	return issueCount, memoryCount, nil
}

func loadExportAutoState(beadsDir string) *exportAutoState {
	path := filepath.Join(beadsDir, exportAutoStateFile)
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return &exportAutoState{}
	}
	var state exportAutoState
	if err := json.Unmarshal(data, &state); err != nil {
		return &exportAutoState{}
	}
	return &state
}

func saveExportAutoState(beadsDir string, state *exportAutoState) {
	path := filepath.Join(beadsDir, exportAutoStateFile)
	data, err := json.Marshal(state)
	if err != nil {
		debug.Logf("auto-export: failed to marshal state: %v\n", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		debug.Logf("auto-export: failed to save state: %v\n", err)
	}
}

// gitAddFile stages a file in the enclosing git repo.
func gitAddFile(path string) error {
	cmd := exec.Command("git", "add", path)
	cmd.Dir = filepath.Dir(path)
	return cmd.Run()
}
