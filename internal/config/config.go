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
	"gopkg.in/yaml.v3"
)

var v *viper.Viper

// overriddenKeys tracks keys explicitly set via Set() at runtime, so
// GetValueSource can distinguish them from Viper defaults.
var overriddenKeys = map[string]bool{}

// Initialize sets up the viper configuration singleton
// Should be called once at application startup
func Initialize() error {
	v = viper.New()

	// Set config type to yaml (we only load config.yaml, not config.json)
	v.SetConfigType("yaml")

	// Collect config files from lowest to highest priority.
	// We load the lowest first with ReadInConfig, then MergeInConfig each
	// subsequent file so higher-priority values overwrite lower-priority ones.
	//
	// Precedence (highest to lowest):
	//   BEADS_DIR/config.yaml > project .beads/config.yaml > ~/.config/bd/config.yaml > ~/.beads/config.yaml
	//
	// Previously, only ONE config file was loaded (the highest-priority match),
	// which meant user-level config was silently ignored when project-level
	// config existed — e.g., the idle-monitor daemon with BEADS_DIR set (GH#2375).
	var configPaths []string     // ordered lowest priority first
	var primaryConfigPath string // project-level config (for config.local.yaml and SaveConfigValue)

	// 3. Legacy: ~/.beads/config.yaml (lowest priority)
	if homeDir, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(homeDir, ".beads", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			configPaths = append(configPaths, p)
		}
	}

	// 2. User: ~/.config/bd/config.yaml
	if configDir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(configDir, "bd", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			configPaths = append(configPaths, p)
		}
	}

	// 1. Project: walk up from CWD to find .beads/config.yaml
	cwd, err := os.Getwd()
	if err == nil {
		// In the beads repo, `.beads/config.yaml` is tracked and may set sync.mode=dolt-native.
		// In `go test` (especially for `cmd/bd`), we want to avoid unintentionally picking up
		// the repo-local config, while still allowing tests to load config.yaml from temp repos.
		//
		// If BEADS_TEST_IGNORE_REPO_CONFIG is set, we will ignore the config at
		// <module-root>/.beads/config.yaml (where module-root is the nearest parent containing go.mod).
		ignoreRepoConfig := os.Getenv("BEADS_TEST_IGNORE_REPO_CONFIG") != ""
		var moduleRoot string
		if ignoreRepoConfig {
			// Find module root by walking up to go.mod.
			for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					moduleRoot = dir
					break
				}
			}
		}

		// Walk up parent directories to find .beads/config.yaml
		for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			beadsDir := filepath.Join(dir, ".beads")
			p := filepath.Join(beadsDir, "config.yaml")
			if _, err := os.Stat(p); err == nil {
				if ignoreRepoConfig && moduleRoot != "" {
					// Only ignore the repo-local config (moduleRoot/.beads/config.yaml).
					wantIgnore := filepath.Clean(p) == filepath.Clean(filepath.Join(moduleRoot, ".beads", "config.yaml"))
					if wantIgnore {
						continue
					}
				}
				configPaths = append(configPaths, p)
				primaryConfigPath = p
				break
			}
		}
	}

	// 0. BEADS_DIR: highest priority
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		p := filepath.Join(beadsDir, "config.yaml")
		if _, err := os.Stat(p); err == nil {
			// Avoid duplicate if BEADS_DIR points to same config as CWD walk
			if primaryConfigPath == "" || filepath.Clean(p) != filepath.Clean(primaryConfigPath) {
				configPaths = append(configPaths, p)
			}
			primaryConfigPath = p
		}
	}

	// Automatic environment variable binding
	// Environment variables take precedence over config file
	// E.g., BD_JSON, BD_NO_DAEMON, BD_ACTOR, BD_DB
	v.SetEnvPrefix("BD")

	// Replace hyphens and dots with underscores for env var mapping
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	// Set defaults for all flags
	v.SetDefault("json", false)
	v.SetDefault("events-export", false)
	v.SetDefault("no-db", false)
	v.SetDefault("db", "")
	v.SetDefault("actor", "")
	v.SetDefault("issue-prefix", "")
	// Additional environment variables (not prefixed with BD_)
	_ = v.BindEnv("identity", "BEADS_IDENTITY") // BindEnv only fails with zero args, which can't happen here
	v.SetDefault("identity", "")

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

	// Metadata schema validation (GH#1416 Phase 2)
	// - "none": no metadata schema validation (default)
	// - "warn": validate and print warnings but proceed
	// - "error": validate and reject invalid metadata
	v.SetDefault("validation.metadata.mode", "none")

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

	// Backup configuration defaults (JSONL export to .beads/backup/)
	v.SetDefault("backup.enabled", false)
	v.SetDefault("backup.interval", "15m")
	v.SetDefault("backup.git-push", false)
	v.SetDefault("backup.git-repo", "")

	// AI configuration defaults
	v.SetDefault("ai.model", "claude-haiku-4-5-20251001")

	// Output configuration (GH#1384)
	// Controls title display in command feedback messages.
	// 0 = hide title, N > 0 = truncate to N chars with "…"
	v.SetDefault("output.title-length", 255)

	// External projects for cross-project dependency resolution (bd-h807)
	// Maps project names to paths for resolving external: blocked_by references
	v.SetDefault("external_projects", map[string]string{})

	// Load config files: lowest priority first, each MergeInConfig overwrites
	if len(configPaths) > 0 {
		v.SetConfigFile(configPaths[0])
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
		debug.Logf("Debug: loaded config from %s\n", configPaths[0])

		for _, p := range configPaths[1:] {
			v.SetConfigFile(p)
			if err := v.MergeInConfig(); err != nil {
				return fmt.Errorf("error merging config file %s: %w", p, err)
			}
			debug.Logf("Debug: merged config from %s\n", p)
		}

		// Restore primary config path as ConfigFileUsed (used by SaveConfigValue,
		// ResolveExternalProjectPath, etc.)
		v.SetConfigFile(primaryConfigPath)

		// Merge local config overrides if present (config.local.yaml)
		// This allows machine-specific settings without polluting tracked config
		localConfigPath := filepath.Join(filepath.Dir(primaryConfigPath), "config.local.yaml")
		if _, err := os.Stat(localConfigPath); err == nil {
			v.SetConfigFile(localConfigPath)
			if err := v.MergeInConfig(); err != nil {
				return fmt.Errorf("error merging local config file: %w", err)
			}
			debug.Logf("Debug: merged local config from %s\n", localConfigPath)
			// Restore primary as ConfigFileUsed
			v.SetConfigFile(primaryConfigPath)
		}
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
	overriddenKeys = map[string]bool{}
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

	// Check if value was explicitly set via Set() at runtime
	if overriddenKeys[key] {
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

// SaveConfigValue sets a key-value pair and writes it to the config file.
// If no config file is currently loaded, it creates config.yaml in the given beadsDir.
// Only the specified key is modified; other file contents are preserved.
func SaveConfigValue(key string, value interface{}, beadsDir string) error {
	if v == nil {
		return fmt.Errorf("config not initialized")
	}
	v.Set(key, value)

	configPath := v.ConfigFileUsed()
	if configPath == "" {
		configPath = filepath.Join(beadsDir, "config.yaml")
		v.SetConfigFile(configPath)
	}

	// Read existing file contents to avoid dumping all merged viper state
	// (defaults, env vars, overrides) into the config file.
	existing := make(map[string]interface{})
	if data, err := os.ReadFile(filepath.Clean(configPath)); err == nil {
		_ = yaml.Unmarshal(data, &existing)
	}

	// Set the single key using dot-path splitting for nested keys (e.g. "sync.mode").
	setNestedKey(existing, key, value)

	out, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(configPath, out, 0o600)
}

// setNestedKey sets a value in a nested map using a dot-separated key path.
func setNestedKey(m map[string]interface{}, key string, value interface{}) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 1 {
		m[key] = value
		return
	}
	sub, ok := m[parts[0]].(map[string]interface{})
	if !ok {
		sub = make(map[string]interface{})
		m[parts[0]] = sub
	}
	setNestedKey(sub, parts[1], value)
}

// GetString retrieves a string configuration value
func GetString(key string) string {
	if v == nil {
		return ""
	}
	return v.GetString(key)
}

// GetStringFromDir reads a single string configuration value directly from
// <beadsDir>/config.yaml without using or modifying global viper state.
// This is intended for library consumers that call NewFromConfigWithOptions
// without first invoking config.Initialize().
//
// The key uses dotted notation (e.g. "dolt.auto-start"). YAML booleans and
// numbers are coerced to their string representations ("true", "false", etc.).
// Returns "" if the file is absent, the key is not found, or any error occurs.
func GetStringFromDir(beadsDir, key string) string {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ""
	}
	parts := strings.SplitN(key, ".", 2)
	node := root
	for len(parts) == 2 {
		val, ok := node[parts[0]]
		if !ok {
			return ""
		}
		m, ok := val.(map[string]interface{})
		if !ok {
			return ""
		}
		node = m
		parts = strings.SplitN(parts[1], ".", 2)
	}
	val, ok := node[parts[0]]
	if !ok {
		return ""
	}
	switch s := val.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", s)
	}
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
		overriddenKeys[key] = true
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

