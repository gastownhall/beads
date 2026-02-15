package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
)

// SyncConflictState tracks pending sync conflicts.
type SyncConflictState struct {
	Conflicts []SyncConflictRecord `json:"conflicts,omitempty"`
}

// SyncConflictRecord represents a conflict detected during sync.
type SyncConflictRecord struct {
	IssueID       string `json:"issue_id"`
	Reason        string `json:"reason"`
	LocalVersion  string `json:"local_version,omitempty"`
	RemoteVersion string `json:"remote_version,omitempty"`
	Strategy      string `json:"strategy,omitempty"` // how it was resolved
}

// LoadSyncConflictState loads the sync conflict state from disk.
func LoadSyncConflictState(beadsDir string) (*SyncConflictState, error) {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	// #nosec G304 -- path is derived from the workspace .beads directory
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SyncConflictState{}, nil
		}
		return nil, err
	}

	var state SyncConflictState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveSyncConflictState saves the sync conflict state to disk.
func SaveSyncConflictState(beadsDir string, state *SyncConflictState) error {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ClearSyncConflictState removes the sync conflict state file.
func ClearSyncConflictState(beadsDir string) error {
	path := filepath.Join(beadsDir, "sync_conflicts.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// resolveSyncConflicts resolves pending sync conflicts using the specified strategy.
// Strategies:
//   - "newest": Keep whichever version has the newer updated_at timestamp (default)
//   - "ours": Keep local version
//   - "theirs": Keep remote version
//   - "manual": Interactive resolution with user prompts
func resolveSyncConflicts(ctx context.Context, jsonlPath string, strategy config.ConflictStrategy, dryRun bool) error {
	beadsDir := filepath.Dir(jsonlPath)

	conflictState, err := LoadSyncConflictState(beadsDir)
	if err != nil {
		return fmt.Errorf("loading sync conflicts: %w", err)
	}

	if len(conflictState.Conflicts) == 0 {
		fmt.Println("No conflicts to resolve")
		return nil
	}

	if dryRun {
		fmt.Printf("→ [DRY RUN] Would resolve %d conflicts using '%s' strategy\n", len(conflictState.Conflicts), strategy)
		for _, c := range conflictState.Conflicts {
			fmt.Printf("  - %s: %s\n", c.IssueID, c.Reason)
		}
		return nil
	}

	fmt.Printf("Resolving conflicts using '%s' strategy...\n", strategy)

	// Load base, local, and remote states for merge
	baseIssues, err := loadBaseState(beadsDir)
	if err != nil {
		return fmt.Errorf("loading base state: %w", err)
	}

	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}

	localIssues, err := store.SearchIssues(ctx, "", beads.IssueFilter{})
	if err != nil {
		return fmt.Errorf("loading local issues: %w", err)
	}

	remoteIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("loading remote issues: %w", err)
	}

	// Build maps for quick lookup
	baseMap := make(map[string]*beads.Issue)
	for _, issue := range baseIssues {
		baseMap[issue.ID] = issue
	}
	localMap := make(map[string]*beads.Issue)
	for _, issue := range localIssues {
		localMap[issue.ID] = issue
	}
	remoteMap := make(map[string]*beads.Issue)
	for _, issue := range remoteIssues {
		remoteMap[issue.ID] = issue
	}

	// Handle manual strategy with interactive resolution
	if strategy == config.ConflictStrategyManual {
		return resolveSyncConflictsManually(ctx, jsonlPath, beadsDir, conflictState, baseMap, localMap, remoteMap)
	}

	// Determine winner for each conflict based on strategy
	winners := make(map[string]*beads.Issue) // issueID -> winning version
	resolved := 0
	for _, conflict := range conflictState.Conflicts {
		local := localMap[conflict.IssueID]
		remote := remoteMap[conflict.IssueID]

		var winner string
		switch strategy {
		case config.ConflictStrategyOurs:
			winner = "local"
		case config.ConflictStrategyTheirs:
			winner = "remote"
		case config.ConflictStrategyNewest:
			fallthrough
		default:
			// Compare updated_at timestamps
			if local != nil && remote != nil {
				if local.UpdatedAt.After(remote.UpdatedAt) {
					winner = "local"
				} else {
					winner = "remote"
				}
			} else if local != nil {
				winner = "local"
			} else {
				winner = "remote"
			}
		}

		// Store the winning version
		if winner == "local" && local != nil {
			winners[conflict.IssueID] = local
		} else if remote != nil {
			winners[conflict.IssueID] = remote
		}

		fmt.Printf("✓ %s: kept %s", conflict.IssueID, winner)
		if strategy == config.ConflictStrategyNewest {
			fmt.Print(" (newer)")
		}
		fmt.Println()
		resolved++
	}

	// Clear conflicts after resolution
	if err := ClearSyncConflictState(beadsDir); err != nil {
		return fmt.Errorf("clearing conflict state: %w", err)
	}

	// Run merge for non-conflicting issues, then override conflicts with chosen winners
	mergeResult := MergeIssues(baseIssues, localIssues, remoteIssues)

	// Override conflicting issues with the strategy-chosen winner
	for i, issue := range mergeResult.Merged {
		if winner, ok := winners[issue.ID]; ok {
			mergeResult.Merged[i] = winner
		}
	}

	// Write merged state
	if err := writeMergedStateToJSONL(jsonlPath, mergeResult.Merged); err != nil {
		return fmt.Errorf("writing merged state: %w", err)
	}

	// Import to database
	if err := importFromJSONLInline(ctx, jsonlPath, false, false, false); err != nil {
		return fmt.Errorf("importing merged state: %w", err)
	}

	// Export to ensure consistency
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Update base state
	finalIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("reloading final state: %w", err)
	}
	if err := saveBaseState(beadsDir, finalIssues); err != nil {
		return fmt.Errorf("saving base state: %w", err)
	}

	fmt.Printf("✓ Merge complete (%d conflicts resolved)\n", resolved)

	return nil
}

