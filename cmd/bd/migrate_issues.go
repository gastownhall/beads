package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

var migrateIssuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "Move issues between repositories",
	Long: `Move issues from one source repository to another with filtering and dependency preservation.

This command updates the source_repo field for selected issues, allowing you to:
- Move contributor planning issues to upstream repository
- Reorganize issues across multi-phase repositories
- Consolidate issues from multiple repos

Examples:
  # Preview migration from planning repo to current repo
  bd migrate-issues --from ~/.beads-planning --to . --dry-run

  # Move all open P1 bugs
  bd migrate-issues --from ~/repo1 --to ~/repo2 --priority 1 --type bug --status open

  # Move specific issues with their dependencies
  bd migrate-issues --from . --to ~/archive --id bd-abc --id bd-xyz --include closure

  # Move issues with label filter
  bd migrate-issues --from . --to ~/feature-work --label frontend --label urgent`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Block writes in readonly mode
		if !dryRun {
			CheckReadonly("migrate-issues")
		}

		ctx := rootCtx

		// Parse flags
		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		statusStr, _ := cmd.Flags().GetString("status")
		priorityInt, _ := cmd.Flags().GetInt("priority")
		typeStr, _ := cmd.Flags().GetString("type")
		labels, _ := cmd.Flags().GetStringSlice("label")
		ids, _ := cmd.Flags().GetStringSlice("id")
		idsFile, _ := cmd.Flags().GetString("ids-file")
		include, _ := cmd.Flags().GetString("include")
		withinFromOnly, _ := cmd.Flags().GetBool("within-from-only")
		strict, _ := cmd.Flags().GetBool("strict")
		yes, _ := cmd.Flags().GetBool("yes")

		// Validate required flags
		if from == "" || to == "" {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "missing_required_flags",
					"message": "Both --from and --to are required",
				})
			} else {
				fmt.Fprintln(os.Stderr, "Error: both --from and --to flags are required")
			}
			os.Exit(1)
		}

		if from == to {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "same_source_and_dest",
					"message": "Source and destination repositories must be different",
				})
			} else {
				fmt.Fprintln(os.Stderr, "Error: --from and --to must be different repositories")
			}
			os.Exit(1)
		}

		// Load IDs from file if specified
		if idsFile != "" {
			fileIDs, err := loadIDsFromFile(idsFile)
			if err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "ids_file_read_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error reading IDs file: %v\n", err)
				}
				os.Exit(1)
			}
			ids = append(ids, fileIDs...)
		}

		// Execute migration
		if err := executeMigrateIssues(ctx, migrateIssuesParams{
			from:           from,
			to:             to,
			status:         statusStr,
			priority:       priorityInt,
			issueType:      typeStr,
			labels:         labels,
			ids:            ids,
			include:        include,
			withinFromOnly: withinFromOnly,
			dryRun:         dryRun,
			strict:         strict,
			yes:            yes,
		}); err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "migration_failed",
					"message": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(1)
		}
	},
}

type migrateIssuesParams struct {
	from           string
	to             string
	status         string
	priority       int
	issueType      string
	labels         []string
	ids            []string
	include        string
	withinFromOnly bool
	dryRun         bool
	strict         bool
	yes            bool
}

type migrationPlan struct {
	TotalSelected     int      `json:"total_selected"`
	AddedByDependency int      `json:"added_by_dependency"`
	IncomingEdges     int      `json:"incoming_edges"`
	OutgoingEdges     int      `json:"outgoing_edges"`
	Orphans           int      `json:"orphans"`
	OrphanSamples     []string `json:"orphan_samples,omitempty"`
	IssueIDs          []string `json:"issue_ids"`
	From              string   `json:"from"`
	To                string   `json:"to"`
}