// DefaultAIModel returns the configured AI model identifier.
// Override via: bd config set ai.model "model-name" or BD_AI_MODEL=model-name
func DefaultAIModel() string {
	return GetString("ai.model")
}

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
	// when running from different directories.
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

// GetCustomTypesFromYAML retrieves custom issue types from config.yaml.
// This is used as a fallback when the database doesn't have types.custom set yet
// (e.g., during bd init auto-import before the database is fully configured).
// Returns nil if no custom types are configured in config.yaml.
func GetCustomTypesFromYAML() []string {
	return getConfigList("types.custom")
}

// GetInfraTypesFromYAML retrieves infrastructure type names from config.yaml.
// Infrastructure types are routed to the wisps table instead of the versioned issues table.
// Returns nil if no infra types are configured in config.yaml (caller should use defaults).
func GetInfraTypesFromYAML() []string {
	return getConfigList("types.infra")
}

// GetCustomStatusesFromYAML retrieves custom statuses from config.yaml.
// This is used as a fallback when the database doesn't have status.custom set yet
// or when the database connection is temporarily unavailable.
// Returns nil if no custom statuses are configured in config.yaml.
func GetCustomStatusesFromYAML() []string {
	return getConfigList("status.custom")
}

// ===== Agent Role Configuration =====
// These functions return agent role types from config.yaml for agent ID parsing.
// Each role category has different parsing semantics:
//   - Town-level: <prefix>-<role> (singleton, no rig)
//   - Rig-level: <prefix>-<rig>-<role> (singleton per rig)
//   - Named: <prefix>-<rig>-<role>-<name> (multiple per rig)

