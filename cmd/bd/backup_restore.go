package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/storage/dolt"
	"github.com/gastownhall/beads/internal/ui"
	"github.com/gastownhall/beads/internal/utils"
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

		// Verify backup directory exists and has files
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup", dir)
		}

		// Check for issues.jsonl as minimum requirement
		issuesPath := filepath.Join(dir, "issues.jsonl")
		if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
			return fmt.Errorf("no issues.jsonl found in %s\nThis doesn't look like a valid backup directory", dir)
		}

		// Validate schema of issues.jsonl before proceeding (GH#2492)
		if err := validateIssueJSONLSchema(issuesPath); err != nil {
			return fmt.Errorf("backup validation failed: %w", err)
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
func runBackupRestore(ctx context.Context, s *dolt.DoltStore, dir string, dryRun bool) (*restoreResult, error) {
	result := &restoreResult{}
	db := s.DB()

	// 1. Restore config (sets issue_prefix and other settings)
	configPath := filepath.Join(dir, "config.jsonl")
	if _, err := os.Stat(configPath); err == nil {
		n, warnings, err := restoreConfig(ctx, s, configPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore config: %w", err)
		}
		result.Config = n
		result.Warnings += warnings
	}

	// 2. Restore issues (must come before comments/deps/labels which reference issue IDs)
	issuesPath := filepath.Join(dir, "issues.jsonl")
	n, err := restoreIssues(ctx, s, issuesPath, dryRun)
	if err != nil {
		return nil, fmt.Errorf("restore issues: %w", err)
	}
	result.Issues = n

	// 3. Restore comments
	commentsPath := filepath.Join(dir, "comments.jsonl")
	if _, err := os.Stat(commentsPath); err == nil {
		n, warnings, err := restoreComments(ctx, db, commentsPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore comments: %w", err)
		}
		result.Comments = n
		result.Warnings += warnings
	}

	// 4. Restore dependencies
	depsPath := filepath.Join(dir, "dependencies.jsonl")
	if _, err := os.Stat(depsPath); err == nil {
		n, warnings, err := restoreDependencies(ctx, db, depsPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore dependencies: %w", err)
		}
		result.Dependencies = n
		result.Warnings += warnings
	}

	// 5. Restore labels
	labelsPath := filepath.Join(dir, "labels.jsonl")
	if _, err := os.Stat(labelsPath); err == nil {
		n, warnings, err := restoreLabels(ctx, db, labelsPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore labels: %w", err)
		}
		result.Labels = n
		result.Warnings += warnings
	}

	// 6. Restore events
	eventsPath := filepath.Join(dir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		n, warnings, err := restoreEvents(ctx, db, eventsPath, dryRun)
		if err != nil {
			return nil, fmt.Errorf("restore events: %w", err)
		}
		result.Events = n
		result.Warnings += warnings
	}

	if !dryRun {
		// Commit the restore to Dolt
		if err := s.Commit(ctx, "bd backup restore"); err != nil {
			if !strings.Contains(err.Error(), "nothing to commit") {
				return nil, fmt.Errorf("failed to commit restore: %w", err)
			}
		}
	}

	return result, nil
}

// restoreConfig reads config.jsonl and sets each key-value pair.
func restoreConfig(ctx context.Context, s *dolt.DoltStore, path string, dryRun bool) (int, int, error) {
	type configEntry struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}

	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var entry configEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid config line: %v\n", err)
			warnings++
			continue
		}
		if entry.Key == "" {
			continue
		}
		if !dryRun {
			if err := s.SetConfig(ctx, entry.Key, entry.Value); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore config %q: %v\n", entry.Key, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// restoreIssues reads issues.jsonl and inserts them via raw SQL.
// Uses raw SQL with dynamic columns to match the backup export format exactly,
// avoiding type mismatches between DB values (e.g., int 0/1 for booleans) and
// Go struct types.
//
// The JSONL may contain denormalized data from `bd export` (labels, dependencies,
// comment counts). These are extracted and inserted into their proper tables.
func restoreIssues(ctx context.Context, s *dolt.DoltStore, path string, dryRun bool) (int, error) {
	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, err
	}

	if dryRun || len(lines) == 0 {
		return len(lines), nil
	}

	db := s.DB()

	// Auto-detect prefix from first issue for config
	var firstRow map[string]interface{}
	if err := json.Unmarshal(lines[0], &firstRow); err == nil {
		if id, ok := firstRow["id"].(string); ok {
			configuredPrefix, _ := s.GetConfig(ctx, "issue_prefix")
			if strings.TrimSpace(configuredPrefix) == "" {
				firstPrefix := utils.ExtractIssuePrefix(id)
				if firstPrefix != "" {
					_ = s.SetConfig(ctx, "issue_prefix", firstPrefix)
				}
			}
		}
	}

	count := 0
	for _, line := range lines {
		var row map[string]interface{}
		if err := json.Unmarshal(line, &row); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid issue line: %v\n", err)
			continue
		}

		if _, ok := row["id"]; !ok {
			continue
		}

		issueID, _ := row["id"].(string)

		// Extract denormalized relational data before SQL insertion.
		// `bd export` embeds labels ([]string) and dependencies ([]*Dependency)
		// in each issue row, but they belong in separate tables.
		var labels []interface{}
		if v, ok := row["labels"]; ok {
			if arr, ok := v.([]interface{}); ok {
				labels = arr
			}
			delete(row, "labels")
		}

		var deps []interface{}
		if v, ok := row["dependencies"]; ok {
			if arr, ok := v.([]interface{}); ok {
				deps = arr
			}
			delete(row, "dependencies")
		}

		// Remove computed count fields that don't exist in the issues table.
		delete(row, "dependency_count")
		delete(row, "dependent_count")
		delete(row, "comment_count")
		delete(row, "parent")

		n, warnings := restoreTableRow(ctx, db, "issues", row)
		count += n
		_ = warnings

		if n == 0 {
			continue // insertion failed, skip relational data
		}

		// Insert extracted labels into the labels table
		for _, l := range labels {
			if label, ok := l.(string); ok && label != "" {
				_, _ = db.ExecContext(ctx,
					"INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)",
					issueID, label)
			}
		}

		// Insert extracted dependencies into the dependencies table
		for _, d := range deps {
			dep, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			depIssueID, _ := dep["issue_id"].(string)
			dependsOnID, _ := dep["depends_on_id"].(string)
			depType, _ := dep["type"].(string)
			createdBy, _ := dep["created_by"].(string)
			metadata, _ := dep["metadata"].(string)
			if metadata == "" {
				metadata = "{}"
			}
			if depIssueID == "" || dependsOnID == "" {
				continue
			}
			createdAtStr, _ := dep["created_at"].(string)
			createdAt := parseTimeOrNow(createdAtStr)
			_, _ = db.ExecContext(ctx,
				"INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, ?, ?)",
				depIssueID, dependsOnID, depType, createdAt, createdBy, metadata)
		}
	}
	return count, nil
}