func executeMigrateIssues(ctx context.Context, p migrateIssuesParams) error {
	// Get database connection (use global store)
	sqlStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return fmt.Errorf("migrate-issues requires SQLite storage")
	}
	db := sqlStore.UnderlyingDB()

	// Step 1: Validate repositories exist
	if err := validateRepos(ctx, db, p.from, p.to, p.strict); err != nil {
		return err
	}

	// Step 2: Build initial candidate set C using filters
	candidates, err := findCandidateIssues(ctx, db, p)
	if err != nil {
		return fmt.Errorf("failed to find candidate issues: %w", err)
	}

	if len(candidates) == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"message": "No issues match the specified filters",
			})
		} else {
			fmt.Println("Nothing to do: no issues match the specified filters")
		}
		return nil
	}

	// Step 3: Expand set to M (migration set) based on --include
	migrationSet, dependencyStats, err := expandMigrationSet(ctx, db, candidates, p)
	if err != nil {
		return fmt.Errorf("failed to compute migration set: %w", err)
	}

	// Step 4: Check for orphaned dependencies
	orphans, err := checkOrphanedDependencies(ctx, db)
	if err != nil {
		return fmt.Errorf("failed to check dependencies: %w", err)
	}

	if len(orphans) > 0 && p.strict {
		return fmt.Errorf("strict mode: found %d orphaned dependencies", len(orphans))
	}

	// Step 5: Build migration plan
	plan := buildMigrationPlan(candidates, migrationSet, dependencyStats, orphans, p.from, p.to)

	// Step 6: Display plan
	if err := displayMigrationPlan(plan, p.dryRun); err != nil {
		return err
	}

	// Step 7: Execute migration if not dry-run
	if !p.dryRun {
		if !p.yes && !jsonOutput {
			if !confirmMigration(plan) {
				fmt.Println("Migration canceled")
				return nil
			}
		}

		if err := executeMigration(ctx, db, migrationSet, p.to); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("Migrated %d issues from %s to %s", len(migrationSet), p.from, p.to),
				"plan":    plan,
			})
		} else {
			fmt.Printf("\n✓ Successfully migrated %d issues from %s to %s\n", len(migrationSet), p.from, p.to)
		}
	}

	return nil
}

func validateRepos(ctx context.Context, db *sql.DB, from, to string, strict bool) error {
	// Check if source repo exists
	var fromCount int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE source_repo = ?", from).Scan(&fromCount)
	if err != nil {
		return fmt.Errorf("failed to check source repository: %w", err)
	}

	if fromCount == 0 {
		msg := fmt.Sprintf("source repository '%s' has no issues", from)
		if strict {
			return fmt.Errorf("%s", msg)
		}
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}

	// Check if destination repo exists (just a warning)
	var toCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE source_repo = ?", to).Scan(&toCount)
	if err != nil {
		return fmt.Errorf("failed to check destination repository: %w", err)
	}

	if toCount == 0 && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Info: destination repository '%s' will be created\n", to)
	}

	return nil
}

func findCandidateIssues(ctx context.Context, db *sql.DB, p migrateIssuesParams) ([]string, error) {
	// Build WHERE clause
	var conditions []string
	var args []interface{}

	// Always filter by source_repo
	conditions = append(conditions, "source_repo = ?")
	args = append(args, p.from)

	// Filter by status
	if p.status != "" && p.status != "all" {
		conditions = append(conditions, "status = ?")
		args = append(args, p.status)
	}

	// Filter by priority
	if p.priority >= 0 {
		conditions = append(conditions, "priority = ?")
		args = append(args, p.priority)
	}

	// Filter by type
	if p.issueType != "" && p.issueType != "all" {
		conditions = append(conditions, "issue_type = ?")
		args = append(args, p.issueType)
	}

	// Filter by labels
	if len(p.labels) > 0 {
		// Issues must have ALL specified labels (AND logic)
		for _, label := range p.labels {
			conditions = append(conditions, `id IN (SELECT issue_id FROM issue_labels WHERE label = ?)`)
			args = append(args, label)
		}
	}

	// Build query
	query := "SELECT id FROM issues WHERE " + strings.Join(conditions, " AND ") // #nosec G202 -- query fragments are constant strings with parameter placeholders

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		candidates = append(candidates, id)
	}

	// Filter by explicit ID list if provided
	if len(p.ids) > 0 {
		idSet := make(map[string]bool)
		for _, id := range p.ids {
			idSet[id] = true
		}

		var filtered []string
		for _, id := range candidates {
			if idSet[id] {
				filtered = append(filtered, id)
			}
		}
		candidates = filtered
	}

	return candidates, nil
}

