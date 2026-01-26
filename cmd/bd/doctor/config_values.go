package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/viper"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// validRoutingModes are the allowed values for routing.mode
var validRoutingModes = map[string]bool{
	"auto":        true,
	"maintainer":  true,
	"contributor": true,
}

// validBranchNameRegex validates git branch names
// Git branch names can't contain: space, ~, ^, :, \, ?, *, [
// Can't start with -, can't end with ., can't contain ..
var validBranchNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)

// validActorRegex validates actor names (alphanumeric with dashes, underscores, dots, and @ for emails)
var validActorRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._@-]*$`)

// validCustomStatusRegex validates custom status names (alphanumeric with underscores)
var validCustomStatusRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// CheckConfigValues validates configuration values in config.yaml and metadata.json
// Returns issues found, or OK if all values are valid
func CheckConfigValues(repoPath string) DoctorCheck {
	var issues []string

	// Check config.yaml values
	yamlIssues := checkYAMLConfigValues(repoPath)
	issues = append(issues, yamlIssues...)

	// Check metadata.json values
	metadataIssues := checkMetadataConfigValues(repoPath)
	issues = append(issues, metadataIssues...)

	// Check database config values (status.custom, etc.)
	dbIssues := checkDatabaseConfigValues(repoPath)
	issues = append(issues, dbIssues...)

	if len(issues) == 0 {
		return DoctorCheck{
			Name:    "Config Values",
			Status:  "ok",
			Message: "All configuration values are valid",
		}
	}

	return DoctorCheck{
		Name:    "Config Values",
		Status:  "warning",
		Message: fmt.Sprintf("Found %d configuration issue(s)", len(issues)),
		Detail:  strings.Join(issues, "\n"),
		Fix:     "Edit config files to fix invalid values. Run 'bd config' to view current settings.",
	}
}

// findConfigPath locates config.yaml in standard locations.
func findConfigPath(repoPath string) string {
	configPath := filepath.Join(repoPath, ".beads", "config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		userConfigPath := filepath.Join(configDir, "bd", "config.yaml")
		if _, err := os.Stat(userConfigPath); err == nil {
			return userConfigPath
		}
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		homeConfigPath := filepath.Join(homeDir, ".beads", "config.yaml")
		if _, err := os.Stat(homeConfigPath); err == nil {
			return homeConfigPath
		}
	}
	return ""
}

// validateDurationConfig validates a duration config value.
func validateDurationConfig(v *viper.Viper, key string, minDuration time.Duration) []string {
	if !v.IsSet(key) {
		return nil
	}
	durStr := v.GetString(key)
	if durStr == "" {
		return nil
	}
	d, err := time.ParseDuration(durStr)
	if err != nil {
		return []string{fmt.Sprintf("%s: invalid duration %q (expected format like \"30s\", \"1m\", \"500ms\")", key, durStr)}
	}
	if minDuration > 0 && d > 0 && d < minDuration {
		return []string{fmt.Sprintf("%s: %q is too low (minimum %s)", key, durStr, minDuration)}
	}
	return nil
}

// validateBooleanConfigs validates boolean config values.
func validateBooleanConfigs(v *viper.Viper, keys []string) []string {
	var issues []string
	for _, key := range keys {
		if v.IsSet(key) {
			strVal := v.GetString(key)
			if strVal != "" && !isValidBoolString(strVal) {
				issues = append(issues, fmt.Sprintf("%s: %q is not a valid boolean value (expected true/false, yes/no, 1/0, on/off)", key, strVal))
			}
		}
	}
	return issues
}

// validateRoutingPaths validates routing path config values.
func validateRoutingPaths(v *viper.Viper) []string {
	var issues []string
	for _, key := range []string{"routing.default", "routing.maintainer", "routing.contributor"} {
		if v.IsSet(key) {
			path := v.GetString(key)
			if path != "" && path != "." {
				expandedPath := expandPath(path)
				if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
					issues = append(issues, fmt.Sprintf("%s: path %q does not exist", key, path))
				}
			}
		}
	}
	return issues
}

// validateRepoPaths validates repos.primary and repos.additional paths.
func validateRepoPaths(v *viper.Viper) []string {
	var issues []string
	if v.IsSet("repos.primary") {
		primary := v.GetString("repos.primary")
		if primary != "" {
			expandedPath := expandPath(primary)
			if info, err := os.Stat(expandedPath); err == nil {
				if !info.IsDir() {
					issues = append(issues, fmt.Sprintf("repos.primary: %q is not a directory", primary))
				}
			} else if !os.IsNotExist(err) {
				issues = append(issues, fmt.Sprintf("repos.primary: cannot access %q: %v", primary, err))
			}
		}
	}
	if v.IsSet("repos.additional") {
		for _, path := range v.GetStringSlice("repos.additional") {
			if path != "" {
				expandedPath := expandPath(path)
				if info, err := os.Stat(expandedPath); err == nil && !info.IsDir() {
					issues = append(issues, fmt.Sprintf("repos.additional: %q is not a directory", path))
				}
			}
		}
	}
	return issues
}

// checkYAMLConfigValues validates values in config.yaml
func checkYAMLConfigValues(repoPath string) []string {
	var issues []string

	configPath := findConfigPath(repoPath)
	if configPath == "" {
		return issues
	}

	v := viper.New()
	v.SetConfigType("yaml")
	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		issues = append(issues, fmt.Sprintf("config.yaml: failed to parse: %v", err))
		return issues
	}

	// Validate duration configs
	issues = append(issues, validateDurationConfig(v, "flush-debounce", 0)...)
	issues = append(issues, validateDurationConfig(v, "remote-sync-interval", 5*time.Second)...)

	// Validate issue-prefix (should be alphanumeric with dashes/underscores, reasonably short)
	if v.IsSet("issue-prefix") {
		prefix := v.GetString("issue-prefix")
		if prefix != "" {
			if len(prefix) > 20 {
				issues = append(issues, fmt.Sprintf("issue-prefix: %q is too long (max 20 characters)", prefix))
			}
			if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`).MatchString(prefix) {
				issues = append(issues, fmt.Sprintf("issue-prefix: %q is invalid (must start with letter, contain only letters, numbers, dashes, underscores)", prefix))
			}
		}
	}

	// Validate routing.mode (should be "auto", "maintainer", or "contributor")
	if v.IsSet("routing.mode") {
		mode := v.GetString("routing.mode")
		if mode != "" && !validRoutingModes[mode] {
			validModes := make([]string, 0, len(validRoutingModes))
			for m := range validRoutingModes {
				validModes = append(validModes, m)
			}
			issues = append(issues, fmt.Sprintf("routing.mode: %q is invalid (valid values: %s)", mode, strings.Join(validModes, ", ")))
		}

		// Validate routing + hydration consistency (bd-fix-routing)
		// When routing.mode=auto with routing targets, those targets should be in repos.additional
		// so routed issues are visible in bd list via multi-repo hydration
		if mode == "auto" {
			contributorRepo := v.GetString("routing.contributor")
			maintainerRepo := v.GetString("routing.maintainer")

			// Check if routing targets are configured (exclude "." which means current repo)
			hasRoutingTargets := (contributorRepo != "" && contributorRepo != ".") || (maintainerRepo != "" && maintainerRepo != ".")

			if hasRoutingTargets {
				// Check if hydration is configured
				additional := v.GetStringSlice("repos.additional")
				hasHydration := len(additional) > 0

				if !hasHydration {
					issues = append(issues,
						"routing.mode=auto with routing targets but repos.additional not configured. "+
							"Issues created via routing will not be visible in bd list. "+
							"Run 'bd repo add <routing-target>' to enable hydration.")
				} else {
					// Check if routing targets are in hydration list
					additionalSet := make(map[string]bool)
					for _, path := range additional {
						additionalSet[expandPath(path)] = true
					}

					if contributorRepo != "" {
						expandedContributor := expandPath(contributorRepo)
						if !additionalSet[expandedContributor] {
							issues = append(issues, fmt.Sprintf(
								"routing.contributor=%q is not in repos.additional. "+
									"Run 'bd repo add %s' to make routed issues visible.",
								contributorRepo, contributorRepo))
						}
					}

					if maintainerRepo != "" && maintainerRepo != "." {
						expandedMaintainer := expandPath(maintainerRepo)
						if !additionalSet[expandedMaintainer] {
							issues = append(issues, fmt.Sprintf(
								"routing.maintainer=%q is not in repos.additional. "+
									"Run 'bd repo add %s' to make routed issues visible.",
								maintainerRepo, maintainerRepo))
						}
					}
				}
			}
		}
	}

	// Validate sync-branch (should be a valid git branch name if set)
	if v.IsSet("sync-branch") {
		branch := v.GetString("sync-branch")
		if branch != "" {
			if !isValidBranchName(branch) {
				issues = append(issues, fmt.Sprintf("sync-branch: %q is not a valid git branch name", branch))
			}
		}
	}

	// Validate routing paths exist if set
	issues = append(issues, validateRoutingPaths(v)...)

	// Validate actor (should be alphanumeric with common special chars if set)
	if v.IsSet("actor") {
		actor := v.GetString("actor")
		if actor != "" && !validActorRegex.MatchString(actor) {
			issues = append(issues, fmt.Sprintf("actor: %q is invalid (must start with letter/number, contain only letters, numbers, dashes, underscores, dots, or @)", actor))
		}
	}

	// Validate db path (should be a valid file path if set)
	if v.IsSet("db") {
		dbPath := v.GetString("db")
		if dbPath != "" {
			// Check for invalid path characters (null bytes, etc.)
			if strings.ContainsAny(dbPath, "\x00") {
				issues = append(issues, fmt.Sprintf("db: %q contains invalid characters", dbPath))
			}
			// Check if it has a valid database extension
			if !strings.HasSuffix(dbPath, ".db") && !strings.HasSuffix(dbPath, ".sqlite") && !strings.HasSuffix(dbPath, ".sqlite3") {
				issues = append(issues, fmt.Sprintf("db: %q has unusual extension (expected .db, .sqlite, or .sqlite3)", dbPath))
			}
		}
	}

	// Validate boolean config values
	boolKeys := []string{"json", "no-daemon", "no-auto-flush", "no-auto-import", "no-db", "auto-start-daemon", "sync.require_confirmation_on_mass_delete"}
	issues = append(issues, validateBooleanConfigs(v, boolKeys)...)

	// Validate repos paths
	issues = append(issues, validateRepoPaths(v)...)

	return issues
}