// restoreTableRow inserts a single row from a JSONL map into the given table.
// Uses INSERT IGNORE to handle duplicates gracefully. Returns (1, 0) on success.
//
// Values that are slices ([]interface{}) are serialized to JSON strings before
// insertion, since the SQL driver cannot handle Go slice types directly.
func restoreTableRow(ctx context.Context, db *sql.DB, table string, row map[string]interface{}) (int, int) {
	if len(row) == 0 {
		return 0, 0
	}

	cols := make([]string, 0, len(row))
	vals := make([]interface{}, 0, len(row))
	placeholders := make([]string, 0, len(row))

	for col, val := range row {
		// Convert slice/map types that the SQL driver cannot handle directly.
		// These typically come from JSON arrays or nested objects in the JSONL.
		switch v := val.(type) {
		case []interface{}:
			serialized, err := json.Marshal(v)
			if err != nil {
				continue // skip unparseable values
			}
			val = string(serialized)
		case map[string]interface{}:
			serialized, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(serialized)
		}
		cols = append(cols, "`"+col+"`")
		placeholders = append(placeholders, "?")
		vals = append(vals, val)
	}

	//nolint:gosec // G201: col names come from backup JSONL (our own export)
	query := fmt.Sprintf("INSERT IGNORE INTO `%s` (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))

	if _, err := db.ExecContext(ctx, query, vals...); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to restore %s row: %v\n", table, err)
		return 0, 1
	}
	return 1, 0
}

