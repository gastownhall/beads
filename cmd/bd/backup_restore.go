// TODO: This is not the correct approach for "restoring" a backup. The
// CALL DOLT_BACKUP('restore', ...) stored procedure should be used instead.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/ui"
)

var backupRestoreCmd = &cobra.Command{
	Use:   "restore [path]",
	Short: "Restore database from JSONL backup files",
	Long: `Restore the beads database from JSONL backup files.

By default, reads from .beads/backup/ (or the configured backup directory).
Optionally specify a path to a directory containing JSONL backup files.

This command:
  1. Detects .beads/backup/*.jsonl files (or accepts a custom path)
  2. Imports config, issues, comments, dependencies, labels, and events
  3. Restores backup_state.json watermarks so incremental backup resumes correctly

Use this after losing your Dolt database (machine crash, new clone, etc.)
when you have JSONL backups on disk or in git.

If your backup snapshots are stored in a git branch, use 'bd backup fetch-git'
to fetch that branch into a temporary worktree and restore from it.

The database must already be initialized (run 'bd init' first if needed).
To initialize and restore in one step, use: bd init && bd backup restore`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx

		var dir string
		if len(args) > 0 {
			dir = args[0]
		} else {
			var err error
			dir, err = backupDir()
			if err != nil {
				return fmt.Errorf("failed to find backup directory: %w", err)
			}
		}

		if err := validateBackupRestoreDir(dir); err != nil {
			return err
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")

		result, err := runBackupRestore(ctx, store, dir, dryRun)
		if err != nil {
			return err
		}

		if jsonOutput {
			data, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(data))
			return nil
		}

		if dryRun {
			fmt.Printf("%s Dry run — no changes made\n\n", ui.RenderWarn("!"))
		} else {
			fmt.Printf("%s Restore complete\n\n", ui.RenderPass("✓"))
		}

		fmt.Printf("  Issues:       %d\n", result.Issues)
		fmt.Printf("  Comments:     %d\n", result.Comments)
		fmt.Printf("  Dependencies: %d\n", result.Dependencies)
		fmt.Printf("  Labels:       %d\n", result.Labels)
		fmt.Printf("  Events:       %d\n", result.Events)
		fmt.Printf("  Config:       %d\n", result.Config)

		if result.Warnings > 0 {
			fmt.Printf("\n  %s %d warnings (see above)\n", ui.RenderWarn("⚠"), result.Warnings)
		}

		return nil
	},
}

func init() {
	backupRestoreCmd.Flags().Bool("dry-run", false, "Show what would be restored without making changes")
	backupCmd.AddCommand(backupRestoreCmd)
}

// restoreResult tracks what a restore operation did.
type restoreResult struct {
	Issues       int      `json:"issues"`
	Comments     int      `json:"comments"`
	Dependencies int      `json:"dependencies"`
	Labels       int      `json:"labels"`
	Events       int      `json:"events"`
	Config       int      `json:"config"`
	Warnings     int      `json:"warnings"`
	Errors       int      `json:"errors"`
	ErrorDetails []string `json:"error_details,omitempty"`
}

// runBackupRestore imports all JSONL backup tables into the Dolt store.
// Order matters: config first (sets prefix), then issues, then related tables.
// When a project prefix is configured, only entries belonging to this project
// are imported. This prevents cross-project contamination on shared Dolt servers.
func runBackupRestore(ctx context.Context, s storage.DoltStorage, dir string, dryRun bool) (*restoreResult, error) {
	if s == nil {
		return nil, fmt.Errorf("database is not initialized. Run 'bd init' first")
	}

	bs, ok := s.(storage.BackupStore)
	if !ok {
		return nil, fmt.Errorf("storage backend does not support backup operations")
	}

	prefix := getBackupPrefix(ctx)

	storeResult, err := bs.BackupRestoreFromDir(ctx, dir, prefix, dryRun)
	if err != nil {
		return nil, err
	}

	result := &restoreResult{
		Issues:       storeResult.Issues,
		Comments:     storeResult.Comments,
		Dependencies: storeResult.Dependencies,
		Labels:       storeResult.Labels,
		Events:       storeResult.Events,
		Config:       storeResult.Config,
		Warnings:     storeResult.Warnings,
	}

	if !dryRun {
		if err := s.Commit(ctx, "bd backup restore"); err != nil {
			if !strings.Contains(err.Error(), "nothing to commit") {
				return nil, fmt.Errorf("failed to commit restore: %w", err)
			}
		}
	}

	return result, nil
}

func validateBackupRestoreDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup", dir)
	}

	issuesPath := filepath.Join(dir, "issues.jsonl")
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return fmt.Errorf("no issues.jsonl found in %s\nThis doesn't look like a valid backup directory", dir)
	}

	if err := validateIssueJSONLSchema(issuesPath); err != nil {
		return fmt.Errorf("backup validation failed: %w", err)
	}

	return nil
}

// validateIssueJSONLSchema checks the first line of a JSONL file to verify it
// contains expected issue fields. This prevents silent data corruption from
// importing export files with incompatible schemas (GH#2492, GH#2465).
//
// Returns nil if the schema looks valid, or an error describing the mismatch.
func validateIssueJSONLSchema(path string) error {
	f, err := os.Open(path) //nolint:gosec // path is from trusted backup directory, not user-controlled
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	if !scanner.Scan() {
		return nil // Empty file, nothing to validate
	}

	line := scanner.Bytes()
	if len(line) == 0 {
		return nil
	}

	// Parse first line as JSON object
	var firstRow map[string]interface{}
	if err := json.Unmarshal(line, &firstRow); err != nil {
		return fmt.Errorf("first line of %s is not valid JSON: %w", path, err)
	}

	// Check for required issue fields
	requiredFields := []string{"id", "title", "status"}
	var missing []string
	for _, field := range requiredFields {
		if _, ok := firstRow[field]; !ok {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("issues.jsonl schema mismatch: missing required fields %v in first row. This file may be a bd export (different format) or corrupted", missing)
	}

	return nil
}

// readJSONLFile delegates to the shared versioncontrolops implementation.
// Kept as a package-level alias for test compatibility.
var readJSONLFile = versioncontrolops.ReadJSONLFile

// parseTimeOrNow parses an RFC3339 time string, returning now if parsing fails.
func parseTimeOrNow(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}