// GetTownLevelRoles returns roles that are town-level singletons.
// These roles have no rig association and appear as: <prefix>-<role>
func GetTownLevelRoles() []string {
	return getConfigList("agent_roles.town_level")
}

// GetRigLevelRoles returns roles that are rig-level singletons.
// These roles have one instance per rig: <prefix>-<rig>-<role>
func GetRigLevelRoles() []string {
	return getConfigList("agent_roles.rig_level")
}

// GetNamedRoles returns roles that can have multiple named instances per rig.
// These roles include a name suffix: <prefix>-<rig>-<role>-<name>
func GetNamedRoles() []string {
	return getConfigList("agent_roles.named")
}

// MetadataValidationMode returns the metadata schema validation mode.
// Returns "none" if config is not initialized or mode is empty/unknown.
func MetadataValidationMode() string {
	if v == nil {
		return "none"
	}
	mode := v.GetString("validation.metadata.mode")
	switch mode {
	case "warn", "error":
		return mode
	default:
		return "none"
	}
}

// MetadataSchemaFields returns the raw field definitions from config.
// Returns nil if config is not initialized or no fields are defined.
// Each entry maps field name → map of properties (type, values, required, min, max).
func MetadataSchemaFields() map[string]interface{} {
	if v == nil {
		return nil
	}
	raw := v.Get("validation.metadata.fields")
	if raw == nil {
		return nil
	}
	// Viper returns map[string]interface{} for nested YAML maps
	if m, ok := raw.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// getConfigList is a helper that retrieves a comma-separated list from config.yaml.
func getConfigList(key string) []string {
	if v == nil {
		debug.Logf("config: viper not initialized, returning nil for key %q", key)
		return nil
	}

	value := v.GetString(key)
	if value == "" {
		return nil
	}

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
