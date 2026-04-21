package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/types"
)

var configCmd = &cobra.Command{
	Use:     "config",
	GroupID: "setup",
	Short:   "Manage configuration settings",
	Long: `Manage configuration settings for external integrations and preferences.

Configuration is stored per-project in the beads database and is version-control-friendly.

Common namespaces:
  - export.*          Auto-export settings (stored in config.yaml)
  - jira.*            Jira integration settings
  - linear.*          Linear integration settings
  - github.*          GitHub integration settings
  - custom.*          Custom integration settings
  - status.*          Issue status configuration
  - doctor.suppress.* Suppress specific bd doctor warnings (GH#1095)

Auto-Export (config.yaml):
  Writes .beads/issues.jsonl after every write command (throttled).
  Enabled by default. Useful for viewers (bv) and git-based sync.

  Keys:
    export.auto       Enable/disable auto-export (default: true)
    export.path       Output filename relative to .beads/ (default: issues.jsonl)
    export.interval   Minimum time between exports (default: 60s)
    export.git-add    Auto-stage the export file (default: true)

Custom Status States:
  You can define custom status states for multi-step pipelines using the
  status.custom config key. Statuses should be comma-separated.

  Example:
    bd config set status.custom "awaiting_review,awaiting_testing,awaiting_docs"

  This enables issues to use statuses like 'awaiting_review' in addition to
  the built-in statuses (open, in_progress, blocked, deferred, closed).

Suppressing Doctor Warnings:
  Suppress specific bd doctor warnings by check name slug:
    bd config set doctor.suppress.pending-migrations true
    bd config set doctor.suppress.git-hooks true
  Check names are converted to slugs: "Git Hooks" → "git-hooks".
  Only warnings are suppressed (errors and passing checks always show).
  To unsuppress: bd config unset doctor.suppress.<slug>

Examples:
  bd config set export.auto false                      # Disable auto-export
  bd config set export.path "beads.jsonl"              # Custom export filename
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set status.custom "awaiting_review,awaiting_testing"
  bd config set doctor.suppress.pending-migrations true
  bd config get export.auto
  bd config list
  bd config unset jira.url`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Args:  cobra.ExactArgs(2),
	Run: func(_ *cobra.Command, args []string) {
		key := args[0]
		value := args[1]

		// Warn on unrecognized config keys so typos don't silently become
		// no-ops. The custom.* namespace is exempt (user-extensible). GH#3293.
		if !isRecognizedConfigKey(key) {
			suggestion := suggestConfigKey(key)
			if suggestion != "" {
				fmt.Fprintf(os.Stderr, "Warning: %q is not a recognized config key. Did you mean %q?\n", key, suggestion)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: %q is not a recognized config key. Use 'custom.*' for user-defined keys.\n", key)
			}
			fmt.Fprintf(os.Stderr, "Run 'bd config --help' for valid namespaces.\n")
		}

		// Check if this is a yaml-only key (startup settings like no-db, etc.)
		// These must be written to config.yaml, not SQLite, because they're read
		// before the database is opened. (GH#536)
		if config.IsYamlOnlyKey(key) {
			if err := config.SetYamlConfig(key, value); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting config: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "config.yaml",
				})
			} else {
				fmt.Printf("Set %s = %s (in config.yaml)\n", key, value)
			}
			printConfigSideEffects(checkConfigSetSideEffects(key, value))
			return
		}

		// beads.role is stored in git config, not SQLite (GH#1531).
		// bd doctor reads it from git config, so we write there for consistency.
		if key == "beads.role" {
			validRoles := map[string]bool{"maintainer": true, "contributor": true}
			if !validRoles[value] {
				fmt.Fprintf(os.Stderr, "Error: invalid role %q (valid values: maintainer, contributor)\n", value)
				os.Exit(1)
			}
			cmd := exec.Command("git", "config", "beads.role", value) //nolint:gosec // value is validated against allowlist above
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting beads.role in git config: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "git config",
				})
			} else {
				fmt.Printf("Set %s = %s (in git config)\n", key, value)
			}
			return
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config set requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		ctx := rootCtx

		// Validate status.custom config before writing
		if key == "status.custom" && value != "" {
			if _, err := types.ParseCustomStatusConfig(value); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid status.custom value: %v\n", err)
				os.Exit(1)
			}
		}

		if err := store.SetConfig(ctx, key, value); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting config: %v\n", err)
			os.Exit(1)
		}
		if _, err := store.CommitPending(ctx, getActor()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit config change: %v\n", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		} else {
			fmt.Printf("Set %s = %s\n", key, value)
		}
		printConfigSideEffects(checkConfigSetSideEffects(key, value))
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a configuration value",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]

		// Check if this is a yaml-only key (startup settings)
		// These are read from config.yaml via viper, not SQLite. (GH#536)
		if config.IsYamlOnlyKey(key) {
			value := config.GetYamlConfig(key)

			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "config.yaml",
				})
			} else {
				if value == "" {
					fmt.Printf("%s (not set in config.yaml)\n", key)
				} else {
					fmt.Printf("%s\n", value)
				}
			}
			return
		}

		// beads.role is stored in git config, not SQLite (GH#1531).
		if key == "beads.role" {
			cmd := exec.Command("git", "config", "--get", "beads.role")
			output, err := cmd.Output()
			value := strings.TrimSpace(string(output))
			if err != nil {
				value = ""
			}
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "git config",
				})
			} else {
				if value == "" {
					fmt.Printf("%s (not set in git config)\n", key)
				} else {
					fmt.Printf("%s\n", value)
				}
			}
			return
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config get requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		ctx := rootCtx
		var value string
		var err error

		value, err = store.GetConfig(ctx, key)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting config: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		} else {
			if value == "" {
				fmt.Printf("%s (not set)\n", key)
			} else {
				fmt.Printf("%s\n", value)
			}
		}
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configuration",
	Run: func(cmd *cobra.Command, args []string) {
		// Config operations work in direct mode only
		if err := ensureDirectMode("config list requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		ctx := rootCtx
		config, err := store.GetAllConfig(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing config: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(config)
			return
		}

		if len(config) == 0 {
			fmt.Println("No configuration set")
			return
		}

		// Sort keys for consistent output
		keys := make([]string, 0, len(config))
		for k := range config {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		fmt.Println("\nConfiguration:")
		for _, k := range keys {
			fmt.Printf("  %s = %s\n", k, config[k])
		}

		// Check for config.yaml overrides that take precedence (bd-20j)
		// This helps diagnose when effective config differs from database config
		showConfigYAMLOverrides(config)
	},
}

// showConfigYAMLOverrides warns when config.yaml or env vars override database settings.
// This addresses the confusion when `bd config list` shows one value but the effective
// value used by commands is different due to higher-priority config sources.
func showConfigYAMLOverrides(dbConfig map[string]string) {
	var envWarnings []string

	// Check each DB config key for env var overrides using config.EnvVarName
	// which handles both BD_* and legacy BEADS_* prefixes with LookupEnv.
	for key, dbValue := range dbConfig {
		if envName := config.EnvVarName(key); envName != "" {
			envValue := os.Getenv(envName)
			if envValue != dbValue {
				envWarnings = append(envWarnings, fmt.Sprintf("  %s: DB has %q, but env %s=%q takes precedence", key, dbValue, envName, envValue))
			}
		}
	}

	// Discover yaml-only keys dynamically via AllKeys() instead of a hardcoded list.
	// This stays in sync as new yaml-only keys are added to the config system.
	allKeys := config.AllKeys()
	sort.Strings(allKeys)

	var yamlOverrides []string
	for _, key := range allKeys {
		// Skip keys already shown in the DB config section
		if _, inDB := dbConfig[key]; inDB {
			continue
		}
		// Only show yaml-only keys that are explicitly set in config.yaml
		if !config.IsYamlOnlyKey(key) {
			continue
		}
		if config.GetValueSource(key) != config.SourceConfigFile {
			continue
		}
		val := config.GetString(key)
		if val != "" {
			yamlOverrides = append(yamlOverrides, fmt.Sprintf("  %s = %s", key, val))
		}
	}

	// Also check yaml-only keys for env var overrides
	for _, key := range allKeys {
		if _, inDB := dbConfig[key]; inDB {
			continue // already checked above
		}
		if envName := config.EnvVarName(key); envName != "" {
			src := config.GetValueSource(key)
			if src == config.SourceEnvVar {
				envWarnings = append(envWarnings, fmt.Sprintf("  %s: env %s=%q overrides config", key, envName, os.Getenv(envName)))
			}
		}
	}

	if len(yamlOverrides) > 0 {
		fmt.Println("\nAlso set in config.yaml (not shown above):")
		for _, line := range yamlOverrides {
			fmt.Println(line)
		}
	}

	if len(envWarnings) > 0 {
		sort.Strings(envWarnings)
		fmt.Println("\n⚠ Environment variable overrides detected:")
		for _, w := range envWarnings {
			fmt.Println(w)
		}
	}

	fmt.Println("\nTip: Run 'bd config show' for all effective config with provenance.")
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Delete a configuration value",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]

		// Check if this is a yaml-only key (startup settings like backup.*, routing.*, etc.)
		// These must be removed from config.yaml, not the database. (GH#2727)
		if config.IsYamlOnlyKey(key) {
			if err := config.UnsetYamlConfig(key); err != nil {
				fmt.Fprintf(os.Stderr, "Error unsetting config: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"location": "config.yaml",
				})
			} else {
				fmt.Printf("Unset %s (in config.yaml)\n", key)
			}
			printConfigSideEffects(checkConfigUnsetSideEffects(key))
			return
		}

		// beads.role is stored in git config, not the database (GH#1531).
		if key == "beads.role" {
			gitCmd := exec.Command("git", "config", "--unset", "beads.role")
			if err := gitCmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error unsetting beads.role in git config: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"key":      key,
					"location": "git config",
				})
			} else {
				fmt.Printf("Unset %s (in git config)\n", key)
			}
			return
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config unset requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		ctx := rootCtx
		if err := store.DeleteConfig(ctx, key); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting config: %v\n", err)
			os.Exit(1)
		}
		if _, err := store.CommitPending(ctx, getActor()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit config change: %v\n", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key": key,
			})
		} else {
			fmt.Printf("Unset %s\n", key)
		}
		printConfigSideEffects(checkConfigUnsetSideEffects(key))
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate sync-related configuration",
	Long: `Validate sync-related configuration settings.

Checks:
  - federation.sovereignty is valid (T1, T2, T3, T4, or empty)
  - federation.remote is set for Dolt sync
  - Remote URL format is valid (dolthub://, gs://, s3://, az://, file://)
  - routing.mode is valid (auto, maintainer, contributor, explicit)

	Examples:
	  bd config validate
	  bd config validate --json`,
	Run: func(cmd *cobra.Command, args []string) {
		repoPath, err := resolvedConfigRepoRoot()
		if err != nil {
			FatalErrorWithHintRespectJSON(activeWorkspaceNotFoundError(), diagHint())
		}

		// Run the existing doctor config values check
		doctorCheck := doctor.CheckConfigValues(repoPath)

		// Run additional sync-related validations
		syncIssues := validateSyncConfig(repoPath)

		// Combine results
		allIssues := []string{}
		if doctorCheck.Detail != "" {
			allIssues = append(allIssues, strings.Split(doctorCheck.Detail, "\n")...)
		}
		allIssues = append(allIssues, syncIssues...)

		// Output results
		if jsonOutput {
			result := map[string]interface{}{
				"valid":  len(allIssues) == 0,
				"issues": allIssues,
			}
			outputJSON(result)
			return
		}

		if len(allIssues) == 0 {
			fmt.Println("✓ All sync-related configuration is valid")
			return
		}

		fmt.Println("Configuration validation found issues:")
		for _, issue := range allIssues {
			if issue != "" {
				fmt.Printf("  • %s\n", issue)
			}
		}
		fmt.Println("\nRun 'bd config set <key> <value>' to fix configuration issues.")
		os.Exit(1)
	},
}

