// Package sqlite provides external dependency resolution for cross-project blocking.
//
// External dependencies use the format: external:<project>:<capability>
// They are satisfied when:
//   - The project is configured in external_projects config
//   - The project's beads database has a closed issue with provides:<capability> label
//
// Resolution happens lazily at query time (GetReadyWork) rather than during
// cache rebuild, to keep cache rebuilds fast and avoid holding multiple DB connections.
package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// ExternalDepStatus represents whether an external dependency is satisfied
type ExternalDepStatus struct {
	Ref        string // The full external reference (external:project:capability)
	Project    string // Parsed project name
	Capability string // Parsed capability name
	Satisfied  bool   // Whether the dependency is satisfied
	Reason     string // Human-readable reason if not satisfied
}

// CheckExternalDep checks if a single external dependency is satisfied.
// Returns status information about the dependency.
func CheckExternalDep(ctx context.Context, ref string) *ExternalDepStatus {
	status := &ExternalDepStatus{
		Ref:       ref,
		Satisfied: false,
	}

	// Parse external:project:capability
	if !strings.HasPrefix(ref, "external:") {
		status.Reason = "not an external reference"
		return status
	}

	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		status.Reason = "invalid format (expected external:project:capability)"
		return status
	}

	status.Project = parts[1]
	status.Capability = parts[2]

	if status.Project == "" || status.Capability == "" {
		status.Reason = "missing project or capability"
		return status
	}

	// Look up project path from config
	projectPath := config.ResolveExternalProjectPath(status.Project)
	if projectPath == "" {
		status.Reason = "project not configured in external_projects"
		return status
	}

	// Find the beads database in the project
	beadsDir := filepath.Join(projectPath, ".beads")
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		status.Reason = "project has no beads database"
		return status
	}

	dbPath := cfg.DatabasePath(beadsDir)

	// Verify database file exists
	if _, err := os.Stat(dbPath); err != nil {
		status.Reason = "database file not found: " + dbPath
		return status
	}

	// Open the external database
	// Use regular mode to ensure we can read from WAL-mode databases
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		status.Reason = "cannot open project database: " + err.Error()
		return status
	}
	defer func() { _ = db.Close() }()

	// Verify we can ping the database
	if err := db.Ping(); err != nil {
		status.Reason = "cannot connect to project database: " + err.Error()
		return status
	}

	// Check for a closed issue with provides:<capability> label
	providesLabel := "provides:" + status.Capability
	var count int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM issues i
		JOIN labels l ON i.id = l.issue_id
		WHERE i.status = 'closed'
		  AND l.label = ?
	`, providesLabel).Scan(&count)

	if err != nil {
		status.Reason = "database query failed: " + err.Error()
		return status
	}

	if count == 0 {
		status.Reason = "capability not shipped (no closed issue with provides:" + status.Capability + " label)"
		return status
	}

	status.Satisfied = true
	status.Reason = "capability shipped"
	return status
}

// CheckExternalDeps checks multiple external dependencies with batching optimization.
// Groups refs by project and opens each external DB only once, checking all
// capabilities for that project in a single query. This avoids O(N) DB opens
// when multiple issues depend on the same external project.
// Returns a map of ref -> status.
func CheckExternalDeps(ctx context.Context, refs []string) map[string]*ExternalDepStatus {
	results := make(map[string]*ExternalDepStatus)

	// Parse and group refs by project
	// Key: project name, Value: map of capability -> list of original refs
	// (multiple refs might have same project:capability, we dedupe)
	projectCaps := make(map[string]map[string][]string)

	for _, ref := range refs {
		parsed := parseExternalRef(ref)
		if parsed == nil {
			results[ref] = &ExternalDepStatus{
				Ref:       ref,
				Satisfied: false,
				Reason:    "invalid external reference format",
			}
			continue
		}

		if projectCaps[parsed.project] == nil {
			projectCaps[parsed.project] = make(map[string][]string)
		}
		projectCaps[parsed.project][parsed.capability] = append(
			projectCaps[parsed.project][parsed.capability], ref)
	}

	// Check each project's capabilities in batch
	for project, caps := range projectCaps {
		capList := make([]string, 0, len(caps))
		for cap := range caps {
			capList = append(capList, cap)
		}

		// Check all capabilities for this project in one DB open
		satisfied := checkProjectCapabilities(ctx, project, capList)

		// Map results back to original refs
		for cap, refList := range caps {
			isSatisfied := satisfied[cap]
			reason := "capability shipped"
			if !isSatisfied {
				reason = "capability not shipped (no closed issue with provides:" + cap + " label)"
			}

			for _, ref := range refList {
				results[ref] = &ExternalDepStatus{
					Ref:        ref,
					Project:    project,
					Capability: cap,
					Satisfied:  isSatisfied,
					Reason:     reason,
				}
			}
		}
	}

	return results
}

// parsedRef holds parsed components of an external reference
type parsedRef struct {
	project    string
	capability string
}

// parseExternalRef parses "external:project:capability" into components.
// Returns nil if the format is invalid.
func parseExternalRef(ref string) *parsedRef {
	if !strings.HasPrefix(ref, "external:") {
		return nil
	}
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil
	}
	return &parsedRef{project: parts[1], capability: parts[2]}
}

// checkProjectCapabilities opens a project's beads DB once and checks
// multiple capabilities in a single query. Returns map of capability -> satisfied.
func checkProjectCapabilities(ctx context.Context, project string, capabilities []string) map[string]bool {
	result := make(map[string]bool)
	for _, cap := range capabilities {
		result[cap] = false // default to unsatisfied
	}

	if len(capabilities) == 0 {
		return result
	}

	// Look up project path from config
	projectPath := config.ResolveExternalProjectPath(project)
	if projectPath == "" {
		return result // all unsatisfied - project not configured
	}

	// Find the beads database in the project
	beadsDir := filepath.Join(projectPath, ".beads")
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return result // all unsatisfied - no beads database
	}

	dbPath := cfg.DatabasePath(beadsDir)

	// Verify database file exists
	if _, err := os.Stat(dbPath); err != nil {
		return result // all unsatisfied - database not found
	}

	// Open the external database once for all capability checks
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return result // all unsatisfied - cannot open
	}
	defer func() { _ = db.Close() }()

	if err := db.Ping(); err != nil {
		return result // all unsatisfied - cannot connect
	}

	// Build query to check all capabilities at once
	// SELECT label FROM labels WHERE label IN ('provides:cap1', 'provides:cap2', ...)
	// AND EXISTS closed issue with that label
	placeholders := make([]string, len(capabilities))
	args := make([]interface{}, len(capabilities))
	for i, cap := range capabilities {
		placeholders[i] = "?"
		args[i] = "provides:" + cap
	}

	// Query returns which provides: labels exist on closed issues
	// #nosec G202 -- placeholders are generated as "?" markers, not user input
	query := `
		SELECT DISTINCT l.label FROM labels l
		JOIN issues i ON l.issue_id = i.id
		WHERE i.status = 'closed'
		  AND l.label IN (` + strings.Join(placeholders, ",") + `)
	`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return result // all unsatisfied - query failed
	}
	defer func() { _ = rows.Close() }()

	// Mark satisfied capabilities
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			continue
		}
		// Extract capability from "provides:capability"
		if strings.HasPrefix(label, "provides:") {
			cap := strings.TrimPrefix(label, "provides:")
			result[cap] = true
		}
	}

	return result
}

// GetUnsatisfiedExternalDeps returns external dependencies that are not satisfied.
func GetUnsatisfiedExternalDeps(ctx context.Context, refs []string) []string {
	var unsatisfied []string
	for _, ref := range refs {
		status := CheckExternalDep(ctx, ref)
		if !status.Satisfied {
			unsatisfied = append(unsatisfied, ref)
		}
	}
	return unsatisfied
}