// isValidBoolString checks if a string represents a valid boolean value
func isValidBoolString(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch lower {
	case "true", "false", "yes", "no", "1", "0", "on", "off", "t", "f", "y", "n":
		return true
	}
	// Also check if it parses as a bool
	_, err := strconv.ParseBool(s)
	return err == nil
}

// expandPath expands ~ to home directory and resolves the path
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	return path
}

// checkMetadataConfigValues validates values in metadata.json
func checkMetadataConfigValues(repoPath string) []string {
	var issues []string

	beadsDir := filepath.Join(repoPath, ".beads")
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		issues = append(issues, fmt.Sprintf("metadata.json: failed to load: %v", err))
		return issues
	}

	if cfg == nil {
		// No metadata.json, that's OK
		return issues
	}

	// Validate database filename
	if cfg.Database != "" {
		if strings.Contains(cfg.Database, string(os.PathSeparator)) || strings.Contains(cfg.Database, "/") {
			issues = append(issues, fmt.Sprintf("metadata.json database: %q should be a filename, not a path", cfg.Database))
		}
		backend := cfg.GetBackend()
		if backend == configfile.BackendSQLite {
			if !strings.HasSuffix(cfg.Database, ".db") && !strings.HasSuffix(cfg.Database, ".sqlite") && !strings.HasSuffix(cfg.Database, ".sqlite3") {
				issues = append(issues, fmt.Sprintf("metadata.json database: %q has unusual extension (expected .db, .sqlite, or .sqlite3)", cfg.Database))
			}
		} else if backend == configfile.BackendDolt {
			// Dolt is directory-backed; `database` should point to a directory (typically "dolt").
			if strings.HasSuffix(cfg.Database, ".db") || strings.HasSuffix(cfg.Database, ".sqlite") || strings.HasSuffix(cfg.Database, ".sqlite3") {
				issues = append(issues, fmt.Sprintf("metadata.json database: %q looks like a SQLite file, but backend is dolt (expected a directory like %q)", cfg.Database, "dolt"))
			}
			if cfg.Database == beads.CanonicalDatabaseName {
				issues = append(issues, fmt.Sprintf("metadata.json database: %q is misleading for dolt backend (expected %q)", cfg.Database, "dolt"))
			}
		}
	}

	// Validate jsonl_export filename
	if cfg.JSONLExport != "" {
		switch cfg.JSONLExport {
		case "deletions.jsonl", "interactions.jsonl", "molecules.jsonl":
			issues = append(issues, fmt.Sprintf("metadata.json jsonl_export: %q is a system file and should not be configured as a JSONL export (expected issues.jsonl)", cfg.JSONLExport))
		}
		if strings.Contains(cfg.JSONLExport, string(os.PathSeparator)) || strings.Contains(cfg.JSONLExport, "/") {
			issues = append(issues, fmt.Sprintf("metadata.json jsonl_export: %q should be a filename, not a path", cfg.JSONLExport))
		}
		if !strings.HasSuffix(cfg.JSONLExport, ".jsonl") {
			issues = append(issues, fmt.Sprintf("metadata.json jsonl_export: %q should have .jsonl extension", cfg.JSONLExport))
		}
	}

	// Validate deletions_retention_days
	if cfg.DeletionsRetentionDays < 0 {
		issues = append(issues, fmt.Sprintf("metadata.json deletions_retention_days: %d is invalid (must be >= 0)", cfg.DeletionsRetentionDays))
	}

	return issues
}

