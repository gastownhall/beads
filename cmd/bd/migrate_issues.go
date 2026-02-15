package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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
	s := store // use global Storage interface

	// Step 1: Validate repositories exist
	if err := validateRepos(ctx, s, p.from, p.to, p.strict); err != nil {
		return err
	}

	// Step 2: Build initial candidate set C using filters
	candidates, err := findCandidateIssues(ctx, s, p)
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
	migrationSet, dependencyStats, err := expandMigrationSet(ctx, s, candidates, p)
	if err != nil {
		return fmt.Errorf("failed to compute migration set: %w", err)
	}

	// Step 4: Check for orphaned dependencies
	orphans, err := checkOrphanedDependencies(ctx, s)
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

		if err := executeMigration(ctx, s, migrationSet, p.to); err != nil {
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

func validateRepos(ctx context.Context, s storage.Storage, from, to string, strict bool) error {
	// Check if source repo has any issues
	fromIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{
		SourceRepo:        &from,
		Limit:             1,
	})
	if err != nil {
		return fmt.Errorf("failed to check source repository: %w", err)
	}

	if len(fromIssues) == 0 {
		msg := fmt.Sprintf("source repository '%s' has no issues", from)
		if strict {
			return fmt.Errorf("%s", msg)
		}
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}

	// Check if destination repo exists (just a warning)
	toIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{
		SourceRepo:        &to,
		Limit:             1,
	})
	if err != nil {
		return fmt.Errorf("failed to check destination repository: %w", err)
	}

	if len(toIssues) == 0 && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Info: destination repository '%s' will be created\n", to)
	}

	return nil
}

func findCandidateIssues(ctx context.Context, s storage.Storage, p migrateIssuesParams) ([]string, error) {
	// Build filter from params
	filter := types.IssueFilter{
		SourceRepo:        &p.from,
	}

	// Filter by status
	if p.status != "" && p.status != "all" {
		status := types.Status(p.status)
		filter.Status = &status
	}

	// Filter by priority
	if p.priority >= 0 {
		filter.Priority = &p.priority
	}

	// Filter by type
	if p.issueType != "" && p.issueType != "all" {
		issueType := types.IssueType(p.issueType)
		filter.IssueType = &issueType
	}

	// Filter by labels (AND semantics)
	if len(p.labels) > 0 {
		filter.Labels = p.labels
	}

	// Filter by explicit IDs if provided (intersect with other filters)
	if len(p.ids) > 0 {
		filter.IDs = p.ids
	}

	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}

	candidates := make([]string, len(issues))
	for i, issue := range issues {
		candidates[i] = issue.ID
	}

	return candidates, nil
}

type dependencyStats struct {
	incomingEdges int
	outgoingEdges int
}

func expandMigrationSet(ctx context.Context, s storage.Storage, candidates []string, p migrateIssuesParams) ([]string, dependencyStats, error) {
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
			deps, err = getUpstreamDependencies(ctx, s, current, p.from, p.withinFromOnly)
		case "downstream":
			deps, err = getDownstreamDependencies(ctx, s, current, p.from, p.withinFromOnly)
		case "closure":
			upDeps, err1 := getUpstreamDependencies(ctx, s, current, p.from, p.withinFromOnly)
			downDeps, err2 := getDownstreamDependencies(ctx, s, current, p.from, p.withinFromOnly)
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
	stats, err := countCrossRepoEdges(ctx, s, result)
	if err != nil {
		return nil, dependencyStats{}, err
	}

	return result, stats, nil
}

// getUpstreamDependencies returns IDs of issues that the given issue depends on.
// If withinFromOnly is true, only returns dependencies whose issues are in fromRepo.
func getUpstreamDependencies(ctx context.Context, s storage.Storage, issueID, fromRepo string, withinFromOnly bool) ([]string, error) {
	// GetDependencyRecords returns deps where issue_id = issueID
	depRecords, err := s.GetDependencyRecords(ctx, issueID)
	if err != nil {
		return nil, err
	}

	var deps []string
	for _, dep := range depRecords {
		if withinFromOnly {
			// Check if the depended-on issue is in the source repo
			depIssue, err := s.GetIssue(ctx, dep.DependsOnID)
			if err != nil {
				return nil, err
			}
			if depIssue == nil || depIssue.SourceRepo != fromRepo {
				continue
			}
		}
		deps = append(deps, dep.DependsOnID)
	}

	return deps, nil
}

