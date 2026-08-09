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

// ignoredConfigKey normalizes a config path into the single form the
// BEADS_TEST_IGNORE_REPO_CONFIG ignore set is keyed by, so that membership does
// not depend on which alias of a directory the caller happened to hold.
//
// The set is built from os.Getwd(), which honors $PWD and therefore reports the
// path the process was given rather than the one the kernel resolved. The
// BEADS_DIR that is tested against it is written by the CLI's own dispatch from
// beads.FindBeadsDir, which runs utils.CanonicalizePath (filepath.EvalSymlinks).
// Comparing those two strings directly misses whenever the workspace is reached
// through a symlink, the ignore is not applied, and the repo config is merged
// after all — which is exactly what the flag exists to prevent.
//
// On macOS that is not an edge case, it is every temp workspace: $TMPDIR is
// /var/folders/... and /var is a symlink to /private/var, so every t.TempDir()
// has two names and the two sides pick different ones.
//
// Symlinks are resolved on the directory, not the file: callers pass candidate
// config paths that may not exist, and EvalSymlinks fails on a missing leaf.
// When the directory cannot be resolved either, fall back to a lexical clean —
// applied identically on both insert and lookup, so the two still agree.
func ignoredConfigKey(path string) string {
	if path == "" {
		return ""
	}
	dir, base := filepath.Split(filepath.Clean(path))
	resolvedDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(resolvedDir, base))
}

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

	// Also check ~/.config/bd/config.yaml explicitly. On macOS,
	// os.UserConfigDir() returns ~/Library/Application Support, not ~/.config.
	// This ensures the documented path works on all platforms.
	if homeDir, err := os.UserHomeDir(); err == nil {
		xdgPath := filepath.Join(homeDir, ".config", "bd", "config.yaml")
		alreadyAdded := false
		for _, existing := range configPaths {
			if filepath.Clean(existing) == filepath.Clean(xdgPath) {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			if _, err := os.Stat(xdgPath); err == nil {
				configPaths = append(configPaths, xdgPath)
			}
		}
	}

	// 1. Project: walk up from CWD to find .beads/config.yaml
	beadsDirEnv := strings.TrimSpace(os.Getenv("BEADS_DIR"))
	beadsEnvConfigPath := ""
	if beadsDirEnv != "" {
		beadsEnvConfigPath = filepath.Clean(filepath.Join(beadsDirEnv, "config.yaml"))
	}
	// A beads checkout usually has its own `.beads/config.yaml` (untracked developer
	// state) that sets non-default values. In `go test` — especially for `cmd/bd` —
	// we want to avoid unintentionally picking up that repo-local config, while still
	// allowing tests to load config.yaml from temp repos.
	//
	// If BEADS_TEST_IGNORE_REPO_CONFIG is set, we ignore the config at
	// <module-root>/.beads/config.yaml (where module-root is the nearest parent
	// containing go.mod) and at the worktree fallback location.
	//
	// The ignore set applies to every source that can name those paths, including
	// BEADS_DIR below. BEADS_DIR used to bypass the flag, and because in-process CLI
	// dispatch sets BEADS_DIR at the checkout's own .beads via a raw os.Setenv with no
	// restore, that bypass re-imported the repo config into every later Initialize in
	// the same test binary (ga-e6h6i). A test that genuinely wants the repo config
	// unsets the flag.
	ignoreRepoConfig := os.Getenv("BEADS_TEST_IGNORE_REPO_CONFIG") != ""
	ignoredRepoConfigPaths := map[string]bool{}

	cwd, err := os.Getwd()
	if err == nil {
		var moduleRoot string
		if ignoreRepoConfig {
			// Find module root by walking up to go.mod.
			for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					moduleRoot = dir
					break
				}
			}
			if moduleRoot != "" {
				ignoredRepoConfigPaths[ignoredConfigKey(filepath.Join(moduleRoot, ".beads", "config.yaml"))] = true
			}
			if fallbackPath := worktreeFallbackConfigPath(cwd); fallbackPath != "" {
				ignoredRepoConfigPaths[ignoredConfigKey(fallbackPath)] = true
			}
		}

		tryProjectConfig := func(path string) bool {
			if path == "" {
				return false
			}
			if _, err := os.Stat(path); err != nil {
				return false
			}
			if ignoreRepoConfig && ignoredRepoConfigPaths[ignoredConfigKey(path)] {
				return false
			}
			configPaths = append(configPaths, path)
			primaryConfigPath = path
			return true
		}

		// Walk up parent directories to find .beads/config.yaml.
		for dir := cwd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			p := filepath.Join(dir, ".beads", "config.yaml")
			if _, err := os.Stat(p); err == nil {
				// When BEADS_DIR points at a different runtime workspace, do not
				// merge the caller repo's config underneath it. That leaks caller
				// settings like readonly/json/actor into explicit-target commands.
				if beadsEnvConfigPath != "" && filepath.Clean(p) != beadsEnvConfigPath {
					break
				}
				if tryProjectConfig(p) {
					break
				}
			}
			if ignoreRepoConfig && moduleRoot != "" && dir == moduleRoot {
				// Don't walk above the test module root: anything further up is
				// outside this repo entirely (e.g. an outer orchestration
				// project's own unrelated .beads/config.yaml) and must never
				// leak into a beads-under-test process (be-yjp4z).
				break
			}
		}

		// Worktree/shared fallback: the active workspace may live outside the
		// worktree tree, so the parent walk above won't find it.
		if primaryConfigPath == "" && beadsEnvConfigPath == "" {
			p := worktreeFallbackConfigPath(cwd)
			_ = tryProjectConfig(p)
		}
	}

	// 0. BEADS_DIR: highest priority
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		p := filepath.Join(beadsDir, "config.yaml")
		// Honor the test ignore set here too, and skip primaryConfigPath along with
		// the merge so ConfigFileUsed does not name the ignored repo config. This
		// fences the read and merge path only: SaveConfigValue falls back to the
		// caller-supplied beadsDir when ConfigFileUsed is empty, so where a write
		// lands is still the caller's choice, not this flag's.
		ignored := ignoreRepoConfig && ignoredRepoConfigPaths[ignoredConfigKey(p)]
		if _, err := os.Stat(p); err == nil && !ignored {
			// Avoid duplicate if BEADS_DIR points to same config as CWD walk
			if primaryConfigPath == "" || filepath.Clean(p) != filepath.Clean(primaryConfigPath) {
				configPaths = append(configPaths, p)
			}
			primaryConfigPath = p
		}
	}

	// Automatic environment variable binding
	// Environment variables take precedence over config file
	// E.g., BD_JSON, BD_NO_DAEMON, BD_DB (BD_ACTOR deprecated in favor of BEADS_ACTOR)
	v.SetEnvPrefix("BD")

	// Replace hyphens and dots with underscores for env var mapping
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	// Set defaults for all flags
	v.SetDefault("json", false)
	v.SetDefault("events-export", false)
	// Durable events journal (bd_events_journal). OFF by default because it
	// costs a snapshot write on every mutation and exists only for a consumer
	// that is actually tailing it. Enable per workspace with
	// `bd config set events-journal true` or BD_EVENTS_JOURNAL=1; read it with
	// `bd events tail/export`.
	v.SetDefault("events-journal", false)
	// Retention floors. retain-days keeps rows younger than N days; retain-rows
	// always keeps the newest N rows. They bound every prune, whether an
	// operator asked for it (`bd events prune`) or maintenance applied it
	// (events-journal-auto-prune). Both default ON: a journal is only ever
	// enabled because something is consuming it, and with both floors at 0 a
	// single `bd events prune --before <large>` deletes the whole journal,
	// stranding a consumer whose checkpoint is now below the floor with no way
	// to recover the lost span. The defaults buy a consumer a week of downtime,
	// or 100k mutations of backlog, whichever is larger. Setting either to 0
	// explicitly disables that floor.
	// Env: BD_EVENTS_JOURNAL_RETAIN_DAYS / BD_EVENTS_JOURNAL_RETAIN_ROWS.
	v.SetDefault("events-journal-retain-days", 7)
	v.SetDefault("events-journal-retain-rows", 100000)
	// Automatic retention, ON by default: after a mutating command commits, and
	// on a timer inside `bd serve`, bd deletes the journal prefix the floors
	// above do not protect. Enabling the journal therefore yields a BOUNDED
	// feed, which is what makes it safe to turn on and forget. Turning both
	// floors to 0 is the deliberate way to keep every record forever; this key
	// is for a consumer that wants the floors respected on reads but wants to
	// own deletion itself. Env: BD_EVENTS_JOURNAL_AUTO_PRUNE.
	v.SetDefault("events-journal-auto-prune", true)
	v.SetDefault("audit.enabled", false)
	v.SetDefault("no-db", false)
	v.SetDefault("no-hooks", false)
	v.SetDefault("db", "")
	v.SetDefault("actor", "")
	v.SetDefault("issue-prefix", "")
	// Additional environment variables (not prefixed with BD_)
	_ = v.BindEnv("identity", "BEADS_IDENTITY") // BindEnv only fails with zero args, which can't happen here
	v.SetDefault("identity", "")
	_ = v.BindEnv("node_id", "BEADS_NODE_ID", "BD_NODE_ID") // replica identity; see NodeID
	v.SetDefault("node_id", "")

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

	v.SetDefault("metrics.disabled", false)
	v.SetDefault("metrics.endpoint", "https://gastownhall-eventsapi.com/mp/collect")

	// Federation configuration (optional Dolt remote)
	v.SetDefault("federation.remote", "")                          // e.g., dolthub://org/beads, gs://bucket/beads, s3://bucket/beads, az://account.blob.core.windows.net/container/beads
	v.SetDefault("federation.sovereignty", "")                     // T1 | T2 | T3 | T4 (empty = no restriction)
	v.SetDefault("federation.allowed-remote-patterns", []string{}) // glob patterns restricting allowed remote URLs (enterprise lockdown)
	v.SetDefault("federation.exclude_types", []string{"wisp"})     // issue types excluded from federation push (privacy filter)

	// Push configuration defaults
	v.SetDefault("no-push", false)

	// Agent profile configuration (gh#3423, follow-up to #4220)
	// Explicit runtime knob for the policy profile (git/commit authority)
	// documented in docs/getting-started/ide-setup.md. `bd prime` uses this to select its
	// close-protocol wording. Values: conservative | minimal | team-maintainer.
	// Invalid values fall back to "conservative" (see GetAgentProfile).
	v.SetDefault("agent.profile", string(ProfileConservative))

	// Create command defaults
	v.SetDefault("create.require-description", false)

	// Validation configuration defaults (bd-t7jq)
	// Values: "warn" | "error" | "none"
	// - "none": no validation (default, backwards compatible)
	// - "warn": validate and print warnings but proceed
	// - "error": validate and fail on missing sections
	v.SetDefault("validation.on-create", "none")
	v.SetDefault("validation.on-close", "none")
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

	// Auto-export: optional JSONL export after mutations for viewers,
	// interchange, and backup. It is not cross-machine sync; Dolt remotes are
	// the source of truth for sync. Viewer integrations can opt in explicitly.
	v.SetDefault("export.auto", false)
	v.SetDefault("export.interval", "60s")
	v.SetDefault("export.path", "issues.jsonl") // relative to .beads/; canonical name
	v.SetDefault("export.git-add", false)

	// Auto-import: legacy compatibility fallback for projects that have not
	// configured a Dolt remote yet. Hook code skips this path when sync.remote
	// is configured because JSONL import is upsert-only, not reconciliation.
	v.SetDefault("import.auto", true)
	v.SetDefault("import.path", "issues.jsonl") // relative to .beads/; canonical import name

	// AI configuration defaults
	v.SetDefault("ai.model", "claude-haiku-4-5-20251001")
	v.SetDefault("ai.base_url", "")

	// List command defaults
	v.SetDefault("list.limit", 50)

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