// checkDatabaseConfigValues validates configuration values stored in the database
func checkDatabaseConfigValues(repoPath string) []string {
	var issues []string

	beadsDir := filepath.Join(repoPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return issues // No .beads directory, nothing to check
	}

	// Get database path (backend-aware)
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		// For Dolt, cfg.DatabasePath() is a directory and sqlite checks are not applicable.
		if cfg.GetBackend() == configfile.BackendDolt {
			return issues
		}
		if cfg.Database != "" {
			dbPath = cfg.DatabasePath(beadsDir)
		}
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return issues // No database, nothing to check
	}

	// Open database in read-only mode
	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return issues // Can't open database, skip
	}
	defer db.Close()

	// Check status.custom - custom status names should be lowercase alphanumeric with underscores
	var statusCustom string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'status.custom'").Scan(&statusCustom)
	if err == nil && statusCustom != "" {
		statuses := strings.Split(statusCustom, ",")
		for _, status := range statuses {
			status = strings.TrimSpace(status)
			if status == "" {
				continue
			}
			if !validCustomStatusRegex.MatchString(status) {
				issues = append(issues, fmt.Sprintf("status.custom: %q is invalid (must start with lowercase letter, contain only lowercase letters, numbers, and underscores)", status))
			}
			// Check for conflicts with built-in statuses
			switch status {
			case "open", "in_progress", "blocked", "closed":
				issues = append(issues, fmt.Sprintf("status.custom: %q conflicts with built-in status", status))
			}
		}
	}

	// Check sync.branch if stored in database (legacy location)
	var syncBranch string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'sync.branch'").Scan(&syncBranch)
	if err == nil && syncBranch != "" {
		if !isValidBranchName(syncBranch) {
			issues = append(issues, fmt.Sprintf("sync.branch (database): %q is not a valid git branch name", syncBranch))
		}
	}

	return issues
}

// isValidBranchName checks if a string is a valid git branch name
func isValidBranchName(name string) bool {
	if name == "" {
		return false
	}

	// Can't start with -
	if strings.HasPrefix(name, "-") {
		return false
	}

	// Can't end with . or /
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") {
		return false
	}

	// Can't contain ..
	if strings.Contains(name, "..") {
		return false
	}

	// Can't contain these characters: space, ~, ^, :, \, ?, *, [
	invalidChars := []string{" ", "~", "^", ":", "\\", "?", "*", "[", "@{"}
	for _, char := range invalidChars {
		if strings.Contains(name, char) {
			return false
		}
	}

	// Can't end with .lock
	if strings.HasSuffix(name, ".lock") {
		return false
	}

	return true
}
