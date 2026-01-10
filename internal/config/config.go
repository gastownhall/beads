package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/steveyegge/beads/internal/debug"
)

var v *viper.Viper

// Initialize sets up the viper configuration singleton
// Should be called once at application startup
func Initialize() error {
	v = viper.New()

	// Set config type to yaml (we only load config.yaml, not config.json)
	v.SetConfigType("yaml")

	// Explicitly locate config.yaml and use SetConfigFile to avoid picking up config.json
	// Precedence: project .beads/config.yaml > ~/.config/bd/config.yaml > ~/.beads/config.yaml
	configFileSet := false

	// 1. Walk up from CWD to find project .beads/config.yaml
	//    This allows commands to work from subdirectories
	cwd, err := os.Getwd()
	if err == nil && !configFileSet {
		// Walk up parent directories to find .beads/config.yaml
		for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			beadsDir := filepath.Join(dir, ".beads")
			configPath := filepath.Join(beadsDir, "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				// Found .beads/config.yaml - set it explicitly
				v.SetConfigFile(configPath)
				configFileSet = true
				break
			}
		}
	}

	// 2. User config directory (~/.config/bd/config.yaml)
	if !configFileSet {
		if configDir, err := os.UserConfigDir(); err == nil {
			configPath := filepath.Join(configDir, "bd", "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				v.SetConfigFile(configPath)
				configFileSet = true
			}
		}
	}

	// 3. Home directory (~/.beads/config.yaml)
	if !configFileSet {
		if homeDir, err := os.UserHomeDir(); err == nil {
			configPath := filepath.Join(homeDir, ".beads", "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				v.SetConfigFile(configPath)
				configFileSet = true
			}
		}
	}

	// Automatic environment variable binding
	// Environment variables take precedence over config file
	// E.g., BD_JSON, BD_NO_DAEMON, BD_ACTOR, BD_DB
	v.SetEnvPrefix("BD")

	// Replace hyphens and dots with underscores for env var mapping
	// This allows BD_NO_DAEMON to map to "no-daemon" config key
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	// Set defaults for all flags
	v.SetDefault("json", false)
	v.SetDefault("no-daemon", false)
	v.SetDefault("no-auto-flush", false)
	v.SetDefault("no-auto-import", false)
	v.SetDefault("no-db", false)
	v.SetDefault("db", "")
	v.SetDefault("actor", "")
	v.SetDefault("issue-prefix", "")
	v.SetDefault("lock-timeout", "30s")

	// Additional environment variables (not prefixed with BD_)
	// These are bound explicitly for backward compatibility
	_ = v.BindEnv("flush-debounce", "BEADS_FLUSH_DEBOUNCE")
	_ = v.BindEnv("auto-start-daemon", "BEADS_AUTO_START_DAEMON")
	_ = v.BindEnv("identity", "BEADS_IDENTITY")
	_ = v.BindEnv("remote-sync-interval", "BEADS_REMOTE_SYNC_INTERVAL")

	// Set defaults for additional settings
	v.SetDefault("flush-debounce", "30s")
	v.SetDefault("auto-start-daemon", true)
	v.SetDefault("identity", "")
	v.SetDefault("remote-sync-interval", "30s")

	// Routing configuration defaults
	v.SetDefault("routing.mode", "auto")
	v.SetDefault("routing.default", ".")
	v.SetDefault("routing.maintainer", ".")
	v.SetDefault("routing.contributor", "~/.beads-planning")

	// Sync configuration defaults (bd-4u8)
	v.SetDefault("sync.require_confirmation_on_mass_delete", false)

	// Push configuration defaults
	v.SetDefault("no-push", false)

	// Create command defaults
	v.SetDefault("create.require-description", false)

	// Validation configuration defaults (bd-t7jq)
	// Values: "warn" | "error" | "none"
	// - "none": no validation (default, backwards compatible)
	// - "warn": validate and print warnings but proceed
	// - "error": validate and fail on missing sections
	v.SetDefault("validation.on-create", "none")
	v.SetDefault("validation.on-sync", "none")

	// Hierarchy configuration defaults (GH#995)
	// Maximum nesting depth for hierarchical IDs (e.g., bd-abc.1.2.3)
	// Default matches types.MaxHierarchyDepth constant
	v.SetDefault("hierarchy.max-depth", 3)

	// Git configuration defaults (GH#600)
	v.SetDefault("git.author", "")         // Override commit author (e.g., "beads-bot <beads@example.com>")
	v.SetDefault("git.no-gpg-sign", false) // Disable GPG signing for beads commits

	// Directory-aware label scoping (GH#541)
	// Maps directory patterns to labels for automatic filtering in monorepos
	v.SetDefault("directory.labels", map[string]string{})

	// External projects for cross-project dependency resolution (bd-h807)
	// Maps project names to paths for resolving external: blocked_by references
	v.SetDefault("external_projects", map[string]string{})

	// Read config file if it was found
	if configFileSet {
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
		debug.Logf("Debug: loaded config from %s\n", v.ConfigFileUsed())
	} else {
		// No config.yaml found - use defaults and environment variables
		debug.Logf("Debug: no config.yaml found; using defaults and environment variables\n")
	}

	return nil
}

