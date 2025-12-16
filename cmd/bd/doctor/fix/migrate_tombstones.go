package fix

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/types"
)

// MigrateTombstones converts legacy deletions.jsonl entries to inline tombstones.
// This is called by bd doctor --fix when legacy deletions are detected.
func MigrateTombstones(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if deletions.jsonl exists
	if _, err := os.Stat(deletionsPath); os.IsNotExist(err) {
		fmt.Println("  No deletions.jsonl found - already using tombstones")
		return nil
	}

	// Load deletions
	loadResult, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		return fmt.Errorf("failed to load deletions: %w", err)
	}

	if len(loadResult.Records) == 0 {
		fmt.Println("  deletions.jsonl is empty - nothing to migrate")
		return nil
	}

	// Load existing JSONL to check for already-existing tombstones
	existingTombstones := make(map[string]bool)
	if file, err := os.Open(jsonlPath); err == nil {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			var issue struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &issue); err == nil {
				if issue.Status == string(types.StatusTombstone) {
					existingTombstones[issue.ID] = true
				}
			}
		}
		file.Close()
	}

	// Convert deletions to tombstones
	var toMigrate []deletions.DeletionRecord
	var skipped int
	for _, record := range loadResult.Records {
		if existingTombstones[record.ID] {
			skipped++
			continue
		}
		toMigrate = append(toMigrate, record)
	}

	if len(toMigrate) == 0 {
		fmt.Printf("  All %d deletion(s) already have tombstones - archiving deletions.jsonl\n", skipped)
	} else {
		// Append tombstones to issues.jsonl
		file, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("failed to open issues.jsonl: %w", err)
		}
		defer file.Close()

		for _, record := range toMigrate {
			tombstone := convertDeletionToTombstone(record)
			data, err := json.Marshal(tombstone)
			if err != nil {
				return fmt.Errorf("failed to marshal tombstone for %s: %w", record.ID, err)
			}
			if _, err := file.Write(append(data, '\n')); err != nil {
				return fmt.Errorf("failed to write tombstone for %s: %w", record.ID, err)
			}
		}
		fmt.Printf("  Migrated %d deletion(s) to tombstones\n", len(toMigrate))
		if skipped > 0 {
			fmt.Printf("  Skipped %d (already had tombstones)\n", skipped)
		}
	}

	// Archive deletions.jsonl
	migratedPath := deletionsPath + ".migrated"
	if err := os.Rename(deletionsPath, migratedPath); err != nil {
		return fmt.Errorf("failed to archive deletions.jsonl: %w", err)
	}
	fmt.Printf("  Archived deletions.jsonl → deletions.jsonl.migrated\n")

	return nil
}

// convertDeletionToTombstone converts a DeletionRecord to a tombstone Issue.
func convertDeletionToTombstone(record deletions.DeletionRecord) *types.Issue {
	now := time.Now()
	deletedAt := record.Timestamp
	if deletedAt.IsZero() {
		deletedAt = now
	}

	return &types.Issue{
		ID:           record.ID,
		Title:        "[Deleted]",
		Status:       types.StatusTombstone,
		IssueType:    types.TypeTask, // Default type for validation
		Priority:     0,              // Unknown priority
		CreatedAt:    deletedAt,
		UpdatedAt:    now,
		DeletedAt:    &deletedAt,
		DeletedBy:    record.Actor,
		DeleteReason: record.Reason,
		OriginalType: string(types.TypeTask),
	}
}
