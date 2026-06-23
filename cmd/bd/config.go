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
	"github.com/steveyegge/beads/internal/metrics"
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
  - import.*          JSONL import settings (stored in config.yaml)
  - jira.*            Jira integration settings
  - linear.*          Linear integration settings
  - github.*          GitHub integration settings
  - custom.*          Custom integration settings
  - status.*          Issue status configuration
  - doctor.suppress.* Suppress specific bd doctor warnings (GH#1095)

Auto-Export (config.yaml):
  Optional JSONL export to .beads/issues.jsonl after write commands (throttled).
  Useful for viewers (bv), interchange, and issue-level migration; not a backup.
  It is not cross-machine sync; use bd dolt push/pull with a Dolt remote.
  Disabled by default. Enable only for integrations that need fresh JSONL.
  Auto-staging is separate and disabled by default.

  Keys:
    export.auto       Enable/disable auto-export (default: false)
    export.path       Output filename relative to .beads/ (default: issues.jsonl)
    export.interval   Minimum time between exports (default: 60s)
    export.git-add    Auto-stage the export file (default: false)

Auto-Import (config.yaml):
  Reads .beads/issues.jsonl by default when a JSONL import path is implied.
  Use a relative filename/path so the import stays within the project .beads/
  directory and remains portable across machines.

  Keys:
    import.path       Input filename relative to .beads/ (default: issues.jsonl)

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
  bd config set export.auto true                       # Enable auto-export for viewer integrations
  bd config set export.path "beads.jsonl"              # Custom export filename
  bd config set import.path "beads.jsonl"              # Custom import filename
  bd config set export.git-add true                    # Also stage the export file
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set status.custom "awaiting_review,awaiting_testing"
  bd config set doctor.suppress.pending-migrations true
  bd config set dolt.debug true                        # Enable Dolt sql-server debug mode (loglevel=debug, --prof cpu)
  bd config set dolt.local-only true                   # Skip wiring a Dolt sync remote during bd init
  bd config get export.auto
  bd config list
  bd config unset jira.url`,
}

var forceGitTracked bool

var configSetCmd = &cobra.Command{
	Use:           "set <key> <value>",
	Short:         "Set a configuration value",
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-set")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		key := args[0]
		value := args[1]

		if msg, rejected := rejectProtectedConfigKey(key); rejected {
			fmt.Fprintln(os.Stderr, msg)
			return SilentExit()
		}

		if key == "dolt.debug" && !usesSQLServer() {
			fmt.Fprintln(os.Stderr, "Error: dolt.debug requires a sql-server-backed project (embedded mode has no managed server).")
			fmt.Fprintln(os.Stderr, "  To migrate: re-init with 'bd init --server' or 'bd init --shared-server'.")
			return SilentExit()
		}

		if !isRecognizedConfigKey(key) {
			suggestion := suggestConfigKey(key)
			if suggestion != "" {
				fmt.Fprintf(os.Stderr, "Warning: %q is not a recognized config key. Did you mean %q?\n", key, suggestion)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: %q is not a recognized config key. Use 'custom.*' for user-defined keys.\n", key)
			}
			fmt.Fprintf(os.Stderr, "Run 'bd config --help' for valid namespaces.\n")
		}

		if !forceGitTracked {
			if err := config.CheckSecretKeyGitSafety(key); err != nil {
				return HandleError("%v", err)
			}
		}

		if config.IsYamlOnlyKey(key) {
			var setErr error
			location := "config.yaml"
			if config.IsUserGlobalKey(key) {
				setErr = config.SetUserYamlConfig(key, value)
				location = config.UserConfigYamlPath()
			} else {
				setErr = config.SetYamlConfig(key, value)
			}
			if setErr != nil {
				return HandleError("setting config: %v", setErr)
			}

			if jsonOutput {
				if err := outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": location,
				}); err != nil {
					return err
				}
			} else {
				fmt.Printf("Set %s = %s (in %s)\n", key, value, location)
			}
			printConfigSideEffects(checkConfigSetSideEffects(key, value))
			return nil
		}

		if key == "beads.role" {
			validRoles := map[string]bool{"maintainer": true, "contributor": true}
			if !validRoles[value] {
				return HandleError("invalid role %q (valid values: maintainer, contributor)", value)
			}
			cmd := exec.Command("git", "config", "beads.role", value) //nolint:gosec // value is validated against allowlist above
			if err := cmd.Run(); err != nil {
				return HandleError("setting beads.role in git config: %v", err)
			}
			if jsonOutput {
				if err := outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "git config",
				}); err != nil {
					return err
				}
			} else {
				fmt.Printf("Set %s = %s (in git config)\n", key, value)
			}
			return nil
		}

		if usesProxiedServer() {
			runConfigSetProxiedServer(rootCtx, key, value)
			return nil
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config set requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx

		if key == "status.custom" && value != "" {
			if _, err := types.ParseCustomStatusConfig(value); err != nil {
				return HandleError("invalid status.custom value: %v", err)
			}
		}

		if err := store.SetConfig(ctx, key, value); err != nil {
			return HandleError("setting config: %v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			if err := outputJSON(map[string]string{
				"key":   key,
				"value": value,
			}); err != nil {
				return err
			}
		} else {
			fmt.Printf("Set %s = %s\n", key, value)
		}
		printConfigSideEffects(checkConfigSetSideEffects(key, value))
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:           "get <key>",
	Short:         "Get a configuration value",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-get")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		key := args[0]

		if config.IsYamlOnlyKey(key) {
			// User-global keys (e.g. metrics.*) must be read from the user-global
			// config.yaml only — the same source the runtime uses for metrics
			// consent and endpoint. Reading the merged value here would let a
			// project's .beads/config.yaml shadow the effective value and report the
			// opposite of what `bd metrics` actually honors.
			if config.IsUserGlobalKey(key) {
				value := config.GetUserYamlConfig(key)
				location := config.UserConfigYamlPath()
				if jsonOutput {
					return outputJSON(map[string]interface{}{
						"key":      key,
						"value":    value,
						"location": location,
					})
				}
				if value == "" {
					fmt.Printf("%s (not set in %s)\n", key, location)
				} else {
					fmt.Printf("%s\n", value)
				}
				return nil
			}

			value := config.GetYamlConfig(key)

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "config.yaml",
				})
			}
			if value == "" {
				fmt.Printf("%s (not set in config.yaml)\n", key)
			} else {
				fmt.Printf("%s\n", value)
			}
			return nil
		}

		if key == "beads.role" {
			cmd := exec.Command("git", "config", "--get", "beads.role")
			output, err := cmd.Output()
			value := strings.TrimSpace(string(output))
			if err != nil {
				value = ""
			}
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"key":      key,
					"value":    value,
					"location": "git config",
				})
			}
			if value == "" {
				fmt.Printf("%s (not set in git config)\n", key)
			} else {
				fmt.Printf("%s\n", value)
			}
			return nil
		}

		if usesProxiedServer() {
			runConfigGetProxiedServer(rootCtx, key)
			return nil
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config get requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		var value string
		var err error

		value, err = store.GetConfig(ctx, key)

		if err != nil {
			return HandleError("getting config: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		}
		if value == "" {
			fmt.Printf("%s (not set)\n", key)
		} else {
			fmt.Printf("%s\n", value)
		}
		return nil
	},
}

var configListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List all configuration",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			runConfigListProxiedServer(rootCtx)
			return nil
		}

		// Config operations work in direct mode only
		if err := ensureDirectMode("config list requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		config, err := store.GetAllConfig(ctx)
		if err != nil {
			return HandleError("listing config: %v", err)
		}

		if jsonOutput {
			return outputJSON(config)
		}

		if len(config) == 0 {
			fmt.Println("No configuration set")
			return nil
		}

		keys := make([]string, 0, len(config))
		for k := range config {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		fmt.Println("\nConfiguration:")
		for _, k := range keys {
			fmt.Printf("  %s = %s\n", k, config[k])
		}

		showConfigYAMLOverrides(config)
		return nil
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
	Use:           "unset <key>",
	Short:         "Delete a configuration value",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-unset")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		key := args[0]

		if config.IsYamlOnlyKey(key) {
			location := "config.yaml"
			var unsetErr error
			if config.IsUserGlobalKey(key) {
				unsetErr = config.UnsetUserYamlConfig(key)
				location = config.UserConfigYamlPath()
			} else {
				unsetErr = config.UnsetYamlConfig(key)
			}
			if unsetErr != nil {
				return HandleError("unsetting config: %v", unsetErr)
			}

			if jsonOutput {
				if err := outputJSON(map[string]interface{}{
					"key":      key,
					"location": location,
				}); err != nil {
					return err
				}
			} else {
				fmt.Printf("Unset %s (in %s)\n", key, location)
			}
			printConfigSideEffects(checkConfigUnsetSideEffects(key))
			return nil
		}

		if key == "beads.role" {
			gitCmd := exec.Command("git", "config", "--unset", "beads.role")
			if err := gitCmd.Run(); err != nil {
				return HandleError("unsetting beads.role in git config: %v", err)
			}
			if jsonOutput {
				if err := outputJSON(map[string]interface{}{
					"key":      key,
					"location": "git config",
				}); err != nil {
					return err
				}
			} else {
				fmt.Printf("Unset %s (in git config)\n", key)
			}
			return nil
		}

		if usesProxiedServer() {
			runConfigUnsetProxiedServer(rootCtx, key)
			return nil
		}

		// Database-stored config requires direct mode
		if err := ensureDirectMode("config unset requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		if err := store.DeleteConfig(ctx, key); err != nil {
			return HandleError("deleting config: %v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			if err := outputJSON(map[string]string{
				"key": key,
			}); err != nil {
				return err
			}
		} else {
			fmt.Printf("Unset %s\n", key)
		}
		printConfigSideEffects(checkConfigUnsetSideEffects(key))
		return nil
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-validate")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		repoPath, err := resolvedConfigRepoRoot()
		if err != nil {
			return HandleErrorWithHintRespectJSON(activeWorkspaceNotFoundError(), diagHint())
		}

		doctorCheck := doctor.CheckConfigValues(repoPath)

		syncIssues := validateSyncConfig(repoPath)

		allIssues := []string{}
		if doctorCheck.Detail != "" {
			allIssues = append(allIssues, strings.Split(doctorCheck.Detail, "\n")...)
		}
		allIssues = append(allIssues, syncIssues...)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"valid":  len(allIssues) == 0,
				"issues": allIssues,
			})
		}

		if len(allIssues) == 0 {
			fmt.Println("✓ All sync-related configuration is valid")
			return nil
		}

		fmt.Println("Configuration validation found issues:")
		for _, issue := range allIssues {
			if issue != "" {
				fmt.Printf("  • %s\n", issue)
			}
		}
		fmt.Println("\nRun 'bd config set <key> <value>' to fix configuration issues.")
		return SilentExit()
	},
}

// validateSyncConfig performs additional sync-related config validation
// beyond what doctor.CheckConfigValues covers.
func validateSyncConfig(repoPath string) []string {
	var issues []string

	// Load config.yaml from the resolved workspace so shared worktrees validate
	// the same config file they actually run with.
	configPath := filepath.Join(doctor.ResolveBeadsDirForRepo(repoPath), "config.yaml")
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
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("config-set-many")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		type kvPair struct {
			key, value string
		}
		pairs := make([]kvPair, 0, len(args))
		for _, arg := range args {
			idx := strings.Index(arg, "=")
			if idx <= 0 {
				return HandleError("invalid argument %q (expected key=value format)", arg)
			}
			pairs = append(pairs, kvPair{key: arg[:idx], value: arg[idx+1:]})
		}

		for _, p := range pairs {
			if p.key == "beads.role" {
				validRoles := map[string]bool{"maintainer": true, "contributor": true}
				if !validRoles[p.value] {
					return HandleError("invalid role %q (valid values: maintainer, contributor)", p.value)
				}
			}
			if p.key == "status.custom" && p.value != "" {
				if _, err := types.ParseCustomStatusConfig(p.value); err != nil {
					return HandleError("invalid status.custom value: %v", err)
				}
			}
		}

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

		if !forceGitTracked {
			for _, p := range yamlPairs {
				if err := config.CheckSecretKeyGitSafety(p.key); err != nil {
					return HandleError("%v", err)
				}
			}
		}

		for _, p := range yamlPairs {
			var setErr error
			if config.IsUserGlobalKey(p.key) {
				setErr = config.SetUserYamlConfig(p.key, p.value)
			} else {
				setErr = config.SetYamlConfig(p.key, p.value)
			}
			if setErr != nil {
				return HandleError("setting config %s: %v", p.key, setErr)
			}
		}

		for _, p := range gitPairs {
			cmd := exec.Command("git", "config", "beads.role", p.value) //nolint:gosec // value is validated against allowlist above
			if err := cmd.Run(); err != nil {
				return HandleError("setting %s in git config: %v", p.key, err)
			}
		}

		if len(dbPairs) > 0 {
			if usesProxiedServer() {
				keys := make([]string, len(dbPairs))
				values := make([]string, len(dbPairs))
				for i, p := range dbPairs {
					keys[i] = p.key
					values[i] = p.value
				}
				runConfigSetManyProxiedServer(rootCtx, keys, values)
			} else {
				if err := ensureDirectMode("config set-many requires direct database access"); err != nil {
					return HandleError("%v", err)
				}

				ctx := rootCtx
				for _, p := range dbPairs {
					if err := store.SetConfig(ctx, p.key, p.value); err != nil {
						return HandleError("setting config %s: %v", p.key, err)
					}
				}
				commandDidWrite.Store(true)
			}
		}

		if jsonOutput {
			results := make([]map[string]string, 0, len(pairs))
			for _, p := range pairs {
				location := "database"
				if config.IsUserGlobalKey(p.key) {
					location = config.UserConfigYamlPath()
				} else if config.IsYamlOnlyKey(p.key) {
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
			if err := outputJSON(results); err != nil {
				return err
			}
		} else {
			for _, p := range pairs {
				location := ""
				if config.IsUserGlobalKey(p.key) {
					location = fmt.Sprintf(" (in %s)", config.UserConfigYamlPath())
				} else if config.IsYamlOnlyKey(p.key) {
					location = " (in config.yaml)"
				} else if p.key == "beads.role" {
					location = " (in git config)"
				}
				fmt.Printf("Set %s = %s%s\n", p.key, p.value, location)
			}
		}
		return nil
	},
}

// recognizedConfigPrefixes lists valid top-level config namespaces.
// Keys under custom.* are always accepted (user-extensible).
var recognizedConfigPrefixes = []string{
	"export.", "import.", "dolt.", "jira.", "linear.", "github.", "custom.",
	"status.", "doctor.suppress.", "routing.", "sync.", "git.",
	"directory.", "repos.", "external_projects.", "validation.",
	"hierarchy.", "ai.", "backup.", "federation.", "metrics.",
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

// rejectProtectedConfigKey rejects keys that are owned by a dedicated
// lifecycle command (init/rename) rather than 'bd config set'. The canonical
// example is issue_prefix: 'bd create' reads YAML "issue-prefix"
// then DB "issue_prefix", while 'bd config set' would land in DB
// "issue-prefix" — a third key no reader consults. Accepting either the
// dash or underscore form silently produces a write that looks like it
// succeeded but is never visible to 'bd create'. Reject both and point the
// user at the right command.
func rejectProtectedConfigKey(key string) (string, bool) {
	switch key {
	case "issue_prefix", "issue-prefix":
		return "Error: issue_prefix cannot be set via 'bd config set'.\n" +
			"  - New project:       bd init --prefix <prefix>\n" +
			"  - Fresh clone:       bd bootstrap\n" +
			"  - Rename existing:   bd rename-prefix <new-prefix>", true
	}
	return "", false
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
	configSetCmd.Flags().BoolVar(&forceGitTracked, "force-git-tracked", false, "Allow writing secret keys to git-tracked config files (use with caution)")
	configSetManyCmd.Flags().BoolVar(&forceGitTracked, "force-git-tracked", false, "Allow writing secret keys to git-tracked config files (use with caution)")

	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configSetManyCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configUnsetCmd)
	configCmd.AddCommand(configValidateCmd)
	rootCmd.AddCommand(configCmd)
}