// ConfigSource represents where a configuration value came from
type ConfigSource string

const (
	SourceDefault    ConfigSource = "default"
	SourceConfigFile ConfigSource = "config_file"
	SourceEnvVar     ConfigSource = "env_var"
	SourceFlag       ConfigSource = "flag"
)

// ConfigOverride represents a detected configuration override
type ConfigOverride struct {
	Key            string
	EffectiveValue interface{}
	OverriddenBy   ConfigSource
	OriginalSource ConfigSource
	OriginalValue  interface{}
}

// GetValueSource returns the source of a configuration value.
// Priority (highest to lowest): env var > config file > default
// Note: Flag overrides are handled separately in main.go since viper doesn't know about cobra flags.
func GetValueSource(key string) ConfigSource {
	if v == nil {
		return SourceDefault
	}

	// Check if value is set from environment variable
	// Viper's IsSet returns true if the key is set from any source (env, config, or default)
	// We need to check specifically for env var by looking at the env var directly
	envKey := "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if os.Getenv(envKey) != "" {
		return SourceEnvVar
	}

	// Check BEADS_ prefixed env vars for legacy compatibility
	beadsEnvKey := "BEADS_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if os.Getenv(beadsEnvKey) != "" {
		return SourceEnvVar
	}

	// Check if value is set in config file (as opposed to being a default)
	if v.InConfig(key) {
		return SourceConfigFile
	}

	return SourceDefault
}

// CheckOverrides checks for configuration overrides and returns a list of detected overrides.
// This is useful for informing users when env vars or flags override config file values.
// flagOverrides is a map of key -> (flagValue, flagWasSet) for flags that were explicitly set.
func CheckOverrides(flagOverrides map[string]struct {
	Value  interface{}
	WasSet bool
}) []ConfigOverride {
	var overrides []ConfigOverride

	for key, flagInfo := range flagOverrides {
		if !flagInfo.WasSet {
			continue
		}

		source := GetValueSource(key)
		if source == SourceConfigFile || source == SourceEnvVar {
			// Flag is overriding a config file or env var value
			var originalValue interface{}
			switch v := flagInfo.Value.(type) {
			case bool:
				originalValue = GetBool(key)
			case string:
				originalValue = GetString(key)
			case int:
				originalValue = GetInt(key)
			default:
				originalValue = v
			}

			overrides = append(overrides, ConfigOverride{
				Key:            key,
				EffectiveValue: flagInfo.Value,
				OverriddenBy:   SourceFlag,
				OriginalSource: source,
				OriginalValue:  originalValue,
			})
		}
	}

	// Check for env var overriding config file
	if v != nil {
		for _, key := range v.AllKeys() {
			envSource := GetValueSource(key)
			if envSource == SourceEnvVar && v.InConfig(key) {
				// Env var is overriding config file value
				// Get the config file value by temporarily unsetting the env
				envKey := "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
				envValue := os.Getenv(envKey)
				if envValue == "" {
					envKey = "BEADS_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
					envValue = os.Getenv(envKey)
				}

				// Skip if no env var actually set (shouldn't happen but be safe)
				if envValue == "" {
					continue
				}

				overrides = append(overrides, ConfigOverride{
					Key:            key,
					EffectiveValue: v.Get(key),
					OverriddenBy:   SourceEnvVar,
					OriginalSource: SourceConfigFile,
					OriginalValue:  nil, // We can't easily get the config file value separately
				})
			}
		}
	}

	return overrides
}

// LogOverride logs a message about a configuration override in verbose mode.
func LogOverride(override ConfigOverride) {
	var sourceDesc string
	switch override.OriginalSource {
	case SourceConfigFile:
		sourceDesc = "config file"
	case SourceEnvVar:
		sourceDesc = "environment variable"
	case SourceDefault:
		sourceDesc = "default"
	default:
		sourceDesc = string(override.OriginalSource)
	}

	var overrideDesc string
	switch override.OverriddenBy {
	case SourceFlag:
		overrideDesc = "command-line flag"
	case SourceEnvVar:
		overrideDesc = "environment variable"
	default:
		overrideDesc = string(override.OverriddenBy)
	}

	// Always emit to stderr when verbose mode is enabled (caller guards on verbose)
	fmt.Fprintf(os.Stderr, "Config: %s overridden by %s (was: %v from %s, now: %v)\n",
		override.Key, overrideDesc, override.OriginalValue, sourceDesc, override.EffectiveValue)
}

// GetString retrieves a string configuration value
func GetString(key string) string {
	if v == nil {
		return ""
	}
	return v.GetString(key)
}

