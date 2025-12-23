package fix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// DefaultCleanupAgeDays is the default age threshold for cleanup
const DefaultCleanupAgeDays = 30

// CleanupResult contains the results of a cleanup operation
type CleanupResult struct {
	DeletedCount   int
	TombstoneCount int
	SkippedPinned  int
}

// StaleClosedIssues converts stale closed issues to tombstones.
// This is the fix handler for the "Stale Closed Issues" doctor check.
func StaleClosedIssues(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("  No database found, nothing to clean up")
		return nil
	}

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Find closed issues older than threshold
	cutoff := time.Now().AddDate(0, 0, -DefaultCleanupAgeDays)
	statusClosed := types.StatusClosed
	filter := types.IssueFilter{
		Status:       &statusClosed,
		ClosedBefore: &cutoff,
	}

	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return fmt.Errorf("failed to query issues: %w", err)
	}

	// Filter out pinned issues and delete the rest
	var deleted, skipped int
	for _, issue := range issues {
		if issue.Pinned {
			skipped++
			continue
		}

		if err := store.DeleteIssue(ctx, issue.ID); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", issue.ID, err)
			continue
		}
		deleted++
	}

	if deleted == 0 && skipped == 0 {
		fmt.Println("  No stale closed issues to clean up")
	} else {
		if deleted > 0 {
			fmt.Printf("  Cleaned up %d stale closed issue(s)\n", deleted)
		}
		if skipped > 0 {
			fmt.Printf("  Skipped %d pinned issue(s)\n", skipped)
		}
	}

	return nil
}

// ExpiredTombstones prunes expired tombstones from issues.jsonl.
// This is the fix handler for the "Expired Tombstones" doctor check.
func ExpiredTombstones(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		fmt.Println("  No JSONL file found, nothing to prune")
		return nil
	}

	// Read all issues
	file, err := os.Open(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return fmt.Errorf("failed to open issues.jsonl: %w", err)
	}

	var allIssues []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}
		issue.SetDefaults()
		allIssues = append(allIssues, &issue)
	}
	file.Close()

	ttl := types.DefaultTombstoneTTL

	// Filter out expired tombstones
	var kept []*types.Issue
	var prunedCount int
	for _, issue := range allIssues {
		if issue.IsExpired(ttl) {
			prunedCount++
		} else {
			kept = append(kept, issue)
		}
	}

	if prunedCount == 0 {
		fmt.Println("  No expired tombstones to prune")
		return nil
	}

	// Write back the pruned file atomically
	tempFile, err := os.CreateTemp(beadsDir, "issues.jsonl.prune.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	encoder := json.NewEncoder(tempFile)
	for _, issue := range kept {
		if err := encoder.Encode(issue); err != nil {
			tempFile.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
		}
	}
	tempFile.Close()

	// Atomically replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to replace issues.jsonl: %w", err)
	}

	ttlDays := int(ttl.Hours() / 24)
	fmt.Printf("  Pruned %d expired tombstone(s) (older than %d days)\n", prunedCount, ttlDays)
	return nil
}
