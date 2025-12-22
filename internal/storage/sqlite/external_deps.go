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

// CheckExternalDeps checks multiple external dependencies.
// Returns a map of ref -> status.
func CheckExternalDeps(ctx context.Context, refs []string) map[string]*ExternalDepStatus {
	results := make(map[string]*ExternalDepStatus)
	for _, ref := range refs {
		results[ref] = CheckExternalDep(ctx, ref)
	}
	return results
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