func worktreeFallbackConfigPath(repoPath string) string {
	gitDir, commonDir, ok := gitDirsForRepo(repoPath)
	if !ok || samePath(gitDir, commonDir) {
		return ""
	}

	if filepath.Base(commonDir) == ".git" {
		return filepath.Join(filepath.Dir(commonDir), ".beads", "config.yaml")
	}

	return filepath.Join(commonDir, ".beads", "config.yaml")
}

func gitDirsForRepo(repoPath string) (gitDir, commonDir string, ok bool) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", "", false
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return "", "", false
	}

	gitDir = gitPathForRepo(repoPath, strings.TrimSpace(lines[0]))
	commonDir = gitPathForRepo(repoPath, strings.TrimSpace(lines[1]))
	if gitDir == "" || commonDir == "" {
		return "", "", false
	}

	return gitDir, commonDir, true
}

func gitPathForRepo(repoPath, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, path)
	}

	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	return filepath.Clean(left) == filepath.Clean(right)
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

	// Check if value is set from environment variable.
	// Use LookupEnv (not Getenv) so that explicitly-set-but-empty vars like
	// BD_BACKUP_ENABLED= are recognized as "set by the user" rather than
	// falling through to the default/auto-detect path.
	envKey := "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if _, ok := os.LookupEnv(envKey); ok {
		return SourceEnvVar
	}

	// Check BEADS_ prefixed env vars for legacy compatibility
	beadsEnvKey := "BEADS_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if _, ok := os.LookupEnv(beadsEnvKey); ok {
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

