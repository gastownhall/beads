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

// Sync trigger constants define when sync operations occur.
const (
	// SyncTriggerPush triggers sync on git push operations.
	SyncTriggerPush = "push"

	// SyncTriggerChange triggers sync on every database change.
	SyncTriggerChange = "change"

	// SyncTriggerPull triggers import on git pull operations.
	SyncTriggerPull = "pull"
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

	// Dolt configuration defaults
	// Controls whether beads should automatically create Dolt commits after write commands.
	// Values: off | on
	v.SetDefault("dolt.auto-commit", "on")

	// Routing configuration defaults
	v.SetDefault("routing.mode", "")
	v.SetDefault("routing.default", ".")
	v.SetDefault("routing.maintainer", ".")
	v.SetDefault("routing.contributor", "~/.beads-planning")

	// Sync configuration defaults (bd-4u8)
	v.SetDefault("sync.require_confirmation_on_mass_delete", false)

	// Sync mode configuration (hq-ew1mbr.3)
	// See docs/CONFIG.md for detailed documentation
	v.SetDefault("sync.mode", SyncModeGitPortable)  // git-portable | realtime | dolt-native | belt-and-suspenders
	v.SetDefault("sync.export_on", SyncTriggerPush) // push | change
	v.SetDefault("sync.import_on", SyncTriggerPull) // pull | change

	// Conflict resolution configuration
	v.SetDefault("conflict.strategy", ConflictStrategyNewest) // newest | ours | theirs | manual

	// Federation configuration (optional Dolt remote)
	v.SetDefault("federation.remote", "")      // e.g., dolthub://org/beads, gs://bucket/beads, s3://bucket/beads
	v.SetDefault("federation.sovereignty", "") // T1 | T2 | T3 | T4 (empty = no restriction)

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

// ResetForTesting clears the config state, allowing Initialize() to be called again.
// This is intended for tests that need to change config.yaml between test steps.
// WARNING: Not thread-safe. Only call from single-threaded test contexts.
func ResetForTesting() {
	v = nil
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

// ConfigFileUsed returns the path to the config file that was loaded.
// Returns empty string if no config file was found or viper is not initialized.
// This is useful for resolving relative paths from the config file's directory.
func ConfigFileUsed() string {
	if v == nil {
		return ""
	}
	return v.ConfigFileUsed()
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

	// Resolve relative paths from repo root (parent of .beads/), NOT CWD.
	// This ensures paths like "../beads" in config resolve correctly
	// when running from different directories or in daemon context.
	if !filepath.IsAbs(path) {
		// Config is at .beads/config.yaml, so go up twice to get repo root
		configFile := ConfigFileUsed()
		if configFile != "" {
			repoRoot := filepath.Dir(filepath.Dir(configFile)) // .beads/config.yaml -> repo/
			path = filepath.Join(repoRoot, path)
		} else {
			// Fallback: resolve from CWD (legacy behavior)
			cwd, err := os.Getwd()
			if err != nil {
				return ""
			}
			path = filepath.Join(cwd, path)
		}
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

// SyncConfig holds the sync mode configuration.
type SyncConfig struct {
	Mode     SyncMode // git-portable, realtime, dolt-native, belt-and-suspenders
	ExportOn string   // push, change
	ImportOn string   // pull, change
}

// GetSyncConfig returns the current sync configuration.
func GetSyncConfig() SyncConfig {
	return SyncConfig{
		Mode:     GetSyncMode(),
		ExportOn: GetString("sync.export_on"),
		ImportOn: GetString("sync.import_on"),
	}
}

// ConflictConfig holds the conflict resolution configuration.
type ConflictConfig struct {
	Strategy ConflictStrategy          // newest, ours, theirs, manual (default for all fields)
	Fields   map[string]FieldStrategy  // Per-field strategy overrides
}

// GetConflictConfig returns the current conflict resolution configuration.
func GetConflictConfig() ConflictConfig {
	return ConflictConfig{
		Strategy: GetConflictStrategy(),
		Fields:   GetFieldStrategies(),
	}
}

// GetFieldStrategies retrieves per-field conflict resolution strategies from config.
// Returns a map of field name to strategy (e.g., {"labels": "union", "compaction_level": "max"}).
// Invalid strategies are logged and skipped.
//
// Config key: conflict.fields
// Example:
//
//	conflict:
//	  strategy: newest
//	  fields:
//	    compaction_level: max
//	    labels: union
//	    waiters: union
//	    estimated_minutes: manual
func GetFieldStrategies() map[string]FieldStrategy {
	result := make(map[string]FieldStrategy)
	if v == nil {
		return result
	}

	// Get the raw map from config
	fieldsMap := v.GetStringMapString("conflict.fields")
	if fieldsMap == nil {
		return result
	}

	for field, strategyStr := range fieldsMap {
		strategy := FieldStrategy(strings.ToLower(strings.TrimSpace(strategyStr)))
		if !validFieldStrategies[strategy] {
			logConfigWarning("Warning: invalid conflict.fields.%s strategy %q (valid: %s), skipping\n",
				field, strategyStr, strings.Join(ValidFieldStrategies(), ", "))
			continue
		}
		result[field] = strategy
	}

	return result
}

// GetFieldStrategy returns the merge strategy for a specific field.
// Returns the per-field strategy if configured, otherwise returns "newest" (default).
func GetFieldStrategy(field string) FieldStrategy {
	fields := GetFieldStrategies()
	if strategy, ok := fields[field]; ok {
		return strategy
	}
	return FieldStrategyNewest // Default
}

// FederationConfig holds the federation (Dolt remote) configuration.
type FederationConfig struct {
	Remote      string      // dolthub://org/beads, gs://bucket/beads, s3://bucket/beads
	Sovereignty Sovereignty // T1, T2, T3, T4
}

// GetFederationConfig returns the current federation configuration.
func GetFederationConfig() FederationConfig {
	return FederationConfig{
		Remote:      GetString("federation.remote"),
		Sovereignty: GetSovereignty(),
	}
}

// IsSyncModeValid checks if the given sync mode string is valid.
func IsSyncModeValid(mode string) bool {
	return validSyncModes[SyncMode(mode)]
}

// IsConflictStrategyValid checks if the given conflict strategy string is valid.
func IsConflictStrategyValid(strategy string) bool {
	return validConflictStrategies[ConflictStrategy(strategy)]
}

// IsSovereigntyValid checks if the given sovereignty tier string is valid.
// Note: empty string is valid (means no restriction).
func IsSovereigntyValid(sovereignty string) bool {
	if sovereignty == "" {
		return true
	}
	return validSovereigntyTiers[Sovereignty(sovereignty)]
}

// ShouldExportOnChange returns true if sync.export_on is set to "change".
func ShouldExportOnChange() bool {
	return GetString("sync.export_on") == SyncTriggerChange
}

// ShouldImportOnChange returns true if sync.import_on is set to "change".
func ShouldImportOnChange() bool {
	return GetString("sync.import_on") == SyncTriggerChange
}

// NeedsDoltRemote returns true if the sync mode requires a Dolt remote.
func NeedsDoltRemote() bool {
	mode := GetSyncMode()
	return mode == SyncModeDoltNative || mode == SyncModeBeltAndSuspenders
}

// NeedsJSONL returns true if the sync mode requires JSONL export.
func NeedsJSONL() bool {
	mode := GetSyncMode()
	return mode == SyncModeGitPortable || mode == SyncModeRealtime || mode == SyncModeBeltAndSuspenders
}

// GetCustomTypesFromYAML retrieves custom issue types from config.yaml.
// This is used as a fallback when the database doesn't have types.custom set yet
// (e.g., during bd init auto-import before the database is fully configured).
// Returns nil if no custom types are configured in config.yaml.
func GetCustomTypesFromYAML() []string {
	if v == nil {
		return nil
	}

	// Try to get types.custom from viper (config.yaml or env var)
	value := v.GetString("types.custom")
	if value == "" {
		return nil
	}

	// Parse comma-separated list
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// GetCustomStatusesFromYAML retrieves custom statuses from config.yaml.
// This is used as a fallback when the database doesn't have status.custom set yet
// or when the database connection is temporarily unavailable.
// Returns nil if no custom statuses are configured in config.yaml.
func GetCustomStatusesFromYAML() []string {
	if v == nil {
		return nil
	}

	// Try to get status.custom from viper (config.yaml or env var)
	value := v.GetString("status.custom")
	if value == "" {
		return nil
	}

	// Parse comma-separated list
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
