package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

var repairCmd = &cobra.Command{
	Use:     "repair",
	GroupID: GroupMaintenance,
	Short:   "Repair corrupted database by cleaning orphaned references",
	// Note: This command is in noDbCommands list (main.go) to skip normal db init.
	// We open SQLite directly, bypassing migration invariant checks.
	Long: `Repair a database that won't open due to orphaned foreign key references.

When the database has orphaned dependencies or labels, the migration invariant
check fails and prevents the database from opening. This creates a chicken-and-egg
problem where 'bd doctor --fix' can't run because it can't open the database.

This command opens SQLite directly (bypassing invariant checks) and cleans:
  - Orphaned dependencies (issue_id not in issues)
  - Orphaned dependencies (depends_on_id not in issues, excluding external refs)
  - Orphaned labels (issue_id not in issues)
  - Orphaned comments (issue_id not in issues)
  - Orphaned events (issue_id not in issues)

After repair, normal bd commands should work again.

Examples:
  bd repair              # Repair database in current directory
  bd repair --dry-run    # Show what would be cleaned without making changes
  bd repair --path /other/repo  # Repair database in another location
  bd repair --json       # Output results as JSON`,
	Run: runRepair,
}

var (
	repairDryRun bool
	repairPath   string
	repairJSON   bool
)

func init() {
	repairCmd.Flags().BoolVar(&repairDryRun, "dry-run", false, "Show what would be cleaned without making changes")
	repairCmd.Flags().StringVar(&repairPath, "path", ".", "Path to repository with .beads directory")
	repairCmd.Flags().BoolVar(&repairJSON, "json", false, "Output results as JSON")
	rootCmd.AddCommand(repairCmd)
}

// outputJSONAndExit outputs the repair result as JSON and exits
func outputJSONAndExit(result repairResult, exitCode int) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": "failed to marshal JSON: %v"}`, err)
		os.Exit(1)
	}
	fmt.Println(string(data))
	os.Exit(exitCode)
}

// validateRepairPaths validates the beads directory and database exist.
func validateRepairPaths() (string, error) {
	beadsDir := filepath.Join(repairPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return "", fmt.Errorf(".beads directory not found at %s", beadsDir)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return "", fmt.Errorf("database not found at %s", dbPath)
	}
	return dbPath, nil
}

// findAllOrphans finds all orphaned references in the database.
func findAllOrphans(db *sql.DB) (stats repairStats, orphans allOrphans, err error) {
	orphans.depsIssueID, err = findOrphanedDepsIssueID(db)
	if err != nil {
		return stats, orphans, fmt.Errorf("checking orphaned deps (issue_id): %w", err)
	}
	stats.orphanedDepsIssueID = len(orphans.depsIssueID)

	orphans.depsDependsOn, err = findOrphanedDepsDependsOn(db)
	if err != nil {
		return stats, orphans, fmt.Errorf("checking orphaned deps (depends_on_id): %w", err)
	}
	stats.orphanedDepsDependsOn = len(orphans.depsDependsOn)

	orphans.labels, err = findOrphanedLabels(db)
	if err != nil {
		return stats, orphans, fmt.Errorf("checking orphaned labels: %w", err)
	}
	stats.orphanedLabels = len(orphans.labels)

	orphans.comments, err = findOrphanedComments(db)
	if err != nil {
		return stats, orphans, fmt.Errorf("checking orphaned comments: %w", err)
	}
	stats.orphanedComments = len(orphans.comments)

	orphans.events, err = findOrphanedEvents(db)
	if err != nil {
		return stats, orphans, fmt.Errorf("checking orphaned events: %w", err)
	}
	stats.orphanedEvents = len(orphans.events)

	return stats, orphans, nil
}

// allOrphans holds all found orphaned references.
type allOrphans struct {
	depsIssueID   []orphanedDep
	depsDependsOn []orphanedDep
	labels        []orphanedLabel
	comments      []orphanedComment
	events        []orphanedEvent
}

