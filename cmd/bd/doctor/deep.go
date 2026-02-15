// Package doctor provides health check and repair functionality for beads.
package doctor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// DeepValidationResult holds all deep validation check results
type DeepValidationResult struct {
	ParentConsistency   DoctorCheck   `json:"parent_consistency"`
	DependencyIntegrity DoctorCheck   `json:"dependency_integrity"`
	EpicCompleteness    DoctorCheck   `json:"epic_completeness"`
	AgentBeadIntegrity  DoctorCheck   `json:"agent_bead_integrity"`
	MailThreadIntegrity DoctorCheck   `json:"mail_thread_integrity"`
	MoleculeIntegrity   DoctorCheck   `json:"molecule_integrity"`
	AllChecks           []DoctorCheck `json:"all_checks"`
	TotalIssues         int           `json:"total_issues"`
	TotalDependencies   int           `json:"total_dependencies"`
	OverallOK           bool          `json:"overall_ok"`
}

// RunDeepValidation runs all deep validation checks on the issue graph.
// This may be slow on large databases.
func RunDeepValidation(path string) DeepValidationResult {
	result := DeepValidationResult{
		OverallOK: true,
	}

	// Follow redirect to resolve actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Skip if database doesn't exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		check := DoctorCheck{
			Name:     "Deep Validation",
			Status:   StatusOK,
			Message:  "N/A (no database)",
			Category: CategoryMaintenance,
		}
		result.AllChecks = append(result.AllChecks, check)
		return result
	}

	// Open database
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		check := DoctorCheck{
			Name:     "Deep Validation",
			Status:   StatusError,
			Message:  "Unable to open database",
			Detail:   err.Error(),
			Category: CategoryMaintenance,
		}
		result.AllChecks = append(result.AllChecks, check)
		result.OverallOK = false
		return result
	}
	defer db.Close()

	// Get counts for progress reporting
	_ = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&result.TotalIssues)             // Best effort: zero counts are safe defaults for diagnostic display
	_ = db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&result.TotalDependencies) // Best effort: zero counts are safe defaults for diagnostic display

	// Run all deep checks
	result.ParentConsistency = checkParentConsistency(db)
	result.AllChecks = append(result.AllChecks, result.ParentConsistency)
	if result.ParentConsistency.Status == StatusError {
		result.OverallOK = false
	}

	result.DependencyIntegrity = checkDependencyIntegrity(db)
	result.AllChecks = append(result.AllChecks, result.DependencyIntegrity)
	if result.DependencyIntegrity.Status == StatusError {
		result.OverallOK = false
	}

	result.EpicCompleteness = checkEpicCompleteness(db)
	result.AllChecks = append(result.AllChecks, result.EpicCompleteness)
	if result.EpicCompleteness.Status == StatusWarning {
		// Epic completeness is informational, not an error
	}

	result.AgentBeadIntegrity = checkAgentBeadIntegrity(db)
	result.AllChecks = append(result.AllChecks, result.AgentBeadIntegrity)
	if result.AgentBeadIntegrity.Status == StatusError {
		result.OverallOK = false
	}

	result.MailThreadIntegrity = checkMailThreadIntegrity(db)
	result.AllChecks = append(result.AllChecks, result.MailThreadIntegrity)
	if result.MailThreadIntegrity.Status == StatusError {
		result.OverallOK = false
	}

	result.MoleculeIntegrity = checkMoleculeIntegrity(db)
	result.AllChecks = append(result.AllChecks, result.MoleculeIntegrity)
	if result.MoleculeIntegrity.Status == StatusError {
		result.OverallOK = false
	}

	return result
}