// validateSyncConfig performs additional sync-related config validation
// beyond what doctor.CheckConfigValues covers.
func validateSyncConfig(repoPath string) []string {
	var issues []string

	// Load config.yaml directly from the repo path
	configPath := filepath.Join(repoPath, ".beads", "config.yaml")
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetConfigFile(configPath)

	// Try to read config, but don't error if it doesn't exist
	if err := v.ReadInConfig(); err != nil {
		// Config file doesn't exist or is unreadable - nothing to validate
		return issues
	}

	// Get config from yaml
	federationSov := v.GetString("federation.sovereignty")
	federationRemote := v.GetString("federation.remote")

	// Validate federation.sovereignty
	if federationSov != "" && !config.IsValidSovereignty(federationSov) {
		issues = append(issues, fmt.Sprintf("federation.sovereignty: %q is invalid (valid values: %s, or empty for no restriction)", federationSov, strings.Join(config.ValidSovereigntyTiers(), ", ")))
	}

	// Validate federation.remote is set (required for Dolt sync)
	if federationRemote == "" {
		issues = append(issues, "federation.remote: required for Dolt sync")
	}

	// Strict security validation of remote URL
	if federationRemote != "" {
		if err := remotecache.ValidateRemoteURL(federationRemote); err != nil {
			issues = append(issues, fmt.Sprintf("federation.remote: %s", err))
		}
	}

	// Validate against allowed-remote-patterns if configured
	if federationRemote != "" {
		patterns := v.GetStringSlice("federation.allowed-remote-patterns")
		if len(patterns) > 0 {
			if err := remotecache.ValidateRemoteURLWithPatterns(federationRemote, patterns); err != nil {
				issues = append(issues, fmt.Sprintf("federation.remote: %s", err))
			}
		}
	}

	return issues
}