// EnvVarName returns the environment variable name that would override the given
// config key, if one is set. Returns the BD_ or BEADS_ prefixed name, or empty
// string if no env var is set for this key.
func EnvVarName(key string) string {
	envKey := "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if _, ok := os.LookupEnv(envKey); ok {
		return envKey
	}
	beadsEnvKey := "BEADS_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if _, ok := os.LookupEnv(beadsEnvKey); ok {
		return beadsEnvKey
	}
	return ""
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
				// Env var is overriding config file value.
				// Use LookupEnv to detect presence — empty-string env vars
				// are still intentional overrides.
				envKey := "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
				if _, ok := os.LookupEnv(envKey); !ok {
					envKey = "BEADS_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
					if _, ok := os.LookupEnv(envKey); !ok {
						continue
					}
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

	// Set the single key using dot-path splitting for nested keys (e.g. "routing.mode").
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

// DefaultAIModelFor returns the model for Anthropic-compatible AI calls,
// accounting for which provider the resolved key selects: an explicitly
// configured ai.model always wins; otherwise a MiniMax-selected key gets a
// MiniMax-served default (MINIMAX_MODEL env > MiniMaxDefaultModel), since
// MiniMax does not serve the Claude default model.
func DefaultAIModelFor(keySource AIAPIKeySource) string {
	if GetValueSource("ai.model") != SourceDefault {
		return GetString("ai.model")
	}
	if keySource == AIAPIKeySourceMiniMaxEnv {
		if m := os.Getenv("MINIMAX_MODEL"); m != "" {
			return m
		}
		return MiniMaxDefaultModel
	}
	return GetString("ai.model")
}

// AIAPIKeySource identifies where the active Anthropic-compatible API key came from.
type AIAPIKeySource string

const (
	AIAPIKeySourceNone         AIAPIKeySource = ""
	AIAPIKeySourceAnthropicEnv AIAPIKeySource = "ANTHROPIC_API_KEY" //nolint:gosec // Environment variable name, not a credential.
	AIAPIKeySourceMiniMaxEnv   AIAPIKeySource = "MINIMAX_API_KEY"   //nolint:gosec // Environment variable name, not a credential.
	AIAPIKeySourceConfig       AIAPIKeySource = "ai.api_key"
	AIAPIKeySourceExplicit     AIAPIKeySource = "explicit"

	MiniMaxDefaultBaseURL = "https://api.minimax.io/anthropic"

	// MiniMaxDefaultModel is used when MINIMAX_API_KEY selected the key and
	// the user did not configure ai.model: MiniMax's Anthropic-compatible
	// endpoint does not serve the Claude default model, so key-only setup
	// must route to a model MiniMax actually hosts. Override with
	// MINIMAX_MODEL or ai.model.
	MiniMaxDefaultModel = "MiniMax-M2"
)

// ResolveAIAPIKey returns the API key for Anthropic-compatible AI calls.
//
// Precedence: ANTHROPIC_API_KEY > MINIMAX_API_KEY > ai.api_key > explicit.
func ResolveAIAPIKey(explicit string) (string, AIAPIKeySource) {
	if envKey := os.Getenv("ANTHROPIC_API_KEY"); envKey != "" {
		return envKey, AIAPIKeySourceAnthropicEnv
	}
	if envKey := os.Getenv("MINIMAX_API_KEY"); envKey != "" {
		return envKey, AIAPIKeySourceMiniMaxEnv
	}
	if configKey := GetString("ai.api_key"); configKey != "" {
		return configKey, AIAPIKeySourceConfig
	}
	if explicit != "" {
		return explicit, AIAPIKeySourceExplicit
	}
	return "", AIAPIKeySourceNone
}

// DefaultAIBaseURL returns the configured base URL for Anthropic-compatible AI calls.
//
// Precedence: ai.base_url (or BD_AI_BASE_URL) > MINIMAX_BASE_URL > MiniMax default
// when MINIMAX_API_KEY selected the key. Empty means use the SDK's Anthropic default.
func DefaultAIBaseURL(keySource AIAPIKeySource) string {
	if baseURL := GetString("ai.base_url"); baseURL != "" {
		return baseURL
	}
	if keySource == AIAPIKeySourceMiniMaxEnv {
		if baseURL := os.Getenv("MINIMAX_BASE_URL"); baseURL != "" {
			return baseURL
		}
		return MiniMaxDefaultBaseURL
	}
	return ""
}

// AllSettings returns all configuration settings as a map
func AllSettings() map[string]interface{} {
	if v == nil {
		return map[string]interface{}{}
	}
	return v.AllSettings()
}

// AllKeys returns all keys in the viper registry (defaults + config file + env).
// Keys are returned in lowercase dot-notation (e.g., "federation.remote").
func AllKeys() []string {
	if v == nil {
		return nil
	}
	return v.AllKeys()
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
//	  other-project: /absolute/path/to/other-project
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

// NodeID returns the identity of THIS replica: the beads STORE that grants
// and enforces leases here. A lease is enforceable exactly as far as that
// store reaches, and the replica-aware reclaim guard
// (issueops.ReclaimExpiredLeasesInTx) refuses to revert a lease some OTHER
// node granted.
//
// It is read from BEADS_NODE_ID / BD_NODE_ID, or node_id in config.yaml, and
// from nowhere else. It deliberately does NOT fall back to os.Hostname(),
// because the hostname answers the wrong question — it names the client
// PROCESS's machine, not the store:
//
//   - With a shared or remote dolt sql-server (BEADS_DOLT_SERVER_HOST, or any
//     ServerModeExternal deployment — systemd, Docker, Hosted Dolt, a VPS),
//     many hosts are clients of ONE store. There is no sync interval between
//     them and no stale liveness view to defend against, but per-hostname
//     identity would make a supervisor unable to reap any worker's lease —
//     reclaim would return 0 forever and every dead worker's unit would sit
//     in_progress permanently.
//   - In a container the hostname is the container ID, regenerated on every
//     run, so a replaced worker's own single-machine leases would look
//     foreign to its successor.
//   - On macOS/DHCP the transient hostname changes with the network.
//
// Each of those is a fail-CLOSED regression on a deployment that has no
// federation at all, which is a far worse failure than the cross-replica
// reclaim this guard exists to prevent. So the guard is armed only where an
// operator has said, explicitly, that this store is one replica among
// several. "" means "this deployment does not name its replicas" and is the
// default: every consumer degrades to the pre-replica-aware behavior rather
// than fail closed.
func NodeID() string {
	return strings.TrimSpace(GetString("node_id"))
}

// FederationConfig holds the federation (Dolt remote) configuration.
type FederationConfig struct {
	Remote       string      // dolthub://org/beads, gs://bucket/beads, s3://bucket/beads
	Sovereignty  Sovereignty // T1, T2, T3, T4
	ExcludeTypes []string    // issue types excluded from federation push (e.g. ["wisp"])
}

// GetFederationConfig returns the current federation configuration.
func GetFederationConfig() FederationConfig {
	return FederationConfig{
		Remote:       GetString("federation.remote"),
		Sovereignty:  GetSovereignty(),
		ExcludeTypes: GetStringSlice("federation.exclude_types"),
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

// DefaultAgentsFile is the default filename for agent instructions.
const DefaultAgentsFile = "AGENTS.md"

// AgentsFile returns the configured agents instruction filename.
// Returns DefaultAgentsFile ("AGENTS.md") if no custom value is set.
// Note: Use SafeAgentsFile() when the value will be used for file I/O,
// as config.yaml may be manually edited with invalid values.
func AgentsFile() string {
	if name := GetString("agents.file"); name != "" {
		return name
	}
	return DefaultAgentsFile
}

// SafeAgentsFile returns the configured agents filename after validation.
// If the stored config value is invalid (e.g. manually edited with traversal
// paths), it falls back to DefaultAgentsFile and logs a warning.
func SafeAgentsFile() string {
	name := AgentsFile()
	if err := ValidateAgentsFile(name); err != nil {
		debug.Logf("config: agents.file %q failed validation (%v), using default", name, err)
		return DefaultAgentsFile
	}
	return name
}

// ValidateAgentsFile checks that filename is safe to use as an agents file path.
// It rejects absolute paths, path separators, names longer than 255 characters,
// and non-markdown extensions. This is a pure string validation function — I/O
// checks (e.g. symlink detection) are deferred to the file write layer.
func ValidateAgentsFile(filename string) error {
	if filename == "" {
		return fmt.Errorf("agents file name must not be empty")
	}
	if len(filename) > 255 {
		return fmt.Errorf("agents file name exceeds 255 characters")
	}
	if strings.ContainsAny(filename, "/\\") {
		return fmt.Errorf("agents file must be a simple filename without path separators, got %q", filename)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".md" {
		return fmt.Errorf("agents file must have .md extension, got %q", ext)
	}
	return nil
}

// getConfigList retrieves a list-typed configuration value from config.yaml,
// accepting either the YAML list form (e.g. `types: { custom: [step, wisp] }`)
// or the legacy comma-separated string form (e.g.
// `types.custom = "step,wisp"`). Entries are trimmed; empty entries are
// dropped. The dual-form support is required for project-extension
// types/statuses declared in .beads/config.yaml — see gastownhall/beads#4024.
func getConfigList(key string) []string {
	if v == nil {
		debug.Logf("config: viper not initialized, returning nil for key %q", key)
		return nil
	}

	// Try the YAML-list form first. Viper's GetStringSlice returns:
	//   * []string for a YAML sequence value,
	//   * []string{value} when the underlying value is a single string,
	//   * nil/empty when the key is unset.
	// Re-splitting each entry on comma covers the case where the entry is
	// itself a comma-separated string (legacy form bound via GetStringSlice).
	if slice := v.GetStringSlice(key); len(slice) > 0 {
		result := make([]string, 0, len(slice))
		for _, entry := range slice {
			for _, p := range strings.Split(entry, ",") {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					result = append(result, trimmed)
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Fallback to direct string retrieval for the comma-separated form when
	// GetStringSlice didn't surface a value (e.g. some viper builds short-
	// circuit GetStringSlice for pure-string values).
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