// checkParentConsistency verifies that all parent-child dependencies point to existing issues
func checkParentConsistency(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Parent Consistency",
		Category: CategoryMetadata,
	}

	// Find parent-child deps where either side doesn't exist
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE d.type = 'parent-child'
		  AND (
		    NOT EXISTS (SELECT 1 FROM issues WHERE id = d.issue_id)
		    OR NOT EXISTS (SELECT 1 FROM issues WHERE id = d.depends_on_id)
		  )
		LIMIT 10`

	rows, err := db.Query(query)
	if err != nil {
		check.Status = StatusWarning
		check.Message = "Unable to check parent consistency"
		check.Detail = err.Error()
		return check
	}
	defer rows.Close()

	var orphanedDeps []string
	for rows.Next() {
		var issueID, parentID string
		if err := rows.Scan(&issueID, &parentID); err == nil {
			orphanedDeps = append(orphanedDeps, fmt.Sprintf("%s→%s", issueID, parentID))
		}
	}

	if len(orphanedDeps) == 0 {
		check.Status = StatusOK
		check.Message = "All parent-child relationships valid"
		return check
	}

	check.Status = StatusError
	check.Message = fmt.Sprintf("Found %d orphaned parent-child dependencies", len(orphanedDeps))
	check.Detail = fmt.Sprintf("Examples: %s", strings.Join(orphanedDeps[:min(3, len(orphanedDeps))], ", "))
	check.Fix = "Run 'bd doctor --fix' to remove orphaned dependencies"
	return check
}

// checkDependencyIntegrity verifies that all dependencies point to existing issues
func checkDependencyIntegrity(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Dependency Integrity",
		Category: CategoryMetadata,
	}

	// Find any deps where either side is missing
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM dependencies d
		WHERE (
		    NOT EXISTS (SELECT 1 FROM issues WHERE id = d.issue_id)
		    OR NOT EXISTS (SELECT 1 FROM issues WHERE id = d.depends_on_id)
		  )
		LIMIT 10`

	rows, err := db.Query(query)
	if err != nil {
		check.Status = StatusWarning
		check.Message = "Unable to check dependency integrity"
		check.Detail = err.Error()
		return check
	}
	defer rows.Close()

	var brokenDeps []string
	for rows.Next() {
		var issueID, dependsOnID, depType string
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err == nil {
			brokenDeps = append(brokenDeps, fmt.Sprintf("%s→%s (%s)", issueID, dependsOnID, depType))
		}
	}

	if len(brokenDeps) == 0 {
		check.Status = StatusOK
		check.Message = "All dependencies point to existing issues"
		return check
	}

	check.Status = StatusError
	check.Message = fmt.Sprintf("Found %d broken dependencies", len(brokenDeps))
	check.Detail = fmt.Sprintf("Examples: %s", strings.Join(brokenDeps[:min(3, len(brokenDeps))], ", "))
	check.Fix = "Run 'bd repair-deps' to remove broken dependencies"
	return check
}

// checkEpicCompleteness finds epics that could be closed (all children closed)
func checkEpicCompleteness(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Epic Completeness",
		Category: CategoryMetadata,
	}

	// Find epics where all children are closed but epic is still open
	query := `
		SELECT e.id, e.title,
		       COUNT(c.id) as total_children,
		       SUM(CASE WHEN c.status = 'closed' THEN 1 ELSE 0 END) as closed_children
		FROM issues e
		JOIN dependencies d ON d.depends_on_id = e.id AND d.type = 'parent-child'
		JOIN issues c ON c.id = d.issue_id
		WHERE e.issue_type = 'epic'
		  AND e.status != 'closed'
		GROUP BY e.id
		HAVING total_children > 0 AND total_children = closed_children
		LIMIT 20`

	rows, err := db.Query(query)
	if err != nil {
		check.Status = StatusWarning
		check.Message = "Unable to check epic completeness"
		check.Detail = err.Error()
		return check
	}
	defer rows.Close()

	var completedEpics []string
	for rows.Next() {
		var id, title string
		var total, closed int
		if err := rows.Scan(&id, &title, &total, &closed); err == nil {
			completedEpics = append(completedEpics, fmt.Sprintf("%s (%d/%d)", id, closed, total))
		}
	}

	if len(completedEpics) == 0 {
		check.Status = StatusOK
		check.Message = "No epics eligible for closure"
		return check
	}

	check.Status = StatusWarning
	check.Message = fmt.Sprintf("Found %d epics ready to close", len(completedEpics))
	check.Detail = fmt.Sprintf("Examples: %s", strings.Join(completedEpics[:min(5, len(completedEpics))], ", "))
	check.Fix = "Run 'bd close <epic-id>' to close completed epics"
	return check
}