// isValidRemoteURL validates remote URL formats for sync configuration.
// Uses strict security validation that checks structural correctness,
// rejects control characters, and validates per-scheme requirements.
func isValidRemoteURL(rawURL string) bool {
	return remotecache.ValidateRemoteURL(rawURL) == nil
}

// findBeadsRepoRoot walks up from the given path to find the repo root (containing .beads)
func findBeadsRepoRoot(startPath string) string {
	path := startPath
	for {
		beadsDir := filepath.Join(path, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}

	if isGitRepo() && git.IsWorktree() {
		if fallbackDir := beads.GetWorktreeFallbackBeadsDir(); fallbackDir != "" {
			return filepath.Dir(fallbackDir)
		}
	}

	return ""
}

// resolvedConfigRepoRoot returns the repository root for the active beads
// workspace. It follows FindBeadsDir semantics, including BEADS_DIR and
// worktree/shared fallback resolution.
func resolvedConfigRepoRoot() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("%s", activeWorkspaceNotFoundError())
	}
	return filepath.Dir(beadsDir), nil
}

var configSetManyCmd = &cobra.Command{
	Use:   "set-many <key=value>...",
	Short: "Set multiple configuration values in one operation",
	Long: `Set multiple configuration values at once with a single auto-commit and auto-push.

Each argument must be in key=value format. All values are validated before
any writes occur. This is faster and less noisy than separate 'bd config set'
calls, especially in CI.

Examples:
  bd config set-many ado.state_map.open=New ado.state_map.closed=Closed
  bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ`,
	Args: cobra.MinimumNArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		// Phase 1: Parse all key=value pairs
		type kvPair struct {
			key, value string
		}
		pairs := make([]kvPair, 0, len(args))
		for _, arg := range args {
			idx := strings.Index(arg, "=")
			if idx <= 0 {
				fmt.Fprintf(os.Stderr, "Error: invalid argument %q (expected key=value format)\n", arg)
				os.Exit(1)
			}
			pairs = append(pairs, kvPair{key: arg[:idx], value: arg[idx+1:]})
		}

		// Phase 2: Validate all pairs before writing any
		for _, p := range pairs {
			if p.key == "beads.role" {
				validRoles := map[string]bool{"maintainer": true, "contributor": true}
				if !validRoles[p.value] {
					fmt.Fprintf(os.Stderr, "Error: invalid role %q (valid values: maintainer, contributor)\n", p.value)
					os.Exit(1)
				}
			}
			if p.key == "status.custom" && p.value != "" {
				if _, err := types.ParseCustomStatusConfig(p.value); err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid status.custom value: %v\n", err)
					os.Exit(1)
				}
			}
		}

		// Phase 3: Separate into categories
		var yamlPairs, gitPairs, dbPairs []kvPair
		for _, p := range pairs {
			if config.IsYamlOnlyKey(p.key) {
				yamlPairs = append(yamlPairs, p)
			} else if p.key == "beads.role" {
				gitPairs = append(gitPairs, p)
			} else {
				dbPairs = append(dbPairs, p)
			}
		}

		// Phase 4: Write yaml-only keys
		for _, p := range yamlPairs {
			if err := config.SetYamlConfig(p.key, p.value); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting config %s: %v\n", p.key, err)
				os.Exit(1)
			}
		}

		// Phase 5: Write git config keys
		for _, p := range gitPairs {
			cmd := exec.Command("git", "config", "beads.role", p.value) //nolint:gosec // value is validated against allowlist above
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting %s in git config: %v\n", p.key, err)
				os.Exit(1)
			}
		}

		// Phase 6: Write DB keys in batch
		if len(dbPairs) > 0 {
			if err := ensureDirectMode("config set-many requires direct database access"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			ctx := rootCtx
			for _, p := range dbPairs {
				if err := store.SetConfig(ctx, p.key, p.value); err != nil {
					fmt.Fprintf(os.Stderr, "Error setting config %s: %v\n", p.key, err)
					os.Exit(1)
				}
			}
			if _, err := store.CommitPending(ctx, getActor()); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to commit config changes: %v\n", err)
			}
		}

		// Phase 7: Output results
		if jsonOutput {
			results := make([]map[string]string, 0, len(pairs))
			for _, p := range pairs {
				location := "database"
				if config.IsYamlOnlyKey(p.key) {
					location = "config.yaml"
				} else if p.key == "beads.role" {
					location = "git config"
				}
				results = append(results, map[string]string{
					"key":      p.key,
					"value":    p.value,
					"location": location,
				})
			}
			outputJSON(results)
		} else {
			for _, p := range pairs {
				location := ""
				if config.IsYamlOnlyKey(p.key) {
					location = " (in config.yaml)"
				} else if p.key == "beads.role" {
					location = " (in git config)"
				}
				fmt.Printf("Set %s = %s%s\n", p.key, p.value, location)
			}
		}
	},
}

