package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TombstonePruneResult contains the results of tombstone pruning
type TombstonePruneResult struct {
	PrunedCount int
	PrunedIDs   []string
	TTLDays     int
}

// pruneExpiredTombstones reads issues.jsonl, removes expired tombstones,
// and writes back the pruned file. Returns the prune result.
// If customTTL is > 0, it overrides the default TTL (bypasses MinTombstoneTTL safety).
// If customTTL is 0, uses DefaultTombstoneTTL.
func pruneExpiredTombstones(customTTL time.Duration) (*TombstonePruneResult, error) {
	beadsDir := filepath.Dir(dbPath)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if issues.jsonl exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return &TombstonePruneResult{}, nil
	}

	// Read all issues
	// nolint:gosec // G304: issuesPath is controlled from beadsDir
	file, err := os.Open(issuesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open issues.jsonl: %w", err)
	}

	var allIssues []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Skip corrupt lines
			continue
		}
		allIssues = append(allIssues, &issue)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("failed to close issues file: %w", err)
	}

	// Determine TTL - customTTL > 0 overrides default (for --hard mode)
	ttl := types.DefaultTombstoneTTL
	if customTTL > 0 {
		ttl = customTTL
	}
	ttlDays := int(ttl.Hours() / 24)

	// Filter out expired tombstones
	var kept []*types.Issue
	var prunedIDs []string
	for _, issue := range allIssues {
		if issue.IsExpired(ttl) {
			prunedIDs = append(prunedIDs, issue.ID)
		} else {
			kept = append(kept, issue)
		}
	}

	if len(prunedIDs) == 0 {
		return &TombstonePruneResult{TTLDays: ttlDays}, nil
	}

	// Write back the pruned file atomically
	dir := filepath.Dir(issuesPath)
	base := filepath.Base(issuesPath)
	tempFile, err := os.CreateTemp(dir, base+".prune.*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	encoder := json.NewEncoder(tempFile)
	for _, issue := range kept {
		if err := encoder.Encode(issue); err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
		}
	}

	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomically replace
	if err := os.Rename(tempPath, issuesPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to replace issues.jsonl: %w", err)
	}

	return &TombstonePruneResult{
		PrunedCount: len(prunedIDs),
		PrunedIDs:   prunedIDs,
		TTLDays:     ttlDays,
	}, nil
}

// previewPruneTombstones checks what tombstones would be pruned without modifying files.
// Used for dry-run mode in cleanup command.
// If customTTL is > 0, it overrides the default TTL (bypasses MinTombstoneTTL safety).
// If customTTL is 0, uses DefaultTombstoneTTL.
func previewPruneTombstones(customTTL time.Duration) (*TombstonePruneResult, error) {
	beadsDir := filepath.Dir(dbPath)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if issues.jsonl exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return &TombstonePruneResult{}, nil
	}

	// Read all issues
	// nolint:gosec // G304: issuesPath is controlled from beadsDir
	file, err := os.Open(issuesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open issues.jsonl: %w", err)
	}
	defer file.Close()

	var allIssues []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Skip corrupt lines
			continue
		}
		allIssues = append(allIssues, &issue)
	}

	// Determine TTL - customTTL > 0 overrides default (for --hard mode)
	ttl := types.DefaultTombstoneTTL
	if customTTL > 0 {
		ttl = customTTL
	}
	ttlDays := int(ttl.Hours() / 24)

	// Count expired tombstones
	var prunedIDs []string
	for _, issue := range allIssues {
		if issue.IsExpired(ttl) {
			prunedIDs = append(prunedIDs, issue.ID)
		}
	}

	return &TombstonePruneResult{
		PrunedCount: len(prunedIDs),
		PrunedIDs:   prunedIDs,
		TTLDays:     ttlDays,
	}, nil
}