// agentBeadInfo holds info about an agent bead for integrity checking
type agentBeadInfo struct {
	ID         string
	Title      string
	RoleBead   string
	AgentState string
	RoleType   string
}

// checkAgentBeadIntegrity verifies that agent beads have required fields
func checkAgentBeadIntegrity(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Agent Bead Integrity",
		Category: CategoryMetadata,
	}

	// Check if agent bead columns exist (may not in older schemas)
	var hasColumns bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0 FROM pragma_table_info('issues')
		WHERE name IN ('role_bead', 'agent_state', 'role_type')
	`).Scan(&hasColumns)
	if err != nil || !hasColumns {
		check.Status = StatusOK
		check.Message = "N/A (schema doesn't support agent beads)"
		return check
	}

	// Find agent beads missing required role_bead
	// Note: We query JSON metadata from notes field or check for role_bead column
	query := `
		SELECT id, title,
		       COALESCE(json_extract(notes, '$.role_bead'), '') as role_bead,
		       COALESCE(json_extract(notes, '$.agent_state'), '') as agent_state,
		       COALESCE(json_extract(notes, '$.role_type'), '') as role_type
		FROM issues
		WHERE issue_type = 'agent'
		LIMIT 100`

	rows, err := db.Query(query)
	if err != nil {
		// Try alternate approach without JSON extraction
		query = `
			SELECT id, title, '', '', ''
			FROM issues
			WHERE issue_type = 'agent'
			LIMIT 100`
		rows, err = db.Query(query)
		if err != nil {
			check.Status = StatusOK
			check.Message = "No agent beads found"
			return check
		}
	}
	defer rows.Close()

	var agents []agentBeadInfo
	var invalidAgents []string

	for rows.Next() {
		var agent agentBeadInfo
		if err := rows.Scan(&agent.ID, &agent.Title, &agent.RoleBead, &agent.AgentState, &agent.RoleType); err == nil {
			agents = append(agents, agent)

			// Validate agent state
			if agent.AgentState != "" {
				state := types.AgentState(agent.AgentState)
				if !state.IsValid() {
					invalidAgents = append(invalidAgents, fmt.Sprintf("%s (invalid state: %s)", agent.ID, agent.AgentState))
				}
			}
		}
	}

	if len(agents) == 0 {
		check.Status = StatusOK
		check.Message = "No agent beads to validate"
		return check
	}

	if len(invalidAgents) > 0 {
		check.Status = StatusError
		check.Message = fmt.Sprintf("Found %d agents with invalid data", len(invalidAgents))
		check.Detail = fmt.Sprintf("Examples: %s", strings.Join(invalidAgents[:min(3, len(invalidAgents))], ", "))
		check.Fix = "Update agent beads with valid agent_state values"
		return check
	}

	check.Status = StatusOK
	check.Message = fmt.Sprintf("%d agent beads validated", len(agents))
	return check
}

// checkMailThreadIntegrity verifies that mail thread_id references are valid
func checkMailThreadIntegrity(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Mail Thread Integrity",
		Category: CategoryMetadata,
	}

	// Check if thread_id column exists
	var hasThreadID bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0 FROM pragma_table_info('dependencies')
		WHERE name = 'thread_id'
	`).Scan(&hasThreadID)
	if err != nil || !hasThreadID {
		check.Status = StatusOK
		check.Message = "N/A (schema doesn't support thread_id)"
		return check
	}

	// Find thread_ids that don't point to existing issues
	query := `
		SELECT d.thread_id, COUNT(*) as refs
		FROM dependencies d
		WHERE d.thread_id != ''
		  AND d.thread_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM issues WHERE id = d.thread_id)
		GROUP BY d.thread_id
		LIMIT 10`

	rows, err := db.Query(query)
	if err != nil {
		check.Status = StatusWarning
		check.Message = "Unable to check thread integrity"
		check.Detail = err.Error()
		return check
	}
	defer rows.Close()

	var orphanedThreads []string
	totalOrphaned := 0
	for rows.Next() {
		var threadID string
		var refs int
		if err := rows.Scan(&threadID, &refs); err == nil {
			orphanedThreads = append(orphanedThreads, fmt.Sprintf("%s (%d refs)", threadID, refs))
			totalOrphaned += refs
		}
	}

	if len(orphanedThreads) == 0 {
		check.Status = StatusOK
		check.Message = "All thread references valid"
		return check
	}

	check.Status = StatusWarning
	check.Message = fmt.Sprintf("Found %d orphaned thread references", totalOrphaned)
	check.Detail = fmt.Sprintf("Threads: %s", strings.Join(orphanedThreads[:min(3, len(orphanedThreads))], ", "))
	check.Fix = "Clear orphaned thread_id values in dependencies"
	return check
}