// resolveSyncConflictsManually handles manual conflict resolution with interactive prompts.
func resolveSyncConflictsManually(ctx context.Context, jsonlPath, beadsDir string, conflictState *SyncConflictState,
	baseMap, localMap, remoteMap map[string]*beads.Issue) error {

	// Build interactive conflicts list
	var interactiveConflicts []InteractiveConflict
	for _, c := range conflictState.Conflicts {
		interactiveConflicts = append(interactiveConflicts, InteractiveConflict{
			IssueID: c.IssueID,
			Local:   localMap[c.IssueID],
			Remote:  remoteMap[c.IssueID],
			Base:    baseMap[c.IssueID],
		})
	}

	// Run interactive resolution
	resolvedIssues, skipped, err := resolveConflictsInteractively(interactiveConflicts)
	if err != nil {
		return fmt.Errorf("interactive resolution: %w", err)
	}

	if skipped > 0 {
		fmt.Printf("\n⚠ %d conflict(s) skipped - will remain unresolved\n", skipped)
	}

	if len(resolvedIssues) == 0 && skipped == len(conflictState.Conflicts) {
		fmt.Println("No conflicts were resolved")
		return nil
	}

	// Build the merged issue list:
	// 1. Start with issues that weren't in conflict
	// 2. Add the resolved issues
	conflictIDSet := make(map[string]bool)
	for _, c := range conflictState.Conflicts {
		conflictIDSet[c.IssueID] = true
	}

	// Build resolved issue map for quick lookup
	resolvedMap := make(map[string]*beads.Issue)
	for _, issue := range resolvedIssues {
		if issue != nil {
			resolvedMap[issue.ID] = issue
		}
	}

	// Collect all unique IDs from base, local, remote
	allIDSet := make(map[string]bool)
	for id := range baseMap {
		allIDSet[id] = true
	}
	for id := range localMap {
		allIDSet[id] = true
	}
	for id := range remoteMap {
		allIDSet[id] = true
	}

	// Build final merged list
	var mergedIssues []*beads.Issue
	for id := range allIDSet {
		if conflictIDSet[id] {
			// This was a conflict
			if resolved, ok := resolvedMap[id]; ok {
				// User resolved this conflict - use their choice
				mergedIssues = append(mergedIssues, resolved)
			} else {
				// Skipped - keep local version in output, conflict remains for later
				if local := localMap[id]; local != nil {
					mergedIssues = append(mergedIssues, local)
				}
			}
		} else {
			// Not a conflict - use standard 3-way merge logic
			local := localMap[id]
			remote := remoteMap[id]
			base := baseMap[id]
			merged, _, _ := MergeIssue(base, local, remote)
			if merged != nil {
				mergedIssues = append(mergedIssues, merged)
			}
		}
	}

	// Clear resolved conflicts (keep skipped ones)
	if skipped == 0 {
		if err := ClearSyncConflictState(beadsDir); err != nil {
			return fmt.Errorf("clearing conflict state: %w", err)
		}
	} else {
		// Update conflict state to only keep skipped conflicts
		var remaining []SyncConflictRecord
		for _, c := range conflictState.Conflicts {
			if _, resolved := resolvedMap[c.IssueID]; !resolved {
				remaining = append(remaining, c)
			}
		}
		conflictState.Conflicts = remaining
		if err := SaveSyncConflictState(beadsDir, conflictState); err != nil {
			return fmt.Errorf("saving updated conflict state: %w", err)
		}
	}

	// Write merged state
	if err := writeMergedStateToJSONL(jsonlPath, mergedIssues); err != nil {
		return fmt.Errorf("writing merged state: %w", err)
	}

	// Import to database
	if err := importFromJSONLInline(ctx, jsonlPath, false, false, false); err != nil {
		return fmt.Errorf("importing merged state: %w", err)
	}

	// Export to ensure consistency
	if err := exportToJSONL(ctx, jsonlPath); err != nil {
		return fmt.Errorf("exporting: %w", err)
	}

	// Update base state
	finalIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		return fmt.Errorf("reloading final state: %w", err)
	}
	if err := saveBaseState(beadsDir, finalIssues); err != nil {
		return fmt.Errorf("saving base state: %w", err)
	}

	resolvedCount := len(resolvedIssues)
	fmt.Printf("\n✓ Manual resolution complete (%d resolved, %d skipped)\n", resolvedCount, skipped)

	return nil
}
