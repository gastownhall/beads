package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// CheckMultiRepoTypes discovers and reports custom types used by child repos in multi-repo setups.
// This is informational - the federation trust model means we don't require parent config to
// list child types, but it's useful to know what types each child uses.
func CheckMultiRepoTypes(repoPath string) DoctorCheck {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil || len(multiRepo.Additional) == 0 {
		return DoctorCheck{
			Name:     "Multi-Repo Types",
			Status:   StatusOK,
			Message:  "N/A (single-repo mode)",
			Category: CategoryData,
		}
	}

	var details []string
	var warnings []string

	// Discover types from each child repo
	for _, repoPathStr := range multiRepo.Additional {
		childTypes, err := discoverChildTypes(repoPathStr)
		if err != nil {
			details = append(details, fmt.Sprintf("  %s: unable to read (%v)", repoPathStr, err))
			continue
		}

		if len(childTypes) > 0 {
			details = append(details, fmt.Sprintf("  %s: %s", repoPathStr, strings.Join(childTypes, ", ")))
		} else {
			details = append(details, fmt.Sprintf("  %s: (no custom types)", repoPathStr))
		}
	}

	// Check for hydrated issues using types not found anywhere
	unknownTypes := findUnknownTypesInHydratedIssues(repoPath, multiRepo)
	if len(unknownTypes) > 0 {
		warnings = append(warnings, fmt.Sprintf("Issues with unknown types: %s", strings.Join(unknownTypes, ", ")))
	}

	status := StatusOK
	message := fmt.Sprintf("Discovered types from %d child repo(s)", len(multiRepo.Additional))

	if len(warnings) > 0 {
		status = StatusWarning
		message = fmt.Sprintf("Found %d type warning(s)", len(warnings))
		details = append(details, "")
		details = append(details, "Warnings:")
		details = append(details, warnings...)
	}

	return DoctorCheck{
		Name:     "Multi-Repo Types",
		Status:   status,
		Message:  message,
		Detail:   strings.Join(details, "\n"),
		Category: CategoryData,
	}
}

// discoverChildTypes reads custom types from a child repo's config or database
func discoverChildTypes(repoPath string) ([]string, error) {
	// Expand tilde
	if strings.HasPrefix(repoPath, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			repoPath = filepath.Join(home, repoPath[1:])
		}
	}

	beadsDir := filepath.Join(repoPath, ".beads")

	// First try reading from database config table
	types, err := readTypesFromDB(beadsDir)
	if err == nil && len(types) > 0 {
		return types, nil
	}

	// Fall back to reading from config.yaml
	types, err = readTypesFromYAML(beadsDir)
	if err == nil {
		return types, nil
	}

	// No custom types found (not an error - child may not have any)
	return nil, nil
}

// readTypesFromDB reads types.custom from the database config table
func readTypesFromDB(beadsDir string) ([]string, error) {
	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, err
	}

	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var typesStr string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'types.custom'").Scan(&typesStr)
	if err != nil {
		return nil, err
	}

	if typesStr == "" {
		return nil, nil
	}

	// Parse comma-separated list
	var types []string
	for _, t := range strings.Split(typesStr, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			types = append(types, t)
		}
	}

	return types, nil
}

// readTypesFromYAML reads types.custom from config.yaml
func readTypesFromYAML(beadsDir string) ([]string, error) {
	configPath := filepath.Join(beadsDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, err
	}

	// Use viper to read the config
	// For simplicity, we'll parse it manually to avoid viper state issues
	content, err := os.ReadFile(configPath) // #nosec G304 - path is controlled
	if err != nil {
		return nil, err
	}

	// Simple YAML parsing for types.custom
	// Looking for "types:" section with "custom:" key
	lines := strings.Split(string(content), "\n")
	inTypes := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "types:") {
			inTypes = true
			continue
		}
		if inTypes && strings.HasPrefix(trimmed, "custom:") {
			// Parse the value
			value := strings.TrimPrefix(trimmed, "custom:")
			value = strings.TrimSpace(value)
			// Handle array format [a, b, c] or string format "a,b,c"
			value = strings.Trim(value, "[]\"'")
			if value == "" {
				return nil, nil
			}
			var types []string
			for _, t := range strings.Split(value, ",") {
				t = strings.TrimSpace(t)
				t = strings.Trim(t, "\"'")
				if t != "" {
					types = append(types, t)
				}
			}
			return types, nil
		}
		// Exit types section if we hit another top-level key
		if inTypes && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break
		}
	}

	return nil, nil
}

// findUnknownTypesInHydratedIssues checks if any hydrated issues use types not found in any config
func findUnknownTypesInHydratedIssues(repoPath string, multiRepo *config.MultiRepoConfig) []string {
	beadsDir := filepath.Join(repoPath, ".beads")

	// Get database path
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	}

	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return nil
	}
	defer db.Close()

	// Collect all known types (built-in + parent custom + all child custom)
	knownTypes := map[string]bool{
		"bug": true, "feature": true, "task": true, "epic": true, "chore": true,
		"message": true, "merge-request": true, "molecule": true, "gate": true, "event": true,
	}

	// Add parent's custom types
	var parentTypes string
	if err := db.QueryRow("SELECT value FROM config WHERE key = 'types.custom'").Scan(&parentTypes); err == nil {
		for _, t := range strings.Split(parentTypes, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				knownTypes[t] = true
			}
		}
	}

	// Add child types
	for _, repoPathStr := range multiRepo.Additional {
		childTypes, err := discoverChildTypes(repoPathStr)
		if err == nil {
			for _, t := range childTypes {
				knownTypes[t] = true
			}
		}
	}

	// Find issues with types not in knownTypes
	rows, err := db.Query(`
		SELECT DISTINCT issue_type FROM issues
		WHERE status != 'tombstone' AND source_repo != '' AND source_repo != '.'
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var unknownTypes []string
	seen := make(map[string]bool)
	for rows.Next() {
		var issueType string
		if err := rows.Scan(&issueType); err != nil {
			continue
		}
		if !knownTypes[issueType] && !seen[issueType] {
			unknownTypes = append(unknownTypes, issueType)
			seen[issueType] = true
		}
	}

	return unknownTypes
}