type dependencyStats struct {
	incomingEdges int
	outgoingEdges int
}

func expandMigrationSet(ctx context.Context, db *sql.DB, candidates []string, p migrateIssuesParams) ([]string, dependencyStats, error) {
	if p.include == "none" || p.include == "" {
		return candidates, dependencyStats{}, nil
	}

	// Build initial set
	migrationSet := make(map[string]bool)
	for _, id := range candidates {
		migrationSet[id] = true
	}

	// BFS traversal for dependency closure
	visited := make(map[string]bool)
	queue := make([]string, len(candidates))
	copy(queue, candidates)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current] {
			continue
		}
		visited[current] = true

		// Traverse based on include mode
		var deps []string
		var err error

		switch p.include {
		case "upstream":
			deps, err = getUpstreamDependencies(ctx, db, current, p.from, p.withinFromOnly)
		case "downstream":
			deps, err = getDownstreamDependencies(ctx, db, current, p.from, p.withinFromOnly)
		case "closure":
			upDeps, err1 := getUpstreamDependencies(ctx, db, current, p.from, p.withinFromOnly)
			downDeps, err2 := getDownstreamDependencies(ctx, db, current, p.from, p.withinFromOnly)
			if err1 != nil {
				err = err1
			} else if err2 != nil {
				err = err2
			} else {
				deps = append(upDeps, downDeps...)
			}
		}

		if err != nil {
			return nil, dependencyStats{}, err
		}

		for _, dep := range deps {
			if !visited[dep] {
				migrationSet[dep] = true
				queue = append(queue, dep)
			}
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(migrationSet))
	for id := range migrationSet {
		result = append(result, id)
	}

	// Count cross-repo edges
	stats, err := countCrossRepoEdges(ctx, db, result)
	if err != nil {
		return nil, dependencyStats{}, err
	}

	return result, stats, nil
}

