package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
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

// checkYAMLConfigValues validates values in config.yaml
func checkYAMLConfigValues(repoPath string) []string {
	var issues []string

	// Load config.yaml if it exists
	v := viper.New()
	v.SetConfigType("yaml")

	configPath := filepath.Join(repoPath, ".beads", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// No config.yaml, check user config dirs
		configPath = ""
		if configDir, err := os.UserConfigDir(); err == nil {
			userConfigPath := filepath.Join(configDir, "bd", "config.yaml")
			if _, err := os.Stat(userConfigPath); err == nil {
				configPath = userConfigPath
			}
		}
		if configPath == "" {
			if homeDir, err := os.UserHomeDir(); err == nil {
				homeConfigPath := filepath.Join(homeDir, ".beads", "config.yaml")
				if _, err := os.Stat(homeConfigPath); err == nil {
					configPath = homeConfigPath
				}
			}
		}
	}

	if configPath == "" {
		// No config.yaml found anywhere
		return issues
	}

	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		issues = append(issues, fmt.Sprintf("config.yaml: failed to parse: %v", err))
		return issues
	}

	// Validate flush-debounce (should be a valid duration)
	if v.IsSet("flush-debounce") {
		debounceStr := v.GetString("flush-debounce")
		if debounceStr != "" {
			_, err := time.ParseDuration(debounceStr)
			if err != nil {
				issues = append(issues, fmt.Sprintf("flush-debounce: invalid duration %q (expected format like \"30s\", \"1m\", \"500ms\")", debounceStr))
			}
		}
	}

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
	for _, key := range []string{"routing.default", "routing.maintainer", "routing.contributor"} {
		if v.IsSet(key) {
			path := v.GetString(key)
			if path != "" && path != "." {
				// Expand ~ to home directory
				if strings.HasPrefix(path, "~") {
					if home, err := os.UserHomeDir(); err == nil {
						path = filepath.Join(home, path[1:])
					}
				}
				// Check if path exists (only warn, don't error - it might be created later)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					issues = append(issues, fmt.Sprintf("%s: path %q does not exist", key, v.GetString(key)))
				}
			}
		}
	}

	return issues
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
		if !strings.HasSuffix(cfg.Database, ".db") && !strings.HasSuffix(cfg.Database, ".sqlite") && !strings.HasSuffix(cfg.Database, ".sqlite3") {
			issues = append(issues, fmt.Sprintf("metadata.json database: %q has unusual extension (expected .db, .sqlite, or .sqlite3)", cfg.Database))
		}
	}

	// Validate jsonl_export filename
	if cfg.JSONLExport != "" {
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