// runCompactPrune handles the --prune mode for standalone tombstone pruning.
// This mode only prunes expired tombstones from issues.jsonl without doing
// any semantic compaction. It's useful for reducing sync overhead.
func runCompactPrune() {
	start := time.Now()

	// Calculate TTL from --older-than flag
	// -1 (default) = use 30 day default, 0 = expire all, >0 = N days
	var customTTL time.Duration
	if compactOlderThan >= 0 {
		if compactOlderThan == 0 {
			// --older-than=0 means "expire all tombstones"
			customTTL = 1 * time.Nanosecond
		} else {
			customTTL = time.Duration(compactOlderThan) * 24 * time.Hour
		}
	}

	if compactDryRun {
		// Preview mode - show what would be pruned
		result, err := previewPruneTombstones(customTTL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to preview tombstones: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"dry_run":       true,
				"prune_count":   result.PrunedCount,
				"ttl_days":      result.TTLDays,
				"tombstone_ids": result.PrunedIDs,
			}
			outputJSON(output)
			return
		}

		fmt.Printf("DRY RUN - Tombstone Pruning\n\n")
		fmt.Printf("TTL: %d days\n", result.TTLDays)
		fmt.Printf("Tombstones that would be pruned: %d\n", result.PrunedCount)
		if len(result.PrunedIDs) > 0 && len(result.PrunedIDs) <= 20 {
			fmt.Println("\nTombstone IDs:")
			for _, id := range result.PrunedIDs {
				fmt.Printf("  - %s\n", id)
			}
		} else if len(result.PrunedIDs) > 20 {
			fmt.Printf("\nFirst 20 tombstone IDs:\n")
			for _, id := range result.PrunedIDs[:20] {
				fmt.Printf("  - %s\n", id)
			}
			fmt.Printf("  ... and %d more\n", len(result.PrunedIDs)-20)
		}
		return
	}

	// Actually prune tombstones
	result, err := pruneExpiredTombstones(customTTL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to prune tombstones: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(start)

	if jsonOutput {
		output := map[string]interface{}{
			"success":       true,
			"pruned_count":  result.PrunedCount,
			"ttl_days":      result.TTLDays,
			"tombstone_ids": result.PrunedIDs,
			"elapsed_ms":    elapsed.Milliseconds(),
		}
		outputJSON(output)
		return
	}

	if result.PrunedCount == 0 {
		fmt.Printf("No expired tombstones to prune (TTL: %d days)\n", result.TTLDays)
		return
	}

	fmt.Printf("✓ Pruned %d expired tombstone(s)\n", result.PrunedCount)
	fmt.Printf("  TTL: %d days\n", result.TTLDays)
	fmt.Printf("  Time: %v\n", elapsed)
	if len(result.PrunedIDs) <= 10 {
		fmt.Println("\nPruned IDs:")
		for _, id := range result.PrunedIDs {
			fmt.Printf("  - %s\n", id)
		}
	}
}

// PurgeTombstonesResult contains results of dependency-aware tombstone purging
type PurgeTombstonesResult struct {
	TombstonesBefore  int      // Total tombstones before purge
	TombstonesDeleted int      // Tombstones deleted
	TombstonesKept    int      // Tombstones kept (have open deps)
	DepsRemoved       int      // Stale deps from closed issues to tombstones
	OrphanDepsRemoved int      // Orphaned deps cleaned up
	DeletedIDs        []string // IDs of deleted tombstones
	KeptIDs           []string // IDs of kept tombstones (for debugging)
}

// purgeTombstonesByDependency removes tombstones that have no open issues depending on them.
// This is more aggressive than age-based pruning because it removes tombstones regardless of age.
// Steps:
// 1. Find all tombstones
// 2. Build dependency graph to find which tombstones have open issues depending on them
// 3. Remove deps from closed issues to tombstones (stale historical deps)
// 4. Delete tombstones that have no remaining live open deps
// 5. Clean up any orphaned deps/labels
func purgeTombstonesByDependency(dryRun bool) (*PurgeTombstonesResult, error) {
	beadsDir := filepath.Dir(dbPath)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if issues.jsonl exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return &PurgeTombstonesResult{}, nil
	}

	// Read all issues
	file, err := os.Open(issuesPath) //nolint:gosec // G304: issuesPath from beads.FindBeadsDir()
	if err != nil {
		return nil, fmt.Errorf("failed to open issues.jsonl: %w", err)
	}

	var allIssues []*types.Issue
	issueMap := make(map[string]*types.Issue)
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			continue
		}
		allIssues = append(allIssues, &issue)
		issueMap[issue.ID] = &issue
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("failed to close issues file: %w", err)
	}

	// Identify tombstones and live issues
	tombstones := make(map[string]*types.Issue)
	liveOpen := make(map[string]bool)   // Open, non-deleted issues
	liveClosed := make(map[string]bool) // Closed, non-deleted issues

	for _, issue := range allIssues {
		if issue.DeletedAt != nil {
			tombstones[issue.ID] = issue
		} else if issue.Status == "open" {
			liveOpen[issue.ID] = true
		} else {
			liveClosed[issue.ID] = true
		}
	}

	result := &PurgeTombstonesResult{
		TombstonesBefore: len(tombstones),
	}

	// Build reverse dependency map: tombstone_id -> list of issues that depend on it
	depsToTombstone := make(map[string][]string)
	for _, issue := range allIssues {
		for _, dep := range issue.Dependencies {
			if dep.DependsOnID != "" {
				depsToTombstone[dep.DependsOnID] = append(depsToTombstone[dep.DependsOnID], issue.ID)
			}
		}
	}

	// Find tombstones safe to delete (no open issues depend on them)
	safeToDelete := make(map[string]bool)
	for tombstoneID := range tombstones {
		hasOpenDep := false
		for _, depID := range depsToTombstone[tombstoneID] {
			if liveOpen[depID] {
				hasOpenDep = true
				break
			}
		}
		if !hasOpenDep {
			safeToDelete[tombstoneID] = true
		}
	}

	// Calculate what we'll keep
	for tombstoneID := range tombstones {
		if safeToDelete[tombstoneID] {
			result.DeletedIDs = append(result.DeletedIDs, tombstoneID)
		} else {
			result.KeptIDs = append(result.KeptIDs, tombstoneID)
		}
	}
	result.TombstonesDeleted = len(result.DeletedIDs)
	result.TombstonesKept = len(result.KeptIDs)

	// Count stale deps (from closed issues to tombstones) that will be removed
	for _, issue := range allIssues {
		if liveClosed[issue.ID] {
			for _, dep := range issue.Dependencies {
				if tombstones[dep.DependsOnID] != nil {
					result.DepsRemoved++
				}
			}
		}
	}

	if dryRun {
		return result, nil
	}

	// Actually modify: filter out deleted tombstones and clean deps
	var kept []*types.Issue
	for _, issue := range allIssues {
		if safeToDelete[issue.ID] {
			continue // Skip deleted tombstones
		}

		// Clean deps pointing to deleted tombstones
		var cleanDeps []*types.Dependency
		for _, dep := range issue.Dependencies {
			if !safeToDelete[dep.DependsOnID] {
				cleanDeps = append(cleanDeps, dep)
			}
		}
		issue.Dependencies = cleanDeps
		kept = append(kept, issue)
	}

	// Write back atomically
	dir := filepath.Dir(issuesPath)
	base := filepath.Base(issuesPath)
	tempFile, err := os.CreateTemp(dir, base+".purge.*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	encoder := json.NewEncoder(tempFile)
	for _, issue := range kept {
		if err := encoder.Encode(issue); err != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
		}
	}

	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tempPath, issuesPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to replace issues.jsonl: %w", err)
	}

	return result, nil
}