func getUpstreamDependencies(ctx context.Context, db *sql.DB, issueID, fromRepo string, withinFromOnly bool) ([]string, error) {
	query := `SELECT depends_on_id FROM dependencies WHERE issue_id = ?`
	if withinFromOnly {
		query = `SELECT d.depends_on_id FROM dependencies d 
		         JOIN issues i ON d.depends_on_id = i.id 
		         WHERE d.issue_id = ? AND i.source_repo = ?`
	}

	var rows *sql.Rows
	var err error

	if withinFromOnly {
		rows, err = db.QueryContext(ctx, query, issueID, fromRepo)
	} else {
		rows, err = db.QueryContext(ctx, query, issueID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}

	return deps, nil
}

func getDownstreamDependencies(ctx context.Context, db *sql.DB, issueID, fromRepo string, withinFromOnly bool) ([]string, error) {
	query := `SELECT issue_id FROM dependencies WHERE depends_on_id = ?`
	if withinFromOnly {
		query = `SELECT d.issue_id FROM dependencies d 
		         JOIN issues i ON d.issue_id = i.id 
		         WHERE d.depends_on_id = ? AND i.source_repo = ?`
	}

	var rows *sql.Rows
	var err error

	if withinFromOnly {
		rows, err = db.QueryContext(ctx, query, issueID, fromRepo)
	} else {
		rows, err = db.QueryContext(ctx, query, issueID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []string
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}

	return deps, nil
}

func countCrossRepoEdges(ctx context.Context, db *sql.DB, migrationSet []string) (dependencyStats, error) {
	if len(migrationSet) == 0 {
		return dependencyStats{}, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(migrationSet))
	args := make([]interface{}, len(migrationSet))
	for i, id := range migrationSet {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Count incoming edges (external issues depend on migrated issues)
	incomingQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM dependencies 
		WHERE depends_on_id IN (%s) 
		AND issue_id NOT IN (%s)`, inClause, inClause) // #nosec G201 -- inClause generated from sanitized placeholders

	var incoming int
	if err := db.QueryRowContext(ctx, incomingQuery, append(args, args...)...).Scan(&incoming); err != nil {
		return dependencyStats{}, err
	}

	// Count outgoing edges (migrated issues depend on external issues)
	outgoingQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM dependencies 
		WHERE issue_id IN (%s) 
		AND depends_on_id NOT IN (%s)`, inClause, inClause) // #nosec G201 -- inClause generated from sanitized placeholders

	var outgoing int
	if err := db.QueryRowContext(ctx, outgoingQuery, append(args, args...)...).Scan(&outgoing); err != nil {
		return dependencyStats{}, err
	}

	return dependencyStats{
		incomingEdges: incoming,
		outgoingEdges: outgoing,
	}, nil
}

func checkOrphanedDependencies(ctx context.Context, db *sql.DB) ([]string, error) {
	// Check for dependencies referencing non-existent issues
	query := `
		SELECT DISTINCT d.depends_on_id 
		FROM dependencies d 
		LEFT JOIN issues i ON d.depends_on_id = i.id 
		WHERE i.id IS NULL
		UNION
		SELECT DISTINCT d.issue_id 
		FROM dependencies d 
		LEFT JOIN issues i ON d.issue_id = i.id 
		WHERE i.id IS NULL
	`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var orphan string
		if err := rows.Scan(&orphan); err != nil {
			return nil, err
		}
		orphans = append(orphans, orphan)
	}

	return orphans, nil
}

func buildMigrationPlan(candidates, migrationSet []string, stats dependencyStats, orphans []string, from, to string) migrationPlan {
	orphanSamples := orphans
	if len(orphanSamples) > 10 {
		orphanSamples = orphanSamples[:10]
	}

	return migrationPlan{
		TotalSelected:     len(candidates),
		AddedByDependency: len(migrationSet) - len(candidates),
		IncomingEdges:     stats.incomingEdges,
		OutgoingEdges:     stats.outgoingEdges,
		Orphans:           len(orphans),
		OrphanSamples:     orphanSamples,
		IssueIDs:          migrationSet,
		From:              from,
		To:                to,
	}
}

func displayMigrationPlan(plan migrationPlan, dryRun bool) error {
	if jsonOutput {
		output := map[string]interface{}{
			"plan":    plan,
			"dry_run": dryRun,
		}
		outputJSON(output)
		return nil
	}

	// Human-readable output
	fmt.Println("\n=== Migration Plan ===")
	fmt.Printf("From: %s\n", plan.From)
	fmt.Printf("To:   %s\n", plan.To)
	fmt.Println()
	fmt.Printf("Total selected:           %d issues\n", plan.TotalSelected)
	if plan.AddedByDependency > 0 {
		fmt.Printf("Added by dependencies:    %d issues\n", plan.AddedByDependency)
	}
	fmt.Printf("Total to migrate:         %d issues\n", len(plan.IssueIDs))
	fmt.Println()
	fmt.Printf("Cross-repo edges preserved:\n")
	fmt.Printf("  Incoming:  %d\n", plan.IncomingEdges)
	fmt.Printf("  Outgoing:  %d\n", plan.OutgoingEdges)

	if plan.Orphans > 0 {
		fmt.Println()
		fmt.Printf("⚠️  Warning: Found %d orphaned dependencies\n", plan.Orphans)
		if len(plan.OrphanSamples) > 0 {
			fmt.Println("Sample orphaned IDs:")
			for _, id := range plan.OrphanSamples {
				fmt.Printf("  - %s\n", id)
			}
		}
	}

	if dryRun {
		fmt.Println("\n[DRY RUN] No changes made")
		if len(plan.IssueIDs) <= 20 {
			fmt.Println("\nIssues to migrate:")
			for _, id := range plan.IssueIDs {
				fmt.Printf("  - %s\n", id)
			}
		} else {
			fmt.Printf("\n(%d issues would be migrated, showing first 20)\n", len(plan.IssueIDs))
			for i := 0; i < 20 && i < len(plan.IssueIDs); i++ {
				fmt.Printf("  - %s\n", plan.IssueIDs[i])
			}
		}
	}

	return nil
}

func confirmMigration(plan migrationPlan) bool {
	fmt.Printf("\nMigrate %d issues from %s to %s? [y/N] ", len(plan.IssueIDs), plan.From, plan.To)
	var response string
	_, _ = fmt.Scanln(&response)
	return strings.ToLower(strings.TrimSpace(response)) == "y"
}

func executeMigration(ctx context.Context, db *sql.DB, migrationSet []string, to string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()

	// Update source_repo for all issues in migration set
	for _, id := range migrationSet {
		_, err := tx.ExecContext(ctx,
			"UPDATE issues SET source_repo = ?, updated_at = ? WHERE id = ?",
			to, now, id)
		if err != nil {
			return fmt.Errorf("failed to update issue %s: %w", id, err)
		}

		// Mark as dirty for export
		_, err = tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO dirty_issues(issue_id) VALUES (?)", id)
		if err != nil {
			return fmt.Errorf("failed to mark issue %s as dirty: %w", id, err)
		}
	}

	return tx.Commit()
}

func loadIDsFromFile(path string) ([]string, error) {
	// #nosec G304 -- file path supplied explicitly via CLI flag
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	var ids []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			ids = append(ids, line)
		}
	}

	return ids, nil
}