// printOrphansText prints orphan details in text mode.
func printOrphansText(stats repairStats, orphans allOrphans) {
	fmt.Printf("Found %d orphaned reference(s):\n", stats.total())
	if stats.orphanedDepsIssueID > 0 {
		fmt.Printf("  • %d dependencies with missing issue_id\n", stats.orphanedDepsIssueID)
		for _, dep := range orphans.depsIssueID {
			fmt.Printf("    - %s → %s\n", dep.issueID, dep.dependsOnID)
		}
	}
	if stats.orphanedDepsDependsOn > 0 {
		fmt.Printf("  • %d dependencies with missing depends_on_id\n", stats.orphanedDepsDependsOn)
		for _, dep := range orphans.depsDependsOn {
			fmt.Printf("    - %s → %s\n", dep.issueID, dep.dependsOnID)
		}
	}
	if stats.orphanedLabels > 0 {
		fmt.Printf("  • %d labels with missing issue_id\n", stats.orphanedLabels)
		for _, label := range orphans.labels {
			fmt.Printf("    - %s: %s\n", label.issueID, label.label)
		}
	}
	if stats.orphanedComments > 0 {
		fmt.Printf("  • %d comments with missing issue_id\n", stats.orphanedComments)
		for _, comment := range orphans.comments {
			fmt.Printf("    - %s (by %s)\n", comment.issueID, comment.author)
		}
	}
	if stats.orphanedEvents > 0 {
		fmt.Printf("  • %d events with missing issue_id\n", stats.orphanedEvents)
		for _, event := range orphans.events {
			fmt.Printf("    - %s: %s\n", event.issueID, event.eventType)
		}
	}
	fmt.Println()
}

