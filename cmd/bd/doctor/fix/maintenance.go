package fix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// CleanupResult contains the results of a cleanup operation
type CleanupResult struct {
	DeletedCount   int
	TombstoneCount int
	SkippedPinned  int
}

// StaleClosedIssues converts stale closed issues to tombstones.
// This is the fix handler for the "Stale Closed Issues" doctor check.
//
// This fix is DISABLED by default (stale_closed_issues_days=0). Users must
// explicitly set a positive threshold in metadata.json to enable cleanup.
func StaleClosedIssues(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Load config and check if cleanup is enabled
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Dolt backend: this fix uses SQLite-specific storage, skip for now
	if cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		fmt.Println("  Stale closed issues cleanup skipped (dolt backend)")
		return nil
	}

	// Get threshold; 0 means disabled
	var thresholdDays int
	if cfg != nil {
		thresholdDays = cfg.GetStaleClosedIssuesDays()
	}

	if thresholdDays == 0 {
		fmt.Println("  Stale closed issues cleanup disabled (set stale_closed_issues_days to enable)")
		return nil
	}

	// Get database path
	var dbPath string
	if cfg != nil && cfg.Database != "" {
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

	// Find closed issues older than configured threshold
	cutoff := time.Now().AddDate(0, 0, -thresholdDays)
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
			fmt.Printf("  Cleaned up %d stale closed issue(s) (older than %d days)\n", deleted, thresholdDays)
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
	_ = file.Close()

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
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
		}
	}
	_ = tempFile.Close()

	// Atomically replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to replace issues.jsonl: %w", err)
	}

	ttlDays := int(ttl.Hours() / 24)
	fmt.Printf("  Pruned %d expired tombstone(s) (older than %d days)\n", prunedCount, ttlDays)
	return nil
}

// PatrolPollution deletes patrol digest and session ended beads that pollute the database.
// This is the fix handler for the "Patrol Pollution" doctor check.
//
// It removes beads matching:
// - Patrol digests: titles matching "Digest: mol-*-patrol"
// - Session ended beads: titles matching "Session ended: *"
//
// After deletion, runs compact --purge-tombstones equivalent to clean up.
func PatrolPollution(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		fmt.Println("  No JSONL file found, nothing to clean up")
		return nil
	}

	// Get database path and check backend
	cfg, _ := configfile.Load(beadsDir)

	// Dolt backend: this fix uses SQLite-specific storage, skip for now
	if cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		fmt.Println("  Patrol pollution cleanup skipped (dolt backend)")
		return nil
	}

	var dbPath string
	if cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Get all issues and identify pollution
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to query issues: %w", err)
	}

	var patrolDigestCount, sessionBeadCount int
	var toDelete []string

	for _, issue := range issues {
		// Skip tombstones
		if issue.DeletedAt != nil {
			continue
		}

		title := issue.Title

		// Check for patrol digest pattern: "Digest: mol-*-patrol"
		if strings.HasPrefix(title, "Digest: mol-") && strings.HasSuffix(title, "-patrol") {
			patrolDigestCount++
			toDelete = append(toDelete, issue.ID)
			continue
		}

		// Check for session ended pattern: "Session ended: *"
		if strings.HasPrefix(title, "Session ended:") {
			sessionBeadCount++
			toDelete = append(toDelete, issue.ID)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println("  No patrol pollution beads to delete")
		return nil
	}

	// Delete all pollution beads
	var deleted int
	for _, id := range toDelete {
		if err := store.DeleteIssue(ctx, id); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", id, err)
			continue
		}
		deleted++
	}

	// Report results
	if patrolDigestCount > 0 {
		fmt.Printf("  Deleted %d patrol digest bead(s)\n", patrolDigestCount)
	}
	if sessionBeadCount > 0 {
		fmt.Printf("  Deleted %d session ended bead(s)\n", sessionBeadCount)
	}
	fmt.Printf("  Total: %d pollution bead(s) removed\n", deleted)

	// Suggest running compact to purge tombstones
	fmt.Println("  💡 Run 'bd compact --purge-tombstones' to reclaim space")

	return nil
}