// getDownstreamDependencies returns IDs of issues that depend on the given issue.
// If withinFromOnly is true, only returns dependents whose issues are in fromRepo.
func getDownstreamDependencies(ctx context.Context, s storage.Storage, issueID, fromRepo string, withinFromOnly bool) ([]string, error) {
	// GetDependents returns full Issue objects that depend on issueID
	dependents, err := s.GetDependents(ctx, issueID)
	if err != nil {
		return nil, err
	}

	var deps []string
	for _, issue := range dependents {
		if withinFromOnly && issue.SourceRepo != fromRepo {
			continue
		}
		deps = append(deps, issue.ID)
	}

	return deps, nil
}

func countCrossRepoEdges(ctx context.Context, s storage.Storage, migrationSet []string) (dependencyStats, error) {
	if len(migrationSet) == 0 {
		return dependencyStats{}, nil
	}

	setMap := make(map[string]bool, len(migrationSet))
	for _, id := range migrationSet {
		setMap[id] = true
	}

	// Get all dependency records for migration set issues (outgoing direction)
	depsByIssue, err := s.GetDependencyRecordsForIssues(ctx, migrationSet)
	if err != nil {
		return dependencyStats{}, fmt.Errorf("failed to get dependency records: %w", err)
	}

	// Count outgoing edges: migrated issues depend on external issues
	outgoing := 0
	for _, deps := range depsByIssue {
		for _, dep := range deps {
			if !setMap[dep.DependsOnID] {
				outgoing++
			}
		}
	}

	// For incoming edges, we need to find all deps where depends_on_id is in
	// the migration set but issue_id is not. Use GetAllDependencyRecords.
	allDeps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return dependencyStats{}, fmt.Errorf("failed to get all dependency records: %w", err)
	}

	incoming := 0
	for issueID, deps := range allDeps {
		if setMap[issueID] {
			continue // Skip edges from within the migration set
		}
		for _, dep := range deps {
			if setMap[dep.DependsOnID] {
				incoming++
			}
		}
	}

	return dependencyStats{
		incomingEdges: incoming,
		outgoingEdges: outgoing,
	}, nil
}

func checkOrphanedDependencies(ctx context.Context, s storage.Storage) ([]string, error) {
	// Get all dependency records to check for orphans
	allDeps, err := s.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency records: %w", err)
	}

	// Collect all unique IDs referenced in dependencies
	referencedIDs := make(map[string]bool)
	for issueID, deps := range allDeps {
		referencedIDs[issueID] = true
		for _, dep := range deps {
			referencedIDs[dep.DependsOnID] = true
		}
	}

	// Batch-check which IDs exist
	idList := make([]string, 0, len(referencedIDs))
	for id := range referencedIDs {
		idList = append(idList, id)
	}

	existingIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{
		IDs:               idList,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}

	existingSet := make(map[string]bool, len(existingIssues))
	for _, issue := range existingIssues {
		existingSet[issue.ID] = true
	}

	// Find orphans (referenced but non-existent)
	var orphans []string
	for id := range referencedIDs {
		if !existingSet[id] {
			orphans = append(orphans, id)
		}
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

func executeMigration(ctx context.Context, s storage.Storage, migrationSet []string, to string) error {
	return s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		for _, id := range migrationSet {
			if err := tx.UpdateIssue(ctx, id, map[string]interface{}{
				"source_repo": to,
			}, actor); err != nil {
				return fmt.Errorf("failed to update issue %s: %w", id, err)
			}
		}
		return nil
	})
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
	migrateIssuesCmd.Flags().String("type", "", "Filter by issue type (bug/feature/task/epic/chore/decision)")
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