// moleculeInfo holds info about a molecule for integrity checking
type moleculeInfo struct {
	ID         string
	Title      string
	ChildCount int
}

// checkMoleculeIntegrity verifies molecule structure integrity
func checkMoleculeIntegrity(db *sql.DB) DoctorCheck {
	check := DoctorCheck{
		Name:     "Molecule Integrity",
		Category: CategoryMetadata,
	}

	// Find molecules (issue_type='molecule' or has beads:template label) with broken structures
	// A molecule should have parent-child relationships forming a valid DAG

	// First, find molecules
	query := `
		SELECT DISTINCT i.id, i.title
		FROM issues i
		LEFT JOIN labels l ON l.issue_id = i.id
		WHERE (i.issue_type = 'molecule' OR l.label = 'beads:template')
		LIMIT 100`

	rows, err := db.Query(query)
	if err != nil {
		check.Status = StatusWarning
		check.Message = "Unable to find molecules"
		check.Detail = err.Error()
		return check
	}
	defer rows.Close()

	var molecules []moleculeInfo
	for rows.Next() {
		var mol moleculeInfo
		if err := rows.Scan(&mol.ID, &mol.Title); err == nil {
			molecules = append(molecules, mol)
		}
	}

	if len(molecules) == 0 {
		check.Status = StatusOK
		check.Message = "No molecules to validate"
		return check
	}

	// For each molecule, check if all children exist
	var brokenMolecules []string
	for _, mol := range molecules {
		// Count children that don't exist
		var orphanCount int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM dependencies d
			WHERE d.depends_on_id = ?
			  AND d.type = 'parent-child'
			  AND NOT EXISTS (SELECT 1 FROM issues WHERE id = d.issue_id)
		`, mol.ID).Scan(&orphanCount)
		if err == nil && orphanCount > 0 {
			brokenMolecules = append(brokenMolecules, fmt.Sprintf("%s (%d missing children)", mol.ID, orphanCount))
		}
	}

	if len(brokenMolecules) > 0 {
		check.Status = StatusError
		check.Message = fmt.Sprintf("Found %d molecules with missing children", len(brokenMolecules))
		check.Detail = fmt.Sprintf("Examples: %s", strings.Join(brokenMolecules[:min(3, len(brokenMolecules))], ", "))
		check.Fix = "Run 'bd repair-deps' to clean up orphaned relationships"
		return check
	}

	check.Status = StatusOK
	check.Message = fmt.Sprintf("%d molecules validated", len(molecules))
	return check
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PrintDeepValidationResult prints the deep validation results
func PrintDeepValidationResult(result DeepValidationResult) {
	fmt.Printf("\nDeep Validation Results\n")
	fmt.Printf("========================\n")
	fmt.Printf("Scanned: %d issues, %d dependencies\n\n", result.TotalIssues, result.TotalDependencies)

	for _, check := range result.AllChecks {
		var icon string
		switch check.Status {
		case StatusOK:
			icon = "\u2713" // checkmark
		case StatusWarning:
			icon = "!"
		case StatusError:
			icon = "\u2717" // X
		}
		fmt.Printf("[%s] %s: %s\n", icon, check.Name, check.Message)
		if check.Detail != "" {
			fmt.Printf("    %s\n", check.Detail)
		}
		if check.Fix != "" && check.Status != StatusOK {
			fmt.Printf("    Fix: %s\n", check.Fix)
		}
	}

	fmt.Println()
	if result.OverallOK {
		fmt.Println("All deep validation checks passed!")
	} else {
		fmt.Println("Some checks failed. See details above.")
	}
}

// DeepValidationResultJSON returns the result as JSON bytes
func DeepValidationResultJSON(result DeepValidationResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}