// recognizedConfigPrefixes lists valid top-level config namespaces.
// Keys under custom.* are always accepted (user-extensible).
var recognizedConfigPrefixes = []string{
	"export.", "dolt.", "jira.", "linear.", "github.", "custom.",
	"status.", "doctor.suppress.", "routing.", "sync.", "git.",
	"directory.", "repos.", "external_projects.", "validation.",
	"hierarchy.", "ai.", "backup.", "federation.",
}

// recognizedConfigKeys lists valid non-namespaced config keys.
var recognizedConfigKeys = map[string]bool{
	"no-db": true, "json": true, "db": true, "actor": true,
	"identity": true, "no-push": true, "no-git-ops": true,
	"create.require-description": true, "beads.role": true,
	"auto_compact_enabled": true, "schema_version": true,
	"output.title-length": true,
}

func isRecognizedConfigKey(key string) bool {
	if recognizedConfigKeys[key] {
		return true
	}
	for _, prefix := range recognizedConfigPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// suggestConfigKey tries to find a close match for a mistyped key by checking
// if the key's prefix is a known prefix with a typo. Returns empty string if
// no suggestion can be made.
func suggestConfigKey(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) < 2 {
		return ""
	}
	prefix := parts[0] + "."

	bestMatch := ""
	bestDist := 3 // max edit distance to suggest
	for _, known := range recognizedConfigPrefixes {
		knownPrefix := strings.TrimSuffix(known, ".")
		d := levenshteinDistance(parts[0], knownPrefix)
		if d > 0 && d < bestDist {
			bestDist = d
			bestMatch = known + parts[1]
		}
	}
	_ = prefix
	return bestMatch
}

func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func init() {
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configSetManyCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configUnsetCmd)
	configCmd.AddCommand(configValidateCmd)
	rootCmd.AddCommand(configCmd)
}