// restoreComments reads comments.jsonl and inserts them via raw SQL.
// Uses raw SQL to avoid side effects (the high-level API validates issue existence
// and may fail for wisps that haven't been restored yet).
func restoreComments(ctx context.Context, db *sql.DB, path string, dryRun bool) (int, int, error) {
	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var comment struct {
			ID        json.Number `json:"id"`
			IssueID   string      `json:"issue_id"`
			Author    string      `json:"author"`
			Text      string      `json:"text"`
			CreatedAt string      `json:"created_at"`
		}
		if err := json.Unmarshal(line, &comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid comment line: %v\n", err)
			warnings++
			continue
		}
		if comment.IssueID == "" {
			continue
		}
		if !dryRun {
			createdAt := parseTimeOrNow(comment.CreatedAt)
			_, err := db.ExecContext(ctx, `
				INSERT IGNORE INTO comments (issue_id, author, text, created_at)
				VALUES (?, ?, ?, ?)
			`, comment.IssueID, comment.Author, comment.Text, createdAt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore comment for %s: %v\n", comment.IssueID, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// restoreDependencies reads dependencies.jsonl and inserts them via raw SQL.
// Uses raw SQL to avoid validation side effects (cycle detection, existence checks).
func restoreDependencies(ctx context.Context, db *sql.DB, path string, dryRun bool) (int, int, error) {
	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var dep struct {
			IssueID     string  `json:"issue_id"`
			DependsOnID string  `json:"depends_on_id"`
			Type        string  `json:"type"`
			CreatedAt   string  `json:"created_at"`
			CreatedBy   string  `json:"created_by"`
			Metadata    *string `json:"metadata"`
		}
		if err := json.Unmarshal(line, &dep); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid dependency line: %v\n", err)
			warnings++
			continue
		}
		if dep.IssueID == "" || dep.DependsOnID == "" {
			continue
		}
		if !dryRun {
			createdAt := parseTimeOrNow(dep.CreatedAt)
			meta := "{}"
			if dep.Metadata != nil {
				raw := strings.TrimSpace(*dep.Metadata)
				if raw != "" {
					if !json.Valid([]byte(raw)) {
						fmt.Fprintf(os.Stderr, "Warning: invalid dependency metadata for %s -> %s; defaulting to {}\n", dep.IssueID, dep.DependsOnID)
						warnings++
					} else {
						meta = raw
					}
				}
			}
			_, err := db.ExecContext(ctx, `
				INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata)
				VALUES (?, ?, ?, ?, ?, ?)
			`, dep.IssueID, dep.DependsOnID, dep.Type, createdAt, dep.CreatedBy, meta)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore dependency %s -> %s: %v\n", dep.IssueID, dep.DependsOnID, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// restoreLabels reads labels.jsonl and inserts them via raw SQL.
// Uses raw SQL to avoid event creation side effects from AddLabel.
func restoreLabels(ctx context.Context, db *sql.DB, path string, dryRun bool) (int, int, error) {
	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var label struct {
			IssueID string `json:"issue_id"`
			Label   string `json:"label"`
		}
		if err := json.Unmarshal(line, &label); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid label line: %v\n", err)
			warnings++
			continue
		}
		if label.IssueID == "" || label.Label == "" {
			continue
		}
		if !dryRun {
			_, err := db.ExecContext(ctx, `
				INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)
			`, label.IssueID, label.Label)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore label %q for %s: %v\n", label.Label, label.IssueID, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// restoreEvents reads events.jsonl and inserts them via raw SQL.
func restoreEvents(ctx context.Context, db *sql.DB, path string, dryRun bool) (int, int, error) {
	lines, err := readJSONLFile(path)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	warnings := 0
	for _, line := range lines {
		var event struct {
			ID        json.Number `json:"id"`
			IssueID   string      `json:"issue_id"`
			EventType string      `json:"event_type"`
			Actor     string      `json:"actor"`
			OldValue  *string     `json:"old_value"`
			NewValue  *string     `json:"new_value"`
			Comment   *string     `json:"comment"`
			CreatedAt string      `json:"created_at"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid event line: %v\n", err)
			warnings++
			continue
		}
		if event.IssueID == "" {
			continue
		}
		if !dryRun {
			createdAt := parseTimeOrNow(event.CreatedAt)
			_, err := db.ExecContext(ctx, `
				INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, event.IssueID, event.EventType, event.Actor, event.OldValue, event.NewValue, event.Comment, createdAt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to restore event for %s: %v\n", event.IssueID, err)
				warnings++
				continue
			}
		}
		count++
	}
	return count, warnings, nil
}

// readJSONLFile reads a JSONL file and returns each non-empty line as raw JSON.
func readJSONLFile(path string) ([]json.RawMessage, error) {
	//nolint:gosec // G304: path is from backup directory
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	var lines []json.RawMessage
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Make a copy since scanner reuses the buffer
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, json.RawMessage(cp))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan %s: %w", path, err)
	}
	return lines, nil
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