func runRepair(cmd *cobra.Command, args []string) {
	dbPath, err := validateRepairPaths()
	if err != nil {
		if repairJSON {
			outputJSONAndExit(repairResult{Status: "error", Error: err.Error()}, 1)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !repairJSON {
		fmt.Printf("Repairing database: %s\n", dbPath)
		if repairDryRun {
			fmt.Println("[DRY-RUN] No changes will be made")
		}
		fmt.Println()
	}

	db, err := openRepairDB(dbPath)
	if err != nil {
		if repairJSON {
			outputJSONAndExit(repairResult{DatabasePath: dbPath, Status: "error", Error: fmt.Sprintf("opening database: %v", err)}, 1)
		}
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Find all orphaned references
	stats, orphans, err := findAllOrphans(db)
	if err != nil {
		if repairJSON {
			outputJSONAndExit(repairResult{DatabasePath: dbPath, Status: "error", Error: err.Error()}, 1)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Build JSON result structure (used for both JSON output and tracking)
	jsonResult := repairResult{
		DatabasePath: dbPath,
		DryRun:       repairDryRun,
		OrphanCounts: repairOrphanCounts{
			DependenciesIssueID:   stats.orphanedDepsIssueID,
			DependenciesDependsOn: stats.orphanedDepsDependsOn,
			Labels:                stats.orphanedLabels,
			Comments:              stats.orphanedComments,
			Events:                stats.orphanedEvents,
			Total:                 stats.total(),
		},
	}

	// Build orphan details
	for _, dep := range orphans.depsIssueID {
		jsonResult.OrphanDetails.DependenciesIssueID = append(jsonResult.OrphanDetails.DependenciesIssueID,
			repairDepDetail{IssueID: dep.issueID, DependsOnID: dep.dependsOnID})
	}
	for _, dep := range orphans.depsDependsOn {
		jsonResult.OrphanDetails.DependenciesDependsOn = append(jsonResult.OrphanDetails.DependenciesDependsOn,
			repairDepDetail{IssueID: dep.issueID, DependsOnID: dep.dependsOnID})
	}
	for _, label := range orphans.labels {
		jsonResult.OrphanDetails.Labels = append(jsonResult.OrphanDetails.Labels,
			repairLabelDetail{IssueID: label.issueID, Label: label.label})
	}
	for _, comment := range orphans.comments {
		jsonResult.OrphanDetails.Comments = append(jsonResult.OrphanDetails.Comments,
			repairCommentDetail{ID: comment.id, IssueID: comment.issueID, Author: comment.author})
	}
	for _, event := range orphans.events {
		jsonResult.OrphanDetails.Events = append(jsonResult.OrphanDetails.Events,
			repairEventDetail{ID: event.id, IssueID: event.issueID, EventType: event.eventType})
	}

	// Handle no orphans case
	if stats.total() == 0 {
		if repairJSON {
			jsonResult.Status = "no_orphans"
			outputJSONAndExit(jsonResult, 0)
		}
		fmt.Printf("%s No orphaned references found - database is clean\n", ui.RenderPass("✓"))
		return
	}

	// Print findings (text mode)
	if !repairJSON {
		printOrphansText(stats, orphans)
	}

	// Handle dry-run
	if repairDryRun {
		if repairJSON {
			jsonResult.Status = "dry_run"
			outputJSONAndExit(jsonResult, 0)
		}
		fmt.Printf("[DRY-RUN] Would delete %d orphaned reference(s)\n", stats.total())
		return
	}

	// Create backup before destructive operations
	backupPath := dbPath + ".pre-repair"
	if !repairJSON {
		fmt.Printf("Creating backup: %s\n", filepath.Base(backupPath))
	}
	if err := copyFile(dbPath, backupPath); err != nil {
		if repairJSON {
			jsonResult.Status = "error"
			jsonResult.Error = fmt.Sprintf("creating backup: %v", err)
			outputJSONAndExit(jsonResult, 1)
		}
		fmt.Fprintf(os.Stderr, "Error creating backup: %v\n", err)
		fmt.Fprintf(os.Stderr, "Aborting repair. Fix backup issue and retry.\n")
		os.Exit(1)
	}
	jsonResult.BackupPath = backupPath
	if !repairJSON {
		fmt.Printf("  %s Backup created\n\n", ui.RenderPass("✓"))
	}

	// Apply repairs in a transaction
	if !repairJSON {
		fmt.Println("Cleaning orphaned references...")
	}

	tx, err := db.Begin()
	if err != nil {
		if repairJSON {
			jsonResult.Status = "error"
			jsonResult.Error = fmt.Sprintf("starting transaction: %v", err)
			outputJSONAndExit(jsonResult, 1)
		}
		fmt.Fprintf(os.Stderr, "Error starting transaction: %v\n", err)
		os.Exit(1)
	}

	var repairErr error

	// Delete orphaned deps (issue_id) and mark affected issues dirty
	if len(orphans.depsIssueID) > 0 && repairErr == nil {
		// Note: orphanedIssueID contains deps where issue_id doesn't exist,
		// so we can't mark them dirty (the issue is gone). But for depends_on orphans,
		// the issue_id still exists and should be marked dirty.
		result, err := tx.Exec(`
			DELETE FROM dependencies
			WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = dependencies.issue_id)
		`)
		if err != nil {
			repairErr = fmt.Errorf("deleting orphaned deps (issue_id): %w", err)
		} else if !repairJSON {
			deleted, _ := result.RowsAffected()
			fmt.Printf("  %s Deleted %d dependencies with missing issue_id\n", ui.RenderPass("✓"), deleted)
		}
	}

	// Delete orphaned deps (depends_on_id) and mark parent issues dirty
	if len(orphans.depsDependsOn) > 0 && repairErr == nil {
		// Mark parent issues as dirty for export
		for _, dep := range orphans.depsDependsOn {
			_, _ = tx.Exec("INSERT OR IGNORE INTO dirty_issues (issue_id) VALUES (?)", dep.issueID)
		}

		result, err := tx.Exec(`
			DELETE FROM dependencies
			WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = dependencies.depends_on_id)
			  AND dependencies.depends_on_id NOT LIKE 'external:%'
		`)
		if err != nil {
			repairErr = fmt.Errorf("deleting orphaned deps (depends_on_id): %w", err)
		} else if !repairJSON {
			deleted, _ := result.RowsAffected()
			fmt.Printf("  %s Deleted %d dependencies with missing depends_on_id\n", ui.RenderPass("✓"), deleted)
		}
	}

	// Delete orphaned labels
	if len(orphans.labels) > 0 && repairErr == nil {
		// Labels reference non-existent issues, so no dirty marking needed
		result, err := tx.Exec(`
			DELETE FROM labels
			WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = labels.issue_id)
		`)
		if err != nil {
			repairErr = fmt.Errorf("deleting orphaned labels: %w", err)
		} else if !repairJSON {
			deleted, _ := result.RowsAffected()
			fmt.Printf("  %s Deleted %d labels with missing issue_id\n", ui.RenderPass("✓"), deleted)
		}
	}

	// Delete orphaned comments
	if len(orphans.comments) > 0 && repairErr == nil {
		// Comments reference non-existent issues, so no dirty marking needed
		result, err := tx.Exec(`
			DELETE FROM comments
			WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = comments.issue_id)
		`)
		if err != nil {
			repairErr = fmt.Errorf("deleting orphaned comments: %w", err)
		} else if !repairJSON {
			deleted, _ := result.RowsAffected()
			fmt.Printf("  %s Deleted %d comments with missing issue_id\n", ui.RenderPass("✓"), deleted)
		}
	}

	// Delete orphaned events
	if len(orphans.events) > 0 && repairErr == nil {
		// Events reference non-existent issues, so no dirty marking needed
		result, err := tx.Exec(`
			DELETE FROM events
			WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = events.issue_id)
		`)
		if err != nil {
			repairErr = fmt.Errorf("deleting orphaned events: %w", err)
		} else if !repairJSON {
			deleted, _ := result.RowsAffected()
			fmt.Printf("  %s Deleted %d events with missing issue_id\n", ui.RenderPass("✓"), deleted)
		}
	}

	// Commit or rollback
	if repairErr != nil {
		_ = tx.Rollback()
		if repairJSON {
			jsonResult.Status = "error"
			jsonResult.Error = repairErr.Error()
			outputJSONAndExit(jsonResult, 1)
		}
		fmt.Fprintf(os.Stderr, "\n%s Error: %v\n", ui.RenderFail("✗"), repairErr)
		fmt.Fprintf(os.Stderr, "Transaction rolled back. Database unchanged.\n")
		fmt.Fprintf(os.Stderr, "Backup available at: %s\n", backupPath)
		os.Exit(1)
	}

	if err := tx.Commit(); err != nil {
		if repairJSON {
			jsonResult.Status = "error"
			jsonResult.Error = fmt.Sprintf("committing transaction: %v", err)
			outputJSONAndExit(jsonResult, 1)
		}
		fmt.Fprintf(os.Stderr, "\n%s Error committing transaction: %v\n", ui.RenderFail("✗"), err)
		fmt.Fprintf(os.Stderr, "Backup available at: %s\n", backupPath)
		os.Exit(1)
	}

	// Run WAL checkpoint to persist changes
	if !repairJSON {
		fmt.Print("  Running WAL checkpoint... ")
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		if !repairJSON {
			fmt.Printf("%s %v\n", ui.RenderFail("✗"), err)
		}
	} else if !repairJSON {
		fmt.Printf("%s\n", ui.RenderPass("✓"))
	}

	// Final output
	if repairJSON {
		jsonResult.Status = "success"
		outputJSONAndExit(jsonResult, 0)
	}

	fmt.Println()
	fmt.Printf("%s Repair complete. Try running 'bd doctor' to verify.\n", ui.RenderPass("✓"))
	fmt.Printf("Backup preserved at: %s\n", filepath.Base(backupPath))
}

// repairStats tracks what was found/cleaned
type repairStats struct {
	orphanedDepsIssueID   int
	orphanedDepsDependsOn int
	orphanedLabels        int
	orphanedComments      int
	orphanedEvents        int
}

func (s repairStats) total() int {
	return s.orphanedDepsIssueID + s.orphanedDepsDependsOn + s.orphanedLabels + s.orphanedComments + s.orphanedEvents
}

// orphanedDep represents an orphaned dependency
type orphanedDep struct {
	issueID     string
	dependsOnID string
}

// orphanedLabel represents an orphaned label
type orphanedLabel struct {
	issueID string
	label   string
}

// orphanedComment represents an orphaned comment
type orphanedComment struct {
	id      int
	issueID string
	author  string
}

// orphanedEvent represents an orphaned event
type orphanedEvent struct {
	id        int
	issueID   string
	eventType string
}

// repairResult is the JSON output structure
type repairResult struct {
	DatabasePath  string              `json:"database_path"`
	DryRun        bool                `json:"dry_run"`
	OrphanCounts  repairOrphanCounts  `json:"orphan_counts"`
	OrphanDetails repairOrphanDetails `json:"orphan_details"`
	Status        string              `json:"status"` // "success", "no_orphans", "dry_run", "error"
	BackupPath    string              `json:"backup_path,omitempty"`
	Error         string              `json:"error,omitempty"`
}

type repairOrphanCounts struct {
	DependenciesIssueID   int `json:"dependencies_issue_id"`
	DependenciesDependsOn int `json:"dependencies_depends_on"`
	Labels                int `json:"labels"`
	Comments              int `json:"comments"`
	Events                int `json:"events"`
	Total                 int `json:"total"`
}

type repairOrphanDetails struct {
	DependenciesIssueID   []repairDepDetail     `json:"dependencies_issue_id,omitempty"`
	DependenciesDependsOn []repairDepDetail     `json:"dependencies_depends_on,omitempty"`
	Labels                []repairLabelDetail   `json:"labels,omitempty"`
	Comments              []repairCommentDetail `json:"comments,omitempty"`
	Events                []repairEventDetail   `json:"events,omitempty"`
}

type repairDepDetail struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
}

type repairLabelDetail struct {
	IssueID string `json:"issue_id"`
	Label   string `json:"label"`
}

type repairCommentDetail struct {
	ID      int    `json:"id"`
	IssueID string `json:"issue_id"`
	Author  string `json:"author"`
}

type repairEventDetail struct {
	ID        int    `json:"id"`
	IssueID   string `json:"issue_id"`
	EventType string `json:"event_type"`
}

// openRepairDB opens SQLite directly for repair, bypassing all beads layer code
func openRepairDB(dbPath string) (*sql.DB, error) {
	// Build connection string with pragmas
	busyMs := int64(30 * time.Second / time.Millisecond)
	if v := strings.TrimSpace(os.Getenv("BD_LOCK_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			busyMs = int64(d / time.Millisecond)
		}
	}

	connStr := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(OFF)&_time_format=sqlite",
		dbPath, busyMs)

	return sql.Open("sqlite3", connStr)
}

// findOrphanedDepsIssueID finds dependencies where issue_id doesn't exist
func findOrphanedDepsIssueID(db *sql.DB) ([]orphanedDep, error) {
	rows, err := db.Query(`
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = d.issue_id)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orphans []orphanedDep
	for rows.Next() {
		var dep orphanedDep
		if err := rows.Scan(&dep.issueID, &dep.dependsOnID); err != nil {
			return nil, err
		}
		orphans = append(orphans, dep)
	}
	return orphans, rows.Err()
}

// findOrphanedDepsDependsOn finds dependencies where depends_on_id doesn't exist
func findOrphanedDepsDependsOn(db *sql.DB) ([]orphanedDep, error) {
	rows, err := db.Query(`
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = d.depends_on_id)
		  AND d.depends_on_id NOT LIKE 'external:%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orphans []orphanedDep
	for rows.Next() {
		var dep orphanedDep
		if err := rows.Scan(&dep.issueID, &dep.dependsOnID); err != nil {
			return nil, err
		}
		orphans = append(orphans, dep)
	}
	return orphans, rows.Err()
}

// findOrphanedLabels finds labels where issue_id doesn't exist
func findOrphanedLabels(db *sql.DB) ([]orphanedLabel, error) {
	rows, err := db.Query(`
		SELECT l.issue_id, l.label
		FROM labels l
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = l.issue_id)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var labels []orphanedLabel
	for rows.Next() {
		var label orphanedLabel
		if err := rows.Scan(&label.issueID, &label.label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// findOrphanedComments finds comments where issue_id doesn't exist
func findOrphanedComments(db *sql.DB) ([]orphanedComment, error) {
	rows, err := db.Query(`
		SELECT c.id, c.issue_id, c.author
		FROM comments c
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = c.issue_id)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []orphanedComment
	for rows.Next() {
		var comment orphanedComment
		if err := rows.Scan(&comment.id, &comment.issueID, &comment.author); err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// findOrphanedEvents finds events where issue_id doesn't exist
func findOrphanedEvents(db *sql.DB) ([]orphanedEvent, error) {
	rows, err := db.Query(`
		SELECT e.id, e.issue_id, e.event_type
		FROM events e
		WHERE NOT EXISTS (SELECT 1 FROM issues WHERE id = e.issue_id)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []orphanedEvent
	for rows.Next() {
		var event orphanedEvent
		if err := rows.Scan(&event.id, &event.issueID, &event.eventType); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