// GetBool retrieves a boolean configuration value
func GetBool(key string) bool {
	if v == nil {
		return false
	}
	return v.GetBool(key)
}

// GetInt retrieves an integer configuration value
func GetInt(key string) int {
	if v == nil {
		return 0
	}
	return v.GetInt(key)
}

// GetDuration retrieves a duration configuration value
func GetDuration(key string) time.Duration {
	if v == nil {
		return 0
	}
	return v.GetDuration(key)
}

// Set sets a configuration value
func Set(key string, value interface{}) {
	if v != nil {
		v.Set(key, value)
	}
}

// BindPFlag is reserved for future use if we want to bind Cobra flags directly to Viper
// For now, we handle flag precedence manually in PersistentPreRun
// Uncomment and implement if needed:
//
// func BindPFlag(key string, flag *pflag.Flag) error {
// 	if v == nil {
// 		return fmt.Errorf("viper not initialized")
// 	}
// 	return v.BindPFlag(key, flag)
// }

// AllSettings returns all configuration settings as a map
func AllSettings() map[string]interface{} {
	if v == nil {
		return map[string]interface{}{}
	}
	return v.AllSettings()
}

// GetStringSlice retrieves a string slice configuration value
func GetStringSlice(key string) []string {
	if v == nil {
		return []string{}
	}
	return v.GetStringSlice(key)
}

// GetStringMapString retrieves a map[string]string configuration value
func GetStringMapString(key string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v.GetStringMapString(key)
}

// GetDirectoryLabels returns labels for the current working directory based on config.
// It checks directory.labels config for matching patterns.
// Returns nil if no labels are configured for the current directory.
func GetDirectoryLabels() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	dirLabels := GetStringMapString("directory.labels")
	if len(dirLabels) == 0 {
		return nil
	}

	// Check each configured directory pattern
	for pattern, label := range dirLabels {
		// Support both exact match and suffix match
		// e.g., "packages/maverick" matches "/path/to/repo/packages/maverick"
		if strings.HasSuffix(cwd, pattern) || strings.HasSuffix(cwd, filepath.Clean(pattern)) {
			return []string{label}
		}
		// Also try as a path prefix (user might be in a subdirectory)
		if strings.Contains(cwd, "/"+pattern+"/") || strings.Contains(cwd, "/"+pattern) {
			return []string{label}
		}
	}

	return nil
}

// MultiRepoConfig contains configuration for multi-repo support
type MultiRepoConfig struct {
	Primary    string   // Primary repo path (where canonical issues live)
	Additional []string // Additional repos to hydrate from
}

// GetMultiRepoConfig retrieves multi-repo configuration
// Returns nil if multi-repo is not configured (single-repo mode)
func GetMultiRepoConfig() *MultiRepoConfig {
	if v == nil {
		return nil
	}

	// Check if repos.primary is set (indicates multi-repo mode)
	primary := v.GetString("repos.primary")
	if primary == "" {
		return nil // Single-repo mode
	}

	return &MultiRepoConfig{
		Primary:    primary,
		Additional: v.GetStringSlice("repos.additional"),
	}
}

// GetExternalProjects returns the external_projects configuration.
// Maps project names to paths for cross-project dependency resolution.
// Example config.yaml:
//
//	external_projects:
//	  beads: ../beads
//	  gastown: /absolute/path/to/gastown
func GetExternalProjects() map[string]string {
	return GetStringMapString("external_projects")
}

// ResolveExternalProjectPath resolves a project name to its absolute path.
// Returns empty string if project not configured or path doesn't exist.
func ResolveExternalProjectPath(projectName string) string {
	projects := GetExternalProjects()
	path, ok := projects[projectName]
	if !ok {
		return ""
	}

	// Expand relative paths from config file location or cwd
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		path = filepath.Join(cwd, path)
	}

	// Verify path exists
	if _, err := os.Stat(path); err != nil {
		return ""
	}

	return path
}

// GetIdentity resolves the user's identity for messaging.
// Priority chain:
//  1. flagValue (if non-empty, from --identity flag)
//  2. BEADS_IDENTITY env var / config.yaml identity field (via viper)
//  3. git config user.name
//  4. hostname
//
// This is used as the sender field in bd mail commands.
func GetIdentity(flagValue string) string {
	// 1. Command-line flag takes precedence
	if flagValue != "" {
		return flagValue
	}

	// 2. BEADS_IDENTITY env var or config.yaml identity (viper handles both)
	if identity := GetString("identity"); identity != "" {
		return identity
	}

	// 3. git config user.name
	cmd := exec.Command("git", "config", "user.name")
	if output, err := cmd.Output(); err == nil {
		if gitUser := strings.TrimSpace(string(output)); gitUser != "" {
			return gitUser
		}
	}

	// 4. hostname
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}

	return "unknown"
}
