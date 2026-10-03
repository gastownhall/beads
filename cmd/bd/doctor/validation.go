package doctor

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openStoreDB opens the beads database and returns the underlying *sql.DB for
// raw queries. The caller must close the returned store when done.
func openStoreDB(beadsDir string) (*sql.DB, storage.DoltStorage, error) {
	ctx := context.Background()
	doltPath := getDatabasePath(beadsDir)
	cfg := doltServerConfig(beadsDir, doltPath)
	store, err := dolt.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	db := store.UnderlyingDB()
	if db == nil {
		_ = store.Close() // Best effort cleanup
		return nil, nil, fmt.Errorf("storage backend has no underlying database")
	}
	return db, store, nil
}

// CheckOrphanedDependencies detects dependencies pointing to non-existent issues.
func CheckOrphanedDependencies(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	return checkOrphanedDependenciesDB(db)
}

// checkOrphanedDependenciesDB is the core logic for CheckOrphanedDependencies.
func checkOrphanedDependenciesDB(db *sql.DB) DoctorCheck {
	// Query for orphaned dependencies.
	// Exclude external: refs — these are synthetic cross-rig tracking deps
	// injected by the JSONL exporter and intentionally reference issues not
	// present in the local database (#1593).
	//nolint:gosec // G202: doctorDependencyUnionSQL returns a fixed internal SELECT fragment.
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM (` + doctorDependencyUnionSQL() + `) d
		WHERE d.depends_on_id NOT LIKE 'external:%'
		  AND NOT EXISTS (SELECT 1 FROM issues i WHERE i.id = d.depends_on_id)
		  AND NOT EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_id)
	`
	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  StatusWarning,
			Message: "N/A (query failed)",
		}
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var issueID, dependsOnID, depType string
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err == nil {
			orphans = append(orphans, fmt.Sprintf("%s→%s", issueID, dependsOnID))
		}
	}
	if err := rows.Err(); err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  StatusWarning,
			Message: "Row iteration error",
			Detail:  err.Error(),
		}
	}

	if len(orphans) == 0 {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "No orphaned dependencies",
		}
	}

	detail := strings.Join(orphans, ", ")
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}

	return DoctorCheck{
		Name:    "Orphaned Dependencies",
		Status:  "warning",
		Message: fmt.Sprintf("%d orphaned dependency reference(s)", len(orphans)),
		Detail:  detail,
		Fix:     "Run 'bd doctor --fix' to remove orphaned dependencies",
	}
}

// CheckDuplicateIssues detects issues with identical content.
// When orchestratorMode is true, the threshold parameter defines how many duplicates
// are acceptable before warning (default 1000 for orchestrator's ephemeral wisps).
func CheckDuplicateIssues(path string, orchestratorMode bool, orchestratorThreshold int) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	return checkDuplicateIssuesDB(db, orchestratorMode, orchestratorThreshold)
}

// checkDuplicateIssuesDB is the core logic for CheckDuplicateIssues, operating
// on a *sql.DB directly. This enables fast testing with branch-per-test isolation
// instead of per-test database creation.
func checkDuplicateIssuesDB(db *sql.DB, orchestratorMode bool, orchestratorThreshold int) DoctorCheck {
	// Use SQL aggregation to find duplicates without loading all issues into memory.
	// The old approach loaded every issue via SearchIssues which was O(n) in both
	// time and memory — catastrophically slow on large databases (e.g., 23k+ issues
	// took 66 seconds over MySQL wire protocol).
	query := `
		SELECT COUNT(*) as group_count, SUM(cnt - 1) as dup_count
		FROM (
			SELECT COUNT(*) as cnt
			FROM issues
			WHERE status != 'closed'
			GROUP BY title, description, design, acceptance_criteria, status
			HAVING COUNT(*) > 1
		) dups
	`
	var groupCount, dupCount sql.NullInt64
	if err := db.QueryRow(query).Scan(&groupCount, &dupCount); err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  StatusWarning,
			Message: "N/A (unable to query issues)",
		}
	}

	duplicateGroups := int(groupCount.Int64)
	totalDuplicates := int(dupCount.Int64)

	// Apply threshold based on mode
	threshold := 0 // Default: any duplicates are warnings
	if orchestratorMode {
		threshold = orchestratorThreshold // Orchestrator: configurable threshold (default 1000)
	}

	if totalDuplicates == 0 {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "No duplicate issues",
		}
	}

	// Only warn if duplicate count exceeds threshold
	if totalDuplicates > threshold {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "warning",
			Message: fmt.Sprintf("%d duplicate issue(s) in %d group(s)", totalDuplicates, duplicateGroups),
			Detail:  "Duplicates cannot be auto-fixed",
			Fix:     "Run 'bd duplicates' to review and merge duplicates",
		}
	}

	// Under threshold - OK
	message := "No duplicate issues"
	if orchestratorMode && totalDuplicates > 0 {
		message = fmt.Sprintf("%d duplicate(s) detected (within orchestrator threshold of %d)", totalDuplicates, threshold)
	}
	return DoctorCheck{
		Name:    "Duplicate Issues",
		Status:  "ok",
		Message: message,
	}
}

// CheckTestPollution detects test issues that may have leaked into the database.
func CheckTestPollution(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Look for common test patterns in titles
	query := `
		SELECT COUNT(*) FROM issues
		WHERE (
			title LIKE 'test-%' OR
			title LIKE 'Test Issue%' OR
			title LIKE '%test issue%' OR
			id LIKE 'test-%'
		)
	`
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  StatusWarning,
			Message: "N/A (query failed)",
		}
	}

	if count == 0 {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "No test pollution detected",
		}
	}

	return DoctorCheck{
		Name:    "Test Pollution",
		Status:  "warning",
		Message: fmt.Sprintf("%d potential test issue(s) detected", count),
		Detail:  "Test issues may have leaked into production database",
		Fix:     "Run 'bd doctor --check=pollution' to review and clean test issues",
	}
}

// CheckGitConflicts detects unresolved merge conflicts.
// For Dolt backends, queries the dolt_conflicts system table (GH-2249).
// For legacy backends, scans JSONL files for git conflict markers.
func CheckGitConflicts(path string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(path)

	// Dolt backend: check for unresolved Dolt merge conflicts
	if backend == configfile.BackendDolt {
		return checkDoltConflicts(beadsDir)
	}

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Legacy: scan JSONL files for conflict markers
	matches, err := filepath.Glob(filepath.Join(beadsDir, "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "No JSONL files to check",
		}
	}

	var conflictFiles []string
	for _, fpath := range matches {
		f, err := os.Open(fpath) // #nosec G304 - path constructed from beadsDir
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		hasConflict := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, ">>>>>>>") || strings.HasPrefix(line, "=======") {
				hasConflict = true
				break
			}
		}
		_ = f.Close()
		if hasConflict {
			if rel, err := filepath.Rel(beadsDir, fpath); err == nil {
				conflictFiles = append(conflictFiles, rel)
			} else {
				conflictFiles = append(conflictFiles, filepath.Base(fpath))
			}
		}
	}

	if len(conflictFiles) == 0 {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "No conflict markers found",
		}
	}

	return DoctorCheck{
		Name:    "Git Conflicts",
		Status:  StatusError,
		Message: fmt.Sprintf("Unresolved git conflicts in %d file(s)", len(conflictFiles)),
		Detail:  strings.Join(conflictFiles, ", "),
		Fix:     "Resolve merge conflicts in .beads/ files, then commit",
	}
}

// CheckChildParentDependencies detects child→parent blocking dependencies.
// These often indicate a modeling mistake (deadlock: child waits for parent, parent waits for children).
// However, they may be intentional in some workflows, so removal requires explicit opt-in.
func CheckChildParentDependencies(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	return checkChildParentDependenciesDB(db)
}

// detailMaxBytes is the budget for a DoctorCheck Detail line: enough to show
// the shape of a finding, short enough not to bury the report.
const detailMaxBytes = 200

// truncateDetail cuts a detail line to detailMaxBytes WITHOUT splitting a
// rune. The gate inventory joins its entries with U+2192 (three bytes), so a
// plain detail[:200] lands mid-rune about two times in three and puts invalid
// UTF-8 into `bd doctor --json`, where it is no longer a display nuisance but
// a value a consumer has to decode.
func truncateDetail(detail string) string {
	if len(detail) <= detailMaxBytes {
		return detail
	}
	cut := detailMaxBytes
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut] + "..."
}

// CheckParentBlocksOwnChild reports parents that carry a blocking edge onto one
// of their own parent-child descendants — the "close gate on the epic" idiom,
// where P blocks on C1 and C2 (or on a grandchild) so P cannot close before
// them.
//
// It is INFORMATIONAL and has no fix. Before gastownhall/beads#6506 these
// edges were a trap: the parent's blocked bit cascaded to its children
// whatever set it, so the gate hid the very children it was waiting for and
// nothing in the subtree could ever become ready. The cascade now propagates
// only exogenous blockedness, so the edges are harmless and frequently
// intentional; what an operator wants from doctor is the inventory — which
// beads in this store use the idiom — not a repair.
func CheckParentBlocksOwnChild(path string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(path)

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Parent Close Gates",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryMetadata,
		}
	}
	defer func() { _ = store.Close() }()

	return checkParentBlocksOwnChildDB(db)
}

// checkParentBlocksOwnChildDB is the core logic for CheckParentBlocksOwnChild.
// The pair is symmetric-but-opposite to checkChildParentDependenciesDB above:
// that one finds a CHILD blocking on its PARENT (still a deadlock, still
// fixable); this one finds a PARENT blocking on its own DESCENDANT, and
// matches on real parent-child edges rather than on dotted-ID ancestry,
// because the close-gate idiom is wired with explicit edges between unrelated
// ids. "Descendant" is walked to the same four parent-child levels the cascade
// fix uses to decide "inside my own subtree" (issueops subtreeWalkDepth), so
// the inventory and the fix agree on which gates are gates: a parent that
// blocks on its grandchild is listed, one that blocks five levels down is not.
func checkParentBlocksOwnChildDB(db *sql.DB) DoctorCheck {
	//nolint:gosec // G202: doctorDependencyUnionSQL returns a fixed internal SELECT fragment.
	query := `
		SELECT DISTINCT b.issue_id, b.depends_on_id
		FROM (` + doctorDependencyUnionSQL() + `) b
		JOIN (
		    SELECT l1.depends_on_id AS anc, l1.issue_id AS des
		    FROM (` + doctorDependencyUnionSQL() + `) l1
		    WHERE l1.type = 'parent-child'
		  UNION
		    SELECT l1.depends_on_id, l2.issue_id
		    FROM (` + doctorDependencyUnionSQL() + `) l1
		    JOIN (` + doctorDependencyUnionSQL() + `) l2
		      ON l2.type = 'parent-child' AND l2.depends_on_id = l1.issue_id
		    WHERE l1.type = 'parent-child'
		  UNION
		    SELECT l1.depends_on_id, l3.issue_id
		    FROM (` + doctorDependencyUnionSQL() + `) l1
		    JOIN (` + doctorDependencyUnionSQL() + `) l2
		      ON l2.type = 'parent-child' AND l2.depends_on_id = l1.issue_id
		    JOIN (` + doctorDependencyUnionSQL() + `) l3
		      ON l3.type = 'parent-child' AND l3.depends_on_id = l2.issue_id
		    WHERE l1.type = 'parent-child'
		  UNION
		    SELECT l1.depends_on_id, l4.issue_id
		    FROM (` + doctorDependencyUnionSQL() + `) l1
		    JOIN (` + doctorDependencyUnionSQL() + `) l2
		      ON l2.type = 'parent-child' AND l2.depends_on_id = l1.issue_id
		    JOIN (` + doctorDependencyUnionSQL() + `) l3
		      ON l3.type = 'parent-child' AND l3.depends_on_id = l2.issue_id
		    JOIN (` + doctorDependencyUnionSQL() + `) l4
		      ON l4.type = 'parent-child' AND l4.depends_on_id = l3.issue_id
		    WHERE l1.type = 'parent-child'
		) d ON d.anc = b.issue_id AND d.des = b.depends_on_id
		WHERE b.type IN ('blocks', 'conditional-blocks')
		ORDER BY b.issue_id, b.depends_on_id
	`
	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:     "Parent Close Gates",
			Status:   StatusOK,
			Message:  "N/A (query failed)",
			Category: CategoryMetadata,
		}
	}
	defer rows.Close()

	var gates []string
	for rows.Next() {
		var issueID, dependsOnID string
		if err := rows.Scan(&issueID, &dependsOnID); err == nil {
			gates = append(gates, fmt.Sprintf("%s→%s", issueID, dependsOnID))
		}
	}
	if err := rows.Err(); err != nil {
		return DoctorCheck{
			Name:     "Parent Close Gates",
			Status:   StatusOK,
			Message:  "N/A (row iteration error)",
			Detail:   err.Error(),
			Category: CategoryMetadata,
		}
	}

	if len(gates) == 0 {
		return DoctorCheck{
			Name:     "Parent Close Gates",
			Status:   StatusOK,
			Message:  "No parent→own-descendant blocking edges",
			Category: CategoryMetadata,
		}
	}

	detail := truncateDetail(strings.Join(gates, ", "))

	return DoctorCheck{
		Name:     "Parent Close Gates",
		Status:   StatusOK,
		Message:  fmt.Sprintf("%d parent→own-descendant blocking edge(s) (informational: the children stay in bd ready)", len(gates)),
		Detail:   detail,
		Category: CategoryMetadata,
	}
}

// checkDoltConflicts queries the Dolt server for unresolved merge conflicts (GH-2249).
func checkDoltConflicts(beadsDir string) DoctorCheck {
	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "N/A (no Dolt database)",
		}
	}

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "N/A (unable to open database)",
		}
	}
	defer func() { _ = store.Close() }()

	rows, err := db.Query("SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		// Table may not exist in older Dolt versions — not an error
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "doesn't exist") {
			return DoctorCheck{
				Name:    "Git Conflicts",
				Status:  StatusOK,
				Message: "No conflicts",
			}
		}
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusWarning,
			Message: "Unable to check Dolt conflicts",
			Detail:  err.Error(),
		}
	}
	defer rows.Close()

	var tables []string
	totalConflicts := 0
	for rows.Next() {
		var tableName string
		var numConflicts int
		if err := rows.Scan(&tableName, &numConflicts); err == nil && numConflicts > 0 {
			tables = append(tables, fmt.Sprintf("%s (%d)", tableName, numConflicts))
			totalConflicts += numConflicts
		}
	}
	if err := rows.Err(); err != nil {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusWarning,
			Message: "Error reading Dolt conflicts",
			Detail:  err.Error(),
		}
	}

	if totalConflicts == 0 {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  StatusOK,
			Message: "No Dolt merge conflicts",
		}
	}

	return DoctorCheck{
		Name:    "Git Conflicts",
		Status:  StatusError,
		Message: fmt.Sprintf("Unresolved Dolt merge conflicts in %d table(s)", len(tables)),
		Detail:  strings.Join(tables, ", "),
		Fix:     "Resolve conflicts with 'bd dolt conflicts resolve' or 'dolt conflicts resolve --ours/--theirs'",
	}
}

// checkChildParentDependenciesDB is the core logic for CheckChildParentDependencies.
func checkChildParentDependenciesDB(db *sql.DB) DoctorCheck {
	// Query for child→parent BLOCKING dependencies where issue_id starts with target id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	//nolint:gosec // G202: doctorDependencyUnionSQL returns a fixed internal SELECT fragment.
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM (` + doctorDependencyUnionSQL() + `) d
		WHERE d.issue_id LIKE CONCAT(d.depends_on_id, '.%')
		  AND d.type IN ('blocks', 'conditional-blocks', 'waits-for')
	`
	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  StatusWarning,
			Message: "N/A (query failed)",
		}
	}
	defer rows.Close()

	var badDeps []string
	for rows.Next() {
		var issueID, dependsOnID string
		if err := rows.Scan(&issueID, &dependsOnID); err == nil {
			badDeps = append(badDeps, fmt.Sprintf("%s→%s", issueID, dependsOnID))
		}
	}
	if err := rows.Err(); err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  StatusWarning,
			Message: "Row iteration error",
			Detail:  err.Error(),
		}
	}

	if len(badDeps) == 0 {
		return DoctorCheck{
			Name:     "Child-Parent Dependencies",
			Status:   "ok",
			Message:  "No child→parent dependencies",
			Category: CategoryMetadata,
		}
	}

	detail := strings.Join(badDeps, ", ")
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}

	return DoctorCheck{
		Name:     "Child-Parent Dependencies",
		Status:   "warning",
		Message:  fmt.Sprintf("%d child→parent dependency detected (may cause deadlock)", len(badDeps)),
		Detail:   detail,
		Fix:      "Run 'bd doctor --fix --fix-child-parent' to remove (if unintentional)",
		Category: CategoryMetadata,
	}
}