// runCompactPurgeTombstones handles the --purge-tombstones mode for dependency-aware cleanup.
// Unlike --prune which removes tombstones by age, this removes tombstones that have no
// open issues depending on them, regardless of age.
func runCompactPurgeTombstones() {
	start := time.Now()

	if compactDryRun {
		result, err := purgeTombstonesByDependency(true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to analyze tombstones: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"dry_run":              true,
				"tombstones_before":    result.TombstonesBefore,
				"tombstones_to_delete": result.TombstonesDeleted,
				"tombstones_to_keep":   result.TombstonesKept,
				"deps_to_remove":       result.DepsRemoved,
				"deleted_ids":          result.DeletedIDs,
				"kept_ids":             result.KeptIDs,
			}
			outputJSON(output)
			return
		}

		fmt.Printf("DRY RUN - Dependency-Aware Tombstone Purge\n\n")
		fmt.Printf("Tombstones found: %d\n", result.TombstonesBefore)
		fmt.Printf("Safe to delete: %d (no open issues depend on them)\n", result.TombstonesDeleted)
		fmt.Printf("Must keep: %d (have open deps)\n", result.TombstonesKept)
		fmt.Printf("Stale deps to clean: %d (from closed issues to tombstones)\n", result.DepsRemoved)

		if len(result.KeptIDs) > 0 && len(result.KeptIDs) <= 10 {
			fmt.Println("\nKept tombstones (have open deps):")
			for _, id := range result.KeptIDs {
				fmt.Printf("  - %s\n", id)
			}
		}
		return
	}

	// Actually purge
	result, err := purgeTombstonesByDependency(false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to purge tombstones: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(start)

	if jsonOutput {
		output := map[string]interface{}{
			"success":            true,
			"tombstones_before":  result.TombstonesBefore,
			"tombstones_deleted": result.TombstonesDeleted,
			"tombstones_kept":    result.TombstonesKept,
			"deps_removed":       result.DepsRemoved,
			"elapsed_ms":         elapsed.Milliseconds(),
		}
		outputJSON(output)
		return
	}

	if result.TombstonesDeleted == 0 {
		fmt.Printf("No tombstones to purge (all %d have open deps)\n", result.TombstonesBefore)
		return
	}

	fmt.Printf("✓ Purged %d tombstone(s)\n", result.TombstonesDeleted)
	fmt.Printf("  Before: %d tombstones\n", result.TombstonesBefore)
	fmt.Printf("  Deleted: %d (no open deps)\n", result.TombstonesDeleted)
	fmt.Printf("  Kept: %d (have open deps)\n", result.TombstonesKept)
	fmt.Printf("  Stale deps cleaned: %d\n", result.DepsRemoved)
	fmt.Printf("  Time: %v\n", elapsed)
}
