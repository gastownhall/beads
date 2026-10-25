package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
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
		childTypes, refused := discoverChildTypes(repoPathStr)
		if len(childTypes) > 0 {
			details = append(details, fmt.Sprintf("  %s: %s", repoPathStr, strings.Join(childTypes, ", ")))
		} else {
			details = append(details, fmt.Sprintf("  %s: (no custom types)", repoPathStr))
		}
		if refused != nil {
			// Types that repo's database declares are missing from the line
			// above, and from the known types the unknown-type warning below
			// checks against, so say why.
			details = append(details, fmt.Sprintf("    database not read, so only config.yaml types are listed: %v", refused))
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
// Returns nil types if no custom types are found (not an error - child may not
// have any). refused is set when bd's admission refuses the child's workspace
// from its own contents (a format this build cannot open, a historical SQLite
// workspace; configfile.LoadDiscoveredForOpen): its database was not read, so
// the types come from its config.yaml alone.
func discoverChildTypes(repoPath string) (childTypes []string, refused error) {
	// Expand tilde
	if strings.HasPrefix(repoPath, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			repoPath = filepath.Join(home, repoPath[1:])
		}
	}

	beadsDir := ResolveBeadsDirForRepo(repoPath)

	// First try reading from database config table
	types, err := readTypesFromDB(beadsDir)
	if err == nil && len(types) > 0 {
		return types, nil
	}
	var formatErr *configfile.UnsupportedWorkspaceFormatError
	var legacyErr *configfile.LegacyWorkspaceError
	if errors.As(err, &formatErr) || errors.As(err, &legacyErr) {
		refused = err
	}

	// Fall back to reading from config.yaml
	types, err = readTypesFromYAML(beadsDir)
	if err == nil {
		return types, refused
	}

	// No custom types found
	return nil, refused
}

// AdmitAdditionalRepo, when set, refuses an additional repository's .beads
// that bd's foreign-workspace admission refuses, before readTypesFromDB opens
// its database (that admission admits some legacy Dolt layouts bd run inside
// the repository refuses; see admitForeignWorkspace):
// the open would otherwise start a Dolt server in a legacy Dolt workspace
// (ApplyCLIAutoStart) or create one beside its root. bd sets it to its own
// foreign-workspace admission (admitForeignWorkspace in package main), which
// classifies the legacy Dolt layouts this package cannot; a refusal it
// returns as a *configfile.LegacyWorkspaceError is reported beside the
// repository's types, as the discovery refusals are. Unset, only those
// discovery refusals apply.
var AdmitAdditionalRepo func(beadsDir string) error

// readTypesFromDB reads types.custom from the database config table of an
// additional repository's workspace.
//
// Command admission checked only the workspace doctor runs in, so beadsDir's
// metadata is read here as discovery reads it, without migrating a legacy
// config.json, and a workspace admission would refuse from its own contents
// (a format this build cannot open, a historical SQLite workspace) is refused
// before anything else touches it, with admission's text
// (configfile.LoadDiscoveredForOpen). configfile.Load (getDatabasePath included)
// would migrate that config.json into metadata.json, dropping graph_* keys it
// does not know, before the store constructor could refuse a graph workspace
// it marks; an ordinary legacy workspace is migrated only by that
// constructor, when there is a database to open. Metadata naming a backend
// this build does not recognize is refused before the open too, with doctor's
// own message; the constructor would also refuse it before its Load.
func readTypesFromDB(beadsDir string) ([]string, error) {
	cfg, err := configfile.LoadDiscoveredForOpen(beadsDir)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		// No metadata, or metadata that does not load: either way there is
		// no database to read here, and the caller falls back to config.yaml.
		return nil, fmt.Errorf("no config")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		return nil, fmt.Errorf("not dolt backend")
	}
	// GetBackend reads a backend this build does not recognize as Dolt.
	if !configfile.IsSupportedBackend(cfg.Backend) {
		return nil, fmt.Errorf("unrecognized backend %q", cfg.Backend)
	}

	doltPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return nil, err
	}
	if AdmitAdditionalRepo != nil {
		if err := AdmitAdditionalRepo(beadsDir); err != nil {
			return nil, err
		}
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

	// ParseTypesConfigValue returns nil for an empty or whitespace-only value.
	return issueops.ParseTypesConfigValue(typesStr), nil
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
	beadsDir := ResolveBeadsDirForRepo(repoPath)

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

	// Collect known custom types (parent custom + all child custom).
	// Built-in types — including orchestrator types like gate, molecule,
	// spike, story, and milestone — are recognized via IsBuiltIn below, so
	// this map only needs the configured custom types.
	knownTypes := make(map[string]bool)

	// Add parent's custom types
	parentTypes, err := store.GetConfig(ctx, "types.custom")
	if err == nil && parentTypes != "" {
		for _, t := range issueops.ParseTypesConfigValue(parentTypes) {
			knownTypes[t] = true
		}
	}

	// Add child types. A refused child's config.yaml types are all there is
	// to add; CheckMultiRepoTypes reports the refusal beside its types.
	for _, repoPathStr := range multiRepo.Additional {
		childTypes, _ := discoverChildTypes(repoPathStr)
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
		if !types.IssueType(issueType).IsBuiltIn() && !knownTypes[issueType] && !seen[issueType] {
			unknownTypes = append(unknownTypes, issueType)
			seen[issueType] = true
		}
	}
	// Best effort: rows.Err() ignored since partial results are acceptable for type discovery
	_ = rows.Err()

	return unknownTypes
}
