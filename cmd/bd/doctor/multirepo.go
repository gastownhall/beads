package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// multiRepoYAMLConfig represents the types section of config.yaml for YAML unmarshaling.
type multiRepoYAMLConfig struct {
	Types struct {
		Custom []string `yaml:"custom"`
	} `yaml:"types"`
}

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
		childTypes := discoverChildTypes(repoPathStr)
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

// discoverChildTypes reads custom types from a child repo's config or database.
// Returns nil if no custom types are found (not an error - child may not have any).
func discoverChildTypes(repoPath string) []string {
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
		return types
	}

	// Fall back to reading from config.yaml
	types, err = readTypesFromYAML(beadsDir)
	if err == nil {
		return types
	}

	// No custom types found
	return nil
}

// readTypesFromDB reads types.custom from the database config table
func readTypesFromDB(beadsDir string) ([]string, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("no config")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil, fmt.Errorf("not dolt backend")
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return nil, err
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	typesStr, err := store.GetConfig(ctx, "types.custom")
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
	content, err := os.ReadFile(configPath) // #nosec G304 - path is controlled
	if err != nil {
		return nil, err
	}

	var cfg multiRepoYAMLConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config.yaml: %w", err)
	}

	if len(cfg.Types.Custom) == 0 {
		return nil, nil
	}

	return cfg.Types.Custom, nil
}

// findUnknownTypesInHydratedIssues checks if any hydrated issues use types not found in any config
func findUnknownTypesInHydratedIssues(repoPath string, multiRepo *config.MultiRepoConfig) []string {
	beadsDir := filepath.Join(repoPath, ".beads")

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return nil
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer func() { _ = store.Close() }()

	// Collect all known types (core work types + parent custom + all child custom)
	// Only core work types are built-in; Gas Town types require types.custom config.
	knownTypes := map[string]bool{
		"bug": true, "feature": true, "task": true, "epic": true, "chore": true, "decision": true,
	}

	// Add parent's custom types
	parentTypes, err := store.GetConfig(ctx, "types.custom")
	if err == nil && parentTypes != "" {
		for _, t := range strings.Split(parentTypes, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				knownTypes[t] = true
			}
		}
	}

	// Add child types
	for _, repoPathStr := range multiRepo.Additional {
		childTypes := discoverChildTypes(repoPathStr)
		for _, t := range childTypes {
			knownTypes[t] = true
		}
	}

	// Find issues with types not in knownTypes
	db := store.UnderlyingDB()
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT issue_type FROM issues
		WHERE source_repo != '' AND source_repo != '.'
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
	// Best effort: rows.Err() ignored since partial results are acceptable for type discovery
	_ = rows.Err()

	return unknownTypes
}
