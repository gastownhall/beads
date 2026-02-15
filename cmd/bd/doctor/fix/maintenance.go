package fix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// cleanupResult contains the results of a cleanup operation
type cleanupResult struct {
	DeletedCount  int
	SkippedPinned int
}

// StaleClosedIssues deletes stale closed issues.
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

	// Open database using factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
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

// PatrolPollution deletes patrol digest and session ended beads that pollute the database.
// This is the fix handler for the "Patrol Pollution" doctor check.
//
// It removes beads matching:
// - Patrol digests: titles matching "Digest: mol-*-patrol"
// - Session ended beads: titles matching "Session ended: *"
//
// After deletion, cleans up any orphaned data.
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

	// Open database using factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
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

	return nil
}