func init() {
	migrateCmd.AddCommand(migrateIssuesCmd)

	migrateIssuesCmd.Flags().String("from", "", "Source repository (required)")
	migrateIssuesCmd.Flags().String("to", "", "Destination repository (required)")
	migrateIssuesCmd.Flags().String("status", "", "Filter by status (open/closed/all)")
	migrateIssuesCmd.Flags().Int("priority", -1, "Filter by priority (0-4)")
	migrateIssuesCmd.Flags().String("type", "", "Filter by issue type (bug/feature/task/epic/chore)")
	migrateIssuesCmd.Flags().StringSlice("label", nil, "Filter by labels (can specify multiple)")
	migrateIssuesCmd.Flags().StringSlice("id", nil, "Specific issue IDs to migrate (can specify multiple)")
	migrateIssuesCmd.Flags().String("ids-file", "", "File containing issue IDs (one per line)")
	migrateIssuesCmd.Flags().String("include", "none", "Include dependencies: none/upstream/downstream/closure")
	migrateIssuesCmd.Flags().Bool("within-from-only", true, "Only include dependencies from source repo")
	migrateIssuesCmd.Flags().Bool("dry-run", false, "Show plan without making changes")
	migrateIssuesCmd.Flags().Bool("strict", false, "Fail on orphaned dependencies or missing repos")
	migrateIssuesCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	_ = migrateIssuesCmd.MarkFlagRequired("from") // Only fails if flag missing (caught in tests)
	_ = migrateIssuesCmd.MarkFlagRequired("to")   // Only fails if flag missing (caught in tests)

	// Backwards compatibility alias at root level (hidden)
	migrateIssuesAliasCmd := *migrateIssuesCmd
	migrateIssuesAliasCmd.Use = "migrate-issues"
	migrateIssuesAliasCmd.Hidden = true
	migrateIssuesAliasCmd.Deprecated = "use 'bd migrate issues' instead (will be removed in v1.0.0)"
	rootCmd.AddCommand(&migrateIssuesAliasCmd)
}
